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
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"cuelang.org/go/cue/format"

	"github.com/oam-dev/kubevela/pkg/definition/analysis"
	"github.com/oam-dev/kubevela/pkg/utils"
)

// navigationRequest answers go to definition, find references, rename and
// code actions.
func (s *Server) navigationRequest(msg message) (interface{}, *ResponseError) {
	switch msg.Method {
	case "textDocument/formatting":
		var p DocumentFormattingParams
		if rerr := decode(msg.Params, &p); rerr != nil {
			return nil, rerr
		}
		return formatEdits(s.docs[p.TextDocument.URI], pathOf(p.TextDocument.URI)), nil
	case "textDocument/codeLens":
		var p CodeLensParams
		if rerr := decode(msg.Params, &p); rerr != nil {
			return nil, rerr
		}
		text := s.docs[p.TextDocument.URI]
		lenses := []CodeLens{}
		if utils.IsCUETestFile(pathOf(p.TextDocument.URI)) {
			lenses = append(lenses, CodeLens{Command: &Command{Title: "Add test case", Command: CommandNewTestCase, Arguments: []interface{}{p.TextDocument.URI}}})
		}
		if ext := filepath.Ext(pathOf(p.TextDocument.URI)); ext == ".yaml" || ext == ".yml" {
			titles := map[string]string{analysis.AddComponent: "Add component", analysis.AddTrait: "Add trait", analysis.AddPolicy: "Add policy", analysis.AddWorkflowStep: "Add workflow step", analysis.AddSource: "Add source"}
			for _, l := range analysis.ApplicationLenses(text) {
				line := uint32(l.Line - 1)
				arg := AddToApplicationParams{TextDocument: p.TextDocument, Line: line, Kind: l.Kind}
				lenses = append(lenses, CodeLens{Range: Range{Start: Position{Line: line}, End: Position{Line: line}}, Command: &Command{Title: titles[l.Kind], Command: CommandAddToApplication, Arguments: []interface{}{arg}}})
			}
		}
		return lenses, nil
	case "textDocument/inlayHint":
		var p InlayHintParams
		if rerr := decode(msg.Params, &p); rerr != nil {
			return nil, rerr
		}
		text := s.docs[p.TextDocument.URI]
		hints := []InlayHint{}
		for _, h := range analysis.InlayHints(pathOf(p.TextDocument.URI), text, s.options()) {
			pos := toProtocolPosition(text, h.Position.Line, h.Position.Column)
			if pos.Line < p.Range.Start.Line || pos.Line > p.Range.End.Line {
				continue
			}
			hints = append(hints, InlayHint{Position: pos, Label: h.Label, Kind: inlayHintType, Tooltip: h.Tooltip, PaddingLeft: true})
		}
		return hints, nil
	case "textDocument/documentSymbol":
		var p DocumentSymbolParams
		if rerr := decode(msg.Params, &p); rerr != nil {
			return nil, rerr
		}
		text := s.docs[p.TextDocument.URI]
		syms, _ := analysis.Outline(pathOf(p.TextDocument.URI), text)
		return documentSymbols(text, syms), nil
	case "textDocument/codeAction":
		var p CodeActionParams
		if rerr := decode(msg.Params, &p); rerr != nil {
			return nil, rerr
		}
		text := s.docs[p.TextDocument.URI]
		actions := upgradeActions(p, text)
		for _, f := range analysis.ParameterActions(pathOf(p.TextDocument.URI), text, byteOffset(text, p.Range.Start)) {
			changes := make([]TextEdit, 0, len(f.Edits))
			for _, e := range f.Edits {
				changes = append(changes, TextEdit{Range: protocolRange(text, e.Range), NewText: e.NewText})
			}
			actions = append(actions, CodeAction{Title: f.Title, Kind: "refactor.extract", Edit: &WorkspaceEdit{Changes: map[string][]TextEdit{p.TextDocument.URI: changes}}})
		}
		if name, ok := analysis.MoveToTraitAt(pathOf(p.TextDocument.URI), text, byteOffset(text, p.Range.Start)); ok {
			title := "Move " + name + " to a trait"
			actions = append(actions, CodeAction{Title: title, Kind: "refactor.move", Command: &Command{Title: title, Command: analysis.MoveToTraitCommand, Arguments: []interface{}{p.TextDocument.URI, name}}})
		}
		return actions, nil
	case "textDocument/definition":
		var p TextDocumentPositionParams
		if rerr := decode(msg.Params, &p); rerr != nil {
			return nil, rerr
		}
		locs := s.definitionAt(p.TextDocument.URI, p.Position)
		if len(locs) == 0 {
			return nil, nil
		}
		return locs, nil
	case MethodRenderedDefinition:
		var p RenderedDefinitionParams
		if rerr := decode(msg.Params, &p); rerr != nil {
			return nil, rerr
		}
		return s.renderedDefinition(p), nil
	case "textDocument/references":
		var p ReferenceParams
		if rerr := decode(msg.Params, &p); rerr != nil {
			return nil, rerr
		}
		text := s.docs[p.TextDocument.URI]
		refs, _ := analysis.References(pathOf(p.TextDocument.URI), text, byteOffset(text, p.Position))
		locs := []Location{}
		for _, r := range refs {
			locs = append(locs, Location{URI: p.TextDocument.URI, Range: protocolRange(text, r)})
		}
		return locs, nil
	case "textDocument/rename":
		var p RenameParams
		if rerr := decode(msg.Params, &p); rerr != nil {
			return nil, rerr
		}
		text := s.docs[p.TextDocument.URI]
		edits, err := analysis.RenameEdits(pathOf(p.TextDocument.URI), text, byteOffset(text, p.Position), p.NewName)
		if err != nil {
			return nil, &ResponseError{Code: CodeInvalidParams, Message: err.Error()}
		}
		changes := make([]TextEdit, 0, len(edits))
		for _, e := range edits {
			changes = append(changes, TextEdit{Range: protocolRange(text, e.Range), NewText: e.NewText})
		}
		all := map[string][]TextEdit{p.TextDocument.URI: changes}
		for uri, edits := range s.propertyRenames(p.TextDocument.URI, text, byteOffset(text, p.Position), p.NewName) {
			all[uri] = edits
		}
		return WorkspaceEdit{Changes: all}, nil
	}
	return nil, nil
}

