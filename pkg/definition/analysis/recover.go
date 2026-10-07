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
	"path"

	"cuelang.org/go/cue/ast"
	"cuelang.org/go/cue/ast/astutil"
	cueerrors "cuelang.org/go/cue/errors"
	"cuelang.org/go/cue/literal"
	"cuelang.org/go/cue/token"
)

// blankOut replaces what each error points at with top (_): an unresolved
// reference, a selection of a field that does not exist, or an import that
// does not exist together with every use of it; a let nothing refers to is
// removed. It reports whether anything was replaced, since an error it
// cannot place would otherwise be recompiled forever.
func blankOut(f *ast.File, errs []cueerrors.Error) bool {
	at := map[token.Pos]bool{}
	for _, e := range errs {
		if p := e.Position(); p.IsValid() {
			at[p] = true
		}
	}
	changed := false
	dropped := map[string]bool{}
	decls := f.Decls[:0]
	for _, decl := range f.Decls {
		if imp, ok := decl.(*ast.ImportDecl); ok {
			specs := imp.Specs[:0]
			for _, spec := range imp.Specs {
				if at[spec.Path.Pos()] {
					dropped[importName(spec)] = true
					continue
				}
				specs = append(specs, spec)
			}
			if imp.Specs = specs; len(specs) == 0 {
				continue
			}
		}
		decls = append(decls, decl)
	}
	f.Decls = decls
	astutil.Apply(f, func(c astutil.Cursor) bool {
		switch n := c.Node().(type) {
		case *ast.LetClause:
			// A let nothing refers to fails the whole file: it goes.
			if at[n.Pos()] || at[n.Ident.Pos()] {
				c.Delete()
				changed = true
				return false
			}
		case *ast.SelectorExpr:
			if at[n.Sel.Pos()] {
				c.Replace(ast.NewIdent("_"))
				changed = true
				return false
			}
		case *ast.Ident:
			if at[n.Pos()] {
				c.Replace(ast.NewIdent("_"))
				changed = true
			}
		}
		return true
	}, nil)
	if len(dropped) == 0 {
		return changed
	}
	astutil.Apply(f, func(c astutil.Cursor) bool {
		if sel, ok := c.Node().(*ast.SelectorExpr); ok {
			if root, _ := flatten(sel); root != nil && dropped[root.Name] {
				c.Replace(ast.NewIdent("_"))
				return false
			}
		}
		return true
	}, nil)
	return true
}

// importName is the name an import is referred to by.
func importName(spec *ast.ImportSpec) string {
	if spec.Name != nil {
		return spec.Name.Name
	}
	p, _ := literal.Unquote(spec.Path.Value)
	return path.Base(p)
}
