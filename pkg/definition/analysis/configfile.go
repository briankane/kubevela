/*
Copyright 2026 The KubeVela Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package analysis

import (
	"fmt"
	"strings"

	"cuelang.org/go/cue"
	"cuelang.org/go/cue/ast"
	"cuelang.org/go/cue/cuecontext"
	"cuelang.org/go/cue/format"
	"cuelang.org/go/cue/literal"
	"cuelang.org/go/cue/parser"
	"cuelang.org/go/cue/token"
	cueyaml "cuelang.org/go/encoding/yaml"
)

// ConfigTemplate is a config template a Config may name.
type ConfigTemplate struct {
	Name      string
	Sensitive bool
	// Where it was found: the workspace or the cluster.
	Where string
	// Path is its file, for one in the workspace.
	Path string
	// Source names the SourceDefinition that made it, which writes its
	// configs itself.
	Source string
	// CUE is the template's source.
	CUE string
}

// configCUE is the Config resource KubeVela accepts, closed where its CRD is.
const configCUE = `
#config: {
	apiVersion: string
	kind:       "Config"
	metadata: {
		name:       string
		namespace?: string
		labels?: [string]: string
		annotations?: [string]: string
		...
	}
	spec: {
		templateRef?: {
			name:       string
			namespace?: string
		}
		properties?: {...}
		propertiesFrom?: secretRef: {
			name: string
			key?: string
		}
		alias?:       string
		description?: string
	}
	status?: {...}
}
`

// isConfig reports whether a YAML document is a Config resource.
func isConfig(doc cue.Value) bool {
	apiVersion, _ := doc.LookupPath(cue.ParsePath("apiVersion")).String()
	kind, _ := doc.LookupPath(cue.ParsePath("kind")).String()
	return kind == "Config" && strings.HasPrefix(apiVersion, "config.oam.dev/")
}

// configTemplateFields are a file's top-level structs, when they are a
// config template's: a metadata and a template, and no definition header.
func configTemplateFields(f *ast.File) (metadata *ast.Field, ok bool) {
	var headers []*ast.Field
	template := false
	for _, decl := range f.Decls {
		x, isField := decl.(*ast.Field)
		if !isField {
			continue
		}
		if _, isStruct := x.Value.(*ast.StructLit); !isStruct {
			continue
		}
		if labelName(x.Label) == templateLabel {
			template = true
		} else {
			headers = append(headers, x)
		}
	}
	if !template || !isConfigTemplate(headers) {
		return nil, false
	}
	return headers[0], true
}

// ConfigTemplateHeader reads the name and sensitivity a config template's
// metadata gives it, compiling nothing. It is false for a file that is not
// a config template or names none.
func ConfigTemplateHeader(path string, src []byte) (name string, sensitive bool, ok bool) {
	f, err := parser.ParseFile(path, src)
	if err != nil {
		return "", false, false
	}
	metadata, ok := configTemplateFields(f)
	if !ok {
		return "", false, false
	}
	if n, found := fieldIn(metadata, "name"); found {
		if lit, isLit := n.Value.(*ast.BasicLit); isLit && lit.Kind == token.STRING {
			name, _ = literal.Unquote(lit.Value)
		}
	}
	if s, found := fieldIn(metadata, "sensitive"); found {
		if lit, isLit := s.Value.(*ast.BasicLit); isLit {
			sensitive = lit.Value == "true"
		}
	}
	return name, sensitive, name != ""
}

// parameterIn is the template's closed parameter, built in ctx.
func (t ConfigTemplate) parameterIn(ctx *cue.Context) (cue.Value, bool) {
	path := t.Path
	if path == "" {
		path = t.Where + "/" + t.Name + ".cue"
	}
	d := &document{path: path, src: []byte(t.CUE)}
	if _, ok := d.parse(); !ok {
		return cue.Value{}, false
	}
	c, _, _ := d.addonCompile("", addonKindConfig)
	v, _ := d.build(ctx, c.pkgs.imports(), c.extra+closedParameterPath+": template.parameter\n", c.files...)
	param := v.LookupPath(cue.MakePath(cue.Def(closedParameterPath)))
	return param, param.Exists()
}

// CheckConfigFile checks each Config in a YAML stream: against the Config
// KubeVela accepts, and its properties against the parameter of the
// template it names, from opts' ConfigTemplates. It is false when the
// stream holds no Config.
func CheckConfigFile(path string, src []byte, opts Options) ([]Diagnostic, bool) {
	d := &document{path: path, src: src, opts: opts}
	f, err := d.extractYAML()
	if err != nil {
		return nil, false
	}
	ctx := cuecontext.New()
	data := ctx.BuildFile(f)
	if data.Err() != nil {
		return nil, false
	}
	docs, nodes := yamlDocuments(f, data)
	// The checked syntax drops null fields, as Kubernetes does; a null is a
	// placeholder here, read from the raw syntax.
	var rawNodes []ast.Node
	if raw, err := cueyaml.Extract(path, src); err == nil {
		_, rawNodes = yamlDocuments(raw, ctx.BuildFile(raw))
	}
	found := false
	var diags []Diagnostic
	for i, doc := range docs {
		if !isConfig(doc) {
			continue
		}
		found = true
		nulls := map[string]*ast.Field{}
		if i < len(rawNodes) {
			for p, field := range yamlFields(rawNodes[i]) {
				if lit, ok := field.Value.(*ast.BasicLit); ok && lit.Kind == token.NULL {
					nulls[p] = field
				}
			}
		}
		diags = append(diags, d.checkConfig(ctx, doc, yamlFields(nodes[i]), nulls)...)
	}
	return sortDiagnostics(firstPerPosition(diags)), found
}

// yamlDocuments are the documents of a YAML stream, each with its syntax.
func yamlDocuments(f *ast.File, data cue.Value) ([]cue.Value, []ast.Node) {
	it, err := data.List()
	if err != nil {
		return []cue.Value{data}, []ast.Node{f}
	}
	var docs []cue.Value
	for it.Next() {
		docs = append(docs, it.Value())
	}
	var nodes []ast.Node
	for _, decl := range f.Decls {
		if e, ok := decl.(*ast.EmbedDecl); ok {
			if l, ok := e.Expr.(*ast.ListLit); ok {
				for _, el := range l.Elts {
					nodes = append(nodes, el)
				}
			}
		}
	}
	for len(nodes) < len(docs) {
		nodes = append(nodes, f)
	}
	return docs, nodes
}

// checkConfig checks one Config, its fields' syntax at hand.
func (d *document) checkConfig(ctx *cue.Context, config cue.Value, fields, nulls map[string]*ast.Field) []Diagnostic {
	schema := ctx.CompileString(configCUE).LookupPath(cue.ParsePath("#config"))
	diags := d.fromErrors(schema.Unify(config).Validate(), "#config")
	if name, err := config.LookupPath(cue.ParsePath("metadata.name")).String(); err == nil && (len(name) > 63 || !dnsLabel.MatchString(name)) {
		if f, ok := fields["metadata.name"]; ok {
			diags = append(diags, Diagnostic{Range: d.scalarRange(f.Value.Pos()), Severity: SeverityError, Message: "metadata.name names the config's Secret: lower case letters, digits and hyphens, at most 63"})
		}
	}
	props := config.LookupPath(cue.ParsePath("spec.properties"))
	from := config.LookupPath(cue.ParsePath("spec.propertiesFrom"))
	if props.Exists() && from.Exists() {
		if r, ok := fieldRange(fields, "spec.propertiesFrom"); ok {
			diags = append(diags, Diagnostic{Range: r, Severity: SeverityError, Message: "a Config takes its values from properties or propertiesFrom, not both"})
		}
	}
	return append(diags, d.checkConfigProperties(ctx, config, props, from.Exists(), fields, nulls)...)
}

// checkConfigProperties checks a Config's properties against the parameter
// of the template it names.
func (d *document) checkConfigProperties(ctx *cue.Context, config, props cue.Value, fromSecret bool, fields, nulls map[string]*ast.Field) []Diagnostic {
	name, err := config.LookupPath(cue.ParsePath("spec.templateRef.name")).String()
	ref, written := fields["spec.templateRef.name"]
	if err != nil || !written {
		return nil
	}
	refAt := d.scalarRange(ref.Value.Pos())
	t, ok := d.opts.ConfigTemplates[name]
	if !ok {
		where, severity := "in the workspace", SeverityWarning
		if d.opts.ConfigTemplatesFromCluster {
			where, severity = "in the workspace or on the cluster", SeverityError
		}
		diag := Diagnostic{Range: refAt, Severity: severity, Message: fmt.Sprintf("no config template named %s %s", name, where)}
		names := make([]string, 0, len(d.opts.ConfigTemplates))
		for n := range d.opts.ConfigTemplates {
			names = append(names, n)
		}
		if to := closest(name, names); to != "" {
			diag.Fixes = []Fix{{Title: "Change to " + to, Edits: []RangeEdit{{Range: refAt, NewText: to}}}}
		}
		return []Diagnostic{diag}
	}
	var diags []Diagnostic
	if t.Sensitive && props.Exists() {
		if r, ok := fieldRange(fields, "spec.properties"); ok {
			diags = append(diags, Diagnostic{Range: r, Severity: SeverityWarning, Message: fmt.Sprintf("%s is sensitive: this file holds its values in plain text; keep them in a Secret named by propertiesFrom.secretRef", name)})
		}
	}
	// Values kept in a Secret are not in the file to check.
	if fromSecret && !props.Exists() {
		return diags
	}
	param, ok := t.parameterIn(ctx)
	if !ok {
		return diags
	}
	label := "config template " + name
	placeholders, filling := d.placeholders(param, label, nulls)
	diags = append(diags, placeholders...)
	if props.Exists() {
		diags = append(diags, d.unknownProperties(props, param, label, "spec.properties", nil, fields)...)
		for _, diag := range d.fromErrors(param.Unify(props).Validate(), closedParameterPath) {
			if !strings.HasSuffix(diag.Message, "field not allowed") {
				diags = append(diags, diag)
			}
		}
	}
	if missing := without(requiredMissing(param, props), filling); len(missing) > 0 {
		diags = append(diags, Diagnostic{Range: refAt, Severity: SeverityError, Message: fmt.Sprintf("%s requires %s in properties", label, strings.Join(missing, ", "))})
	}
	return diags
}

// Parameter is the template's closed parameter.
func (t ConfigTemplate) Parameter() (cue.Value, bool) {
	return t.parameterIn(cuecontext.New())
}

// RequiredParameters are the parameters a value must give: declared, not
// optional, with no default.
func RequiredParameters(param cue.Value) []string {
	return requiredMissing(param, param.Context().CompileString("{}"))
}

// NewConfigTemplate is a config template to start from: a Secret holding
// an endpoint and an optional token.
func NewConfigTemplate(name, alias, description, scope string, sensitive bool) string {
	var b strings.Builder
	b.WriteString("metadata: {\n")
	fmt.Fprintf(&b, "\tname: %q\n", name)
	if alias != "" {
		fmt.Fprintf(&b, "\talias: %q\n", alias)
	}
	if description != "" {
		fmt.Fprintf(&b, "\tdescription: %q\n", description)
	}
	fmt.Fprintf(&b, "\tscope: %q\n", scope)
	fmt.Fprintf(&b, "\t// A sensitive config's values are never read back, by the API or a workflow step.\n\tsensitive: %t\n", sensitive)
	b.WriteString(`}

template: {
	// A config is kept as this Secret; outputs may add other objects.
	output: {
		apiVersion: "v1"
		kind:       "Secret"
		metadata: {
			name:      context.name
			namespace: context.namespace
		}
		type: "Opaque"
		stringData: {
			endpoint: parameter.endpoint
			if parameter.token != _|_ {
				token: parameter.token
			}
		}
	}

	parameter: {
		// +usage=Where the service is, such as https://example.com
		endpoint: string
		// +usage=The token to reach it with
		token?: string
	}
}
`)
	out, err := format.Source([]byte(b.String()))
	if err != nil {
		return b.String()
	}
	return string(out)
}

// propertiesPrefix is where a Config's values are, as yamlFields paths them.
const propertiesPrefix = "spec.properties."

// placeholders reports each value written as null under the properties, a
// placeholder the parameter does not take, at its field: what is still to
// fill in. It also names the top-level ones, which are not missing.
func (d *document) placeholders(param cue.Value, label string, nulls map[string]*ast.Field) ([]Diagnostic, map[string]bool) {
	var diags []Diagnostic
	filling := map[string]bool{}
	for p, field := range nulls {
		name, ok := strings.CutPrefix(p, propertiesPrefix)
		if !ok {
			continue
		}
		want := param
		for _, seg := range strings.Split(name, ".") {
			want = schemaChild(want, cue.Str(seg))
		}
		if !want.Exists() || want.Unify(want.Context().CompileString("null")).Err() == nil {
			continue
		}
		filling[strings.Split(name, ".")[0]] = true
		diags = append(diags, Diagnostic{Range: span(field.Label.Pos(), field.Label.End()), Severity: SeverityError, Message: fmt.Sprintf("%s is still to fill in: %s requires a %s", name, label, kindName(want))})
	}
	return diags, filling
}

// without is names less those in leave.
func without(names []string, leave map[string]bool) []string {
	var out []string
	for _, n := range names {
		if !leave[n] {
			out = append(out, n)
		}
	}
	return out
}
