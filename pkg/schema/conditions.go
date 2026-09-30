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

package schema

import (
	"fmt"
	"sort"
	"strings"

	"cuelang.org/go/cue"
	"cuelang.org/go/cue/ast"
	"cuelang.org/go/cue/format"
	"cuelang.org/go/cue/parser"
)

// maxCandidates bounds the discriminators evaluated for one object.
const maxCandidates = 16

// Condition limits a field to some values, or to the presence, of another
// field.
type Condition struct {
	// Ref names the other field as VelaUX resolves it from the field's own
	// form: `type` for a sibling, `../mode` for a field of the enclosing
	// object, `storage.kind` for a field of a child object.
	Ref string
	// Values the other field must take, for a value condition.
	Values []any
	// Exists is set for a presence condition: true when the other field must
	// be set, false when it must not.
	Exists *bool

	// path is the other field's path under parameter, to fill when
	// evaluating a condition that depends on this one.
	path cue.Path
}

// discriminator is a field whose value may decide which fields exist.
type discriminator struct {
	ref  string
	path cue.Path
	// values are an enum's or a bool's values; nil for a presence
	// discriminator.
	values []any
	// conds are the discriminator's own conditions: it exists only when
	// they hold, so they are filled whenever it is evaluated.
	conds []Condition
}

// scope is an object being walked, as its descendants see it.
type scope struct {
	path  cue.Path
	discs []*discriminator
}

// outcome is one evaluation of a discriminator.
type outcome struct {
	value  any
	exists *bool
}

// object reads a struct's fields, and the fields an `if` adds depending on a
// field of the struct, of an enclosing struct or of a child struct.
func (w *walker) object(f *Field, v cue.Value, path cue.Path, refs []string, depth int) {
	f.Kind = KindObject
	own := w.scopeFor(v, path)
	w.scopes = append(w.scopes, own)
	base := w.fields(v, path, refs, depth)
	w.scopes = w.scopes[:len(w.scopes)-1]

	if pv := v.LookupPath(cue.MakePath(cue.AnyString)); pv.Exists() {
		// `...` reads as a `_` pattern: an open struct, not a map, unless it
		// declares nothing else.
		values := func() *Field {
			return w.field("", pv, appendPath(path, cue.AnyString), refs, depth+1)
		}
		switch {
		case len(base) == 0:
			f.Kind = KindMap
			f.Values = values()
		case pv.IncompleteKind() == cue.TopKind:
			f.Open = true
		default:
			f.Values = values()
		}
	}

	var queue []*discriminator
	queue = append(queue, own.discs...)
	for i := len(w.scopes) - 1; i >= 0; i-- {
		up := strings.Repeat("../", len(w.scopes)-i)
		for _, d := range w.scopes[i].discs {
			queue = append(queue, &discriminator{ref: up + d.ref, path: d.path, values: d.values, conds: d.conds})
		}
	}
	queue = append(queue, w.childDiscriminators(base, path, "", childDepth)...)

	fields := base
	for i := 0; i < len(queue) && i < maxCandidates; i++ {
		d := queue[i]
		var found []*discriminator
		var conditional bool
		fields, found, conditional = w.branch(d, fields, path, refs, depth)
		queue = append(queue, found...)
		if conditional && d.values != nil && !strings.ContainsAny(d.ref, "./") && len(d.conds) == 0 {
			f.Discriminators = append(f.Discriminators, d.ref)
		}
	}
	f.Fields = w.sortBySource(fields)
}

// scopeFor lists the fields of v that an `if` could read: those named in a
// condition and either finite (an enum or a bool) or optional.
func (w *walker) scopeFor(v cue.Value, path cue.Path) scope {
	s := scope{path: path}
	it, err := v.Fields(cue.Optional(true))
	if err != nil {
		return s
	}
	for it.Next() {
		sel := it.Selector()
		name := labelName(sel)
		if !w.condNames[name] {
			continue
		}
		// A regular selector: filling through `tls?` would add another
		// optional constraint rather than a value.
		fp := pathJoin(path, name)
		if values := finiteValues(it.Value()); values != nil {
			s.discs = append(s.discs, &discriminator{ref: name, path: fp, values: values})
		}
		if sel.ConstraintType() == cue.OptionalConstraint {
			s.discs = append(s.discs, &discriminator{ref: name, path: fp})
		}
	}
	return s
}

// childDepth bounds how far into child objects a condition is looked for.
const childDepth = 3

