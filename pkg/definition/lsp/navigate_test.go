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
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// posOf is the protocol position of the first occurrence of word in text,
// plus within.
func posOf(text, word string, within int) Position {
	i := strings.Index(text, word) + within
	line := strings.Count(text[:i], "\n")
	return Position{Line: uint32(line), Character: uint32(i - strings.LastIndex(text[:i], "\n") - 1)}
}

func TestGoToDefinition(t *testing.T) {
	dir := t.TempDir()
	write := func(rel, text string) string {
		p := filepath.Join(dir, rel)
		require.NoError(t, os.WriteFile(p, []byte(text), 0o600))
		return p
	}
	webPath := write("web.cue", parentSrc)
	pkgPath := write("hello-package.yaml", helloPackageYAML)
	c := newClient(t)
	c.drain()
	c.response(c.send("initialize", map[string]interface{}{"rootUri": "file://" + dir}, true))
	c.send("initialized", map[string]interface{}{}, false)
	open := func(name, text string) string {
		u := "file://" + filepath.Join(dir, name)
		c.send("textDocument/didOpen", DidOpenTextDocumentParams{TextDocument: TextDocumentItem{URI: u, LanguageID: "cue", Version: 1, Text: text}}, false)
		return u
	}
	definition := func(u string, pos Position) []Location {
		var locs []Location
		for end := 0; end < 50 && len(locs) == 0; end++ {
			m := c.response(c.send("textDocument/definition", TextDocumentPositionParams{TextDocument: TextDocumentIdentifier{URI: u}, Position: pos}, true))
			if string(m["result"]) != "null" {
				require.NoError(t, json.Unmarshal(m["result"], &locs))
			}
		}
		return locs
	}

	child := open("tenant-web.cue", childSrc)
	locs := definition(child, posOf(childSrc, `extends: "web"`, len(`extends: "w`)))
	require.Len(t, locs, 1, "the parent, once the workspace is indexed")
	assert.Equal(t, "file://"+webPath, locs[0].URI)

	greeter := open("greeter.cue", usesHelloSrc)
	locs = definition(greeter, posOf(usesHelloSrc, "hello.#Say", len("hello.#S")))
	require.Len(t, locs, 1)
	assert.Equal(t, "file://"+pkgPath, locs[0].URI)
	assert.Equal(t, uint32(strings.Count(helloPackageYAML[:strings.Index(helloPackageYAML, "#Say:")], "\n")), locs[0].Range.Start.Line)

	locs = definition(greeter, posOf(usesHelloSrc, `"ext/hello"`, 3))
	require.Len(t, locs, 1, "an import, to its Package")
	assert.Equal(t, "file://"+pkgPath, locs[0].URI)
	assert.Equal(t, uint32(strings.Count(helloPackageYAML[:strings.Index(helloPackageYAML, "path: ext/hello")], "\n")), locs[0].Range.Start.Line)

}

func TestRenameAParameter(t *testing.T) {
	c := newClient(t)
	text := "\"c\": {\n\ttype: \"component\"\n\tattributes: workload: type: \"autodetects.core.oam.dev\"\n}\ntemplate: {\n\toutput: {apiVersion: \"v1\", kind: \"ConfigMap\", metadata: name: parameter.name, data: n: parameter.name}\n\tparameter: name: string\n}\n"
	c.send("textDocument/didOpen", DidOpenTextDocumentParams{TextDocument: TextDocumentItem{URI: uri, LanguageID: "cue", Version: 1, Text: text}}, false)
	c.diagnostics()

	m := c.response(c.send("textDocument/references", ReferenceParams{TextDocument: TextDocumentIdentifier{URI: uri}, Position: posOf(text, "name: string", 1), Context: ReferenceContext{IncludeDeclaration: true}}, true))
	var refs []Location
	require.NoError(t, json.Unmarshal(m["result"], &refs))
	assert.Len(t, refs, 3)

	m = c.response(c.send("textDocument/rename", RenameParams{TextDocument: TextDocumentIdentifier{URI: uri}, Position: posOf(text, "name: string", 1), NewName: "appName"}, true))
	var edit WorkspaceEdit
	require.NoError(t, json.Unmarshal(m["result"], &edit))
	assert.Len(t, edit.Changes[uri], 3)
	for _, e := range edit.Changes[uri] {
		assert.Equal(t, "appName", e.NewText)
	}

	m = c.response(c.send("textDocument/rename", RenameParams{TextDocument: TextDocumentIdentifier{URI: uri}, Position: posOf(text, "name: string", 1), NewName: "app name"}, true))
	assert.Contains(t, string(m["error"]), "not a name")
}

func TestOutlineRequest(t *testing.T) {
	c := newClient(t)
	c.send("textDocument/didOpen", DidOpenTextDocumentParams{TextDocument: TextDocumentItem{URI: uri, LanguageID: "cue", Version: 1, Text: parentSrc}}, false)
	c.diagnostics()
	m := c.response(c.send("textDocument/documentSymbol", DocumentSymbolParams{TextDocument: TextDocumentIdentifier{URI: uri}}, true))
	var syms []DocumentSymbol
	require.NoError(t, json.Unmarshal(m["result"], &syms))
	require.Len(t, syms, 2)
	assert.Equal(t, "web", syms[0].Name)
	assert.Equal(t, 5, syms[0].Kind)
	assert.Equal(t, "template", syms[1].Name)
}
