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
	"reflect"
	"strings"

	"cuelang.org/go/cue"
	"cuelang.org/go/cue/ast"
	"cuelang.org/go/cue/parser"

	"github.com/oam-dev/kubevela/pkg/appfile"
)

// Kind is the shape of a parameter field.
type Kind string

// Kinds a parameter field can take.
const (
	KindString  Kind = "string"
	KindInt     Kind = "integer"
	KindNumber  Kind = "number"
	KindBool    Kind = "boolean"
	KindBytes   Kind = "bytes"
	KindObject  Kind = "object"
	KindMap     Kind = "map"
	KindArray   Kind = "array"
	KindOneOf   Kind = "oneOf"
	KindAny     Kind = "any"
	KindUnknown Kind = ""
)

// maxDepth bounds the walk, whatever the template's shape.
const maxDepth = 32

// Field is one parameter, as read from the compiled template.
type Field struct {
	Name        string
	Kind        Kind
	Description string
	Immutable   bool
	// Optional is true for a field that need not be supplied: marked `?`, or
	// carrying a default.
	Optional bool
	Nullable bool
	// HasDefault distinguishes a null default from none.
	HasDefault bool
	Default    any
	Enum       []any

	Min, Max                   *float64
	ExclusiveMin, ExclusiveMax bool
	MinLength, MaxLength       *uint64
	MinItems, MaxItems         *uint64
	MinProperties              *uint64
	MultipleOf                 *float64
	Pattern                    string
	NotPattern                 string
	NotEqual                   []any
	UniqueItems                bool

	// Fields are an object's fields, in declaration order.
	Fields []*Field
	// Items is an array's element.
	Items *Field
	// Values is a map's value.
	Values *Field
	// Variants are the alternatives of a disjunction that is not an enum.
	Variants []*Field
	// Open marks an object that accepts fields beyond those it declares.
	Open bool
	// Recursive marks an object cut short where its type refers to itself.
	Recursive bool
	// Condition is set on a field that exists only for some values of a
	// sibling.
	Condition *Condition
	// Discriminator names the sibling that decides which conditional fields
	// of this object exist.
	Discriminator string
}

// Condition limits a field to some values of a sibling field.
type Condition struct {
	Field  string
	Values []any
}

// BuildParameter reads the parameter value of a compiled template. src is the
// template's source, used only to find which fields an `if` reads.
func BuildParameter(param cue.Value, src string) (*Field, error) {
	w := &walker{root: param, condNames: conditionNames(src)}
	f := w.field("", param, cue.Path{}, nil, 0)
	if w.err != nil {
		return nil, w.err
	}
	return f, nil
}

type walker struct {
	root      cue.Value
	condNames map[string]bool
	err       error
}

func (w *walker) fail(format string, args ...any) {
	if w.err == nil {
		w.err = fmt.Errorf(format, args...)
	}
}