// childDiscriminators are the fields of child objects an `if` in this object
// could read, up to depth levels down, named by their dotted path.
func (w *walker) childDiscriminators(fields []*Field, path cue.Path, prefix string, depth int) []*discriminator {
	if depth == 0 {
		return nil
	}
	var out []*discriminator
	for _, c := range fields {
		if c.Kind != KindObject || len(c.Conditions) > 0 || !w.condNames[c.Name] {
			continue
		}
		cp := pathJoin(path, c.Name)
		for _, g := range c.Fields {
			if !w.condNames[g.Name] || len(g.Conditions) > 0 {
				continue
			}
			gp := pathJoin(cp, g.Name)
			ref := prefix + c.Name + "." + g.Name
			if values := g.values(); values != nil {
				out = append(out, &discriminator{ref: ref, path: gp, values: values})
			}
			if g.Optional && !g.HasDefault {
				out = append(out, &discriminator{ref: ref, path: gp})
			}
		}
		out = append(out, w.childDiscriminators(c.Fields, cp, prefix+c.Name+".", depth-1)...)
	}
	return out
}

// branch evaluates the object for each outcome of a discriminator. A field
// every outcome produces is left as it is; one only some produce gets a
// condition. A field whose shape differs by outcome is split into one field
// per shape. Finite fields that only appear in some outcomes are returned as
// further discriminators, carrying the condition that brings them.
func (w *walker) branch(d *discriminator, fields []*Field, path cue.Path, refs []string, depth int) ([]*Field, []*discriminator, bool) {
	var outcomes []outcome
	if d.values != nil {
		for _, v := range d.values {
			outcomes = append(outcomes, outcome{value: v})
		}
	} else {
		yes, no := true, false
		outcomes = []outcome{{exists: &yes}, {exists: &no}}
	}

	type variant struct {
		name  string
		shape string
		sel   cue.Selector
		value cue.Value
		prev  string
		in    []outcome
		// sub is the struct of the first outcome that produced it.
		sub cue.Value
	}
	var order []string
	variants := map[string]*variant{}
	for _, o := range outcomes {
		sub := lookupShape(w.evaluate(d, o), path)
		it, err := sub.Fields(cue.Optional(true))
		if err != nil {
			continue
		}
		prev := ""
		for it.Next() {
			sel := it.Selector()
			name := labelName(sel)
			if pathJoin(path, name).String() == d.path.String() {
				prev = name
				continue
			}
			key := name + "|" + shapeOf(it.Value())
			vr, ok := variants[key]
			if !ok {
				vr = &variant{name: name, shape: key, sel: sel, value: it.Value(), prev: prev, sub: sub}
				variants[key] = vr
				order = append(order, key)
			}
			vr.in = append(vr.in, o)
			prev = name
		}
	}

	shapes := map[string]int{}
	for _, key := range order {
		shapes[variants[key].name]++
	}
	conditional := false
	var found []*discriminator
	for _, key := range order {
		vr := variants[key]
		if len(vr.in) == len(outcomes) && shapes[vr.name] == 1 {
			continue
		}
		cond := d.condition(vr.in)
		existing := findField(fields, vr.name, vr.shape, shapes[vr.name] == 1)
		if existing == nil {
			fl := w.field(vr.name, vr.value, appendPath(path, vr.sel), refs, depth+1)
			fl.pos = vr.value.Pos()
			w.placeInClause(fl, vr.sub)
			fl.Optional = vr.sel.ConstraintType() == cue.OptionalConstraint || fl.HasDefault
			fl.Conditions = append(append([]Condition(nil), d.conds...), cond)
			fields = insertAfter(fields, fl, vr.prev)
			if w.condNames[fl.Name] {
				fp := pathJoin(path, fl.Name)
				if values := fl.values(); values != nil {
					found = append(found, &discriminator{ref: fl.Name, path: fp, values: values, conds: fl.Conditions})
				}
				if fl.Optional && !fl.HasDefault {
					found = append(found, &discriminator{ref: fl.Name, path: fp, conds: fl.Conditions})
				}
			}
		} else {
			existing.Conditions = append(existing.Conditions, cond)
			w.placeInClause(existing, vr.sub)
		}
		conditional = true
	}
	return fields, found, conditional
}

// evaluate fills the discriminator's own conditions and one of its outcomes.
func (w *walker) evaluate(d *discriminator, o outcome) cue.Value {
	root := w.root
	for _, c := range d.conds {
		root = fill(root, c.path, c.Values, c.Exists)
	}
	var values []any
	if o.exists == nil {
		values = []any{o.value}
	}
	return fill(root, d.path, values, o.exists)
}

func fill(root cue.Value, path cue.Path, values []any, exists *bool) cue.Value {
	switch {
	case len(values) > 0:
		return root.FillPath(path, values[0])
	case exists != nil && *exists:
		// Filling an optional field with top makes it present without
		// narrowing it; an optional field cannot be looked up until then.
		return root.FillPath(path, root.Context().CompileString("_"))
	}
	return root
}

