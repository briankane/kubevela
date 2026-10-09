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
	"sort"
	"strconv"
	"strings"

	"cuelang.org/go/cue"
	"cuelang.org/go/cue/ast"
	"cuelang.org/go/cue/cuecontext"
	"cuelang.org/go/cue/parser"
	"cuelang.org/go/cue/token"

	"github.com/oam-dev/kubevela/pkg/definition/propexpr"
)

// applicationCUE is the Application KubeVela accepts.
const applicationCUE = `
#app: #addonApplication & {
	apiVersion?: =~"^core.oam.dev/"
	kind?:       "Application"
}
`

// appItems are the lists of an Application's spec whose items name a
// definition, and that definition's type.
var appItems = []struct {
	path    string
	defType string
}{
	{"spec.components", componentType},
	{"spec.policies", policyType},
	{"spec.workflow.steps", workflowStepType},
	{"spec.sources", sourceType},
}

// isApplication reports whether a YAML document is a KubeVela Application.
func isApplication(doc cue.Value) bool {
	apiVersion, _ := doc.LookupPath(cue.ParsePath("apiVersion")).String()
	kind, _ := doc.LookupPath(cue.ParsePath("kind")).String()
	return applicationKind(apiVersion, kind)
}

// applicationKind reports whether an apiVersion and kind are an Application's.
func applicationKind(apiVersion, kind string) bool {
	return kind == "Application" && strings.HasPrefix(apiVersion, "core.oam.dev/")
}

// CheckApplicationFile checks each Application in a YAML stream: against
// the Application KubeVela accepts, and each component, trait, policy and
// workflow step against the definition its type names, from opts'
// Applications. It is false when the stream holds no Application.
func CheckApplicationFile(path string, src []byte, opts Options) ([]Diagnostic, bool) {
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
	found := false
	var diags []Diagnostic
	for i, doc := range docs {
		if !isApplication(doc) {
			continue
		}
		found = true
		diags = append(diags, d.checkApplication(ctx, doc, yamlFields(nodes[i]))...)
	}
	return sortDiagnostics(firstPerPosition(diags)), found
}

// yamlFields indexes the fields of a YAML document's syntax by their path:
// labels joined by dots, list items by their index.
func yamlFields(n ast.Node) map[string]*ast.Field {
	out := map[string]*ast.Field{}
	var walk func(n ast.Node, prefix string)
	walk = func(n ast.Node, prefix string) {
		join := func(s string) string {
			if prefix == "" {
				return s
			}
			return prefix + "." + s
		}
		switch x := n.(type) {
		case *ast.File:
			for _, d := range x.Decls {
				walk(d, prefix)
			}
		case *ast.EmbedDecl:
			walk(x.Expr, prefix)
		case *ast.StructLit:
			for _, e := range x.Elts {
				walk(e, prefix)
			}
		case *ast.Field:
			p := join(labelName(x.Label))
			out[p] = x
			walk(x.Value, p)
		case *ast.ListLit:
			for i, e := range x.Elts {
				walk(e, join(strconv.Itoa(i)))
			}
		}
	}
	walk(n, "")
	return out
}

// checkApplication checks one Application, its fields' syntax at hand.
func (d *document) checkApplication(ctx *cue.Context, app cue.Value, fields map[string]*ast.Field) []Diagnostic {
	schema := ctx.CompileString(addonApplicationCUE + applicationCUE).LookupPath(cue.ParsePath("#app"))
	diags := d.fromErrors(schema.Unify(app).Validate(), "#app")
	diags = append(diags, d.checkTyped(app.LookupPath(cue.ParsePath("spec")))...)
	diags = append(diags, d.checkApplicationGates(app, fields)...)
	if d.opts.Applications == nil {
		return diags
	}
	diags = append(diags, d.checkTraits(app, fields)...)
	diags = append(diags, d.checkExpressions(app, fields)...)
	for _, list := range appItems {
		items, err := app.LookupPath(cue.ParsePath(list.path)).List()
		if err != nil {
			continue
		}
		for i := 0; items.Next(); i++ {
			at := fmt.Sprintf("%s.%d", list.path, i)
			diags = append(diags, d.checkAppItem(ctx, items.Value(), at, list.defType, fields)...)
			nested := map[string]string{componentType: "traits", workflowStepType: "subSteps"}[list.defType]
			if nested == "" {
				continue
			}
			nestedType := map[string]string{componentType: traitType, workflowStepType: workflowStepType}[list.defType]
			sub, err := items.Value().LookupPath(cue.ParsePath(nested)).List()
			if err != nil {
				continue
			}
			for j := 0; sub.Next(); j++ {
				diags = append(diags, d.checkAppItem(ctx, sub.Value(), fmt.Sprintf("%s.%s.%d", at, nested, j), nestedType, fields)...)
			}
		}
	}
	return diags
}