// protocolRange is a range of the analysis, in text, as the protocol counts.
func protocolRange(text string, r analysis.Range) Range {
	return Range{Start: toProtocolPosition(text, r.Start.Line, r.Start.Column), End: toProtocolPosition(text, r.End.Line, r.End.Column)}
}

var (
	quotedAt     = regexp.MustCompile(`"([^"\\]*)"`)
	headerKey    = regexp.MustCompile(`([A-Za-z]+)\s*:\s*$`)
	memberAt     = regexp.MustCompile(`([A-Za-z_][A-Za-z0-9_]*)\.(#?[A-Za-z_][A-Za-z0-9_]*)`)
	importLineAt = regexp.MustCompile(`^\s*(?:import\s+)?(?:[A-Za-z_][A-Za-z0-9_]*\s+)?"[^"]*"\s*$`)
	importAlias  = regexp.MustCompile(`(?m)^\s*(?:import\s+)?(?:([A-Za-z_][A-Za-z0-9_]*)\s+)?"([^"]+)"\s*$`)
)

// definitionAt is where what is at pos in the document is declared: a name
// the workspace knows (a definition extended, a package imported, a member of
// one, the definition a test case names), or a declaration in the CUE.
func (s *Server) definitionAt(uri string, pos Position) []Location {
	return s.definitionIn(uri, s.docs[uri], pos)
}

// renderedDefinition finds a declaration from a position in a DefKit
// definition's generated CUE, read as a file beside the Go one. A location in
// that CUE has no URI, as the CUE is the client's and no file holds it.
func (s *Server) renderedDefinition(p RenderedDefinitionParams) []Location {
	path := pathOf(p.TextDocument.URI) + "." + p.Name + ".rendered.cue"
	locs := s.definitionIn("file://"+path, p.Text, p.Position)
	out := []Location{}
	for _, l := range locs {
		if l.URI == "file://"+path {
			l.URI = ""
		}
		out = append(out, l)
	}
	return out
}

