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
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"cuelang.org/go/cue/ast"
	"cuelang.org/go/cue/parser"
	"cuelang.org/go/cue/token"
)

// Location is a range in a file.
type Location struct {
	Path  string
	Range Range
}

// RangeEdit replaces a range of a document with NewText.
type RangeEdit struct {
	Range   Range
	NewText string
}

// navIndex is what navigation needs of a parsed file: the field each value
// belongs to, each field's path from the file's root, the field each label
// names, and the chain each selector belongs to.
type navIndex struct {
	path    string
	file    *ast.File
	fieldOf map[ast.Node]*ast.Field
	pathOf  map[*ast.Field]string
	labelOf map[ast.Node]*ast.Field
	chainOf map[*ast.Ident]navChain
	// forOf is the for clause declaring each comprehension's variable.
	forOf  map[*ast.Ident]*ast.ForClause
	idents []*ast.Ident
}

// navChain is a reference, as root.a.b, up to one of its selectors.
type navChain struct {
	root   *ast.Ident
	labels []string
}

func parseNav(path, doc string) (*navIndex, bool) {
	f, err := parser.ParseFile(path, doc, parser.ParseComments)
	if err != nil {
		return nil, false
	}
	ix := &navIndex{path: path, file: f, fieldOf: map[ast.Node]*ast.Field{}, pathOf: map[*ast.Field]string{}, labelOf: map[ast.Node]*ast.Field{}, chainOf: map[*ast.Ident]navChain{}, forOf: map[*ast.Ident]*ast.ForClause{}}
	var stack []string
	ast.Walk(f, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.Field:
			stack = append(stack, labelName(x.Label))
			ix.fieldOf[x.Value] = x
			ix.pathOf[x] = strings.Join(stack, ".")
			ix.labelOf[x.Label] = x
		case *ast.ListLit:
			stack = append(stack, listItem)
		case *ast.SelectorExpr:
			root, chain := flatten(x)
			if root != nil {
				var labels []string
				for _, id := range chain {
					labels = append(labels, id.Name)
					ix.chainOf[id] = navChain{root: root, labels: append([]string{}, labels...)}
				}
			}
		case *ast.ForClause:
			ix.forOf[x.Value] = x
			if x.Key != nil {
				ix.forOf[x.Key] = x
			}
		case *ast.Ident:
			ix.idents = append(ix.idents, x)
		}
		return true
	}, func(n ast.Node) {
		switch n.(type) {
		case *ast.Field, *ast.ListLit:
			stack = stack[:len(stack)-1]
		}
	})
	return ix, true
}

// nodeAt is the identifier, or quoted label, that offset is in.
func (ix *navIndex) nodeAt(offset int) ast.Node {
	var found ast.Node
	ast.Walk(ix.file, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.Ident:
			if x.Pos().Offset() <= offset && offset <= x.End().Offset() {
				found = x
			}
		case *ast.BasicLit:
			if _, isLabel := ix.labelOf[x]; isLabel && x.Pos().Offset() <= offset && offset <= x.End().Offset() {
				found = x
			}
		}
		return true
	}, nil)
	return found
}

// decl is what a reference resolves to: a field, a let, or a comprehension's
// variable, with the index of the file it is in.
type decl struct {
	ix    *navIndex
	field *ast.Field
	node  ast.Node
}

// key identifies a declaration across its split parts: a field by its path,
// anything else by itself.
func (d decl) key() string {
	if d.field != nil {
		return d.ix.path + "|" + d.ix.pathOf[d.field]
	}
	return fmt.Sprintf("%s|%p", d.ix.path, d.node)
}

func (d decl) location() Location {
	var start, end token.Pos
	switch {
	case d.field != nil:
		start, end = d.field.Label.Pos(), d.field.Label.End()
	default:
		if l, ok := d.node.(*ast.LetClause); ok {
			start, end = l.Ident.Pos(), l.Ident.End()
		} else {
			start, end = d.node.Pos(), d.node.End()
		}
	}
	return Location{Path: d.ix.path, Range: Range{
		Start: Position{Line: start.Line(), Column: start.Column()},
		End:   Position{Line: end.Line(), Column: end.Column()},
	}}
}

