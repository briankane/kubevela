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

func complete(t *testing.T, c *client, line, character uint32) CompletionList {
	m := c.response(c.send("textDocument/completion", CompletionParams{
		TextDocument: TextDocumentIdentifier{URI: uri},
		Position:     Position{Line: line, Character: character},
	}, true))
	require.Nil(t, m["error"])
	var list CompletionList
	require.NoError(t, json.Unmarshal(m["result"], &list))
	return list
}

func TestCompletesMarkers(t *testing.T) {
	c := newClient(t)
	init := c.response(c.send("initialize", map[string]interface{}{}, true))
	var res InitializeResult
	require.NoError(t, json.Unmarshal(init["result"], &res))
	require.NotNil(t, res.Capabilities.CompletionProvider)
	assert.Contains(t, res.Capabilities.CompletionProvider.TriggerCharacters, "+")

	text := "\"x\": {type: \"component\"}\ntemplate: {\n\tparameter: {\n\t\t// +us\n\t\t// é +patchStrategy=\n\t\ta: string\n\t}\n}\n"
	c.send("textDocument/didOpen", DidOpenTextDocumentParams{TextDocument: TextDocumentItem{URI: uri, LanguageID: "cue", Version: 1, Text: text}}, false)
	c.diagnostics()

	list := complete(t, c, 3, 8)
	require.Len(t, list.Items, 1)
	item := list.Items[0]
	assert.Equal(t, "+usage", item.Label)
	assert.Equal(t, CompletionItemKindKeyword, item.Kind)
	assert.Equal(t, "usage=", item.TextEdit.NewText)
	assert.Equal(t, Range{Start: Position{Line: 3, Character: 6}, End: Position{Line: 3, Character: 8}}, item.TextEdit.Range)
	assert.NotEmpty(t, item.Documentation.Value)

	// A comment that is not a marker gets nothing.
	assert.Empty(t, complete(t, c, 4, 6).Items)
	// An edit is seen at once.
	c.send("textDocument/didChange", DidChangeTextDocumentParams{
		TextDocument:   VersionedTextDocumentIdentifier{URI: uri, Version: 2},
		ContentChanges: []TextDocumentContentChangeEvent{{Text: "\"x\": {type: \"trait\"}\ntemplate: patch: {\n\t// +patchStrategy=r\n}\n"}},
	}, false)
	c.diagnostics()
	values := complete(t, c, 2, 20)
	assert.ElementsMatch(t, []string{"replace", "retainKeys"}, itemLabels(values))
}

func TestCompletionInAnUnopenedDocumentIsEmpty(t *testing.T) {
	assert.Empty(t, complete(t, newClient(t), 0, 0).Items)
}

func itemLabels(l CompletionList) []string {
	var out []string
	for _, i := range l.Items {
		out = append(out, i.Label)
	}
	return out
}

func TestCompletesFunctionsAndImports(t *testing.T) {
	c := newClient(t)
	text := "import (\n\t\"vela/kube\"\n\t\"vela/\n)\n\"x\": {type: \"component\"}\ntemplate: {\n\tapply: kube.#Ap\n}\n"
	c.send("textDocument/didOpen", DidOpenTextDocumentParams{TextDocument: TextDocumentItem{URI: uri, LanguageID: "cue", Version: 1, Text: text}}, false)
	c.diagnostics()

	fns := complete(t, c, 6, 15)
	require.Len(t, fns.Items, 1)
	assert.Equal(t, "#Apply", fns.Items[0].Label)
	assert.Equal(t, CompletionItemKindFunction, fns.Items[0].Kind)
	assert.Contains(t, fns.Items[0].Documentation.Value, "The resource to apply")
	assert.Equal(t, 2, fns.Items[0].InsertTextFormat, "inserted as a snippet")
	assert.Contains(t, fns.Items[0].TextEdit.NewText, "resource: ${1}")

	imports := complete(t, c, 2, 7)
	assert.Contains(t, itemLabels(imports), "vela/http")
	assert.NotContains(t, itemLabels(imports), "vela/op")
}

func TestCompletesWhatAFunctionReturns(t *testing.T) {
	c := newClient(t)
	text := "import \"vela/http\"\n\"x\": {\n\ttype: \"component\"\n\tattributes: workload: type: \"autodetects.core.oam.dev\"\n}\ntemplate: {\n\t_req: http.#Do & {$params: {method: \"GET\", url: \"https://x\"}}\n\toutput: {apiVersion: \"v1\", kind: \"ConfigMap\", data: c: _req.$returns.}\n}\n"
	c.send("textDocument/didOpen", DidOpenTextDocumentParams{TextDocument: TextDocumentItem{URI: uri, LanguageID: "cue", Version: 1, Text: text}}, false)
	c.diagnostics()
	line := "\toutput: {apiVersion: \"v1\", kind: \"ConfigMap\", data: c: _req.$returns."
	assert.Contains(t, itemLabels(complete(t, c, 7, uint32(len(line)))), "statusCode")
}

func TestCompletesInATestFile(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "scaler.cue"), []byte("\"scaler\": {\n\ttype: \"trait\"\n}\ntemplate: patch: {}\n"), 0o600))
	testURI := "file://" + filepath.Join(dir, "scaler_test.cue")
	c := newClient(t)
	text := "import \"vela/test\"\n\n\"patches\": test.#\n"
	c.send("textDocument/didOpen", DidOpenTextDocumentParams{TextDocument: TextDocumentItem{URI: testURI, LanguageID: "cue", Version: 1, Text: text}}, false)
	c.diagnostics()
	m := c.response(c.send("textDocument/completion", CompletionParams{TextDocument: TextDocumentIdentifier{URI: testURI}, Position: Position{Line: 2, Character: uint32(len("\"patches\": test.#"))}}, true))
	var list CompletionList
	require.NoError(t, json.Unmarshal(m["result"], &list))
	assert.ElementsMatch(t, []string{"#TraitRender", "#TraitStatus"}, itemLabels(list))
	assert.Contains(t, list.Items[0].TextEdit.NewText, `definition: "scaler"`)
}