func (w *walker) field(name string, v cue.Value, path cue.Path, refs []string, depth int) *Field {
	f := &Field{Name: name}
	if depth > maxDepth {
		f.Kind, f.Recursive = KindObject, true
		return f
	}
	f.Description, f.Immutable = describe(v)

	if root, ref := v.ReferencePath(); root.Exists() && ref.String() != "" {
		r := ref.String()
		for _, seen := range refs {
			if seen == r {
				f.Kind, f.Recursive = KindObject, true
				return f
			}
		}
		refs = append(append([]string(nil), refs...), r)
	}

	op, args := unwrap(v)
	if d, ok := v.Default(); ok && d.IsConcrete() && d.Validate(cue.Concrete(true)) == nil {
		var x any
		if err := d.Decode(&x); err == nil && !emptyOpen(x, op) {
			// A null default makes the field optional but is not shown.
			f.Default = x
			f.HasDefault = true
		}
	}

	kind, shape := effectiveKind(v)
	if kind&cue.NullKind != 0 && kind != cue.NullKind && kind != cue.TopKind {
		f.Nullable = true
		kind &^= cue.NullKind
	}

	if op == cue.OrOp {
		w.disjunction(f, v, args, path, refs, depth)
		return f
	}

	switch kind {
	case cue.StringKind:
		f.Kind = KindString
	case cue.IntKind:
		f.Kind = KindInt
	case cue.FloatKind, cue.NumberKind:
		f.Kind = KindNumber
	case cue.BoolKind:
		f.Kind = KindBool
	case cue.BytesKind:
		f.Kind = KindBytes
	case cue.StructKind:
		w.object(f, shape, path, refs, depth)
	case cue.ListKind:
		f.Kind = KindArray
		if elem, ok := listElem(shape); ok {
			f.Items = w.field("", elem, appendPath(path, cue.AnyIndex), refs, depth+1)
		} else if it, err := shape.List(); err == nil && it.Next() {
			// A closed list reads its element's shape from the first entry,
			// but not that entry's value.
			f.Items = w.field("", it.Value(), path, refs, depth+1)
			f.Items.Enum, f.Items.Default, f.Items.HasDefault = nil, nil, false
		}
	case cue.TopKind:
		f.Kind = KindAny
	default:
		if kind.IsAnyOf(cue.StringKind | cue.NumberKind | cue.BoolKind) {
			f.Kind = KindAny
		} else {
			f.Kind = KindUnknown
		}
	}
	if v.IsConcrete() && kind&(cue.StructKind|cue.ListKind) == 0 {
		var x any
		if err := v.Decode(&x); err == nil {
			f.Enum = []any{x}
		}
	}
	constraints(f, v)
	return f
}

// disjunction reads `a | b | c`: an enum when every branch is concrete,
// otherwise variants.
func (w *walker) disjunction(f *Field, v cue.Value, args []cue.Value, path cue.Path, refs []string, depth int) {
	var enum []any
	var kinds []cue.Kind
	allConcrete := true
	for _, a := range args {
		if a.IncompleteKind() == cue.NullKind {
			f.Nullable = true
			continue
		}
		if !a.IsConcrete() || a.IncompleteKind()&(cue.StructKind|cue.ListKind) != 0 {
			allConcrete = false
			break
		}
		var x any
		if err := a.Decode(&x); err != nil {
			allConcrete = false
			break
		}
		if !containsValue(enum, x) {
			enum = append(enum, x)
		}
		kinds = append(kinds, a.IncompleteKind())
	}
	if allConcrete && len(enum) > 0 {
		f.Enum = enum
		f.Kind = kindOf(kinds)
		return
	}

	// A `_` arm, such as a default read from the open `context`, says
	// nothing about the shape.
	var branches []cue.Value
	nullable := false
	for _, a := range args {
		switch a.IncompleteKind() {
		case cue.NullKind:
			nullable = true
		case cue.TopKind:
		default:
			branches = append(branches, a)
		}
	}
	if len(branches) == 0 {
		f.Kind, f.Nullable = KindAny, nullable
		return
	}
	if k, ok := sameScalarKind(branches); ok && len(branches) > 1 {
		// `"go" | "java" | string` is a string: the literals are only
		// suggestions.
		f.Kind, f.Nullable = k, nullable
		return
	}
	if len(branches) == 1 {
		inner := w.field(f.Name, branches[0], path, refs, depth)
		inner.Default, inner.HasDefault, inner.Nullable = f.Default, f.HasDefault, nullable
		if inner.Description == "" {
			inner.Description = f.Description
		}
		inner.Immutable = inner.Immutable || f.Immutable
		*f = *inner
		return
	}

	// A list with a default is `*[...] | [...T]`: the schema is the open list.
	if v.IncompleteKind() == cue.ListKind {
		for _, b := range branches {
			if b.Len().IsConcrete() {
				// A closed list is the default's value, not the element type.
				continue
			}
			if elem := b.LookupPath(cue.MakePath(cue.AnyIndex)); elem.Exists() {
				f.Kind = KindArray
				f.Items = w.field("", elem, appendPath(path, cue.AnyIndex), refs, depth+1)
				return
			}
		}
	}

	f.Kind = KindOneOf
	for _, b := range branches {
		f.Variants = append(f.Variants, w.field("", b, path, refs, depth+1))
	}
}

