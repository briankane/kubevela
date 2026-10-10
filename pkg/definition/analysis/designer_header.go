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
	"sort"
	"strconv"
	"strings"

	"cuelang.org/go/cue"
	"cuelang.org/go/cue/ast"
	"cuelang.org/go/cue/cuecontext"
	"cuelang.org/go/cue/format"
	"cuelang.org/go/cue/parser"
	"cuelang.org/go/cue/token"
)

// HeaderField is a field of a definition's header that the designer shows.
type HeaderField struct {
	// Path is where it is in the header: ["attributes", "podDisruptive"].
	Path []string `json:"path"`
	// Kind is the input it takes: string, bool, int, enum, strings (a list),
	// map, cue (a CUE program, edited in the file) or other (shown, not edited).
	Kind    string   `json:"kind"`
	Enum    []string `json:"enum,omitempty"`
	Label   string   `json:"label"`
	Doc     string   `json:"doc,omitempty"`
	Section string   `json:"section"`
	// Suggest names the values offered for it: workloads, traits or definitions.
	Suggest string `json:"suggest,omitempty"`
	// Required is whether the file must set it.
	Required bool `json:"required,omitempty"`
	// Items are, for a list of structs, the fields of each, by name.
	Items []HeaderField `json:"items,omitempty"`
}

// Header sections, in the order the designer shows them.
const (
	SectionAbout      = "About"
	SectionAttributes = "Attributes"
	SectionMeta       = "Labels and annotations"
	SectionLimits     = "Restrictions"
	SectionStatus     = "Status"
)

// headerExtras are what the generated schema cannot say of an attribute:
// the values a string takes, what to suggest, and its section.
var headerExtras = map[string]HeaderField{
	"stage":              {Kind: "enum", Enum: []string{"", "PreDispatch", "DefaultDispatch", "PostDispatch"}},
	"scope":              {Kind: "enum", Enum: []string{"", "Application"}},
	"appliesToWorkloads": {Suggest: "workloads"},
	"conflictsWith":      {Suggest: "traits"},
	"version":            {Section: SectionAbout},
}

// expanded are the attribute structs whose fields the designer shows one by one.
var expanded = map[string]bool{"workload": true, "workload.definition": true, "status": true, "restrictions": true}

// HeaderFields are the header fields of a definition of type typ, in the
// order the designer shows them: the top-level ones, then its attributes as
// the Definition CRD of its type has them, less extends and abstract, which
// are shown at the top level.
func HeaderFields(typ string) []HeaderField {
	out := []HeaderField{
		{Path: []string{"description"}, Kind: "string", Label: "Description", Doc: "What it is, as VelaUX and vela show say.", Section: SectionAbout},
		{Path: []string{"alias"}, Kind: "string", Label: "Alias", Doc: "A shorter name shown beside its own.", Section: SectionAbout},
	}
	schema := cuecontext.New().BuildFile(&ast.File{Decls: append([]ast.Decl{mustField(fmt.Sprintf("#this: #attributes[%q]", typ))}, headerSchema()...)}).LookupPath(cue.ParsePath("#this"))
	if !schema.Exists() {
		return out
	}
	if schemaChild(schema, cue.Str("extends")).Exists() {
		out = append(out,
			HeaderField{Path: []string{"extends"}, Kind: "string", Label: "Extends", Doc: firstSentences(schemaChild(schema, cue.Str("extends"))), Section: SectionAbout, Suggest: "definitions"},
			HeaderField{Path: []string{"abstract"}, Kind: "bool", Label: "Abstract", Doc: firstSentences(schemaChild(schema, cue.Str("abstract"))), Section: SectionAbout},
		)
	}
	var attrs, rest []HeaderField
	walkAttributes(schema, []string{"attributes"}, &attrs)
	for _, f := range attrs {
		if f.Section == SectionAbout {
			out = append(out, f)
		} else {
			rest = append(rest, f)
		}
	}
	sort.SliceStable(rest, func(i, j int) bool { return sectionOrder(rest[i].Section) < sectionOrder(rest[j].Section) })
	out = append(out, rest...)
	return append(out,
		HeaderField{Path: []string{"labels"}, Kind: "map", Label: "Labels", Doc: "A key without oam.dev in it is written with custom.definition.oam.dev/ before it.", Section: SectionMeta},
		HeaderField{Path: []string{"annotations"}, Kind: "map", Label: "Annotations", Doc: "A key without oam.dev in it is written with custom.definition.oam.dev/ before it.", Section: SectionMeta},
	)
}

