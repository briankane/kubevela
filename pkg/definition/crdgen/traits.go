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

package crdgen

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"cuelang.org/go/cue/ast"
	"cuelang.org/go/cue/parser"
)

// Definition is a trait already made, as the workspace, the cluster or
// KubeVela has it.
type Definition struct {
	Name   string
	Source string
	CUE    string
	// Path is its file, when it is one in the workspace that can be edited.
	Path string
}

// Existing is a trait already made that patches fields a CRD's spec has.
type Existing struct {
	Name   string `json:"name"`
	Source string `json:"source"`
	// Fields are the spec fields it sets, each one it may be chosen for.
	Fields [][]string `json:"fields"`
	// Applies is whether its appliesToWorkloads already names the CRD.
	Applies bool `json:"applies"`
	// Editable is whether its file is in the workspace, so its
	// appliesToWorkloads can be made to name the CRD.
	Editable bool `json:"editable,omitempty"`
}

// write is a path a trait's patch sets in the workload, "[]" for a list's
// elements, with the workload paths its guards need to exist.
type write struct {
	path  []string
	needs [][]string
}

// traitShape is what a trait's definition says of what it patches.
type traitShape struct {
	appliesTo []string
	// any is whether it applies to every workload.
	any    bool
	writes []write
}

// readTrait is the shape of a trait definition's CUE, false for one that is
// not a trait or does not parse.
func readTrait(src string) (traitShape, bool) {
	f, err := parser.ParseFile("trait.cue", src, parser.ParseComments)
	if err != nil {
		return traitShape{}, false
	}
	var header, template *ast.StructLit
	for _, d := range f.Decls {
		fd, ok := d.(*ast.Field)
		if !ok {
			continue
		}
		s, ok := fd.Value.(*ast.StructLit)
		if !ok {
			continue
		}
		if name, _ := labelName(fd.Label); name == "template" {
			template = s
		} else if t, ok := fieldOf(s, "type").(*ast.BasicLit); ok && t.Value == `"trait"` {
			header = s
		}
	}
	if header == nil || template == nil {
		return traitShape{}, false
	}
	var t traitShape
	attrs, _ := fieldOf(header, "attributes").(*ast.StructLit)
	list, _ := fieldOf(attrs, "appliesToWorkloads").(*ast.ListLit)
	if list == nil {
		t.any = true
	} else {
		for _, e := range list.Elts {
			if b, ok := e.(*ast.BasicLit); ok {
				if v, err := strconv.Unquote(b.Value); err == nil {
					t.appliesTo = append(t.appliesTo, v)
					t.any = t.any || v == "*"
				}
			}
		}
	}
	scope := map[string]ast.Expr{}
	for _, d := range template.Elts {
		if fd, ok := d.(*ast.Field); ok {
			// The parameter is followed too: patch: spec: parameter sets each of its fields.
			if name, ok := labelName(fd.Label); ok && name != "patch" {
				scope[name] = fd.Value
			}
		}
	}
	if patch := fieldOf(template, "patch"); patch != nil {
		writesOf(patch, nil, nil, scope, 0, &t.writes)
	}
	return t, true
}

// fieldOf is the value of s's field named name, or nil.
func fieldOf(s *ast.StructLit, name string) ast.Expr {
	if s == nil {
		return nil
	}
	for _, d := range s.Elts {
		if fd, ok := d.(*ast.Field); ok {
			if n, ok := labelName(fd.Label); ok && n == name {
				return fd.Value
			}
		}
	}
	return nil
}

// labelName is a field's name, false for one computed.
func labelName(l ast.Label) (string, bool) {
	switch l := l.(type) {
	case *ast.Ident:
		return l.Name, true
	case *ast.BasicLit:
		if v, err := strconv.Unquote(l.Value); err == nil {
			return v, true
		}
	}
	return "", false
}

// maxResolve bounds how deep a patch's references to its own lets are followed.
const maxResolve = 8