// object reads a struct's fields, and the conditional fields an `if` adds for
// each value of a finite sibling.
func (w *walker) object(f *Field, v cue.Value, path cue.Path, refs []string, depth int) {
	f.Kind = KindObject
	base := w.fields(v, path, refs, depth)
	if pv := v.LookupPath(cue.MakePath(cue.AnyString)); pv.Exists() {
		// `...` reads as a `_` pattern: an open struct, not a map, unless it
		// declares nothing else.
		switch {
		case len(base) == 0:
			f.Kind = KindMap
			f.Values = w.field("", pv, appendPath(path, cue.AnyString), refs, depth+1)
		case pv.IncompleteKind() == cue.TopKind:
			f.Open = true
		default:
			f.Values = w.field("", pv, appendPath(path, cue.AnyString), refs, depth+1)
		}
	}

	for _, c := range base {
		if !w.condNames[c.Name] || (c.Kind != KindBool && len(c.Enum) < 2) {
			continue
		}
		if w.branch(f, c, base, path, refs, depth) {
			return
		}
	}
	f.Fields = base
}

// branch evaluates the object once for each value of the candidate
// discriminator. A field present for only some values becomes conditional.
func (w *walker) branch(f *Field, disc *Field, base []*Field, path cue.Path, refs []string, depth int) bool {
	values := disc.Enum
	if disc.Kind == KindBool {
		values = []any{true, false}
	}
	type seen struct {
		field  *Field
		values []any
	}
	var order []string
	found := map[string]*seen{}
	add := func(fl *Field, val any) {
		s, ok := found[fl.Name]
		if !ok {
			s = &seen{field: fl}
			found[fl.Name] = s
			order = append(order, fl.Name)
		}
		s.values = append(s.values, val)
	}
	for _, fl := range base {
		found[fl.Name] = &seen{field: fl}
		order = append(order, fl.Name)
	}
	for _, val := range values {
		filled := w.root.FillPath(pathJoin(path, disc.Name), val)
		sub := filled.LookupPath(path)
		for _, fl := range w.fields(sub, path, refs, depth) {
			if fl.Name == disc.Name {
				continue
			}
			add(fl, val)
		}
	}
	conditional := false
	var out []*Field
	for _, name := range order {
		s := found[name]
		fl := s.field
		if fl.Name == disc.Name {
			// Keep the discriminator's enum and default from the unfilled read.
			out = append(out, fl)
			continue
		}
		if len(s.values) > 0 && len(s.values) < len(values) && !inBase(base, name) {
			fl.Condition = &Condition{Field: disc.Name, Values: s.values}
			conditional = true
		}
		out = append(out, fl)
	}
	if !conditional {
		return false
	}
	f.Fields = out
	f.Discriminator = disc.Name
	return true
}

func (w *walker) fields(v cue.Value, path cue.Path, refs []string, depth int) []*Field {
	it, err := v.Fields(cue.Optional(true))
	if err != nil {
		return nil
	}
	var out []*Field
	for it.Next() {
		sel := it.Selector()
		name := labelName(sel)
		child := w.field(name, it.Value(), appendPath(path, sel), refs, depth+1)
		if sel.ConstraintType() == cue.OptionalConstraint || child.HasDefault {
			child.Optional = true
		}
		out = append(out, child)
	}
	return out
}

// constraints reads bounds, patterns and validators from a conjunction.
func constraints(f *Field, v cue.Value) {
	if op, _ := unwrap(v); op == cue.SelectorOp {
		v = cue.Dereference(v)
	}
	parts := []cue.Value{v}
	if op, args := unwrap(v); op == cue.AndOp {
		parts = flattenAnd(args)
	}
	for _, p := range parts {
		pop, pargs := unwrap(p)
		switch pop {
		case cue.GreaterThanEqualOp, cue.GreaterThanOp:
			if n, ok := number(pargs); ok {
				if f.Min == nil || n > *f.Min {
					f.Min, f.ExclusiveMin = &n, pop == cue.GreaterThanOp
				}
			}
		case cue.LessThanEqualOp, cue.LessThanOp:
			if n, ok := number(pargs); ok {
				if f.Max == nil || n < *f.Max {
					f.Max, f.ExclusiveMax = &n, pop == cue.LessThanOp
				}
			}
		case cue.RegexMatchOp:
			if s, ok := str(pargs); ok {
				f.Pattern = s
			}
		case cue.NotRegexMatchOp:
			if s, ok := str(pargs); ok {
				f.NotPattern = s
			}
		case cue.NotEqualOp:
			if len(pargs) == 1 {
				var x any
				if pargs[0].Decode(&x) == nil {
					f.NotEqual = append(f.NotEqual, x)
				}
			}
		case cue.CallOp:
			call(f, pargs)
		}
	}
	// int32 and friends arrive as bounds; they are the kind's own range, not a
	// constraint worth showing.
	if f.Kind == KindInt && f.Min != nil && f.Max != nil && *f.Min <= -2147483648 {
		f.Min, f.Max = nil, nil
	}
}

