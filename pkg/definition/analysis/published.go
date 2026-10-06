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
	"cuelang.org/go/cue/build"
	"cuelang.org/go/cue/cuecontext"
	"cuelang.org/go/cue/parser"
	"cuelang.org/go/cue/token"
)

// Published is what a global, Application-scoped policy publishes in its
// output.ctx: KubeVela carries it into every render that follows as
// context.custom.
type Published struct {
	Policy string
	Path   string
	Fields []ContextField
}

// PublishedContext reads the ctx a policy definition publishes, when it is a
// global policy scoped to the Application and declares one.
func PublishedContext(path string, src []byte) (Published, bool) {
	f, err := parser.ParseFile(path, src, parser.ParseComments)
	if err != nil {
		return Published{}, false
	}
	d, ok := newDocument(path, src, f)
	if !ok || d.typ != policyType || d.headerString("attributes", "scope") != "Application" || !d.headerBool("attributes", "global") {
		return Published{}, false
	}
	ctx := d.publishedValue()
	if !ctx.Exists() {
		return Published{}, false
	}
	it, err := ctx.Fields()
	if err != nil {
		return Published{}, false
	}
	p := Published{Policy: d.name, Path: path}
	for it.Next() {
		p.Fields = append(p.Fields, ContextField{
			Name: it.Selector().Unquoted(),
			Type: typeName(it.Value()),
			Doc:  fmt.Sprintf("Published by the global policy %s, when it applies.", d.name),
		})
	}
	sort.Slice(p.Fields, func(i, j int) bool { return p.Fields[i].Name < p.Fields[j].Name })
	return p, len(p.Fields) > 0
}

// publishedValue is the template's output.ctx, evaluated with an open
// context, so a value read from a parameter takes the parameter's type.
func (d *document) publishedValue() cue.Value {
	tmpl := d.template.Value.(*ast.StructLit)
	tmpl.Elts = append([]ast.Decl{mustField("context: {...}")}, tmpl.Elts...)
	decls := make([]ast.Decl, 0, len(d.imports)+1)
	for _, imp := range d.imports {
		decls = append(decls, imp)
	}
	decls = append(decls, d.template)
	bi := build.NewContext().NewInstance(d.path, nil)
	bi.Imports = d.packages().imports()
	if err := bi.AddSyntax(&ast.File{Filename: d.path, Decls: decls}); err != nil {
		return cue.Value{}
	}
	return cuecontext.New().BuildInstance(bi).LookupPath(cue.ParsePath(templateLabel + ".output.ctx"))
}

// typeName is a value's type as CUE: its kind, or a struct's fields' types.
func typeName(v cue.Value) string {
	switch v.IncompleteKind() {
	case cue.StringKind:
		return "string"
	case cue.IntKind:
		return "int"
	case cue.FloatKind, cue.NumberKind:
		return "number"
	case cue.BoolKind:
		return "bool"
	case cue.ListKind:
		return "[...]"
	case cue.StructKind:
		it, err := v.Fields()
		if err != nil {
			return "{...}"
		}
		var fields []string
		for it.Next() {
			fields = append(fields, it.Selector().Unquoted()+": "+typeName(it.Value()))
		}
		return "{" + strings.Join(fields, ", ") + "}"
	}
	return "_"
}

// headerBool is the bool value at a path in the header.
func (d *document) headerBool(path ...string) bool {
	f := d.headers[0]
	for _, p := range path {
		child, ok := fieldIn(f, p)
		if !ok {
			return false
		}
		f = child
	}
	lit, ok := f.Value.(*ast.BasicLit)
	if !ok || (lit.Kind != token.TRUE && lit.Kind != token.FALSE) {
		return false
	}
	b, _ := strconv.ParseBool(lit.Value)
	return b
}
