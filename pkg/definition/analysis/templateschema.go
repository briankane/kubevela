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
	_ "embed"
	"fmt"
	"strings"
	"sync"

	"cuelang.org/go/cue"
	"cuelang.org/go/cue/ast"
	"cuelang.org/go/cue/cuecontext"
	"cuelang.org/go/cue/literal"
	"cuelang.org/go/cue/parser"
	"cuelang.org/go/cue/token"

	"github.com/oam-dev/kubevela/apis/core.oam.dev/v1alpha1"
)

//go:embed template.cue
var templateSchemaSource []byte

// templateSchema is the declarations of #velaTemplates.
var templateSchema = sync.OnceValue(func() []ast.Decl {
	f, err := parser.ParseFile("template.cue", templateSchemaSource)
	if err != nil {
		panic(fmt.Sprintf("embedded template.cue: %v", err))
	}
	return f.Decls
})

const applicationPolicy = "application-policy"

// templateKind is the entry of #velaTemplates the template is checked
// against, or "" when its type has none.
func (d *document) templateKind() string {
	switch d.typ {
	case componentType, traitType, workflowStepType, sourceType, workloadType:
		return d.typ
	case policyType:
		if d.headerString("attributes", "scope") == "Application" {
			return applicationPolicy
		}
		return policyType
	}
	return ""
}

// templateConstraint unifies the template with its type's schema, and its
// output with the schema it declares for it.
func (d *document) templateConstraint() []ast.Decl {
	var decls []ast.Decl
	declared := map[string]*ast.Field{}
	topLevelFields(d.template.Value, declared)
	if declared["schema"] != nil && declared["output"] != nil {
		decls = append(decls, unbound(mustField(fmt.Sprintf("%s: output: %s.schema", templateLabel, templateLabel))))
	}
	if kind := d.templateKind(); kind != "" {
		decls = append(decls, mustField(fmt.Sprintf("%s: #velaTemplates[%q]", templateLabel, kind)))
		decls = append(decls, templateSchema()...)
	}
	return decls
}

// templateSchemaFor is the compiled schema of a template kind.
func templateSchemaFor(kind string) (cue.Value, bool) {
	if kind == "" {
		return cue.Value{}, false
	}
	v := cuecontext.New().BuildFile(&ast.File{Decls: append([]ast.Decl{mustField(fmt.Sprintf("#this: #velaTemplates[%q]", kind))}, templateSchema()...)})
	this := v.LookupPath(cue.ParsePath("#this"))
	return this, this.Exists()
}

// requireNested reports a struct field, written as a struct, that does not
// declare each of the fields named.
func (d *document) requireNested(f *ast.Field, label string, fields ...string) []Diagnostic {
	if f == nil {
		return nil
	}
	s, ok := f.Value.(*ast.StructLit)
	if !ok || hasEmbedding(s) {
		return nil
	}
	declared := map[string]*ast.Field{}
	topLevelFields(s, declared)
	var missing []string
	for _, name := range fields {
		if declared[name] == nil {
			missing = append(missing, name)
		}
	}
	if len(missing) == 0 {
		return nil
	}
	return []Diagnostic{d.at(f.Label.Pos(), fmt.Sprintf("%s must declare %s", label, andList(missing)))}
}

// andList joins words as "a", "a and b", or "a, b and c".
func andList(words []string) string {
	if len(words) <= 1 {
		return strings.Join(words, "")
	}
	return strings.Join(words[:len(words)-1], ", ") + " and " + words[len(words)-1]
}

// unbound clears the references the parser bound within a snippet, so they
// are resolved in the file the snippet is compiled into.
func unbound(f *ast.Field) *ast.Field {
	ast.Walk(f.Value, func(n ast.Node) bool {
		if id, ok := n.(*ast.Ident); ok {
			id.Node, id.Scope = nil, nil
		}
		return true
	}, nil)
	return f
}

// objectFields are the template fields each type renders Kubernetes objects
// from: a single object, or a map of them.
var objectFields = map[string]struct{ single, many bool }{
	componentType: {single: true, many: true},
	traitType:     {many: true},
	policyType:    {single: true, many: true},
}