func call(f *Field, args []cue.Value) {
	if len(args) == 0 {
		return
	}
	name := funcName(args[0])
	var n *uint64
	if len(args) > 1 {
		if i, err := args[1].Uint64(); err == nil {
			n = &i
		}
	}
	switch name {
	case "strings.MinRunes":
		f.MinLength = n
	case "strings.MaxRunes":
		f.MaxLength = n
	case "list.MinItems":
		f.MinItems = n
	case "list.MaxItems":
		f.MaxItems = n
	case "list.UniqueItems":
		f.UniqueItems = true
	case "struct.MinFields":
		f.MinProperties = n
	case "math.MultipleOf":
		if len(args) > 1 {
			if x, err := args[1].Float64(); err == nil {
				f.MultipleOf = &x
			}
		}
	}
}

func funcName(fn cue.Value) string {
	if op, args := fn.Expr(); op == cue.SelectorOp && len(args) == 2 {
		if sel, err := args[1].String(); err == nil {
			return pkgName(args[0]) + "." + sel
		}
	}
	return fmt.Sprint(fn)
}

// pkgName names a builtin package from the functions it holds, since the
// package value itself has no name.
func pkgName(pkg cue.Value) string {
	for _, candidate := range []struct{ field, name string }{
		{"MinRunes", "strings"}, {"MinItems", "list"}, {"MinFields", "struct"}, {"MultipleOf", "math"},
	} {
		if pkg.LookupPath(cue.ParsePath(candidate.field)).Exists() {
			return candidate.name
		}
	}
	return ""
}

// unwrap strips the NoOp layers Expr puts around a value.
func unwrap(v cue.Value) (cue.Op, []cue.Value) {
	op, args := v.Expr()
	for i := 0; i < maxDepth && op == cue.NoOp && len(args) == 1; i++ {
		nop, nargs := args[0].Expr()
		if nop == cue.NoOp && len(nargs) == 1 && reflect.DeepEqual(nargs, args) {
			break
		}
		op, args = nop, nargs
	}
	return op, args
}

func flattenAnd(args []cue.Value) []cue.Value {
	var out []cue.Value
	for _, a := range args {
		op, inner := unwrap(a)
		if op == cue.AndOp {
			out = append(out, flattenAnd(inner)...)
			continue
		}
		out = append(out, a)
	}
	return out
}

func number(args []cue.Value) (float64, bool) {
	if len(args) != 1 {
		return 0, false
	}
	n, err := args[0].Float64()
	return n, err == nil
}

func str(args []cue.Value) (string, bool) {
	if len(args) != 1 {
		return "", false
	}
	s, err := args[0].String()
	return s, err == nil
}

func kindOf(kinds []cue.Kind) Kind {
	var k cue.Kind
	for _, x := range kinds {
		k |= x
	}
	switch k {
	case cue.StringKind:
		return KindString
	case cue.IntKind:
		return KindInt
	case cue.FloatKind, cue.NumberKind:
		return KindNumber
	case cue.BoolKind:
		return KindBool
	}
	return KindAny
}

func containsValue(list []any, x any) bool {
	for _, y := range list {
		if reflect.DeepEqual(x, y) {
			return true
		}
	}
	return false
}

func inBase(base []*Field, name string) bool {
	for _, f := range base {
		if f.Name == name {
			return true
		}
	}
	return false
}

func pathJoin(p cue.Path, name string) cue.Path {
	return appendPath(p, cue.Str(name))
}

