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

	"cuelang.org/go/cue/ast"
	"cuelang.org/go/cue/format"
	"cuelang.org/go/cue/literal"
	"cuelang.org/go/cue/parser"
	"cuelang.org/go/cue/token"
)

// MoveToTraitCommand is the client command that asks the trait's name and moves a parameter to it.
const MoveToTraitCommand = "kubevela.moveToTrait"

// paramUse is where the template reads a parameter.
type paramUse struct {
	// path is the field's labels from the template, as "output", "spec", "replicas".
	path  []string
	field *ast.Field
	// whole says the field's value is the parameter and nothing else.
	whole  bool
	inList bool
	inCond bool
}

// componentParameter is a component definition's top-level parameter, and the template.
type componentParameter struct {
	d     *document
	tmpl  *ast.StructLit
	decl  *ast.Field
	block *ast.StructLit
}

func componentParam(path, doc, name string) (componentParameter, error) {
	f, err := parser.ParseFile(path, doc, parser.ParseComments)
	if err != nil {
		return componentParameter{}, err
	}
	d, ok := newDocument(path, []byte(doc), f)
	if !ok || d.typ != "component" {
		return componentParameter{}, fmt.Errorf("only a component's parameter moves to a trait")
	}
	c := componentParameter{d: d, tmpl: d.template.Value.(*ast.StructLit)}
	for _, e := range c.tmpl.Elts {
		if pf, ok := e.(*ast.Field); ok && labelName(pf.Label) == "parameter" {
			c.block, _ = pf.Value.(*ast.StructLit)
		}
	}
	if c.block != nil {
		for _, e := range c.block.Elts {
			if pf, ok := e.(*ast.Field); ok && labelName(pf.Label) == name {
				c.decl = pf
			}
		}
	}
	if c.decl == nil {
		return componentParameter{}, fmt.Errorf("the component has no parameter %s", name)
	}
	return c, nil
}

// hasDefault is whether a parameter's value marks a default, as *1 | int.
func hasDefault(v ast.Expr) bool {
	b, ok := v.(*ast.BinaryExpr)
	if !ok || b.Op != token.OR {
		return false
	}
	if u, ok := b.X.(*ast.UnaryExpr); ok && u.Op == token.MUL {
		return true
	}
	return hasDefault(b.X) || hasDefault(b.Y)
}

// MoveToTraitAt is the parameter Move to a Trait is offered on at offset: a
// component's top-level parameter that is optional or has a default.
func MoveToTraitAt(path, doc string, offset int) (string, bool) {
	f, err := parser.ParseFile(path, doc, parser.ParseComments)
	if err != nil {
		return "", false
	}
	d, ok := newDocument(path, []byte(doc), f)
	if !ok || d.typ != "component" {
		return "", false
	}
	for _, e := range d.template.Value.(*ast.StructLit).Elts {
		pf, ok := e.(*ast.Field)
		if !ok || labelName(pf.Label) != "parameter" {
			continue
		}
		block, ok := pf.Value.(*ast.StructLit)
		if !ok {
			return "", false
		}
		for _, pe := range block.Elts {
			decl, ok := pe.(*ast.Field)
			if !ok || offset < decl.Label.Pos().Offset() || offset > decl.Label.End().Offset() {
				continue
			}
			if decl.Constraint == token.OPTION || hasDefault(decl.Value) {
				return labelName(decl.Label), true
			}
		}
	}
	return "", false
}