// checkObjects reports output, or an entry of outputs, written as a struct
// in the template without an apiVersion or kind, so not a Kubernetes object.
// Only what the template spells out is judged: an object read from a
// parameter or a helper, or one with an embedding, is filled in at render.
// A definition that extends another inherits what it leaves out.
func (d *document) checkObjects() []Diagnostic {
	which, ok := objectFields[d.templateKind()]
	if !ok || d.extends() {
		return nil
	}
	var diags []Diagnostic
	// check judges an object from every declaration of it, which a template
	// may split, as in `output: {...}` and later `output: kind: "X"`.
	check := func(path string, decls []*ast.Field) {
		declared := map[string]*ast.Field{}
		for _, f := range decls {
			s, ok := f.Value.(*ast.StructLit)
			if !ok || hasEmbedding(s) {
				return
			}
			topLevelFields(s, declared)
		}
		var missing []string
		for _, name := range []string{"apiVersion", "kind"} {
			if declared[name] == nil {
				missing = append(missing, name)
			}
		}
		if len(missing) > 0 {
			diags = append(diags, d.at(decls[0].Label.Pos(), fmt.Sprintf("%s is not a Kubernetes object: it has no %s", path, strings.Join(missing, " or "))))
		}
	}
	all := map[string][]*ast.Field{}
	allTopLevelFields(d.template.Value, all)
	if decls := all["output"]; which.single && len(decls) > 0 {
		check("output", decls)
	}
	if decls := all["outputs"]; which.many {
		entries := map[string][]*ast.Field{}
		for _, f := range decls {
			allTopLevelFields(f.Value, entries)
		}
		for name, e := range entries {
			check("outputs."+name, e)
		}
	}
	return diags
}

// allTopLevelFields collects every declaration of each field a struct
// declares, including those under an if or for at its top level.
func allTopLevelFields(n ast.Node, into map[string][]*ast.Field) {
	s, ok := n.(*ast.StructLit)
	if !ok {
		return
	}
	for _, elt := range s.Elts {
		switch x := elt.(type) {
		case *ast.Field:
			name := labelName(x.Label)
			into[name] = append(into[name], x)
		case *ast.Comprehension:
			allTopLevelFields(x.Value, into)
		}
	}
}

// hasEmbedding reports whether a struct takes fields from elsewhere: an
// embedded value, at its top level or under an if or for, or a `...`.
func hasEmbedding(s *ast.StructLit) bool {
	for _, elt := range s.Elts {
		switch x := elt.(type) {
		case *ast.EmbedDecl, *ast.Ellipsis:
			return true
		case *ast.Comprehension:
			if body, ok := x.Value.(*ast.StructLit); ok && hasEmbedding(body) {
				return true
			}
		}
	}
	return false
}

// autodetectWorkload is the workload type that takes the workload from the
// output, whatever it is.
const autodetectWorkload = "autodetects.core.oam.dev"

// checkWorkload reports a component whose declared workload is not the kind
// its output renders, unless the workload is detected from the output. An
// output whose kind is not settled, as one from a parameter, is not judged.
func (d *document) checkWorkload(v cue.Value) []Diagnostic {
	if d.typ != componentType || d.extends() || d.headerString("attributes", "workload", "type") == autodetectWorkload {
		return nil
	}
	defPath := []string{"attributes", "workload", "definition"}
	wantAPI, wantKind := d.headerString(append(defPath, "apiVersion")...), d.headerString(append(defPath, "kind")...)
	if wantAPI == "" || wantKind == "" {
		return nil
	}
	settled := func(path string) (string, bool) {
		f := v.LookupPath(cue.ParsePath(templateLabel + ".output." + path))
		if _, hasDefault := f.Default(); hasDefault || !f.IsConcrete() {
			return "", false
		}
		s, err := f.String()
		return s, err == nil
	}
	gotAPI, ok1 := settled("apiVersion")
	gotKind, ok2 := settled("kind")
	if !ok1 || !ok2 || (gotAPI == wantAPI && gotKind == wantKind) {
		return nil
	}
	at := d.headers[0].Label.Pos()
	f := d.headers[0]
	for _, p := range append(defPath, "kind") {
		if child, ok := fieldIn(f, p); ok {
			f, at = child, child.Label.Pos()
		}
	}
	return []Diagnostic{d.at(at, fmt.Sprintf("the workload is %s %s, but output is %s %s: make them match, or set workload: type: %q to take it from the output",
		wantAPI, wantKind, gotAPI, gotKind, autodetectWorkload))}
}

func (d *document) extends() bool {
	return d.headerString("extends") != "" || d.headerString("attributes", "extends") != ""
}

// requiredFields are what a template of a type must declare: each group is
// satisfied by any one of its fields, and is named in a message by its label.
var requiredFields = map[string][]struct {
	any   []string
	label string
}{
	componentType:     {{[]string{"output"}, "output"}},
	traitType:         {{[]string{"patch", "patchOutputs", "outputs"}, "patch or outputs"}},
	policyType:        {{[]string{"output"}, "output"}},
	applicationPolicy: {{[]string{"output"}, "output"}},
	sourceType:        {{[]string{"schema"}, "schema"}, {[]string{"output"}, "output"}, {[]string{"storage"}, "storage"}},
}