// fieldRange is the range of the label of the field at path, if written.
func fieldRange(fields map[string]*ast.Field, path string) (Range, bool) {
	f, ok := fields[path]
	if !ok {
		return Range{}, false
	}
	return span(f.Label.Pos(), f.Label.End()), true
}

// checkAppItem checks a component, trait, policy or workflow step at path
// against the definition its type names.
func (d *document) checkAppItem(ctx *cue.Context, item cue.Value, path, defType string, fields map[string]*ast.Field) []Diagnostic {
	name, err := item.LookupPath(cue.ParsePath("type")).String()
	typeField, written := fields[path+".type"]
	if err != nil || !written {
		return nil
	}
	typeAt := d.scalarRange(typeField.Value.Pos())
	def, ok := d.opts.Applications.Lookup(defType, name)
	if !ok {
		where := "in the workspace or built in"
		severity := SeverityWarning
		if d.opts.ClusterRead {
			where, severity = "in the workspace, on the cluster or built in", SeverityError
		}
		diag := Diagnostic{Range: typeAt, Severity: severity, Message: fmt.Sprintf("no %s definition named %s %s", defType, name, where)}
		var names []string
		for _, other := range d.opts.Applications.List(defType) {
			names = append(names, other.Name)
		}
		if to := closest(name, names); to != "" {
			diag.Fixes = []Fix{{Title: "Change to " + to, Edits: []RangeEdit{{Range: typeAt, NewText: to}}}}
		}
		return []Diagnostic{diag}
	}
	param, ok := def.parameterIn(ctx, d.opts)
	if !ok {
		return nil
	}
	props := item.LookupPath(cue.ParsePath("properties"))
	var diags []Diagnostic
	if props.Exists() {
		diags = append(diags, d.unknownProperties(props, param, def.Name, path+".properties", nil, fields)...)
		exprs := expressionLiterals(props)
		for _, diag := range d.fromErrors(param.Unify(props).Validate(), closedParameterPath) {
			if !strings.HasSuffix(diag.Message, "field not allowed") && !aboutAny(diag.Message, exprs) {
				diags = append(diags, diag)
			}
		}
	}
	if missing := requiredMissing(param, props); len(missing) > 0 {
		diag := Diagnostic{Range: typeAt, Severity: SeverityError, Message: fmt.Sprintf("%s requires %s in properties", def.Name, strings.Join(missing, ", "))}
		if fix, ok := d.addPropertiesFix(fields, path, missing); ok {
			diag.Fixes = []Fix{fix}
		}
		diags = append(diags, diag)
	}
	return diags
}

// unknownProperties reports each property the definition's parameter does
// not take, at any depth written as a mapping.
func (d *document) unknownProperties(props, param cue.Value, name, path string, under []string, fields map[string]*ast.Field) []Diagnostic {
	it, err := props.Fields()
	if err != nil || param.IncompleteKind() != cue.StructKind {
		return nil
	}
	var diags []Diagnostic
	for it.Next() {
		key := it.Selector().Unquoted()
		at := strings.Join(append(append([]string{}, under...), key), ".")
		if !param.Allows(cue.Str(key)) {
			r, ok := fieldRange(fields, path+"."+key)
			if !ok {
				continue
			}
			diag := Diagnostic{Range: r, Severity: SeverityError, Message: fmt.Sprintf("%s takes no parameter %s", name, at)}
			if to := closest(key, fieldNames(param)); to != "" {
				diag.Fixes = []Fix{{Title: "Change to " + to, Edits: []RangeEdit{{Range: r, NewText: to}}}}
			}
			diags = append(diags, diag)
			continue
		}
		child := schemaChild(param, cue.Str(key))
		value := it.Value()
		if value.IncompleteKind() == cue.ListKind && child.IncompleteKind() == cue.ListKind {
			elems, _ := value.List()
			element := child.LookupPath(cue.MakePath(cue.AnyIndex))
			for i := 0; elems.Next(); i++ {
				diags = append(diags, d.unknownProperties(elems.Value(), element, name, fmt.Sprintf("%s.%s.%d", path, key, i), append(append([]string{}, under...), key), fields)...)
			}
			continue
		}
		diags = append(diags, d.unknownProperties(value, child, name, path+"."+key, append(append([]string{}, under...), key), fields)...)
	}
	return diags
}

