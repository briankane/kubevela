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
	"bytes"
	"errors"
	"io"
	"strings"

	"cuelang.org/go/cue/ast"
	"cuelang.org/go/cue/literal"
	"cuelang.org/go/cue/parser"
	"cuelang.org/go/cue/token"
	"gopkg.in/yaml.v3"
)

// ParameterAt is the path, under the template's parameter, of the parameter
// declared or referred to at offset of a definition.
func ParameterAt(path, doc string, offset int) ([]string, bool) {
	ix, ok := parseNav(path, doc)
	if !ok {
		return nil, false
	}
	d, ok := ix.target(offset, nil)
	if !ok || d.field == nil {
		return nil, false
	}
	const prefix = templateLabel + ".parameter."
	p := ix.pathOf[d.field]
	if !strings.HasPrefix(p, prefix) || strings.Contains(p, listItem) {
		return nil, false
	}
	return strings.Split(strings.TrimPrefix(p, prefix), "."), true
}

// PropertyRenameEdits rename, in the Applications in src, the property at
// param of each use of the definition of defType and name, to to.
func PropertyRenameEdits(src, defType, name string, param []string, to string) []RangeEdit {
	if len(param) == 0 {
		return nil
	}
	var edits []RangeEdit
	dec := yaml.NewDecoder(bytes.NewReader([]byte(src)))
	for {
		var doc yaml.Node
		if err := dec.Decode(&doc); err != nil {
			if !errors.Is(err, io.EOF) {
				return edits
			}
			break
		}
		if len(doc.Content) == 0 {
			continue
		}
		root := doc.Content[0]
		if scalar(mapValue(root, "kind")) != "Application" {
			continue
		}
		for _, use := range usesOf(mapValue(root, "spec"), defType) {
			t := scalar(mapValue(use, "type"))
			if t != name && !strings.HasPrefix(t, name+"@") {
				continue
			}
			at := mapValue(use, "properties")
			for _, step := range param[:len(param)-1] {
				at = mapValue(at, step)
			}
			if key := mapKey(at, param[len(param)-1]); key != nil {
				col := key.Column
				if key.Style&(yaml.DoubleQuotedStyle|yaml.SingleQuotedStyle) != 0 {
					col++
				}
				edits = append(edits, RangeEdit{
					Range:   Range{Start: Position{Line: key.Line, Column: col}, End: Position{Line: key.Line, Column: col + len(key.Value)}},
					NewText: to,
				})
			}
		}
	}
	return edits
}

// usesOf are the items of an Application's spec that name a definition of defType.
func usesOf(spec *yaml.Node, defType string) []*yaml.Node {
	items := func(n *yaml.Node) []*yaml.Node {
		if n == nil || n.Kind != yaml.SequenceNode {
			return nil
		}
		return n.Content
	}
	var out []*yaml.Node
	switch defType {
	case "component":
		out = items(mapValue(spec, "components"))
	case "trait":
		for _, c := range items(mapValue(spec, "components")) {
			out = append(out, items(mapValue(c, "traits"))...)
		}
	case "policy":
		out = items(mapValue(spec, "policies"))
	case "workflow-step":
		for _, s := range items(mapValue(mapValue(spec, "workflow"), "steps")) {
			out = append(out, s)
			out = append(out, items(mapValue(s, "subSteps"))...)
		}
	case "source":
		out = items(mapValue(spec, "sources"))
	}
	return out
}

// TestParameterRenameEdits rename, in a CUE test file, the parameter at param
// of each case testing a definition named one of names, to to.
func TestParameterRenameEdits(path, src string, names []string, param []string, to string) []RangeEdit {
	f, err := parser.ParseFile(path, src)
	if err != nil || len(param) == 0 {
		return nil
	}
	tested := map[string]bool{}
	for _, n := range names {
		tested[n] = true
	}
	var edits []RangeEdit
	for _, d := range f.Decls {
		c, ok := d.(*ast.Field)
		if !ok {
			continue
		}
		body := caseStruct(c.Value)
		if body == nil {
			continue
		}
		def, ok := fieldIn(&ast.Field{Value: body}, "definition")
		if !ok {
			continue
		}
		lit, ok := def.Value.(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			continue
		}
		if name, err := literal.Unquote(lit.Value); err != nil || !tested[name] {
			continue
		}
		at, ok := fieldIn(&ast.Field{Value: body}, "parameter")
		for _, step := range param {
			if !ok {
				break
			}
			at, ok = fieldIn(at, step)
		}
		if !ok {
			continue
		}
		start, end := at.Label.Pos(), at.Label.End()
		if s, isString := at.Label.(*ast.BasicLit); isString && s.Kind == token.STRING {
			start, end = start.Add(1), end.Add(-1)
		}
		edits = append(edits, RangeEdit{Range: span(start, end), NewText: to})
	}
	return edits
}

// caseStruct is the struct a test case unifies with its test, as in test.#X & {...}.
func caseStruct(v ast.Expr) *ast.StructLit {
	switch x := v.(type) {
	case *ast.StructLit:
		return x
	case *ast.BinaryExpr:
		if x.Op == token.AND {
			if s := caseStruct(x.Y); s != nil {
				return s
			}
			return caseStruct(x.X)
		}
	}
	return nil
}