// builtinPolicies are the policies KubeVela implements in Go: their
// definitions declare only parameters, and need no output.
var builtinPolicies = map[string]bool{
	v1alpha1.TopologyPolicyType:       true,
	v1alpha1.OverridePolicyType:       true,
	v1alpha1.DebugPolicyType:          true,
	v1alpha1.ReplicationPolicyType:    true,
	v1alpha1.GarbageCollectPolicyType: true,
	v1alpha1.ReadOnlyPolicyType:       true,
	v1alpha1.ResourceUpdatePolicyType: true,
	v1alpha1.TakeOverPolicyType:       true,
	v1alpha1.SharedResourcePolicyType: true,
	v1alpha1.ApplyOncePolicyType:      true,
	v1alpha1.EnvBindingPolicyType:     true,
	// The deprecated definition of env-binding is named without the hyphen.
	"envbinding": true,
}

// unreadFields are the fields a template of a type may declare that its
// type's controller never reads.
var unreadFields = map[string][]string{
	componentType:     {"patch", "patchOutputs", "processing"},
	traitType:         {"output"},
	policyType:        {"patch", "patchOutputs", "$super", "$inherit"},
	applicationPolicy: {"patch", "patchOutputs", "outputs", "$super", "$inherit"},
	workflowStepType:  {"$super", "$inherit"},
	sourceType:        {"patch", "patchOutputs", "$super", "$inherit"},
}

var typeNames = map[string]string{
	componentType:     "component",
	traitType:         "trait",
	policyType:        "policy",
	applicationPolicy: "application-scoped policy",
	workflowStepType:  "workflow step",
	sourceType:        "source",
}

// templateOf names a type's template in a message, with its article.
func templateOf(kind string) string {
	name := typeNames[kind]
	if strings.IndexAny(name[:1], "aeiou") == 0 {
		return "an " + name + "'s template"
	}
	return "a " + name + "'s template"
}

// checkTemplateFields reports a field the template's type needs and it does
// not declare, and one it declares that its type never reads.
func (d *document) checkTemplateFields() []Diagnostic {
	kind := d.templateKind()
	declared := map[string]*ast.Field{}
	topLevelFields(d.template.Value, declared)
	var diags []Diagnostic

	var missing []string
	for _, group := range requiredFields[kind] {
		found := false
		for _, name := range group.any {
			found = found || declared[name] != nil
		}
		if !found {
			missing = append(missing, group.label)
		}
	}
	// A component that extends another inherits its output; KubeVela's own
	// policies are implemented in Go.
	if (kind == componentType && d.extends()) || (kind == policyType && builtinPolicies[d.name]) {
		missing = nil
	}
	if len(missing) > 0 {
		diags = append(diags, d.at(d.template.Label.Pos(),
			fmt.Sprintf("%s must declare %s", templateOf(kind), andList(missing))))
	}
	// A source says how long its value is kept, and what serves it when a
	// refresh fails.
	if kind == sourceType {
		diags = append(diags, d.requireNested(declared["storage"], "storage", "storageTTL", "onStaleFailure")...)
	}
	// A global policy applies to every Application unasked, so nothing can
	// set its parameters.
	if f := declared[parameterLabel]; f != nil && (kind == policyType || kind == applicationPolicy) && d.headerBool("attributes", "global") {
		diags = append(diags, d.at(f.Label.Pos(), "a global policy applies to every Application unasked, so nothing sets its parameters: remove parameter"))
	}
	if f := declared[parameterLabel]; f != nil {
		switch f.Value.(type) {
		case *ast.BasicLit, *ast.ListLit:
			diags = append(diags, d.at(f.Label.Pos(), "parameter must be a struct"))
		}
	}
	if schema, ok := templateSchemaFor(kind); ok {
		diags = append(diags, d.unknownKeys(d.template, schema, nil)...)
	}
	for _, name := range unreadFields[kind] {
		if f := declared[name]; f != nil {
			diag := d.at(f.Label.Pos(), fmt.Sprintf("%s has no effect in %s", name, templateOf(kind)))
			diag.Severity = SeverityWarning
			diags = append(diags, diag)
		}
	}
	return diags
}

// topLevelFields collects the fields a struct declares, including those an
// `if` or `for` at its top level declares.
func topLevelFields(n ast.Node, into map[string]*ast.Field) {
	s, ok := n.(*ast.StructLit)
	if !ok {
		return
	}
	for _, elt := range s.Elts {
		switch x := elt.(type) {
		case *ast.Field:
			name := labelName(x.Label)
			if into[name] == nil {
				into[name] = x
			}
		case *ast.Comprehension:
			topLevelFields(x.Value, into)
		case *ast.EmbedDecl:
			topLevelFields(x.Expr, into)
		}
	}
}

// headerString is the string value at a path in the header, or "".
func (d *document) headerString(path ...string) string {
	f := d.headers[0]
	for _, p := range path {
		child, ok := fieldIn(f, p)
		if !ok {
			return ""
		}
		f = child
	}
	if lit, ok := f.Value.(*ast.BasicLit); ok && lit.Kind == token.STRING {
		s, _ := literal.Unquote(lit.Value)
		return s
	}
	return ""
}