func sectionOrder(s string) int {
	for i, x := range []string{SectionAbout, SectionAttributes, SectionLimits, SectionStatus, SectionMeta} {
		if x == s {
			return i
		}
	}
	return 99
}

// walkAttributes adds the fields of an attributes schema under path to out.
func walkAttributes(schema cue.Value, path []string, out *[]HeaderField) {
	it, err := schema.Fields(cue.Optional(true))
	if err != nil {
		return
	}
	for it.Next() {
		name := strings.TrimSuffix(strings.TrimSuffix(it.Selector().String(), "?"), "!")
		if u, err := strconv.Unquote(name); err == nil {
			name = u
		}
		if len(path) == 1 && (name == "extends" || name == "abstract") {
			continue
		}
		p := append(append([]string{}, path...), name)
		key := strings.Join(p[1:], ".")
		v := it.Value()
		section := SectionAttributes
		switch p[1] {
		case "status":
			section = SectionStatus
		case "restrictions":
			section = SectionLimits
		}
		if expanded[key] && v.IncompleteKind() == cue.StructKind {
			walkAttributes(v, p, out)
			continue
		}
		f := HeaderField{Path: p, Kind: kindOfSchema(v), Label: humanise(name), Doc: firstSentences(v), Section: section}
		switch {
		case section == SectionStatus:
			f.Kind = "cue"
		case f.Kind == "list":
			// The header's lists of structs are written in the CUE.
			f.Kind = "other"
		}
		if x, ok := headerExtras[key]; ok {
			if x.Kind != "" {
				f.Kind, f.Enum = x.Kind, x.Enum
			}
			if x.Suggest != "" {
				f.Suggest = x.Suggest
			}
			if x.Section != "" {
				f.Section = x.Section
			}
		}
		*out = append(*out, f)
	}
}

// kindOfSchema is the input a field of this schema takes.
func kindOfSchema(v cue.Value) string {
	switch v.IncompleteKind() {
	case cue.StringKind:
		return "string"
	case cue.BoolKind:
		return "bool"
	case cue.IntKind, cue.NumberKind:
		return "int"
	case cue.ListKind:
		if e := v.LookupPath(cue.MakePath(cue.AnyIndex)); e.Exists() {
			switch e.IncompleteKind() {
			case cue.StringKind:
				return "strings"
			case cue.StructKind:
				return "list"
			}
		}
	case cue.StructKind:
		if e := v.LookupPath(cue.MakePath(cue.AnyString)); e.Exists() && e.IncompleteKind() == cue.StringKind {
			if it, err := v.Fields(cue.Optional(true)); err == nil && !it.Next() {
				return "map"
			}
		}
	}
	return "other"
}

// firstSentences is a schema field's doc comment up to its first blank line, on one line.
func firstSentences(v cue.Value) string {
	var lines []string
	for _, g := range v.Doc() {
		for _, l := range strings.Split(strings.TrimSpace(g.Text()), "\n") {
			if strings.TrimSpace(l) == "" {
				return strings.Join(lines, " ")
			}
			lines = append(lines, strings.TrimSpace(l))
		}
		if len(lines) > 0 {
			break
		}
	}
	return strings.Join(lines, " ")
}

