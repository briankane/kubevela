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
	"strings"

	"cuelang.org/go/cue"
	"cuelang.org/go/cue/ast"
	"cuelang.org/go/cue/parser"
)

func isWordByteAt(b byte) bool {
	return b == '_' || b == '$' || b == '#' || (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9')
}

// Hover describes what is at offset in doc, as Markdown: a marker, a
// context field, a package function, a field of a value read, or a field
// where it is declared, with its type and doc. It is the completion entry
// for the same name where there is one, so the two agree.
func Hover(doc string, offset int, opts Options) (string, bool) {
	if offset < 0 || offset >= len(doc) || !isWordByteAt(doc[offset]) {
		return "", false
	}
	start, end := offset, offset
	for start > 0 && isWordByteAt(doc[start-1]) {
		start--
	}
	for end < len(doc) && isWordByteAt(doc[end]) {
		end++
	}
	lineStart := strings.LastIndex(doc[:start], "\n") + 1
	if text, ok := hoverMarker(doc[lineStart:start], doc[start:], doc[start:end]); ok {
		return text, true
	}
	chainStart := start
	for chainStart > 0 && (isWordByteAt(doc[chainStart-1]) || doc[chainStart-1] == '.') {
		chainStart--
	}
	name := doc[start:end]
	if chainStart < start {
		before := doc[lineStart:end]
		var candidates []Completion
		candidates = append(candidates, CompleteContext(doc, before)...)
		candidates = append(candidates, CompletePackageMemberWith(doc, before, opts.Externals)...)
		candidates = append(candidates, CompleteValueAt(doc, end, opts.Externals)...)
		for _, c := range candidates {
			if c.Label == name {
				return hoverText(name, c.Detail, c.Doc), true
			}
		}
		return "", false
	}
	return hoverDeclaration(doc, start, opts)
}

// hoverMarker describes the marker a word belongs to, in a comment.
func hoverMarker(lineBefore, rest, word string) (string, bool) {
	if !strings.HasSuffix(lineBefore, "+") && !strings.HasSuffix(lineBefore, "+ui:") && !strings.HasSuffix(lineBefore, "+ide:") {
		return "", false
	}
	if !strings.HasPrefix(strings.TrimSpace(lineBefore), "//") {
		return "", false
	}
	name := word
	switch {
	case strings.HasSuffix(lineBefore, "+ui:"):
		name = "ui:" + word
	case strings.HasSuffix(lineBefore, "+ide:"):
		name = "ide:" + strings.SplitN(rest, "=", 2)[0]
		name = strings.TrimSpace(strings.SplitN(name, " ", 2)[0])
	}
	if m := findMarker(Markers(), name); m != nil {
		return fmt.Sprintf("`+%s`\n\n%s", m.Name, m.Doc), true
	}
	return "", false
}

// hoverDeclaration describes the template field whose label is at offset:
// its type, any default, and its doc comment.
func hoverDeclaration(doc string, offset int, opts Options) (string, bool) {
	f, err := parser.ParseFile("hover.cue", doc, parser.ParseComments)
	if err != nil {
		return "", false
	}
	d, ok := newDocument("hover.cue", []byte(doc), f)
	if !ok {
		return "", false
	}
	d.opts = opts
	var path []string
	var found *ast.Field
	var walk func(n ast.Node, prefix []string)
	walk = func(n ast.Node, prefix []string) {
		s, ok := n.(*ast.StructLit)
		if !ok || found != nil {
			return
		}
		declared := map[string][]*ast.Field{}
		allTopLevelFields(s, declared)
		for name, decls := range declared {
			for _, fd := range decls {
				if fd.Label.Pos().Offset() <= offset && offset < fd.Label.End().Offset() {
					found, path = fd, append(append([]string{}, prefix...), name)
					return
				}
				if fd.Pos().Offset() <= offset && offset <= fd.End().Offset() {
					walk(fd.Value, append(append([]string{}, prefix...), name))
				}
			}
		}
	}
	walk(d.template.Value, nil)
	if found == nil {
		return "", false
	}
	v, ok := d.evaluate()
	if !ok {
		return "", false
	}
	sels := []cue.Selector{cue.Str(templateLabel)}
	for _, p := range path {
		sels = append(sels, rootSelector(p))
	}
	field := v.LookupPath(cue.MakePath(sels...))
	if !field.Exists() {
		field = schemaChild(v.LookupPath(cue.MakePath(sels[:len(sels)-1]...)), cue.Str(path[len(path)-1]))
	}
	typ := kindName(field)
	if def, ok := field.Default(); ok {
		if b, err := def.MarshalJSON(); err == nil {
			typ += ", default " + string(b)
		}
	}
	var docLines []string
	for _, cg := range ast.Comments(found) {
		for _, c := range cg.List {
			line := strings.TrimSpace(strings.TrimPrefix(c.Text, "//"))
			docLines = append(docLines, strings.TrimPrefix(line, "+usage="))
		}
	}
	return hoverText(path[len(path)-1], typ, strings.Join(docLines, " ")), true
}

// hoverText lays out a name, its type and its doc as Markdown.
func hoverText(name, typ, doc string) string {
	head := name
	if typ != "" {
		head += ": " + typ
	}
	out := "```cue\n" + head + "\n```"
	if doc != "" {
		out += "\n\n" + doc
	}
	return out
}
