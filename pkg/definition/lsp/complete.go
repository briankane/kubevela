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
	"sync"
	"unicode/utf16"

	cuexruntime "github.com/kubevela/pkg/cue/cuex/runtime"

	"github.com/oam-dev/kubevela/pkg/definition/analysis"
	"github.com/oam-dev/kubevela/pkg/definition/cuetest"
	"github.com/oam-dev/kubevela/pkg/utils"
)

// completions are what can be typed at pos in text, with what global
// policies publish offered under context.custom.
func completions(uri, text string, pos Position, published []analysis.Published) CompletionList {
	list := CompletionList{Items: []CompletionItem{}}
	lines := strings.Split(text, "\n")
	if int(pos.Line) >= len(lines) {
		return list
	}
	before := prefixUTF16(lines[pos.Line], pos.Character)
	upToCursor := strings.Join(append(append([]string{}, lines[:pos.Line]...), before), "\n")
	var candidates []analysis.Completion
	var ext *analysis.Externals
	if utils.IsCUETestFile(pathOf(uri)) {
		ext = testExternals()
		candidates = analysis.CompleteTestFile(upToCursor, pathOf(uri), before, ext)
		candidates = append(candidates, analysis.CompleteTestAttribute(before)...)
	}
	if len(candidates) == 0 {
		candidates = append(candidates, analysis.CompleteMarker(before)...)
		candidates = append(candidates, analysis.CompleteContextWith(text, before, published)...)
		candidates = append(candidates, analysis.CompletePackageMemberWith(text, before, ext)...)
		candidates = append(candidates, analysis.CompleteImportWith(text, upToCursor, ext)...)
	}
	if len(candidates) == 0 {
		candidates = analysis.CompleteValueAt(text, len(upToCursor), ext)
	}
	for _, c := range candidates {
		replaced := before[len(before)-c.Replace:]
		start := pos
		start.Character -= utf16Len(replaced)
		item := CompletionItem{
			Label:         c.Label,
			Kind:          kindOf(c),
			Detail:        c.Detail,
			FilterText:    c.Insert,
			Documentation: MarkupContent{Kind: "markdown", Value: c.Doc},
			TextEdit:      TextEdit{Range: Range{Start: start, End: pos}, NewText: c.Insert},
		}
		if c.Snippet != "" {
			item.TextEdit.NewText, item.InsertTextFormat = c.Snippet, insertTextFormatSnippet
		}
		list.Items = append(list.Items, item)
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
// keyword, a package function as a function, an import as a module, a
// context field as a field.
func kindOf(c analysis.Completion) CompletionItemKind {
	switch {
	case strings.HasPrefix(c.Label, "#"):
		return CompletionItemKindFunction
	case strings.HasPrefix(c.Label, "vela/"):
		return CompletionItemKindModule
	case strings.HasPrefix(c.Label, "+") || c.Detail == "":
		return CompletionItemKindKeyword
	}
	return CompletionItemKindField
}

// insertTextFormatSnippet marks a completion's text as a snippet.
const insertTextFormatSnippet = 2

// testExternals holds vela/test, which a CUE test file imports.
var testExternals = sync.OnceValue(func() *analysis.Externals {
	pkg, err := cuetest.Package()
	if err != nil {
		return nil
	}
	return analysis.NewExternals([]cuexruntime.Package{pkg})
})
