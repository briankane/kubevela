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
	"strings"

	"cuelang.org/go/cue/ast"
	"cuelang.org/go/cue/parser"
)

// LocateField is the range of the label of the field at path, dotted, in a
// CUE file or an Application's YAML: in YAML, a list item by its index. Where
// the whole path is not written, it is the deepest part of it that is.
func LocateField(path string, src []byte, fieldPath string) (Range, bool) {
	if strings.HasSuffix(path, ".yaml") || strings.HasSuffix(path, ".yml") {
		d := &document{path: path, src: src}
		f, err := d.extractYAML()
		if err != nil {
			return Range{}, false
		}
		return nearestFieldRange(yamlFields(f), fieldPath)
	}
	f, err := parser.ParseFile(path, src, parser.ParseComments)
	if err != nil {
		return Range{}, false
	}
	var found *ast.Field
	decls := f.Decls
	for _, seg := range strings.Split(fieldPath, ".") {
		next := fieldAmong(decls, seg)
		if next == nil {
			break
		}
		found = next
		decls = declsOf(next.Value)
	}
	if found == nil {
		return Range{}, false
	}
	return span(found.Label.Pos(), found.Label.End()), true
}

// fieldAmong is the field labelled name among decls, looking into the
// comprehensions and embedded structs that may declare it.
func fieldAmong(decls []ast.Decl, name string) *ast.Field {
	for _, decl := range decls {
		switch x := decl.(type) {
		case *ast.Field:
			if labelName(x.Label) == name {
				return x
			}
		case *ast.Comprehension:
			if f := fieldAmong(declsOf(x.Value), name); f != nil {
				return f
			}
		case *ast.EmbedDecl:
			if f := fieldAmong(declsOf(x.Expr), name); f != nil {
				return f
			}
		}
	}
	return nil
}

// declsOf are the declarations of a struct, or of the structs a unification
// or disjunction is made of.
func declsOf(e ast.Node) []ast.Decl {
	switch x := e.(type) {
	case *ast.StructLit:
		return x.Elts
	case *ast.BinaryExpr:
		return append(declsOf(x.X), declsOf(x.Y)...)
	case *ast.ParenExpr:
		return declsOf(x.X)
	}
	return nil
}
