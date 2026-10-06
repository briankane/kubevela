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
	"strings"

	"cuelang.org/go/cue/ast"
)

// checkUsage recommends a +usage marker on each field under parameter that
// has none: it is what `vela show`, generated docs and VelaUX describe the
// parameter with.
func (d *document) checkUsage() []Diagnostic {
	param, ok := fieldIn(d.template, parameterLabel)
	if !ok {
		return nil
	}
	var diags []Diagnostic
	var visit func(n ast.Node)
	visit = func(n ast.Node) {
		switch x := n.(type) {
		case *ast.StructLit:
			for _, elt := range x.Elts {
				f, ok := elt.(*ast.Field)
				if !ok {
					continue
				}
				name := labelName(f.Label)
				if _, isIdent := f.Label.(*ast.Ident); isIdent && (strings.HasPrefix(name, "_") || strings.HasPrefix(name, "#")) {
					continue
				}
				if _, pattern := f.Label.(*ast.ListLit); pattern {
					continue
				}
				if !hasUsage(f) {
					diag := d.at(f.Label.Pos(), name+" has no +usage: add `// +usage=...` above it to describe it in vela show, docs and VelaUX")
					diag.Severity = SeverityInfo
					diags = append(diags, diag)
				}
				visit(f.Value)
			}
		case *ast.ListLit:
			for _, elt := range x.Elts {
				if e, ok := elt.(*ast.Ellipsis); ok {
					visit(e.Type)
				} else {
					visit(elt)
				}
			}
		case *ast.BinaryExpr:
			visit(x.X)
			visit(x.Y)
		case *ast.UnaryExpr:
			visit(x.X)
		}
	}
	visit(param.Value)
	return diags
}

func hasUsage(f *ast.Field) bool {
	for _, cg := range ast.Comments(f) {
		for _, c := range cg.List {
			if m := markerLine.FindStringSubmatch(strings.TrimSpace(c.Text)); m != nil && m[1] == "usage" {
				return true
			}
		}
	}
	return false
}
