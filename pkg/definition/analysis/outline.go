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
	"strconv"
	"strings"

	"cuelang.org/go/cue/ast"
	"cuelang.org/go/cue/parser"
	"cuelang.org/go/cue/token"
)

// SymbolKind is what a Symbol is.
type SymbolKind int

// Symbol kinds.
const (
	// SymbolDefinition is a definition's header, by its name.
	SymbolDefinition SymbolKind = iota
	// SymbolSection is a part of a template KubeVela reads: template,
	// parameter, output, outputs, patch, status.
	SymbolSection
	// SymbolParameter is a parameter.
	SymbolParameter
	// SymbolObject is a Kubernetes object, as an output.
	SymbolObject
	// SymbolHelper is a hidden field, a let, or a CUE definition (#X).
	SymbolHelper
	// SymbolField is any other field.
	SymbolField
)

// Symbol is an entry of a file's outline.
type Symbol struct {
	Name   string
	Detail string
	Kind   SymbolKind
	// Range is the whole entry; Selection is its name.
	Range, Selection Range
	Children         []Symbol
}

// outlineDepth bounds how deep an outline goes, past parameter, which is
// shown whole.
const outlineDepth = 3

// sections are the fields KubeVela reads of a template.
var sections = map[string]bool{templateLabel: true, parameterLabel: true, "output": true, "outputs": true, "patch": true, "patchOutputs": true, "status": true, "workflow": true, "schema": true, "storage": true}

// Outline is the outline of the CUE file at path: a definition's header by
// its name and type, its template's sections, parameters with their
// +usage, objects with their kinds, and helpers.
func Outline(path, doc string) ([]Symbol, bool) {
	f, err := parser.ParseFile(path, doc, parser.ParseComments)
	if err != nil {
		return nil, false
	}
	var out []Symbol
	for _, d := range f.Decls {
		switch x := d.(type) {
		case *ast.Field:
			sym := fieldSymbol(x, nil, 0)
			if labelName(x.Label) != templateLabel {
				if t, ok := fieldIn(x, "type"); ok {
					if lit, ok := t.Value.(*ast.BasicLit); ok {
						if s, err := strconv.Unquote(lit.Value); err == nil {
							sym.Kind, sym.Detail, sym.Children = SymbolDefinition, s, nil
						}
					}
				}
			}
			out = append(out, sym)
		case *ast.LetClause:
			out = append(out, Symbol{Name: x.Ident.Name, Kind: SymbolHelper, Range: span(x.Pos(), x.End()), Selection: span(x.Ident.Pos(), x.Ident.End())})
		}
	}
	return out, true
}

func span(start, end token.Pos) Range {
	return Range{Start: Position{Line: start.Line(), Column: start.Column()}, End: Position{Line: end.Line(), Column: end.Column()}}
}

// fieldSymbol is a field's entry, under the labels of the fields it is in.
func fieldSymbol(f *ast.Field, under []string, depth int) Symbol {
	name := labelName(f.Label)
	sym := Symbol{Name: name, Kind: SymbolField, Range: span(f.Pos(), f.End()), Selection: span(f.Label.Pos(), f.Label.End())}
	inParameter := false
	for _, u := range under {
		inParameter = inParameter || u == parameterLabel
	}
	switch {
	case inParameter:
		sym.Kind = SymbolParameter
		for _, cg := range ast.Comments(f) {
			for _, c := range cg.List {
				if m := strings.TrimSpace(strings.TrimPrefix(c.Text, "//")); strings.HasPrefix(m, "+usage=") {
					sym.Detail = strings.TrimPrefix(m, "+usage=")
				}
			}
		}
	case strings.HasPrefix(name, "_") || strings.HasPrefix(name, "#"):
		sym.Kind = SymbolHelper
	case sections[name]:
		sym.Kind = SymbolSection
	}
	s, ok := f.Value.(*ast.StructLit)
	if !ok {
		return sym
	}
	if kind, api := literalField(s, "kind"), literalField(s, "apiVersion"); kind != "" {
		sym.Kind, sym.Detail = SymbolObject, strings.TrimSpace(api+" "+kind)
	}
	if depth >= outlineDepth && !inParameter && name != parameterLabel {
		return sym
	}
	for _, e := range s.Elts {
		switch x := e.(type) {
		case *ast.Field:
			sym.Children = append(sym.Children, fieldSymbol(x, append(append([]string{}, under...), name), depth+1))
		case *ast.LetClause:
			sym.Children = append(sym.Children, Symbol{Name: x.Ident.Name, Kind: SymbolHelper, Range: span(x.Pos(), x.End()), Selection: span(x.Ident.Pos(), x.Ident.End())})
		}
	}
	return sym
}

// literalField is the string a struct literal sets a field to, or "".
func literalField(s *ast.StructLit, name string) string {
	for _, e := range s.Elts {
		if f, ok := e.(*ast.Field); ok && labelName(f.Label) == name {
			if lit, ok := f.Value.(*ast.BasicLit); ok && lit.Kind == token.STRING {
				if v, err := strconv.Unquote(lit.Value); err == nil {
					return v
				}
			}
		}
	}
	return ""
}