// writesOf adds the paths e sets, under path, to out: every branch of a
// comprehension, a list's elements under "[]", and a reference to one of
// the template's own values followed into it.
func writesOf(e ast.Expr, path []string, needs [][]string, scope map[string]ast.Expr, depth int, out *[]write) {
	leaf := func() {
		if len(path) > 0 {
			*out = append(*out, write{path: append([]string{}, path...), needs: needs})
		}
	}
	switch e := e.(type) {
	case *ast.StructLit:
		inner := scope
		copied := false
		for _, d := range e.Elts {
			if l, ok := d.(*ast.LetClause); ok {
				if !copied {
					inner, copied = copyScope(scope), true
				}
				inner[l.Ident.Name] = l.Expr
			}
		}
		if len(e.Elts) == 0 {
			leaf()
		}
		for _, d := range e.Elts {
			switch d := d.(type) {
			case *ast.Field:
				name, ok := labelName(d.Label)
				if !ok {
					// A computed label sets whatever it names under path.
					leaf()
					continue
				}
				writesOf(d.Value, append(append([]string{}, path...), name), needs, inner, depth, out)
			case *ast.Comprehension:
				writesOf(d.Value, path, append(append([][]string{}, needs...), guards(d.Clauses)...), inner, depth, out)
			case *ast.EmbedDecl:
				writesOf(d.Expr, path, needs, inner, depth, out)
			}
		}
	case *ast.ListLit:
		elems := append(append([]string{}, path...), "[]")
		for _, x := range e.Elts {
			if _, ok := x.(*ast.Ellipsis); !ok {
				writesOf(x, elems, needs, scope, depth, out)
			}
		}
		if len(e.Elts) == 0 {
			leaf()
		}
	case *ast.Comprehension:
		writesOf(e.Value, path, append(append([][]string{}, needs...), guards(e.Clauses)...), scope, depth, out)
	case *ast.Ident:
		if v, ok := scope[e.Name]; ok && depth < maxResolve {
			writesOf(v, path, needs, scope, depth+1, out)
			return
		}
		leaf()
	case *ast.ParenExpr:
		writesOf(e.X, path, needs, scope, depth, out)
	default:
		leaf()
	}
}

func copyScope(s map[string]ast.Expr) map[string]ast.Expr {
	out := make(map[string]ast.Expr, len(s)+1)
	for k, v := range s {
		out[k] = v
	}
	return out
}

// guards are the workload paths an if needs to exist: each
// context.output.<path> != _|_ among its conditions, joined by &&.
func guards(clauses []ast.Clause) [][]string {
	var out [][]string
	var walk func(e ast.Expr)
	walk = func(e ast.Expr) {
		b, ok := e.(*ast.BinaryExpr)
		if !ok {
			if p, ok := e.(*ast.ParenExpr); ok {
				walk(p.X)
			}
			return
		}
		if b.Op.String() == "&&" {
			walk(b.X)
			walk(b.Y)
			return
		}
		if b.Op.String() != "!=" {
			return
		}
		if _, ok := b.Y.(*ast.BottomLit); !ok {
			return
		}
		if p := selectorPath(b.X); len(p) > 2 && p[0] == "context" && p[1] == "output" {
			out = append(out, p[2:])
		}
	}
	for _, c := range clauses {
		if ic, ok := c.(*ast.IfClause); ok {
			walk(ic.Condition)
		}
	}
	return out
}

// selectorPath is a.b.c as its names, or nil for anything else.
func selectorPath(e ast.Expr) []string {
	switch e := e.(type) {
	case *ast.Ident:
		return []string{e.Name}
	case *ast.SelectorExpr:
		head := selectorPath(e.X)
		name, ok := labelName(e.Sel)
		if head == nil || !ok {
			return nil
		}
		return append(head, name)
	}
	return nil
}

