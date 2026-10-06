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
	idents  []*ast.Ident
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
	ix := &navIndex{path: path, file: f, fieldOf: map[ast.Node]*ast.Field{}, pathOf: map[*ast.Field]string{}, labelOf: map[ast.Node]*ast.Field{}, chainOf: map[*ast.Ident]navChain{}}
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
		if len(labels) > 0 {
			return decl{}, false
		}
		return decl{ix: ix, node: target}, true
	case *ast.Ident:
		if len(labels) > 0 {
			return decl{}, false
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
