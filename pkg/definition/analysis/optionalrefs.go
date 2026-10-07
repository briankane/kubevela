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
	"strconv"
	"strings"

	"cuelang.org/go/cue"
	"cuelang.org/go/cue/ast"
	"cuelang.org/go/cue/build"
	"cuelang.org/go/cue/cuecontext"
	"cuelang.org/go/cue/parser"
	"cuelang.org/go/cue/token"
)

// checkOptionalReferences warns of each read of an optional parameter in
// the template that nothing gates: where the user does not give it, the
// read fails the render, or will once what reads it is used. A read is
// gated under if p != _|_ (for p or an ancestor of it), in a test against
// _|_, in a disjunction, which falls back to another branch, or written into
// an optional field. A for or if at the template's level, or directly in
// outputs, only chooses what is declared, so its clauses are safe. && does
// not short-circuit, so a comparison is not gated by a guard before it in
// the same condition.
func (d *document) checkOptionalReferences() []Diagnostic {
	tmpl, ok := d.template.Value.(*ast.StructLit)
	if !ok {
		return nil
	}
	param, ok := d.parameterSchema()
	if !ok {
		return nil
	}
	c := &optionalCheck{d: d, param: param}
	c.top(tmpl.Elts, nil)
	return c.diags
}

// top checks the template's own declarations under guards.
func (c *optionalCheck) top(elts []ast.Decl, guards [][]string) {
	for _, elt := range elts {
		switch x := elt.(type) {
		case *ast.Field:
			label := labelName(x.Label)
			switch {
			case label == parameterLabel || label == contextLabel:
			case x.Constraint == token.OPTION:
			case label == "outputs":
				c.chooser(x.Value, guards)
			default:
				c.visit(x.Value, guards, x, false)
			}
		case *ast.LetClause:
			c.visit(x.Expr, guards, nil, false)
		case *ast.Comprehension:
			// It chooses what the template declares: its clauses are safe,
			// and what it declares is the template's own.
			inner := guards
			for _, cl := range x.Clauses {
				if ic, ok := cl.(*ast.IfClause); ok {
					inner = append(append([][]string{}, inner...), existenceTests(ic.Condition)...)
				}
			}
			if s, ok := x.Value.(*ast.StructLit); ok {
				c.top(s.Elts, inner)
			}
		case *ast.EmbedDecl:
			c.visit(x.Expr, guards, nil, false)
		}
	}
}

// chooser checks outputs: a for or if directly in it chooses which objects
// are rendered, so its clauses are safe; the objects are rendered.
func (c *optionalCheck) chooser(v ast.Expr, guards [][]string) {
	s, ok := v.(*ast.StructLit)
	if !ok {
		c.visit(v, guards, nil, false)
		return
	}
	for _, elt := range s.Elts {
		cmp, ok := elt.(*ast.Comprehension)
		if !ok {
			c.visit(elt, guards, nil, false)
			continue
		}
		inner := guards
		for _, cl := range cmp.Clauses {
			if ic, ok := cl.(*ast.IfClause); ok {
				inner = append(append([][]string{}, inner...), existenceTests(ic.Condition)...)
			}
		}
		c.chooser(cmp.Value, inner)
	}
}

// parameterSchema is the template's parameter evaluated on its own, from a
// fresh parse of the file, so the template's syntax is not shared with
// another compile: alone, with the file's imports, or, where it refers to
// the template's helpers, with the template, the rest of which does not
// touch it.
func (d *document) parameterSchema() (cue.Value, bool) {
	f, ok := fieldIn(d.template, parameterLabel)
	if !ok {
		return cue.Value{}, false
	}
	var imports strings.Builder
	for _, imp := range d.imports {
		imports.WriteString(string(d.src[imp.Pos().Offset():imp.End().Offset()]) + "\n")
	}
	param := string(d.src[f.Value.Pos().Offset():f.Value.End().Offset()])
	for _, src := range []string{imports.String() + "parameter: " + param + "\n", string(d.src)} {
		file, err := parser.ParseFile(d.path, src)
		if err != nil {
			continue
		}
		bi := build.NewContext().NewInstance(d.path, nil)
		bi.Imports = d.packages().imports()
		if err := bi.AddSyntax(file); err != nil {
			continue
		}
		v := cuecontext.New().BuildInstance(bi)
		p := v.LookupPath(cue.ParsePath(parameterLabel))
		if !p.Exists() {
			p = v.LookupPath(cue.ParsePath(templateLabel + "." + parameterLabel))
		}
		if _, err := p.Fields(cue.Optional(true)); err == nil && p.Exists() {
			return p, true
		}
	}
	return cue.Value{}, false
}

