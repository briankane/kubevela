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

	"cuelang.org/go/cue"
	"cuelang.org/go/cue/ast"
	"cuelang.org/go/cue/token"
)

// Fix is a set of edits that resolves a diagnostic.
type Fix struct {
	Title string
	Edits []RangeEdit
}

// closest is the candidate a misspelt name most likely meant, or "": one at
// most two edits away, and fewer than the name's length.
func closest(name string, candidates []string) string {
	best, bestDist := "", 3
	for _, c := range candidates {
		if c == name {
			continue
		}
		if dist := editDistance(strings.ToLower(name), strings.ToLower(c)); dist < bestDist && dist < len(name) {
			best, bestDist = c, dist
		}
	}
	return best
}

// fieldNames are the names a struct value declares, optional ones too.
func fieldNames(v cue.Value) []string {
	it, err := v.Fields(cue.Optional(true))
	if err != nil {
		return nil
	}
	var out []string
	for it.Next() {
		out = append(out, it.Selector().Unquoted())
	}
	return out
}

// renameFix changes the name written at pos to to.
func renameFix(start, end token.Pos, to string) Fix {
	return Fix{Title: "Change to " + to, Edits: []RangeEdit{{Range: span(start, end), NewText: to}}}
}

// lineIndent is the whitespace a line of the source starts with.
func (d *document) lineIndent(line int) string {
	lines := strings.Split(string(d.src), "\n")
	if line < 1 || line > len(lines) {
		return ""
	}
	l := lines[line-1]
	return l[:len(l)-len(strings.TrimLeft(l, " \t"))]
}

// wholeLines is the range of lines from to through, newline included.
func wholeLines(from, through int) Range {
	return Range{Start: Position{Line: from, Column: 1}, End: Position{Line: through + 1, Column: 1}}
}

// addParameterFix adds name to the template's parameter struct.
func (d *document) addParameterFix(name string) (Fix, bool) {
	if d.template == nil {
		return Fix{}, false
	}
	param, ok := fieldIn(d.template, parameterLabel)
	if !ok {
		return Fix{}, false
	}
	s, ok := param.Value.(*ast.StructLit)
	if !ok || !s.Lbrace.IsValid() || !s.Rbrace.IsValid() {
		return Fix{}, false
	}
	title := "Add " + name + " to parameter"
	indent := d.lineIndent(s.Rbrace.Line())
	if s.Lbrace.Line() == s.Rbrace.Line() {
		inner := Range{
			Start: Position{Line: s.Lbrace.Line(), Column: s.Lbrace.Column() + 1},
			End:   Position{Line: s.Rbrace.Line(), Column: s.Rbrace.Column()},
		}
		return Fix{Title: title, Edits: []RangeEdit{{Range: inner, NewText: "\n" + indent + "\t" + name + ": _\n" + indent}}}, true
	}
	at := Position{Line: s.Rbrace.Line(), Column: 1}
	return Fix{Title: title, Edits: []RangeEdit{{Range: Range{Start: at, End: at}, NewText: indent + "\t" + name + ": _\n"}}}, true
}

// removeImportFix removes spec, with its import declaration when it is the
// declaration's only one.
func removeImportFix(decl *ast.ImportDecl, spec *ast.ImportSpec) Fix {
	r := wholeLines(spec.Pos().Line(), spec.End().Line())
	if len(decl.Specs) == 1 {
		r = wholeLines(decl.Pos().Line(), decl.End().Line())
	}
	return Fix{Title: "Remove the import", Edits: []RangeEdit{{Range: r}}}
}

// usageFix adds a +usage marker above the field at pos, for the author to
// complete.
func (d *document) usageFix(pos token.Pos) Fix {
	at := Position{Line: pos.Line(), Column: 1}
	return Fix{Title: "Add +usage", Edits: []RangeEdit{{Range: Range{Start: at, End: at}, NewText: d.lineIndent(pos.Line()) + "// +usage=\n"}}}
}
