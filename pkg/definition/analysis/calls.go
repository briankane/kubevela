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
	"cuelang.org/go/cue/token"
)

// checkCalls reports a call of a provider function that leaves out a
// $params it requires: one neither optional nor defaulted, inside a struct of
// declared fields to any depth. CUE says nothing of a missing field until a
// value is concrete. A parameter given by reference, or with an embedding, is
// filled in at render and counts as given.
func (d *document) checkCalls() []Diagnostic {
	pkgs := d.importNames()
	var diags []Diagnostic
	check := func(fn *ast.SelectorExpr, body ast.Expr) {
		root, chain := flatten(fn)
		if root == nil || len(chain) != 1 || pkgs[root.Name] == "" || !strings.HasPrefix(chain[0].Name, "#") {
			return
		}
		pkg, ok := d.packages().value(pkgs[root.Name])
		if !ok {
			return
		}
		params := pkg.LookupPath(cue.MakePath(cue.Def(chain[0].Name), cue.Str("$params")))
		if !params.Exists() {
			return
		}
		given := map[string][]*ast.Field{}
		if s, ok := body.(*ast.StructLit); ok {
			allTopLevelFields(s, given)
		}
		if missing := missingParams(params, given["$params"], "$params"); len(missing) > 0 {
			diags = append(diags, d.at(chain[0].Pos(), fmt.Sprintf("%s.%s needs %s", root.Name, chain[0].Name, strings.Join(missing, ", "))))
		}
	}
	ast.Walk(d.template.Value, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.BinaryExpr:
			if x.Op != token.AND {
				return true
			}
			if fn, ok := x.X.(*ast.SelectorExpr); ok {
				check(fn, x.Y)
			}
		case *ast.Field:
			if fn, ok := x.Value.(*ast.SelectorExpr); ok {
				check(fn, nil)
			}
		}
		return true
	}, nil)
	return diags
}

// missingParams are the required fields of want that the declarations given
// do not write, as paths under prefix.
func missingParams(want cue.Value, given []*ast.Field, prefix string) []string {
	inner := map[string][]*ast.Field{}
	for _, f := range given {
		s, ok := f.Value.(*ast.StructLit)
		if !ok || hasEmbedding(s) {
			// Given by reference or embedding: filled in at render.
			return nil
		}
		allTopLevelFields(s, inner)
	}
	it, err := want.Fields()
	if err != nil {
		return nil
	}
	var missing []string
	for it.Next() {
		f := it.Value()
		if !isRequired(f) {
			continue
		}
		name := it.Selector().Unquoted()
		path := prefix + "." + name
		if f.IncompleteKind() == cue.StructKind && hasDeclaredFields(f) {
			if decls := inner[name]; len(decls) > 0 {
				missing = append(missing, missingParams(f, decls, path)...)
			} else if len(requiredLeaves(f)) > 0 {
				missing = append(missing, path)
			}
			continue
		}
		if len(inner[name]) == 0 {
			missing = append(missing, path)
		}
	}
	return missing
}

// requiredLeaves reports whether a struct of declared fields requires any.
func requiredLeaves(v cue.Value) []string {
	it, err := v.Fields()
	if err != nil {
		return nil
	}
	var out []string
	for it.Next() {
		f := it.Value()
		if !isRequired(f) {
			continue
		}
		if f.IncompleteKind() == cue.StructKind && hasDeclaredFields(f) {
			if len(requiredLeaves(f)) == 0 {
				continue
			}
		}
		out = append(out, it.Selector().Unquoted())
	}
	return out
}

// isRequired reports whether a caller must give a field: it has no default,
// and the function does not set it itself.
func isRequired(f cue.Value) bool {
	if _, hasDefault := f.Default(); hasDefault {
		return false
	}
	return f.IncompleteKind() == cue.StructKind || !f.IsConcrete()
}
