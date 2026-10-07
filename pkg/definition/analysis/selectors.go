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
	"cuelang.org/go/cue/literal"
)

// checkSelectors reports references to fields that cannot exist: a member a
// vela/* package does not declare, or a parameter field the template does not
// declare. CUE treats both as incomplete rather than wrong, since an open
// struct could gain the field later, so Validate does not report them.
func (d *document) checkSelectors(v cue.Value) []Diagnostic {
	pkgs := d.importNames()
	param := v.LookupPath(cue.MakePath(cue.Def(closedParameterPath)))
	ctx := v.LookupPath(cue.ParsePath(templateLabel + ".context"))
	modelled := ContextFields(d.typ) != nil
	var diags []Diagnostic
	ast.Walk(d.template.Value, func(n ast.Node) bool {
		sel, ok := n.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		root, chain := flatten(sel)
		if root == nil {
			return true
		}
		switch {
		case root.Name == parameterLabel && param.Exists():
			if diag, bad := d.checkParameter(param, chain); bad {
				diags = append(diags, diag)
			}
		case root.Name == contextLabel && modelled && ctx.Exists():
			if diag, bad := d.checkContext(ctx, chain); bad {
				diags = append(diags, diag)
			}
		case pkgs[root.Name] != "":
			if diag, bad := d.checkMember(pkgs[root.Name], chain[0]); bad {
				diags = append(diags, diag)
			}
		}
		return false
	}, nil)
	return diags
}

// checkParameter walks a parameter.a.b chain through the closed copy of the
// parameter, stopping where the value stops being a struct.
func (d *document) checkParameter(param cue.Value, chain []*ast.Ident) (Diagnostic, bool) {
	cur := param
	walked := []string{parameterLabel}
	for _, id := range chain {
		if cur.IncompleteKind() != cue.StructKind {
			return Diagnostic{}, false
		}
		s, ok := selectorFor(id.Name)
		if !ok {
			return Diagnostic{}, false
		}
		if !cur.Allows(s) {
			diag := d.at(id.Pos(), fmt.Sprintf("%s has no field %s", strings.Join(walked, "."), id.Name))
			if to := closest(id.Name, fieldNames(cur)); to != "" {
				diag.Fixes = append(diag.Fixes, renameFix(id.Pos(), id.End(), to))
			}
			if len(walked) == 1 {
				if add, ok := d.addParameterFix(id.Name); ok {
					diag.Fixes = append(diag.Fixes, add)
				}
			}
			return diag, true
		}
		cur = lookup(cur, s)
		walked = append(walked, id.Name)
	}
	return Diagnostic{}, false
}

// checkContext walks a context.a.b chain through the closed context, which
// catches a read CUE does not evaluate, such as one under an undecided if.
func (d *document) checkContext(ctx cue.Value, chain []*ast.Ident) (Diagnostic, bool) {
	cur := ctx
	walked := []string{contextLabel}
	for _, id := range chain {
		if cur.IncompleteKind() != cue.StructKind {
			return Diagnostic{}, false
		}
		s, ok := selectorFor(id.Name)
		if !ok {
			return Diagnostic{}, false
		}
		if !cur.Allows(s) {
			msg := fmt.Sprintf("%s has no field %s", strings.Join(walked, "."), id.Name)
			if why, ok := explainContextField(d.typ, id.Name); ok && len(walked) == 1 {
				msg = why
			}
			diag := d.at(id.Pos(), msg)
			if to := closest(id.Name, fieldNames(cur)); to != "" {
				diag.Fixes = []Fix{renameFix(id.Pos(), id.End(), to)}
			}
			return diag, true
		}
		cur = schemaChild(cur, s)
		walked = append(walked, id.Name)
	}
	return Diagnostic{}, false
}

func (d *document) checkMember(importPath string, member *ast.Ident) (Diagnostic, bool) {
	pkg, ok := d.packages().value(importPath)
	if !ok {
		return Diagnostic{}, false
	}
	s, ok := selectorFor(member.Name)
	if !ok || lookup(pkg, s).Exists() {
		return Diagnostic{}, false
	}
	return d.at(member.Pos(),
		fmt.Sprintf("package %s has no member %s", importPath, member.Name)), true
}

// importNames maps each name an import is referred to by to its path.
func (d *document) importNames() map[string]string {
	names := map[string]string{}
	for _, decl := range d.imports {
		for _, spec := range decl.Specs {
			if p, err := literal.Unquote(spec.Path.Value); err == nil {
				names[importName(spec)] = p
			}
		}
	}
	return names
}

// flatten splits a.b.c into its root identifier and the selected labels.
func flatten(sel *ast.SelectorExpr) (*ast.Ident, []*ast.Ident) {
	var chain []*ast.Ident
	var x ast.Expr = sel
	for {
		switch e := x.(type) {
		case *ast.SelectorExpr:
			id, ok := e.Sel.(*ast.Ident)
			if !ok {
				return nil, nil
			}
			chain = append([]*ast.Ident{id}, chain...)
			x = e.X
		case *ast.Ident:
			return e, chain
		default:
			return nil, nil
		}
	}
}

func selectorFor(name string) (cue.Selector, bool) {
	switch {
	case strings.HasPrefix(name, "#"):
		return cue.Def(name), true
	case strings.HasPrefix(name, "_"):
		return cue.Selector{}, false
	}
	return cue.Str(name), true
}
