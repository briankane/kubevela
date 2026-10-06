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
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// scalerTests is a test file beside its definition, from cuetest's own fixtures.
func scalerTests(t *testing.T) (uri string, text string) {
	path, err := filepath.Abs("../cuetest/testdata/defs/scaler_test.cue")
	require.NoError(t, err)
	src, err := os.ReadFile(path)
	require.NoError(t, err)
	return "file://" + path, string(src)
}

func openTestFile(c *client, uri, text string) PublishDiagnosticsParams {
	c.send("textDocument/didOpen", DidOpenTextDocumentParams{TextDocument: TextDocumentItem{
		URI: uri, LanguageID: "cue", Version: 1, Text: text,
	}}, false)
	return c.diagnostics()
}

func TestTestFileThatLoadsHasNoDiagnostics(t *testing.T) {
	uri, text := scalerTests(t)
	assert.Empty(t, openTestFile(newClient(t), uri, text).Diagnostics)
}

func TestTestFileCaseErrorIsReportedAtTheCase(t *testing.T) {
	uri, _ := scalerTests(t)
	text := `import "vela/test"

"names a definition that is not there": test.#TraitRender & {
	definition: "no-such-def"
	expect: output: {}
}
`
	p := openTestFile(newClient(t), uri, text)
	require.Len(t, p.Diagnostics, 1)
	d := p.Diagnostics[0]
	assert.Contains(t, d.Message, "no-such-def")
	assert.Equal(t, Range{Start: Position{Line: 2, Character: 0}, End: Position{Line: 2, Character: 38}}, d.Range)
}

func TestTestFileSyntaxErrorIsReportedWhereItIs(t *testing.T) {
	uri, _ := scalerTests(t)
	text := "import \"vela/test\"\n\n\"x\": test.#TraitRender & {\n\tdefinition: \"scaler\"\n\texpect: output: {\n}\n"
	p := openTestFile(newClient(t), uri, text)
	require.NotEmpty(t, p.Diagnostics)
	// CUE places the missing brace at the end of the last line, line 6.
	assert.Equal(t, uint32(5), p.Diagnostics[0].Range.Start.Line, p.Diagnostics[0].Message)
}

func requestTestCases(t *testing.T, c *client, params TestCasesParams) TestCasesResult {
	m := c.response(c.send(MethodTestCases, params, true))
	require.Nil(t, m["error"])
	var r TestCasesResult
	require.NoError(t, json.Unmarshal(m["result"], &r))
	return r
}

func TestTestCasesListsTheCasesOfTheText(t *testing.T) {
	uri, text := scalerTests(t)
	text += "\n\"added in the editor\": test.#TraitRender & {\n\tdefinition: \"scaler\"\n\tparameter: replicas: 4\n\tworkload: _workload\n\texpect: output: spec: replicas: 4\n}\n"
	r := requestTestCases(t, newClient(t), TestCasesParams{TextDocument: TextDocumentIdentifier{URI: uri}, Text: &text})
	assert.Empty(t, r.Error)

	byName := map[string]TestCase{}
	for _, c := range r.Cases {
		byName[c.Name] = c
	}
	require.Contains(t, byName, "patches replicas")
	first := byName["patches replicas"]
	assert.Equal(t, Range{Start: Position{Line: 4, Character: 0}, End: Position{Line: 4, Character: 18}}, first.Range)
	assert.Equal(t, "scaler", first.Definition)
	assert.Equal(t, "trait-render", first.Test)
	assert.Contains(t, first.Labels, "trait")
	assert.False(t, first.Pending)

	assert.True(t, byName["pending on purpose"].Pending)
	assert.Contains(t, byName, "added in the editor")
}

func TestTestCasesReadsTheFileWhenNoTextIsGiven(t *testing.T) {
	uri, _ := scalerTests(t)
	r := requestTestCases(t, newClient(t), TestCasesParams{TextDocument: TextDocumentIdentifier{URI: uri}})
	assert.Empty(t, r.Error)
	assert.NotEmpty(t, r.Cases)
	assert.NotContains(t, names(r.Cases), "added in the editor")
}

func TestTestCasesReportsWhyAFileDidNotLoad(t *testing.T) {
	uri, _ := scalerTests(t)
	text := "a: {\n"
	r := requestTestCases(t, newClient(t), TestCasesParams{TextDocument: TextDocumentIdentifier{URI: uri}, Text: &text})
	assert.NotEmpty(t, r.Error)
	assert.Empty(t, r.Cases)
}

func names(cases []TestCase) []string {
	var out []string
	for _, c := range cases {
		out = append(out, c.Name)
	}
	return out
}

// A case's error goes on the field its path names, not the case's name, and
// what a case passes its definition is checked against that definition.
func TestTestFileErrorsAtTheField(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "scaler.cue"), []byte("\"scaler\": {\n\ttype: \"trait\"\n}\ntemplate: {\n\tpatch: spec: replicas: parameter.replicas\n\tparameter: replicas: *1 | int\n}\n"), 0o600))
	uri := "file://" + filepath.Join(dir, "scaler_test.cue")
	diags := func(body string) []Diagnostic {
		text := "import \"vela/test\"\n\n\"c\": test.#TraitRender & {\n\tdefinition: \"scaler\"\n" + body + "}\n"
		return openTestFile(newClient(t), uri, text).Diagnostics
	}
	got := diags("\tworkload: {}\n\texpct: output: {}\n")
	require.Len(t, got, 1)
	assert.Equal(t, uint32(5), got[0].Range.Start.Line, got[0].Message)
	assert.Contains(t, got[0].Message, "expct")

	got = diags("\tworkload: {}\n\tparameter: replicaz: 2\n\texpect: {}\n")
	require.Len(t, got, 1)
	assert.Equal(t, uint32(5), got[0].Range.Start.Line, got[0].Message)
	assert.Contains(t, got[0].Message, "scaler takes no parameter replicaz")
}