// requiredMissing are the parameters a definition requires that props does
// not give: declared, not optional, with no default and no value of their
// own.
func requiredMissing(param, props cue.Value) []string {
	it, err := param.Fields(cue.Optional(true))
	if err != nil {
		return nil
	}
	var missing []string
	for it.Next() {
		if it.IsOptional() || it.Value().IsConcrete() {
			continue
		}
		if _, ok := it.Value().Default(); ok {
			continue
		}
		if !props.LookupPath(cue.MakePath(it.Selector())).Exists() {
			missing = append(missing, it.Selector().Unquoted())
		}
	}
	sort.Strings(missing)
	return missing
}

// addPropertiesFix adds the names given under the item's properties,
// writing properties after its type when it has none.
func (d *document) addPropertiesFix(fields map[string]*ast.Field, path string, names []string) (Fix, bool) {
	fix := Fix{Title: "Add the required properties"}
	if props, ok := fields[path+".properties"]; ok {
		s, ok := props.Value.(*ast.StructLit)
		if !ok || len(s.Elts) == 0 {
			return Fix{}, false
		}
		first := s.Elts[0].Pos()
		indent := strings.Repeat(" ", first.Column()-1)
		var text strings.Builder
		for _, n := range names {
			text.WriteString(indent + n + ": \n")
		}
		at := Position{Line: first.Line(), Column: 1}
		fix.Edits = []RangeEdit{{Range: Range{Start: at, End: at}, NewText: text.String()}}
		return fix, true
	}
	typeField, ok := fields[path+".type"]
	if !ok {
		return Fix{}, false
	}
	indent := strings.Repeat(" ", typeField.Label.Pos().Column()-1)
	var text strings.Builder
	text.WriteString(indent + "properties:\n")
	for _, n := range names {
		text.WriteString(indent + "  " + n + ": \n")
	}
	at := Position{Line: typeField.Label.Pos().Line() + 1, Column: 1}
	fix.Edits = []RangeEdit{{Range: Range{Start: at, End: at}, NewText: text.String()}}
	return fix, true
}

// parameterIn is the closed parameter of the definition, built in ctx.
func (a AppDefinition) parameterIn(ctx *cue.Context, opts Options) (cue.Value, bool) {
	path := a.Path
	if path == "" {
		path = a.Source + "/" + a.Name + ".cue"
	}
	f, err := parser.ParseFile(path, a.CUE, parser.ParseComments)
	if err != nil {
		return cue.Value{}, false
	}
	d, ok := newDocument(path, []byte(a.CUE), f)
	if !ok {
		return cue.Value{}, false
	}
	d.opts = Options{Externals: opts.Externals}
	v, ok := d.evaluateIn(ctx)
	if !ok {
		return cue.Value{}, false
	}
	params := v.LookupPath(cue.MakePath(cue.Def(closedParameterPath)))
	return params, params.Exists()
}

// scalarRange is the range of the YAML scalar written at pos, to the end of
// its line or a comment: the syntax of a scalar read from YAML ends past it.
func (d *document) scalarRange(pos token.Pos) Range {
	lines := strings.Split(string(d.src), "\n")
	line, col := pos.Line(), pos.Column()
	if line < 1 || line > len(lines) || col < 1 || col > len(lines[line-1])+1 {
		return span(pos, pos)
	}
	text := lines[line-1][col-1:]
	if i := strings.Index(text, " #"); i >= 0 {
		text = text[:i]
	}
	text = strings.TrimRight(text, " \t\r")
	return Range{Start: Position{Line: line, Column: col}, End: Position{Line: line, Column: col + len(text)}}
}

// expressionLiterals are the property values holding $(...) expressions,
// quoted as CUE quotes them in an error. Admission substitutes such a value
// before the parameter sees it, so its type is checkExpressions' to judge.
func expressionLiterals(props cue.Value) []string {
	var data interface{}
	if props.Decode(&data) != nil {
		return nil
	}
	var out []string
	walkStrings(data, "", func(_, raw string) {
		if parsed, err := propexpr.Parse(raw); err == nil && parsed.HasExpr() {
			out = append(out, strconv.Quote(raw))
		}
	})
	return out
}

// aboutAny reports whether a message names any of the quoted values.
func aboutAny(msg string, quoted []string) bool {
	for _, q := range quoted {
		if strings.Contains(msg, q) {
			return true
		}
	}
	return false
}