func appendPath(p cue.Path, sel cue.Selector) cue.Path {
	return cue.MakePath(append(append([]cue.Selector(nil), p.Selectors()...), sel)...)
}

func labelName(sel cue.Selector) string {
	if sel.LabelType() == cue.StringLabel {
		return sel.Unquoted()
	}
	return strings.TrimRight(sel.String(), "?!")
}

// describe reads a field's doc comment the way FixOpenAPISchema does: the text
// after +usage=, cut at +short, with a +immutable line lifted out.
func describe(v cue.Value) (string, bool) {
	var parts []string
	for _, cg := range v.Doc() {
		parts = append(parts, cg.Text())
	}
	d := strings.TrimSpace(strings.Join(parts, "\n"))
	d, immutable := extractMarkerFromDescription(d, appfile.ImmutableTag)
	if strings.Contains(d, appfile.UsageTag) {
		d = strings.Split(d, appfile.UsageTag)[1]
	}
	if strings.Contains(d, appfile.ShortTag) {
		d = strings.Split(d, appfile.ShortTag)[0]
	}
	return strings.TrimSpace(d), immutable
}

// conditionNames collects every identifier read in an `if` clause, which
// bounds the fields worth evaluating as discriminators.
func conditionNames(src string) map[string]bool {
	names := map[string]bool{}
	f, err := parser.ParseFile("template", src)
	if err != nil {
		return names
	}
	ast.Walk(f, func(n ast.Node) bool {
		if c, ok := n.(*ast.IfClause); ok {
			ast.Walk(c.Condition, func(n ast.Node) bool {
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
		return true
	}, nil)
	return names
}

func literalString(lit *ast.BasicLit) (string, error) {
	return strings.Trim(lit.Value, `"`), nil
}

// effectiveKind is a value's kind, and the part of it that carries the shape.
// A conjunction with a validator (`[...string] & list.MinItems(1)`) and a
// struct holding an unresolved `if` both report bottom, though their shape is
// plain.
func effectiveKind(v cue.Value) (cue.Kind, cue.Value) {
	if k := v.IncompleteKind(); k != cue.BottomKind {
		return k, v
	}
	if op, args := unwrap(v); op == cue.AndOp {
		kind, shape := cue.TopKind, v
		for _, a := range flattenAnd(args) {
			if aop, _ := unwrap(a); aop == cue.CallOp {
				continue
			}
			if k := a.IncompleteKind(); k != cue.BottomKind {
				kind &= k
				if k&(cue.StructKind|cue.ListKind) != 0 {
					shape = a
				}
			}
		}
		if kind != cue.TopKind && kind != cue.BottomKind {
			return kind, shape
		}
	}
	if it, err := v.Fields(cue.Optional(true)); err == nil && it.Next() {
		return cue.StructKind, v
	}
	return cue.BottomKind, v
}

// emptyOpen reports the empty value CUE gives as the default of an open list
// or struct that declares none.
func emptyOpen(x any, op cue.Op) bool {
	if op == cue.OrOp {
		return false
	}
	switch x := x.(type) {
	case []any:
		return len(x) == 0
	case map[string]any:
		return len(x) == 0
	}
	return false
}

func sameScalarKind(vs []cue.Value) (Kind, bool) {
	var k cue.Kind
	for i, v := range vs {
		vk := v.IncompleteKind()
		if i > 0 && vk != k {
			return KindUnknown, false
		}
		k = vk
	}
	switch k {
	case cue.StringKind, cue.IntKind, cue.FloatKind, cue.NumberKind, cue.BoolKind:
		return kindOf([]cue.Kind{k}), true
	}
	return KindUnknown, false
}

// listElem is an open list's element type. A list with a default resolves to
// the default when looked into, so the element is read from beneath it.
func listElem(v cue.Value) (cue.Value, bool) {
	if e := v.LookupPath(cue.MakePath(cue.AnyIndex)); e.Exists() {
		return e, true
	}
	_, args := unwrap(v)
	for _, a := range args {
		if e := a.LookupPath(cue.MakePath(cue.AnyIndex)); e.Exists() {
			return e, true
		}
	}
	return cue.Value{}, false
}