// resolve is what root.labels refers to, or false.
func (ix *navIndex) resolve(root *ast.Ident, labels []string, siblings func(name string) (decl, bool)) (decl, bool) {
	var d decl
	switch target := root.Node.(type) {
	case nil:
		if siblings == nil {
			return decl{}, false
		}
		var ok bool
		if d, ok = siblings(root.Name); !ok {
			return decl{}, false
		}
	case *ast.LetClause:
		// A let's field is the field of the value it is bound to.
		if len(labels) > 0 {
			if d, ok := ix.walk(ix.valueOf(target.Expr), labels); ok {
				return d, true
			}
		}
		return decl{ix: ix, node: target}, true
	case *ast.Ident:
		// A comprehension's value variable stands for an element of what it
		// ranges over, so its fields are that element's.
		if fc, ok := ix.forOf[target]; ok && len(labels) > 0 && fc.Value == target {
			if d, ok := ix.walk(elementOf(ix.valueOf(fc.Source)), labels); ok {
				return d, true
			}
		}
		return decl{ix: ix, node: target}, true
	case *ast.ImportSpec:
		return decl{}, false
	default:
		f, ok := ix.fieldOf[target]
		if !ok {
			return decl{}, false
		}
		d = decl{ix: ix, field: f}
	}
	for _, l := range labels {
		next, ok := fieldIn(d.field, l)
		if !ok {
			return decl{}, false
		}
		d.field = next
	}
	return d, true
}

// valueOf is the expression a value is declared with: for a reference, the
// value of the field it names; for an index, the indexed list's element.
func (ix *navIndex) valueOf(e ast.Expr) ast.Expr {
	switch x := e.(type) {
	case *ast.ParenExpr:
		return ix.valueOf(x.X)
	case *ast.IndexExpr:
		return elementOf(ix.valueOf(x.X))
	case *ast.Ident, *ast.SelectorExpr:
		var root *ast.Ident
		var chain []*ast.Ident
		if sel, ok := x.(*ast.SelectorExpr); ok {
			root, chain = flatten(sel)
		} else {
			root = x.(*ast.Ident)
		}
		if root == nil {
			return nil
		}
		var labels []string
		for _, id := range chain {
			labels = append(labels, id.Name)
		}
		d, ok := ix.resolve(root, labels, nil)
		if !ok || d.field == nil {
			return nil
		}
		return d.field.Value
	}
	return e
}

// elementOf is the element of a list, [...X] or [X, ...], or the value of a
// struct's pattern, [string]: X; else e itself.
func elementOf(e ast.Expr) ast.Expr {
	switch x := e.(type) {
	case *ast.ListLit:
		for i := len(x.Elts) - 1; i >= 0; i-- {
			if el, ok := x.Elts[i].(*ast.Ellipsis); ok && el.Type != nil {
				return el.Type
			}
		}
		if len(x.Elts) > 0 {
			return x.Elts[0]
		}
	case *ast.StructLit:
		for _, elt := range x.Elts {
			if f, ok := elt.(*ast.Field); ok {
				if _, pattern := f.Label.(*ast.ListLit); pattern {
					return f.Value
				}
			}
		}
	case *ast.BinaryExpr:
		if el := elementOf(x.X); el != x.X {
			return el
		}
		return elementOf(x.Y)
	case *ast.UnaryExpr:
		return elementOf(x.X)
	}
	return e
}

// walk is the field labels name inside the value e, following references to
// definitions and either side of a unification or disjunction.
func (ix *navIndex) walk(e ast.Expr, labels []string) (decl, bool) {
	var found *ast.Field
	for _, l := range labels {
		f, ok := ix.fieldInExpr(e, l, 0)
		if !ok {
			return decl{}, false
		}
		found, e = f, f.Value
	}
	if found == nil {
		return decl{}, false
	}
	return decl{ix: ix, field: found}, true
}

// fieldInExpr is the field label in the struct e is, or refers to.
func (ix *navIndex) fieldInExpr(e ast.Expr, label string, depth int) (*ast.Field, bool) {
	if e == nil || depth > 8 {
		return nil, false
	}
	switch x := e.(type) {
	case *ast.StructLit:
		for _, elt := range x.Elts {
			if f, ok := elt.(*ast.Field); ok && labelName(f.Label) == label {
				return f, true
			}
		}
		for _, elt := range x.Elts {
			if em, ok := elt.(*ast.EmbedDecl); ok {
				if f, ok := ix.fieldInExpr(em.Expr, label, depth+1); ok {
					return f, true
				}
			}
		}
	case *ast.ParenExpr:
		return ix.fieldInExpr(x.X, label, depth+1)
	case *ast.UnaryExpr:
		return ix.fieldInExpr(x.X, label, depth+1)
	case *ast.BinaryExpr:
		if f, ok := ix.fieldInExpr(x.X, label, depth+1); ok {
			return f, true
		}
		return ix.fieldInExpr(x.Y, label, depth+1)
	case *ast.Ident, *ast.SelectorExpr:
		if v := ix.valueOf(x); v != nil && v != e {
			return ix.fieldInExpr(v, label, depth+1)
		}
	}
	return nil, false
}