// MoveToTrait moves a component's parameter to a new trait named trait: the
// edits that take it out of the component, and the trait's file, whose patch
// sets what the component's output set from it. It refuses a parameter the
// template reads other than as a field's whole value outside lists and
// conditions, since a patch could not say where it goes.
func MoveToTrait(path, doc, name, trait string) ([]RangeEdit, string, error) {
	c, err := componentParam(path, doc, name)
	if err != nil {
		return nil, "", err
	}
	if c.decl.Constraint != token.OPTION && !hasDefault(c.decl.Value) {
		return nil, "", fmt.Errorf("%s is required: a trait could leave it unset", name)
	}
	var uses []paramUse
	var wholes []paramUse
	var other, guards bool
	var walk func(n ast.Node, path []string, inList, inCond bool)
	walk = func(n ast.Node, path []string, inList, inCond bool) {
		switch x := n.(type) {
		case *ast.StructLit:
			for _, e := range x.Elts {
				walk(e, path, inList, inCond)
			}
		case *ast.ListLit:
			for _, e := range x.Elts {
				walk(e, path, true, inCond)
			}
		case *ast.Comprehension:
			for _, cl := range x.Clauses {
				if refersTo(cl, name) {
					guards = true
				}
			}
			walk(x.Value, path, inList, true)
		case *ast.Field:
			p := append(append([]string{}, path...), labelName(x.Label))
			switch v := x.Value.(type) {
			case *ast.SelectorExpr:
				if isParamRef(v, name) {
					uses = append(uses, paramUse{path: p, field: x, whole: true, inList: inList, inCond: inCond})
					return
				}
			case *ast.Ident:
				if v.Name == "parameter" {
					wholes = append(wholes, paramUse{path: p, field: x, inList: inList, inCond: inCond})
					return
				}
			}
			walk(x.Value, p, inList, inCond)
		default:
			if n != nil && refersTo(n, name) {
				other = true
			}
		}
	}
	for _, e := range c.tmpl.Elts {
		if f, ok := e.(*ast.Field); ok && labelName(f.Label) == "parameter" {
			continue
		}
		walk(e, nil, false, false)
	}
	if guards {
		return nil, "", fmt.Errorf("the template sets fields conditionally on %s: move it by hand", name)
	}
	if other {
		return nil, "", fmt.Errorf("the template reads %s in an expression: move it by hand", name)
	}
	var patches []string
	for _, u := range uses {
		switch {
		case u.inList:
			return nil, "", fmt.Errorf("the template sets %s in a list: a patch cannot say which element, so move it by hand", name)
		case u.inCond:
			return nil, "", fmt.Errorf("the template sets %s conditionally: move it by hand", name)
		case u.path[0] != "output":
			return nil, "", fmt.Errorf("%s is read outside the output (%s): move it by hand", name, strings.Join(u.path, "."))
		}
		patches = append(patches, patchLine(u.path[1:], "parameter."+name))
	}
	for _, w := range wholes {
		if w.path[0] == "output" && !w.inList && !w.inCond {
			patches = append(patches, patchLine(w.path[1:], "parameter"))
		}
	}
	if len(patches) == 0 {
		return nil, "", fmt.Errorf("nothing the component outputs reads %s", name)
	}

	lines := strings.Split(doc, "\n")
	from := c.decl.Pos().Line()
	for from > 1 && strings.HasPrefix(strings.TrimSpace(lines[from-2]), "//") {
		from--
	}
	through := c.decl.End().Line()
	edits := []RangeEdit{{Range: wholeLines(from, through)}}
	for _, u := range uses {
		l := u.field.Pos().Line()
		if l != u.field.End().Line() || !strings.HasPrefix(strings.TrimSpace(lines[l-1]), labelText(u.field.Label)) {
			return nil, "", fmt.Errorf("the field setting %s shares its line: move it by hand", name)
		}
		edits = append(edits, RangeEdit{Range: wholeLines(l, l)})
	}

	// The trait imports what the parameter uses; the component keeps an import
	// only while something it keeps uses it.
	removed := []ast.Node{c.decl}
	for _, u := range uses {
		removed = append(removed, u.field)
	}
	var imports []string
	for _, id := range c.d.imports {
		for _, spec := range id.Specs {
			pkg := importName(spec)
			if usesPackage(c.decl, pkg) {
				imports = append(imports, "import "+spec.Path.Value)
			}
			if usedElsewhere(c.d.file, pkg, removed) {
				continue
			}
			if len(id.Specs) == 1 {
				to := id.End().Line()
				if to < len(lines) && strings.TrimSpace(lines[to]) == "" {
					to++
				}
				edits = append(edits, RangeEdit{Range: wholeLines(id.Pos().Line(), to)})
			} else {
				edits = append(edits, RangeEdit{Range: wholeLines(spec.Pos().Line(), spec.End().Line())})
			}
		}
	}

	decl := make([]string, 0, through-from+1)
	for _, l := range lines[from-1 : through] {
		decl = append(decl, strings.TrimSpace(l))
	}
	src := fmt.Sprintf(`%s: {
	type:        "trait"
	description: %s
	attributes: {
		appliesToWorkloads: [%s]
		podDisruptive: false
	}
}
template: {
	%s
	parameter: {
		%s
	}
}
`, strconv.Quote(trait), strconv.Quote(fmt.Sprintf("Sets %s of a %s component.", name, c.d.name)), strconv.Quote(workloadOf(c.d)), strings.Join(patches, "\n\t"), strings.Join(decl, "\n\t\t"))
	if len(imports) > 0 {
		src = strings.Join(imports, "\n") + "\n\n" + src
	}
	out, err := format.Source([]byte(src))
	if err != nil {
		return nil, "", fmt.Errorf("the trait does not format: %w", err)
	}
	return edits, string(out), nil
}