// humanise is a camel-case name as a label: appliesToWorkloads is Applies to workloads.
func humanise(name string) string {
	var b strings.Builder
	for i, r := range name {
		switch {
		case i == 0:
			b.WriteString(strings.ToUpper(string(r)))
		case r >= 'A' && r <= 'Z':
			b.WriteByte(' ')
			b.WriteString(strings.ToLower(string(r)))
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// HeaderLabelKeys and HeaderAnnotationKeys are the keys KubeVela reads.
var (
	HeaderLabelKeys      = []string{"custom.definition.oam.dev/ui-hidden", "custom.definition.oam.dev/deprecated"}
	HeaderAnnotationKeys = []string{"definition.oam.dev/icon", "definition.oam.dev/example-url", "definition.oam.dev/restrict-namespaces", "definition.oam.dev/quota-exempt"}
)

// HeaderValue is a header field as a definition sets it.
type HeaderValue struct {
	HeaderField
	Set bool `json:"set"`
	// Value is its value, when it is a literal: a string, bool, number,
	// []string or map[string]string.
	Value interface{} `json:"value,omitempty"`
	// Computed is whether it is set to something the designer cannot edit.
	Computed bool `json:"computed,omitempty"`
	// Range is where it is written, to go to.
	Range *Range `json:"range,omitempty"`
}

// Header is a definition's header as the designer shows it.
type Header struct {
	Name   string        `json:"name"`
	Type   string        `json:"type"`
	Fields []HeaderValue `json:"fields"`
}

// ReadHeader is the header of the definition in src, false when src holds none.
func ReadHeader(path string, src []byte) (Header, bool) {
	f, err := parser.ParseFile(path, src, parser.ParseComments)
	if err != nil {
		return Header{}, false
	}
	d, ok := newDocument(path, src, f)
	if !ok || d.typ == "" || len(d.headers) == 0 {
		return Header{}, false
	}
	h := Header{Name: d.name, Type: d.typ}
	for _, hf := range HeaderFields(d.typ) {
		// extends and abstract may be written under attributes instead.
		if len(hf.Path) == 1 && (hf.Path[0] == "extends" || hf.Path[0] == "abstract") {
			if _, top := d.headerField(hf.Path...); !top {
				if _, under := d.headerField("attributes", hf.Path[0]); under {
					hf.Path = []string{"attributes", hf.Path[0]}
				}
			}
		}
		v := HeaderValue{HeaderField: hf}
		if fd, ok := d.headerField(hf.Path...); ok {
			v.Set = true
			r := Range{Start: positionOf(fd.Pos()), End: positionOf(fd.End())}
			v.Range = &r
			v.Value, v.Computed = literalOf(fd.Value, hf.Kind)
		}
		h.Fields = append(h.Fields, v)
	}
	return h, true
}

func positionOf(p token.Pos) Position {
	return Position{Line: p.Line(), Column: p.Column()}
}

// literalOf is the value of e as a field of kind takes it, or true for computed
// when it is not a literal of that kind.
func literalOf(e ast.Expr, kind string) (interface{}, bool) {
	switch kind {
	case "cue":
		return nil, false
	case "string", "enum":
		if s, ok := stringLit(e); ok {
			return s, false
		}
	case "bool":
		if b, ok := e.(*ast.BasicLit); ok && (b.Kind == token.TRUE || b.Kind == token.FALSE) {
			return b.Kind == token.TRUE, false
		}
		if id, ok := e.(*ast.Ident); ok && (id.Name == "true" || id.Name == "false") {
			return id.Name == "true", false
		}
	case "int":
		if b, ok := e.(*ast.BasicLit); ok && b.Kind == token.INT {
			if n, err := strconv.Atoi(b.Value); err == nil {
				return n, false
			}
		}
	case "strings":
		if l, ok := e.(*ast.ListLit); ok {
			out := []string{}
			for _, x := range l.Elts {
				s, ok := stringLit(x)
				if !ok {
					return nil, true
				}
				out = append(out, s)
			}
			return out, false
		}
	case "map":
		if s, ok := e.(*ast.StructLit); ok {
			out := map[string]string{}
			for _, x := range s.Elts {
				fd, ok := x.(*ast.Field)
				if !ok {
					return nil, true
				}
				k, ok := fieldName(fd.Label)
				v, vok := stringLit(fd.Value)
				if !ok || !vok {
					return nil, true
				}
				out[k] = v
			}
			return out, false
		}
	}
	return nil, true
}

func stringLit(e ast.Expr) (string, bool) {
	b, ok := e.(*ast.BasicLit)
	if !ok || b.Kind != token.STRING {
		return "", false
	}
	s, err := strconv.Unquote(b.Value)
	if err != nil {
		return "", false
	}
	return s, true
}

// fieldName is a label as a name: an identifier or a quoted string.
func fieldName(l ast.Label) (string, bool) {
	switch l := l.(type) {
	case *ast.Ident:
		return l.Name, true
	case *ast.BasicLit:
		s, err := strconv.Unquote(l.Value)
		return s, err == nil
	}
	return "", false
}

// EditHeader is the edit to src that sets the header field at path to value,
// or removes it for nil, and any struct it leaves empty. value is a string,
// bool, int, []string or map[string]string. The header is rewritten in the
// canonical format, comments kept; the rest of src is left as it is.
func EditHeader(path string, src []byte, at []string, value interface{}) (RangeEdit, error) {
	f, err := parser.ParseFile(path, src, parser.ParseComments)
	if err != nil {
		return RangeEdit{}, err
	}
	d, ok := newDocument(path, src, f)
	if !ok || len(d.headers) == 0 {
		return RangeEdit{}, fmt.Errorf("there is no definition header here")
	}
	if len(at) == 0 {
		return RangeEdit{}, fmt.Errorf("no field named")
	}
	known := len(at) == 2 && at[0] == "attributes" && (at[1] == "extends" || at[1] == "abstract")
	for _, hf := range HeaderFields(d.typ) {
		known = known || (strings.Join(hf.Path, ".") == strings.Join(at, ".") && hf.Kind != "cue" && hf.Kind != "other")
	}
	if !known {
		return RangeEdit{}, fmt.Errorf("%s is not a header field a %s's designer edits", strings.Join(at, "."), d.typ)
	}
	root, ok := d.headers[0].Value.(*ast.StructLit)
	if !ok || !root.Lbrace.IsValid() {
		return RangeEdit{}, fmt.Errorf("the header is not a struct in braces")
	}
	start, end := root.Pos(), root.End()
	if value == nil {
		unset(root, at)
	} else {
		lit, err := exprOf(value)
		if err != nil {
			return RangeEdit{}, err
		}
		set(root, at, lit)
	}
	out, err := format.Node(root)
	if err != nil {
		return RangeEdit{}, fmt.Errorf("the header does not format: %w", err)
	}
	return RangeEdit{Range: Range{Start: positionOf(start), End: positionOf(end)}, NewText: string(out)}, nil
}

// exprOf is value as a CUE literal.
func exprOf(value interface{}) (ast.Expr, error) {
	switch v := value.(type) {
	case string:
		return ast.NewString(v), nil
	case bool:
		return ast.NewBool(v), nil
	case int:
		return ast.NewLit(token.INT, strconv.Itoa(v)), nil
	case float64:
		return ast.NewLit(token.INT, strconv.Itoa(int(v))), nil
	case []string:
		elts := make([]ast.Expr, len(v))
		for i, s := range v {
			elts[i] = ast.NewString(s)
		}
		return ast.NewList(elts...), nil
	case []interface{}:
		elts := make([]ast.Expr, len(v))
		for i, s := range v {
			str, ok := s.(string)
			if !ok {
				return nil, fmt.Errorf("a list takes strings, not %v", s)
			}
			elts[i] = ast.NewString(str)
		}
		return ast.NewList(elts...), nil
	case map[string]string:
		return mapLit(v), nil
	case map[string]interface{}:
		m := map[string]string{}
		for k, x := range v {
			s, ok := x.(string)
			if !ok {
				return nil, fmt.Errorf("%s: a label or annotation is a string, not %v", k, x)
			}
			m[k] = s
		}
		return mapLit(m), nil
	}
	return nil, fmt.Errorf("a header field cannot be set to %T", value)
}

func mapLit(m map[string]string) ast.Expr {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	s := &ast.StructLit{Lbrace: token.Blank.Pos(), Rbrace: token.Newline.Pos()}
	for _, k := range keys {
		f := &ast.Field{Label: labelFor(k), Value: ast.NewString(m[k])}
		ast.SetRelPos(f, token.Newline)
		s.Elts = append(s.Elts, f)
	}
	return s
}

// labelFor is a field label for name: an identifier where one may be, else quoted.
func labelFor(name string) ast.Label {
	if ast.IsValidIdent(name) && !strings.HasPrefix(name, "#") && !strings.HasPrefix(name, "_") {
		return ast.NewIdent(name)
	}
	return ast.NewString(name)
}

// set sets the field at path under s to value, making the structs on the way.
func set(s *ast.StructLit, path []string, value ast.Expr) {
	for i, e := range s.Elts {
		fd, ok := e.(*ast.Field)
		if !ok {
			continue
		}
		if n, ok := fieldName(fd.Label); !ok || n != path[0] {
			continue
		}
		if len(path) == 1 {
			ast.SetComments(value, ast.Comments(fd.Value))
			fd.Value = value
			return
		}
		child, ok := fd.Value.(*ast.StructLit)
		if !ok {
			child = &ast.StructLit{}
		}
		braced(child)
		set(child, path[1:], value)
		fd.Value = child
		s.Elts[i] = fd
		return
	}
	var v ast.Expr = value
	for i := len(path) - 1; i > 0; i-- {
		v = &ast.StructLit{Lbrace: token.Blank.Pos(), Elts: []ast.Decl{&ast.Field{Label: labelFor(path[i]), Value: v}}, Rbrace: token.Newline.Pos()}
	}
	field := &ast.Field{Label: labelFor(path[0]), Value: v}
	ast.SetRelPos(field, token.Newline)
	s.Elts = append(s.Elts, field)
}

// braced makes a struct written as a: b: c print with braces once it holds more.
func braced(s *ast.StructLit) {
	if !s.Lbrace.IsValid() {
		s.Lbrace = token.Blank.Pos()
		s.Rbrace = token.Newline.Pos()
		for _, e := range s.Elts {
			ast.SetRelPos(e, token.Newline)
		}
	}
}

// unset removes the field at path under s, and each struct it leaves empty;
// it reports whether s is empty after.
func unset(s *ast.StructLit, path []string) bool {
	for i, e := range s.Elts {
		fd, ok := e.(*ast.Field)
		if !ok {
			continue
		}
		if n, ok := fieldName(fd.Label); !ok || n != path[0] {
			continue
		}
		if len(path) == 1 {
			s.Elts = append(s.Elts[:i], s.Elts[i+1:]...)
			break
		}
		if child, ok := fd.Value.(*ast.StructLit); ok && unset(child, path[1:]) {
			s.Elts = append(s.Elts[:i], s.Elts[i+1:]...)
		}
		break
	}
	return len(s.Elts) == 0
}

// HeaderSuggestions are the values the designer offers a definition of type
// typ, by what a field's Suggest names: workloads (component types and the
// resources they make), traits, and definitions of its own type; and the
// label and annotation keys KubeVela reads.
func HeaderSuggestions(typ string, opts Options) map[string][]string {
	seen := map[string]bool{}
	var workloads []string
	for _, d := range DefinitionsOfType(componentType, opts) {
		names, _, _ := workloadNames(d)
		for _, n := range names {
			if !seen[n] {
				seen[n] = true
				workloads = append(workloads, n)
			}
		}
	}
	sort.Strings(workloads)
	namesOf := func(t string) []string {
		out := []string{}
		for _, d := range DefinitionsOfType(t, opts) {
			out = append(out, d.Name)
		}
		return out
	}
	return map[string][]string{
		"workloads":   append([]string{"*"}, workloads...),
		"traits":      namesOf("trait"),
		"definitions": namesOf(typ),
		"labels":      HeaderLabelKeys,
		"annotations": HeaderAnnotationKeys,
	}
}