// target is the declaration offset names: a field whose label it is in, or
// what the reference it is in resolves to.
func (ix *navIndex) target(offset int, siblings func(string) (decl, bool)) (decl, bool) {
	n := ix.nodeAt(offset)
	if n == nil {
		return decl{}, false
	}
	if f, ok := ix.labelOf[n]; ok {
		return decl{ix: ix, field: f}, true
	}
	id, ok := n.(*ast.Ident)
	if !ok {
		return decl{}, false
	}
	if c, ok := ix.chainOf[id]; ok {
		return ix.resolve(c.root, c.labels, siblings)
	}
	return ix.resolve(id, nil, siblings)
}

// addonSiblings finds a top-level name in the CUE an addon file compiles
// with: its parameter.cue and the files of its package.
func addonSiblings(path string) func(string) (decl, bool) {
	root, kind, ok := addonCUEFile(path)
	if !ok || kind == addonKindConfig || kind == addonKindView {
		return nil
	}
	return func(name string) (decl, bool) {
		files := []string{filepath.Join(root, addonParameterFile), filepath.Join(root, addonResourcesDir, addonParameterFile), filepath.Join(root, addonTemplateFile)}
		more, _ := filepath.Glob(filepath.Join(root, addonResourcesDir, "*.cue"))
		for _, f := range append(files, more...) {
			if f == path {
				continue
			}
			//nolint:gosec // reading the addon's own files is the point
			src, err := os.ReadFile(f)
			if err != nil {
				continue
			}
			ix, ok := parseNav(f, string(src))
			if !ok {
				continue
			}
			for _, d := range ix.file.Decls {
				if field, ok := d.(*ast.Field); ok && labelName(field.Label) == name {
					return decl{ix: ix, field: field}, true
				}
			}
		}
		return decl{}, false
	}
}

// Declaration is where the name at offset in doc, the file at path, is
// declared: a field, a let or a comprehension's variable, in the file or,
// for an addon's CUE, in the files compiled with it.
func Declaration(path, doc string, offset int) (Location, bool) {
	ix, ok := parseNav(path, doc)
	if !ok {
		return Location{}, false
	}
	d, ok := ix.target(offset, addonSiblings(path))
	if !ok {
		return Location{}, false
	}
	return d.location(), true
}

// References are the declaration of the name at offset in doc and every
// reference to it there.
func References(path, doc string, offset int) ([]Range, bool) {
	ix, ok := parseNav(path, doc)
	if !ok {
		return nil, false
	}
	siblings := addonSiblings(path)
	target, ok := ix.target(offset, siblings)
	if !ok {
		return nil, false
	}
	key := target.key()
	var out []Range
	add := func(start, end token.Pos) {
		out = append(out, Range{Start: Position{Line: start.Line(), Column: start.Column()}, End: Position{Line: end.Line(), Column: end.Column()}})
	}
	if target.ix == ix {
		if target.field != nil {
			for f, p := range ix.pathOf {
				if ix.path+"|"+p == key {
					add(f.Label.Pos(), f.Label.End())
				}
			}
		} else {
			l := target.location()
			out = append(out, l.Range)
		}
	}
	for _, id := range ix.idents {
		if _, isLabel := ix.labelOf[id]; isLabel {
			continue
		}
		var d decl
		var ok bool
		if c, isSel := ix.chainOf[id]; isSel {
			d, ok = ix.resolve(c.root, c.labels, siblings)
		} else {
			d, ok = ix.resolve(id, nil, siblings)
		}
		if ok && d.key() == key && !(d.field == nil && d.node == ast.Node(id)) {
			add(id.Pos(), id.End())
		}
	}
	return out, true
}

var cueIdent = regexp.MustCompile(`^[A-Za-z_$#][A-Za-z0-9_$#]*$`)

// RenameEdits rename the declaration at offset in doc, and every reference
// to it there, to name.
func RenameEdits(path, doc string, offset int, name string) ([]RangeEdit, error) {
	if !cueIdent.MatchString(name) {
		return nil, fmt.Errorf("%s is not a name a reference can use: letters, digits, _, $ and #, not starting with a digit", strconv.Quote(name))
	}
	refs, ok := References(path, doc, offset)
	if !ok || len(refs) == 0 {
		return nil, fmt.Errorf("nothing to rename here")
	}
	edits := make([]RangeEdit, 0, len(refs))
	for _, r := range refs {
		edits = append(edits, RangeEdit{Range: r, NewText: name})
	}
	return edits, nil
}