// condition is the condition a field meets when it appears for the given
// outcomes of the discriminator.
func (d *discriminator) condition(in []outcome) Condition {
	c := Condition{Ref: d.ref, path: d.path}
	if d.values == nil {
		c.Exists = in[0].exists
		return c
	}
	for _, o := range in {
		c.Values = append(c.Values, o.value)
	}
	return c
}

// values are the values a finite field can take: an enum's, or a bool's.
func (f *Field) values() []any {
	switch {
	case f.Kind == KindBool:
		return []any{true, false}
	case len(f.Enum) > 1:
		return f.Enum
	}
	return nil
}

// finiteValues reads the values of an enum or bool field without walking
// anything deeper.
func finiteValues(v cue.Value) []any {
	k := v.IncompleteKind() &^ cue.NullKind
	if k == cue.BoolKind {
		return []any{true, false}
	}
	if op, args := unwrap(v); op == cue.OrOp {
		var out []any
		for _, a := range args {
			if !a.IsConcrete() || a.IncompleteKind()&(cue.StructKind|cue.ListKind) != 0 {
				return nil
			}
			var x any
			if a.Decode(&x) != nil {
				return nil
			}
			if x != nil && !containsValue(out, x) {
				out = append(out, x)
			}
		}
		if len(out) > 1 {
			return out
		}
	}
	return nil
}

// shapeOf distinguishes fields of one name that take different shapes in
// different outcomes.
func shapeOf(v cue.Value) string {
	k, _ := effectiveKind(v)
	return (k &^ cue.NullKind).String()
}

// findField finds the field a variant updates: the one of its name and shape,
// or, when the name takes only one shape, the one of its name.
func findField(fields []*Field, name, key string, oneShape bool) *Field {
	var byName *Field
	count := 0
	for _, f := range fields {
		if f.Name != name {
			continue
		}
		count++
		byName = f
		if name+"|"+f.shape() == key {
			return f
		}
	}
	if oneShape && count == 1 && len(byName.Conditions) == 0 {
		// The unconditional read took this field's shape from the
		// discriminator's default; any shape of it is the same field.
		return byName
	}
	return nil
}

// shape is the key shapeOf gives the value a field was read from.
func (f *Field) shape() string {
	switch f.Kind {
	case KindString, KindBytes:
		return cue.StringKind.String()
	case KindInt:
		return cue.IntKind.String()
	case KindNumber:
		return cue.NumberKind.String()
	case KindBool:
		return cue.BoolKind.String()
	case KindArray:
		return cue.ListKind.String()
	case KindObject, KindMap:
		return cue.StructKind.String()
	}
	return string(f.Kind)
}

// insertAfter places fl after the field named prev, or first when prev is
// empty or absent; sortBySource settles the final order.
func insertAfter(fields []*Field, fl *Field, prev string) []*Field {
	at := 0
	for i, x := range fields {
		if x.Name == prev {
			at = i + 1
		}
	}
	out := append([]*Field(nil), fields[:at]...)
	out = append(out, fl)
	return append(out, fields[at:]...)
}

// sortBySource orders fields as the template declares them. The value lists a
// field an `if` adds before the struct's own, and gives it the `if`'s
// position, so fields of one clause are ordered by the clause's labels.
func (w *walker) sortBySource(fields []*Field) []*Field {
	for _, f := range fields {
		if !f.pos.IsValid() && f.clause == "" {
			return fields
		}
	}
	rank := func(f *Field) (int, int) {
		if c, ok := w.clauses[f.clause]; ok {
			return c.offset, c.index(f.Name)
		}
		off := f.pos.Position().Offset
		if c, ok := w.clauses[offsetKey(off)]; ok {
			return off, c.index(f.Name)
		}
		return off, 0
	}
	sort.SliceStable(fields, func(i, j int) bool {
		ao, ai := rank(fields[i])
		bo, bi := rank(fields[j])
		if ao != bo {
			return ao < bo
		}
		return ai < bi
	})
	return fields
}

// clauseField is a field an `if` body declares.
type clauseField struct {
	name string
	doc  string
}

// clause is an `if` body: where it sits and the fields it declares.
type clause struct {
	offset int
	fields []clauseField
}

func (c clause) index(name string) int {
	for i, f := range c.fields {
		if f.name == name {
			return i
		}
	}
	return 0
}

// clauseMarker prefixes the definition InstrumentClauses puts in each `if`
// body. A definition is never listed as a field, so it changes no schema, but
// its presence in an evaluated struct says the body is active.
const clauseMarker = "#velaClause"

// offsetKey keys a clause that carries no marker by its position.
func offsetKey(off int) string {
	return fmt.Sprintf("@%d", off)
}