// optionalCheck walks a template for ungated reads of optional parameters.
type optionalCheck struct {
	d     *document
	param cue.Value
	diags []Diagnostic
}

// visit checks n under guards, the parameter paths known to exist; field is
// the innermost field n is in, and inCondition whether n is an if's
// condition.
func (c *optionalCheck) visit(n ast.Node, guards [][]string, field *ast.Field, inCondition bool) {
	switch x := n.(type) {
	case *ast.StructLit:
		for _, e := range x.Elts {
			c.visit(e, guards, field, inCondition)
		}
	case *ast.Field:
		// A read written into an optional field leaves it out.
		if x.Constraint != token.OPTION {
			c.visit(x.Value, guards, x, inCondition)
		}
	case *ast.EmbedDecl:
		c.visit(x.Expr, guards, field, inCondition)
	case *ast.LetClause:
		c.visit(x.Expr, guards, field, inCondition)
	case *ast.Comprehension:
		inner := guards
		for _, cl := range x.Clauses {
			switch cl := cl.(type) {
			case *ast.IfClause:
				c.visit(cl.Condition, inner, field, true)
				inner = append(append([][]string{}, inner...), existenceTests(cl.Condition)...)
			case *ast.ForClause:
				c.visit(cl.Source, inner, field, inCondition)
			case *ast.LetClause:
				c.visit(cl.Expr, inner, field, inCondition)
			}
		}
		c.visit(x.Value, inner, nil, false)
	case *ast.BinaryExpr:
		switch {
		case (x.Op == token.NEQ || x.Op == token.EQL) && (isBottom(x.X) || isBottom(x.Y)):
			// A test of existence: safe whatever it tests.
		case x.Op == token.OR:
			// A disjunction falls back to another branch.
		default:
			c.visit(x.X, guards, field, inCondition)
			c.visit(x.Y, guards, field, inCondition)
		}
	case *ast.UnaryExpr:
		c.visit(x.X, guards, field, inCondition)
	case *ast.ParenExpr:
		c.visit(x.X, guards, field, inCondition)
	case *ast.CallExpr:
		for _, a := range x.Args {
			c.visit(a, guards, field, inCondition)
		}
	case *ast.IndexExpr:
		if _, ok := parameterPath(x); ok {
			c.check(x, guards, field, inCondition)
			return
		}
		c.visit(x.X, guards, field, inCondition)
		c.visit(x.Index, guards, field, inCondition)
	case *ast.SliceExpr:
		c.visit(x.X, guards, field, inCondition)
	case *ast.ListLit:
		for _, e := range x.Elts {
			c.visit(e, guards, field, inCondition)
		}
	case *ast.Interpolation:
		for _, e := range x.Elts {
			c.visit(e, guards, field, inCondition)
		}
	case *ast.SelectorExpr:
		c.check(x, guards, field, inCondition)
	}
}

// parameterPath is the labels a reference reads under parameter, written
// with selectors or string indexes, and the node of each.
func parameterPath(e ast.Expr) ([]string, bool) {
	labels, _, ok := parameterRef(e)
	return labels, ok
}

func parameterRef(e ast.Expr) ([]string, []ast.Node, bool) {
	switch x := e.(type) {
	case *ast.Ident:
		return nil, nil, x.Name == parameterLabel
	case *ast.SelectorExpr:
		labels, nodes, ok := parameterRef(x.X)
		if !ok {
			return nil, nil, false
		}
		name := labelName(x.Sel)
		return append(labels, name), append(nodes, x.Sel), true
	case *ast.IndexExpr:
		lit, isLit := x.Index.(*ast.BasicLit)
		if !isLit || lit.Kind != token.STRING {
			return nil, nil, false
		}
		labels, nodes, ok := parameterRef(x.X)
		if !ok {
			return nil, nil, false
		}
		name, err := strconv.Unquote(lit.Value)
		if err != nil {
			return nil, nil, false
		}
		return append(labels, name), append(nodes, lit), true
	}
	return nil, nil, false
}