// usesPackage is whether a node calls into the package imported as pkg.
func usesPackage(n ast.Node, pkg string) bool {
	found := false
	ast.Walk(n, func(n ast.Node) bool {
		if s, ok := n.(*ast.SelectorExpr); ok {
			if id, ok := s.X.(*ast.Ident); ok && id.Name == pkg {
				found = true
			}
		}
		return !found
	}, nil)
	return found
}

// usedElsewhere is whether anything in the file but the removed nodes uses pkg.
func usedElsewhere(f *ast.File, pkg string, removed []ast.Node) bool {
	skip := map[ast.Node]bool{}
	for _, n := range removed {
		skip[n] = true
	}
	found := false
	ast.Walk(f, func(n ast.Node) bool {
		if skip[n] {
			return false
		}
		if s, ok := n.(*ast.SelectorExpr); ok {
			if id, ok := s.X.(*ast.Ident); ok && id.Name == pkg {
				found = true
			}
		}
		return !found
	}, nil)
	return found
}

// patchLine is a trait's patch setting the path of the output to value.
func patchLine(path []string, value string) string {
	labels := make([]string, len(path))
	for i, p := range path {
		labels[i] = label(p)
	}
	if len(labels) == 0 {
		return "patch: " + value
	}
	return "patch: " + strings.Join(labels, ": ") + ": " + value
}

// label is a name as a CUE label, quoted when it must be.
func label(name string) string {
	if ast.IsValidIdent(name) && !strings.HasPrefix(name, "#") && !strings.HasPrefix(name, "_") {
		return name
	}
	return strconv.Quote(name)
}

// labelText is a label as written.
func labelText(l ast.Label) string {
	if b, err := format.Node(l); err == nil {
		return string(b)
	}
	return labelName(l)
}

// isParamRef is whether an expression is parameter.<name>.
func isParamRef(s *ast.SelectorExpr, name string) bool {
	id, ok := s.X.(*ast.Ident)
	return ok && id.Name == "parameter" && labelName(s.Sel) == name
}

// refersTo is whether a node reads parameter.<name>.
func refersTo(n ast.Node, name string) bool {
	found := false
	ast.Walk(n, func(n ast.Node) bool {
		if s, ok := n.(*ast.SelectorExpr); ok && isParamRef(s, name) {
			found = true
		}
		return !found
	}, nil)
	return found
}

// workloadOf is the resource type a component's workload is, as a trait's
// appliesToWorkloads names it: its workload type, or one made from its
// definition's kind and group.
func workloadOf(d *document) string {
	attrs, ok := fieldIn(d.headers[0], "attributes")
	if !ok {
		return "*"
	}
	w, ok := fieldIn(attrs, "workload")
	if !ok {
		return "*"
	}
	if t, ok := fieldIn(w, "type"); ok {
		if lit, ok := t.Value.(*ast.BasicLit); ok {
			if s, err := literal.Unquote(lit.Value); err == nil && s != "" {
				return s
			}
		}
	}
	def, ok := fieldIn(w, "definition")
	if !ok {
		return "*"
	}
	str := func(name string) string {
		if f, ok := fieldIn(def, name); ok {
			if lit, ok := f.Value.(*ast.BasicLit); ok {
				s, _ := literal.Unquote(lit.Value)
				return s
			}
		}
		return ""
	}
	kind, apiVersion := str("kind"), str("apiVersion")
	if kind == "" {
		return "*"
	}
	plural := strings.ToLower(kind) + "s"
	if i := strings.LastIndex(apiVersion, "/"); i > 0 {
		return plural + "." + apiVersion[:i]
	}
	return plural
}
