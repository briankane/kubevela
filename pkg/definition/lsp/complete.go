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

package lsp

import (
	"strings"
	"unicode/utf16"

	"github.com/oam-dev/kubevela/pkg/definition/analysis"
)

// completions are what can be typed at pos in text.
func completions(text string, pos Position) CompletionList {
	list := CompletionList{Items: []CompletionItem{}}
	lines := strings.Split(text, "\n")
	if int(pos.Line) >= len(lines) {
		return list
	}
	before := prefixUTF16(lines[pos.Line], pos.Character)
	candidates := append(analysis.CompleteMarker(before), analysis.CompleteContext(text, before)...)
	for _, c := range candidates {
		replaced := before[len(before)-c.Replace:]
		start := pos
		start.Character -= utf16Len(replaced)
		list.Items = append(list.Items, CompletionItem{
			Label:         c.Label,
			Kind:          kindOf(c),
			Detail:        c.Detail,
			FilterText:    c.Insert,
			Documentation: MarkupContent{Kind: "markdown", Value: c.Doc},
			TextEdit:      TextEdit{Range: Range{Start: start, End: pos}, NewText: c.Insert},
		})
	}
	return list
}

// prefixUTF16 is the start of line up to a UTF-16 character offset.
func prefixUTF16(line string, character uint32) string {
	units := uint32(0)
	for i, r := range line {
		if units >= character {
			return line[:i]
		}
		units += uint32(utf16.RuneLen(r))
	}
	return line
}

func utf16Len(s string) uint32 {
	n := uint32(0)
	for _, r := range s {
		n += uint32(utf16.RuneLen(r))
	}
	return n
}

// kindOf is how the editor shows a candidate: a marker or its value as a
// keyword, a context field as a field.
func kindOf(c analysis.Completion) CompletionItemKind {
	if strings.HasPrefix(c.Label, "+") || c.Detail == "" {
		return CompletionItemKindKeyword
	}
	return CompletionItemKindField
}
