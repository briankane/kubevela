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
	"regexp"
	"strconv"
	"strings"

	"cuelang.org/go/cue/ast"
	cueerrors "cuelang.org/go/cue/errors"
	"cuelang.org/go/cue/literal"
	"cuelang.org/go/cue/parser"

	"github.com/oam-dev/kubevela/pkg/definition/analysis"
	"github.com/oam-dev/kubevela/pkg/definition/cuetest"
)

// caseError is how cuetest names the case an error belongs to.
var caseError = regexp.MustCompile(`^case ("(?:[^"\\]|\\.)*"): `)

// testCases lists the cases of a test file, from text when the editor has it
// and from disk otherwise. Nothing is run.
func testCases(p TestCasesParams) TestCasesResult {
	path := pathOf(p.TextDocument.URI)
	var src []byte
	if p.Text != nil {
		src = []byte(*p.Text)
	} else {
		var err error
		//nolint:gosec // reading the test file the editor named is the point
		if src, err = os.ReadFile(path); err != nil {
			return TestCasesResult{Error: err.Error(), Cases: []TestCase{}}
		}
	}
	s := cuetest.LoadFile(path, src)
	if s.Err != nil {
		return TestCasesResult{Error: s.Err.Error(), Cases: []TestCase{}}
	}
	text := string(src)
	labels := caseLabels(path, text)
	out := TestCasesResult{Cases: make([]TestCase, 0, len(s.Cases))}
	for _, c := range s.Cases {
		r, ok := labels[c.Name]
		if !ok {
			start := toProtocolPosition(text, c.Line, 1)
			r = Range{Start: start, End: start}
		}
		out.Cases = append(out.Cases, TestCase{
			Name:          c.Name,
			Line:          c.Line,
			Range:         r,
			Definition:    c.Subject.Name,
			Test:          string(c.Test),
			Labels:        c.Labels,
			Pending:       c.Pending,
			PendingReason: c.PendingReason,
		})
	}
	return out
}

// testDiagnostics reports why a test file does not load. An error about one
// case goes on that case's name: its positions may be in the definition the
// case renders rather than in this file.
func testDiagnostics(path, text string) []Diagnostic {
	diags := toProtocol(text, analysis.CheckTestFile(path, []byte(text), testExternals()))
	s := cuetest.LoadFile(path, []byte(text))
	if s.Err == nil {
		return diags
	}
	msg := s.Err.Error()
	if m := caseError.FindStringSubmatch(msg); m != nil {
		if name, err := strconv.Unquote(m[1]); err == nil {
			if r, ok := caseFieldRange(path, text, name, strings.TrimPrefix(msg, m[0])); ok {
				return append(diags, testDiagnostic(r, msg))
			}
		}
	}
	found := false
	for _, e := range cueerrors.Errors(s.Err) {
		pos := e.Position()
		if !pos.IsValid() {
			continue
		}
		start := toProtocolPosition(text, pos.Line(), pos.Column())
		diags = append(diags, testDiagnostic(Range{Start: start, End: start}, e.Error()))
		found = true
	}
	if !found {
		diags = append(diags, testDiagnostic(Range{}, msg))
	}
	return diags
}

// caseFieldRange is the range of the field a case's error names, as in
// `c.workload.spec: ...`, or of the case's name when the path is not one it
// writes.
func caseFieldRange(path, text, name, rest string) (Range, bool) {
	f, err := parser.ParseFile(path, text)
	if err != nil {
		return Range{}, false
	}
	var at ast.Label
	var body ast.Expr
	for _, decl := range f.Decls {
		if field, ok := decl.(*ast.Field); ok {
			if label, ok := labelName(field.Label); ok && label == name {
				at, body = field.Label, field.Value
			}
		}
	}
	if at == nil {
		return Range{}, false
	}
	segments := strings.Split(strings.SplitN(rest, ": ", 2)[0], ".")
	if len(segments) > 0 {
		segments = segments[1:]
	}
	for _, seg := range segments {
		next := fieldOf(body, strings.Trim(seg, `"`))
		if next == nil {
			break
		}
		at, body = next.Label, next.Value
	}
	start, end := at.Pos(), at.End()
	return Range{
		Start: toProtocolPosition(text, start.Line(), start.Column()),
		End:   toProtocolPosition(text, end.Line(), end.Column()),
	}, true
}

// fieldOf finds a field a case's value declares: in its struct, or in the
// struct a `test.#X & {...}` unifies with.
func fieldOf(v ast.Expr, name string) *ast.Field {
	switch x := v.(type) {
	case *ast.BinaryExpr:
		if f := fieldOf(x.Y, name); f != nil {
			return f
		}
		return fieldOf(x.X, name)
	case *ast.StructLit:
		for _, elt := range x.Elts {
			if f, ok := elt.(*ast.Field); ok {
				if label, ok := labelName(f.Label); ok && label == name {
					return f
				}
			}
		}
	}
	return nil
}

// toProtocol converts the analysis's diagnostics to protocol positions.
func toProtocol(text string, in []analysis.Diagnostic) []Diagnostic {
	out := make([]Diagnostic, 0, len(in))
	for _, d := range in {
		out = append(out, Diagnostic{
			Range: Range{
				Start: toProtocolPosition(text, d.Range.Start.Line, d.Range.Start.Column),
				End:   toProtocolPosition(text, d.Range.End.Line, d.Range.End.Column),
			},
			Severity: severity(d.Severity),
			Source:   diagnosticSource,
			Message:  d.Message,
		})
	}
	return out
}

func testDiagnostic(r Range, msg string) Diagnostic {
	return Diagnostic{Range: r, Severity: SeverityError, Source: diagnosticSource, Message: msg}
}

// caseLabels maps each top-level field of a test file to the range of its
// label, which is where its case is named.
func caseLabels(path, text string) map[string]Range {
	f, err := parser.ParseFile(path, text)
	if err != nil {
		return nil
	}
	out := map[string]Range{}
	for _, decl := range f.Decls {
		field, ok := decl.(*ast.Field)
		if !ok {
			continue
		}
		name, ok := labelName(field.Label)
		if !ok {
			continue
		}
		start, end := field.Label.Pos(), field.Label.End()
		out[name] = Range{
			Start: toProtocolPosition(text, start.Line(), start.Column()),
			End:   toProtocolPosition(text, end.Line(), end.Column()),
		}
	}
	return out
}

func labelName(l ast.Label) (string, bool) {
	switch x := l.(type) {
	case *ast.Ident:
		return x.Name, true
	case *ast.BasicLit:
		s, err := literal.Unquote(x.Value)
		return s, err == nil
	}
	return "", false
}
