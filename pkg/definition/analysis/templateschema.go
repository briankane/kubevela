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

// requiredFields are the fields a template of a type must declare.
var requiredFields = map[string][]string{
	componentType: {"output"},
	sourceType:    {"schema", "output"},
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

// checkTemplateFields reports a field the template's type needs and it does
// not declare, and one it declares that its type never reads.
func (d *document) checkTemplateFields() []Diagnostic {
	kind := d.templateKind()
	declared := map[string]*ast.Field{}
	topLevelFields(d.template.Value, declared)
	var diags []Diagnostic

	var missing []string
	for _, name := range requiredFields[kind] {
		if declared[name] == nil {
			missing = append(missing, name)
		}
	}
	// A component that extends another inherits its output.
	if kind == componentType && (d.headerString("extends") != "" || d.headerString("attributes", "extends") != "") {
		missing = nil
	}
	if len(missing) > 0 {
		diags = append(diags, d.at(d.template.Label.Pos(),
			fmt.Sprintf("a %s's template must declare %s", typeNames[kind], strings.Join(missing, " and "))))
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
			diag := d.at(f.Label.Pos(), fmt.Sprintf("%s has no effect in a %s's template", name, typeNames[kind]))
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