// definitionIn finds the declaration of what is at pos in text, the content
// of uri.
func (s *Server) definitionIn(uri, text string, pos Position) []Location {
	path := pathOf(uri)
	lines := strings.Split(text, "\n")
	if int(pos.Line) >= len(lines) {
		return nil
	}
	line := lines[pos.Line]
	col := len(prefixUTF16(line, pos.Character))
	if d, ok := analysis.DeclarationInStatusField(path, text, byteOffset(text, pos)); ok {
		return []Location{{URI: "file://" + d.Path, Range: protocolRange(text, d.Range)}}
	}
	if loc, ok := s.workspaceTarget(path, text, line, col); ok {
		return []Location{loc}
	}
	ext := s.externals
	if utils.IsCUETestFile(path) {
		ext = testExternals()
	}
	if d, ok := analysis.DeclarationWith(path, text, byteOffset(text, pos), ext); ok {
		if analysis.IsSource(d.Path) {
			source, _ := analysis.Source(d.Path, ext)
			return []Location{{URI: d.Path, Range: protocolRange(source, d.Range)}}
		}
		target := text
		if d.Path != path {
			target = s.textOf(d.Path)
		}
		return []Location{{URI: "file://" + d.Path, Range: protocolRange(target, d.Range)}}
	}
	return nil
}

// workspaceTarget is the file a string or package member at col of line
// names.
func (s *Server) workspaceTarget(path, text, line string, col int) (Location, bool) {
	for _, m := range quotedAt.FindAllStringSubmatchIndex(line, -1) {
		if col < m[0] || col > m[1] {
			continue
		}
		value := line[m[2]:m[3]]
		key := ""
		if k := headerKey.FindStringSubmatch(line[:m[0]]); k != nil {
			key = k[1]
		}
		switch {
		case key == "extends":
			return s.definitionFile(value)
		case key == "definition" && strings.HasSuffix(path, "_test.cue"):
			return testedFile(path, value)
		case importLineAt.MatchString(line):
			return s.packageFile(value, "")
		}
	}
	for _, m := range memberAt.FindAllStringSubmatchIndex(line, -1) {
		if col < m[0] || col > m[1] {
			continue
		}
		alias, member := line[m[2]:m[3]], line[m[4]:m[5]]
		for _, imp := range importAlias.FindAllStringSubmatch(text, -1) {
			name := imp[1]
			if name == "" {
				name = imp[2][strings.LastIndex(imp[2], "/")+1:]
			}
			if name == alias {
				return s.packageFile(imp[2], member)
			}
		}
	}
	return Location{}, false
}

// definitionFile is the file of the workspace's definition named.
func (s *Server) definitionFile(name string) (Location, bool) {
	for path, d := range s.definitions {
		if d.name == name {
			return Location{URI: "file://" + path}, true
		}
	}
	return Location{}, false
}

// testedFile is the definition a test case at path names.
func testedFile(path, ref string) (Location, bool) {
	rel, _, _ := strings.Cut(ref, "#")
	if !strings.HasSuffix(rel, ".cue") && !strings.HasSuffix(rel, ".go") {
		rel += ".cue"
	}
	file := filepath.Join(filepath.Dir(path), rel)
	if _, err := os.Stat(file); err != nil {
		return Location{}, false
	}
	return Location{URI: "file://" + file}, true
}

// packageFile is where the workspace's Package of importPath declares
// member, or its path when member is empty.
func (s *Server) packageFile(importPath, member string) (Location, bool) {
	for file, pkgs := range s.packages {
		for _, p := range pkgs {
			if p.GetPath() != importPath {
				continue
			}
			text := s.textOf(file)
			find := regexp.MustCompile(`(?m)^\s*path:\s*["']?` + regexp.QuoteMeta(importPath))
			if member != "" {
				find = regexp.MustCompile(`(?m)^\s*` + regexp.QuoteMeta(member) + `\s*:`)
			}
			loc := Location{URI: "file://" + file}
			if i := find.FindStringIndex(text); i != nil {
				at := i[0] + len(text[i[0]:i[1]]) - len(strings.TrimLeft(text[i[0]:i[1]], " \t\n"))
				line := strings.Count(text[:at], "\n")
				start := Position{Line: uint32(line), Character: utf16Len(text[strings.LastIndex(text[:at], "\n")+1 : at])}
				loc.Range = Range{Start: start, End: start}
			}
			return loc, true
		}
	}
	return Location{}, false
}

