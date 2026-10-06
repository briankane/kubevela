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

	"cuelang.org/go/cue/ast"
	cueerrors "cuelang.org/go/cue/errors"
	"cuelang.org/go/cue/literal"
	"cuelang.org/go/cue/parser"

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
	s := cuetest.LoadFile(path, []byte(text))
	if s.Err == nil {
		return []Diagnostic{}
	}
	msg := s.Err.Error()
	if m := caseError.FindStringSubmatch(msg); m != nil {
		if name, err := strconv.Unquote(m[1]); err == nil {
			if r, ok := caseLabels(path, text)[name]; ok {
				return []Diagnostic{testDiagnostic(r, msg)}
			}
		}
	}
	var diags []Diagnostic
	for _, e := range cueerrors.Errors(s.Err) {
		pos := e.Position()
		if !pos.IsValid() {
			continue
		}
		start := toProtocolPosition(text, pos.Line(), pos.Column())
		diags = append(diags, testDiagnostic(Range{Start: start, End: start}, e.Error()))
	}
	if len(diags) == 0 {
		diags = append(diags, testDiagnostic(Range{}, msg))
	}
	return diags
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
