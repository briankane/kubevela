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

package preview

import (
	"context"
	"fmt"
	"strings"

	"cuelang.org/go/cue"
	"cuelang.org/go/cue/ast"
	"cuelang.org/go/cue/format"
	"cuelang.org/go/cue/parser"

	"github.com/oam-dev/kubevela/pkg/definition/analysis"
)

// Evaluation is the value of an expression of a template.
type Evaluation struct {
	// Value is the value as CUE: a concrete value, or what is known of it.
	Value string `json:"value"`
	// Concrete is whether the values made it a concrete value.
	Concrete bool `json:"concrete"`
}

// evalLabel names the field an evaluated expression is put in; no template uses it.
const evalLabel = "vela_evaluated_selection"

// Evaluate is the value of the expression between byte offsets start and end
// of the definition in req, with req.Values: the expression is put in a field
// of the struct it is written in, so it reads what the template there reads.
func Evaluate(_ context.Context, req Request, start, end int) (Evaluation, error) {
	if start < 0 || end > len(req.Source) || start >= end {
		return Evaluation{}, fmt.Errorf("select an expression to evaluate")
	}
	f, tmpl, ok := analysis.TemplateFile(req.Path, req.Source)
	if !ok {
		return Evaluation{}, fmt.Errorf("%s is not a definition, or does not parse", req.Path)
	}
	expr, err := parser.ParseExpr("selection", req.Source[start:end])
	if err != nil {
		return Evaluation{}, fmt.Errorf("the selection is not an expression: %w", err)
	}
	path, target, err := enclosingStruct(f, start, end)
	if err != nil {
		return Evaluation{}, err
	}
	field := &ast.Field{Label: ast.NewIdent(evalLabel), Value: expr}
	if target == nil {
		f.Decls = append(f.Decls, field)
	} else {
		target.Elts = append(target.Elts, field)
	}
	root, _, err := buildTemplate(f, tmpl, req.Values, "")
	if err != nil {
		return Evaluation{}, err
	}
	v := root.LookupPath(cue.MakePath(append(path, cue.Str(evalLabel))...))
	if err := v.Err(); err != nil {
		return Evaluation{}, err
	}
	b, err := format.Node(v.Syntax(cue.Final(), cue.Docs(false)))
	if err != nil {
		return Evaluation{}, err
	}
	return Evaluation{Value: strings.TrimSpace(string(b)), Concrete: v.Validate(cue.Concrete(true)) == nil}, nil
}

// enclosingStruct is the innermost struct of the template the span is in,
// and its path; no struct for the template's own fields.
func enclosingStruct(f *ast.File, start, end int) ([]cue.Selector, *ast.StructLit, error) {
	within := func(n ast.Node) bool { return start >= n.Pos().Offset() && end <= n.End().Offset() }
	inTemplate := false
	for _, d := range f.Decls {
		if _, isImport := d.(*ast.ImportDecl); !isImport && within(d) {
			inTemplate = true
		}
	}
	if !inTemplate {
		return nil, nil, fmt.Errorf("select an expression in the template")
	}
	var path []cue.Selector
	var target *ast.StructLit
	decls := f.Decls
	for {
		var next []ast.Decl
		for _, d := range decls {
			if !within(d) {
				continue
			}
			if _, ok := d.(*ast.Comprehension); ok {
				return nil, nil, fmt.Errorf("the selection is in a comprehension: its variables have no value outside it")
			}
			field, ok := d.(*ast.Field)
			if !ok {
				break
			}
			name, _, err := ast.LabelName(field.Label)
			if err != nil {
				return nil, nil, fmt.Errorf("the selection is under a field whose name is computed")
			}
			switch v := field.Value.(type) {
			case *ast.StructLit:
				if within(v) {
					path, target, next = append(path, selector(name)), v, v.Elts
				}
			case *ast.ListLit:
				for i, e := range v.Elts {
					if !within(e) {
						continue
					}
					switch el := e.(type) {
					case *ast.StructLit:
						path, target, next = append(path, selector(name), cue.Index(i)), el, el.Elts
					case *ast.Comprehension:
						return nil, nil, fmt.Errorf("the selection is in a comprehension: its variables have no value outside it")
					}
				}
			}
			break
		}
		if next == nil {
			return path, target, nil
		}
		decls = next
	}
}

// selector is the path step of a label: hidden, a definition, or a regular field.
func selector(name string) cue.Selector {
	switch {
	case strings.HasPrefix(name, "_"):
		return cue.Hid(name, "_")
	case strings.HasPrefix(name, "#"):
		return cue.Def(name)
	}
	return cue.Str(name)
}