// check reports the first optional parameter a reference reads ungated.
func (c *optionalCheck) check(ref ast.Expr, guards [][]string, field *ast.Field, inCondition bool) {
	labels, nodes, ok := parameterRef(ref)
	if !ok || len(labels) == 0 {
		return
	}
	for k := 1; k <= len(labels); k++ {
		path := labels[:k]
		if !optionalAt(c.param, path) || guarded(guards, path) {
			continue
		}
		ref := parameterLabel + "." + strings.Join(path, ".")
		msg := fmt.Sprintf("%s is optional: where it is not given, reading it fails the render. Read it under if %s != _|_ {...}", ref, ref)
		if inCondition {
			msg = fmt.Sprintf("%s is optional: where it is not given, comparing it fails the render, and && does not stop it. Test it with (%s & value) != _|_, or under if %s != _|_ {...}", ref, ref, ref)
		}
		diag := c.d.at(nodes[k-1].Pos(), msg)
		diag.Severity = SeverityWarning
		if !inCondition && field != nil {
			diag.Fixes = []Fix{c.guardFix(field, ref)}
		}
		c.diags = append(c.diags, diag)
		return
	}
}

// guardFix wraps field in if ref != _|_: on lines of their own when the
// field starts its line, in place otherwise.
func (c *optionalCheck) guardFix(field *ast.Field, ref string) Fix {
	src := string(c.d.src)
	start, end := field.Pos().Offset(), field.End().Offset()
	text := src[start:end]
	lineStart := strings.LastIndex(src[:start], "\n") + 1
	indent := src[lineStart:start]
	guard := "if " + ref + " != _|_ {"
	var wrapped string
	if strings.TrimSpace(indent) == "" {
		body := strings.ReplaceAll(text, "\n", "\n\t")
		wrapped = guard + "\n" + indent + "\t" + body + "\n" + indent + "}"
	} else {
		wrapped = guard + text + "}"
	}
	return Fix{Title: "Read " + ref + " only when it is given", Edits: []RangeEdit{{Range: span(field.Pos(), field.End()), NewText: wrapped}}}
}

// optionalAt reports whether the last label of path is an optional field
// of the parameter, its ancestors found as they are declared.
func optionalAt(param cue.Value, path []string) bool {
	v := param
	for _, l := range path[:len(path)-1] {
		if v = schemaChild(v, cue.Str(l)); !v.Exists() {
			return false
		}
	}
	it, err := v.Fields(cue.Optional(true))
	if err != nil {
		return false
	}
	last := path[len(path)-1]
	for it.Next() {
		if it.Selector().Unquoted() == last {
			return it.IsOptional()
		}
	}
	return false
}

// guarded reports whether a guard shows path exists: one on path or a field
// under it.
func guarded(guards [][]string, path []string) bool {
	for _, g := range guards {
		if len(g) >= len(path) && strings.Join(g[:len(path)], ".") == strings.Join(path, ".") {
			return true
		}
	}
	return false
}

// existenceTests are the parameter paths a condition shows exist: each
// tested != _|_, alone or joined by &&.
func existenceTests(cond ast.Expr) [][]string {
	var out [][]string
	var walk func(e ast.Expr)
	walk = func(e ast.Expr) {
		switch x := e.(type) {
		case *ast.ParenExpr:
			walk(x.X)
		case *ast.BinaryExpr:
			switch {
			case x.Op == token.LAND:
				walk(x.X)
				walk(x.Y)
			case x.Op == token.NEQ && isBottom(x.Y):
				out = append(out, parameterPaths(x.X)...)
			case x.Op == token.NEQ && isBottom(x.X):
				out = append(out, parameterPaths(x.Y)...)
			}
		}
	}
	walk(cond)
	return out
}

// parameterPaths are the parameter paths e reads.
func parameterPaths(e ast.Expr) [][]string {
	var out [][]string
	ast.Walk(e, func(n ast.Node) bool {
		x, ok := n.(ast.Expr)
		if !ok {
			return true
		}
		if labels, ok := parameterPath(x); ok {
			if len(labels) > 0 {
				out = append(out, labels)
			}
			return false
		}
		return true
	}, nil)
	return out
}

// isBottom reports whether e is _|_.
func isBottom(e ast.Expr) bool {
	_, ok := e.(*ast.BottomLit)
	return ok
}