// has is whether a custom resource of this spec can have the path: its
// spec's schema has it, or a part of it that keeps any field does; and
// metadata, which every object has.
func has(spec *schema, path []string) bool {
	if len(path) == 0 {
		return true
	}
	switch path[0] {
	case "metadata", "apiVersion", "kind":
		return true
	case "spec":
	default:
		return false
	}
	s := spec
	for _, seg := range path[1:] {
		switch {
		case s.PreserveUnknown || (s.Additional != nil && seg != "[]"):
			return true
		case seg == "[]":
			if s.Items == nil {
				return false
			}
			s = s.Items
		default:
			s = at(s, []string{seg})
			if s == nil {
				return false
			}
		}
	}
	return true
}

// fits is the spec fields a trait sets in a CRD of this spec, false when
// any path it sets is one the CRD does not have or it sets no spec field.
// A write guarded on a path the CRD does not have is one it never makes.
// Each field is where the paths under one top-level field part, before any list.
func fits(spec *schema, t traitShape) ([][]string, bool) {
	byTop := map[string][][]string{}
	var tops []string
	for _, w := range t.writes {
		live := true
		for _, n := range w.needs {
			live = live && has(spec, n)
		}
		if !live {
			continue
		}
		if !has(spec, w.path) {
			return nil, false
		}
		if w.path[0] != "spec" || len(w.path) < 2 {
			continue
		}
		p := w.path[1:]
		for i, seg := range p {
			if seg == "[]" {
				p = p[:i]
				break
			}
		}
		if _, ok := byTop[p[0]]; !ok {
			tops = append(tops, p[0])
		}
		byTop[p[0]] = append(byTop[p[0]], p)
	}
	if len(tops) == 0 {
		return nil, false
	}
	out := make([][]string, 0, len(tops))
	for _, top := range tops {
		ps := byTop[top]
		common := ps[0]
		for _, p := range ps[1:] {
			n := 0
			for n < len(common) && n < len(p) && common[n] == p[n] {
				n++
			}
			common = common[:n]
		}
		if len(common) > maxDepth+1 {
			common = common[:maxDepth+1]
		}
		out = append(out, common)
	}
	return out, true
}

// existingFor are the traits among defs that patch fields info's spec has,
// by name, leaving out those for every workload.
func existingFor(spec *schema, info Info, defs []Definition) []Existing {
	resource := info.Plural + "." + info.Group
	var out []Existing
	for _, d := range defs {
		t, ok := readTrait(d.CUE)
		if !ok || t.any {
			continue
		}
		fields, ok := fits(spec, t)
		if !ok {
			continue
		}
		applies := false
		for _, a := range t.appliesTo {
			applies = applies || a == resource
		}
		out = append(out, Existing{Name: d.Name, Source: d.Source, Fields: fields, Applies: applies, Editable: d.Path != "" && strings.HasSuffix(d.Path, ".cue")})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// addWorkload is a trait's CUE with resource added to its appliesToWorkloads.
func addWorkload(src, resource string) (string, error) {
	f, err := parser.ParseFile("trait.cue", src, parser.ParseComments)
	if err != nil {
		return "", err
	}
	for _, d := range f.Decls {
		fd, ok := d.(*ast.Field)
		if !ok {
			continue
		}
		s, ok := fd.Value.(*ast.StructLit)
		if !ok {
			continue
		}
		attrs, _ := fieldOf(s, "attributes").(*ast.StructLit)
		list, _ := fieldOf(attrs, "appliesToWorkloads").(*ast.ListLit)
		if list == nil {
			continue
		}
		at := list.Rbrack.Offset()
		if at <= 0 || at > len(src) {
			return "", fmt.Errorf("appliesToWorkloads could not be found in the text")
		}
		insert := strconv.Quote(resource)
		if len(list.Elts) > 0 {
			end := list.Elts[len(list.Elts)-1].End().Offset()
			return src[:end] + ", " + insert + src[end:], nil
		}
		return src[:at] + insert + src[at:], nil
	}
	return "", fmt.Errorf("it has no appliesToWorkloads to add %s to", resource)
}