// textOf is a file's text: as open, or as saved.
func (s *Server) textOf(path string) string {
	if text, ok := s.docs["file://"+path]; ok {
		return text
	}
	//nolint:gosec // reading the workspace's own files is the point
	data, _ := os.ReadFile(path)
	return string(data)
}

// symbolKinds are the protocol's kinds for the outline's.
var symbolKinds = map[analysis.SymbolKind]int{
	analysis.SymbolDefinition: 5,  // Class
	analysis.SymbolSection:    2,  // Module
	analysis.SymbolParameter:  7,  // Property
	analysis.SymbolObject:     19, // Object
	analysis.SymbolHelper:     13, // Variable
	analysis.SymbolField:      8,  // Field
}

func documentSymbols(text string, syms []analysis.Symbol) []DocumentSymbol {
	out := make([]DocumentSymbol, 0, len(syms))
	for _, sym := range syms {
		out = append(out, DocumentSymbol{
			Name:           sym.Name,
			Detail:         sym.Detail,
			Kind:           symbolKinds[sym.Kind],
			Range:          protocolRange(text, sym.Range),
			SelectionRange: protocolRange(text, sym.Selection),
			Children:       documentSymbols(text, sym.Children),
		})
	}
	return out
}

// inlayHintType is the protocol's kind for a hint about a value's type or
// value.
const inlayHintType = 1

// formatEdits format a CUE document as cue fmt does: one edit of the whole
// document, or none for one formatted already, one that does not parse (its
// errors are reported already), or one that is not CUE.
func formatEdits(text, path string) []TextEdit {
	edits := []TextEdit{}
	if filepath.Ext(path) != ".cue" {
		return edits
	}
	out, err := format.Source([]byte(text))
	if err != nil || string(out) == text {
		return edits
	}
	lines := strings.Split(text, "\n")
	end := toProtocolPosition(text, len(lines), len(lines[len(lines)-1])+1)
	return append(edits, TextEdit{Range: Range{End: end}, NewText: string(out)})
}

// propertyRenames are the edits that rename a definition's parameter in the
// workspace's Applications, the property each use of the definition sets, and
// in its test cases, the parameter each case gives it.
func (s *Server) propertyRenames(uri, text string, offset int, to string) map[string][]TextEdit {
	path := pathOf(uri)
	param, ok := analysis.ParameterAt(path, text, offset)
	if !ok {
		return nil
	}
	name, defType, ok := analysis.DefinitionHeader(path, []byte(text))
	if !ok {
		return nil
	}
	stem := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	out := map[string][]TextEdit{}
	for file := range s.workspaceFiles {
		isTest := strings.HasSuffix(file, "_test.cue")
		if !isTest && !strings.HasSuffix(file, ".yaml") && !strings.HasSuffix(file, ".yml") {
			continue
		}
		fileURI := "file://" + file
		src, open := s.docs[fileURI]
		if !open {
			b, err := os.ReadFile(file)
			if err != nil {
				continue
			}
			src = string(b)
		}
		var edits []analysis.RangeEdit
		if isTest {
			edits = analysis.TestParameterRenameEdits(file, src, []string{name, stem}, param, to)
		} else {
			edits = analysis.PropertyRenameEdits(src, defType, name, param, to)
		}
		if len(edits) == 0 {
			continue
		}
		changes := make([]TextEdit, 0, len(edits))
		for _, e := range edits {
			changes = append(changes, TextEdit{Range: protocolRange(src, e.Range), NewText: e.NewText})
		}
		out[fileURI] = changes
	}
	return out
}