// InstrumentClauses marks every body of a comprehension made only of `if`
// clauses. A name several bodies declare has one position and one doc comment
// for all of them, so only the markers say which body a field came from.
// Bodies with a `for` clause are left alone: they can embed a scalar, which a
// definition field would conflict with.
func InstrumentClauses(src string) string {
	f, err := parser.ParseFile("template", src, parser.ParseComments)
	if err != nil {
		return src
	}
	n := 0
	ast.Walk(f, func(node ast.Node) bool {
		c, ok := node.(*ast.Comprehension)
		if !ok {
			return true
		}
		body, ok := c.Value.(*ast.StructLit)
		if !ok {
			return true
		}
		for _, cl := range c.Clauses {
			if _, ok := cl.(*ast.IfClause); !ok {
				return true
			}
		}
		marker := &ast.Field{Label: ast.NewIdent(fmt.Sprintf("%s%d", clauseMarker, n)), Value: ast.NewIdent("_")}
		body.Elts = append([]ast.Decl{marker}, body.Elts...)
		n++
		return true
	}, nil)
	out, err := format.Node(f)
	if err != nil {
		return src
	}
	return string(out)
}

// clauseOf finds the active `if` body of the struct v that declares name.
func (w *walker) clauseOf(v cue.Value, name string) (string, clauseField, bool) {
	keys := make([]string, 0, len(w.clauses))
	for key := range w.clauses {
		if strings.HasPrefix(key, clauseMarker) {
			keys = append(keys, key)
		}
	}
	// Source order, so a name two active bodies declare resolves to the first.
	sort.Slice(keys, func(i, j int) bool { return w.clauses[keys[i]].offset < w.clauses[keys[j]].offset })
	for _, key := range keys {
		c := w.clauses[key]
		for _, cf := range c.fields {
			if cf.name == name && v.LookupPath(cue.ParsePath(key)).Exists() {
				return key, cf, true
			}
		}
	}
	return "", clauseField{}, false
}

// placeInClause records which `if` body a field came from, and takes its
// description from that body's comment, since Doc() gives every body's.
func (w *walker) placeInClause(f *Field, parent cue.Value) {
	key, cf, ok := w.clauseOf(parent, f.Name)
	if !ok {
		return
	}
	f.clause = key
	if cf.doc != "" {
		f.apply(parseDoc(cf.doc))
	}
}

// scanSource collects every identifier an `if` clause reads, which bounds the
// fields worth evaluating as discriminators, and the labels each `if` body
// declares, which orders the fields it adds.
func scanSource(src string) (map[string]bool, map[string]clause) {
	names := map[string]bool{}
	clauses := map[string]clause{}
	f, err := parser.ParseFile("template", src, parser.ParseComments)
	if err != nil {
		return names, clauses
	}
	ast.Walk(f, func(n ast.Node) bool {
		c, ok := n.(*ast.Comprehension)
		if !ok {
			return true
		}
		for _, cl := range c.Clauses {
			ic, ok := cl.(*ast.IfClause)
			if !ok {
				continue
			}
			ast.Walk(ic.Condition, func(n ast.Node) bool {
				switch n := n.(type) {
				case *ast.Ident:
					names[n.Name] = true
				case *ast.SelectorExpr:
					if name, _, err := ast.LabelName(n.Sel); err == nil {
						names[name] = true
					}
				case *ast.IndexExpr:
					if lit, ok := n.Index.(*ast.BasicLit); ok {
						if s, err := literalString(lit); err == nil {
							names[s] = true
						}
					}
				}
				return true
			}, nil)
		}
		if body, ok := c.Value.(*ast.StructLit); ok && len(c.Clauses) > 0 {
			cl := clause{offset: c.Clauses[0].Pos().Offset()}
			key := offsetKey(cl.offset)
			for _, el := range body.Elts {
				fd, ok := el.(*ast.Field)
				if !ok {
					continue
				}
				name, _, err := ast.LabelName(fieldLabel(fd.Label))
				if err != nil {
					continue
				}
				if strings.HasPrefix(name, clauseMarker) {
					key = name
					continue
				}
				var doc []string
				for _, cg := range ast.Comments(fd) {
					if cg.Doc {
						doc = append(doc, cg.Text())
					}
				}
				cl.fields = append(cl.fields, clauseField{name: name, doc: strings.Join(doc, "\n")})
			}
			clauses[key] = cl
		}
		return true
	}, nil)
	return names, clauses
}

// lookupShape looks path up in v, reading a list's element or a map's value
// from beneath a default: looked into directly, `*[...] | [...T]` resolves to
// the default's entries rather than T.
func lookupShape(v cue.Value, path cue.Path) cue.Value {
	for _, sel := range path.Selectors() {
		switch {
		case sel == cue.AnyIndex:
			elem, ok := listElem(v)
			if !ok {
				return cue.Value{}
			}
			v = elem
		case sel == cue.AnyString:
			v = v.LookupPath(cue.MakePath(cue.AnyString))
		default:
			v = v.LookupPath(cue.MakePath(sel))
		}
	}
	return v
}
