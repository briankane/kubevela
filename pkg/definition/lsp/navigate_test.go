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

func TestInlayHintRequest(t *testing.T) {
	c := newClient(t)
	text := "\"c\": {\n\ttype: \"component\"\n\tattributes: workload: type: \"autodetects.core.oam.dev\"\n}\ntemplate: {\n\toutput: {apiVersion: \"apps/v1\", kind: \"Deployment\", spec: replicas: parameter.replicas}\n\tparameter: replicas: *2 | int\n}\n"
	c.send("textDocument/didOpen", DidOpenTextDocumentParams{TextDocument: TextDocumentItem{URI: uri, LanguageID: "cue", Version: 1, Text: text}}, false)
	c.diagnostics()
	m := c.response(c.send("textDocument/inlayHint", InlayHintParams{TextDocument: TextDocumentIdentifier{URI: uri}, Range: Range{End: Position{Line: 100}}}, true))
	var hints []InlayHint
	require.NoError(t, json.Unmarshal(m["result"], &hints))
	require.Len(t, hints, 1)
	assert.Equal(t, "= 2", hints[0].Label)
	assert.Equal(t, uint32(5), hints[0].Position.Line)
}

const statusSrc = "\"web\": {\n\ttype: \"component\"\n\tattributes: {\n\t\tworkload: type: \"autodetects.core.oam.dev\"\n\t\tstatus: healthPolicy: #\"\"\"\n\t\t\tisHealth: context.output.status.readyReplicas == context.output.spec.replicas\n\t\t\t\"\"\"#\n\t}\n}\ntemplate: {\n\toutput: {apiVersion: \"apps/v1\", kind: \"Deployment\", spec: replicas: 1}\n\tparameter: {}\n}\n"

func TestStatusFieldRequests(t *testing.T) {
	c := newClient(t)
	c.send("textDocument/didOpen", DidOpenTextDocumentParams{TextDocument: TextDocumentItem{URI: uri, LanguageID: "cue", Version: 1, Text: statusSrc}}, false)
	c.diagnostics()
	line := strings.Split(statusSrc, "\n")[5]
	pos := Position{Line: 5, Character: uint32(strings.Index(line, "readyReplicas") + 2)}
	m := c.response(c.send("textDocument/completion", CompletionParams{TextDocument: TextDocumentIdentifier{URI: uri}, Position: pos}, true))
	var list CompletionList
	require.NoError(t, json.Unmarshal(m["result"], &list))
	var labels []string
	for _, it := range list.Items {
		labels = append(labels, it.Label)
	}
	assert.Contains(t, labels, "readyReplicas", "the live Deployment's status, in a string")

	m = c.response(c.send("textDocument/hover", HoverParams{TextDocument: TextDocumentIdentifier{URI: uri}, Position: pos}, true))
	var h Hover
	require.NoError(t, json.Unmarshal(m["result"], &h))
	assert.Contains(t, h.Contents.Value, "readyReplicas")
}

// Go to definition on what KubeVela declares leads to a read-only document
// of its source, which vela/source returns.
func TestDefinitionInSources(t *testing.T) {
	src := "import \"vela/kube\"\n\n\"web\": {\n\ttype: \"component\"\n\tattributes: workload: type: \"autodetects.core.oam.dev\"\n}\ntemplate: {\n\t_dep: kube.#Get & {$params: resource: {apiVersion: \"v1\", kind: \"ConfigMap\", metadata: name: context.name}}\n\toutput: {apiVersion: \"v1\", kind: \"ConfigMap\"}\n\tparameter: {}\n}\n"
	c := newClient(t)
	c.send("textDocument/didOpen", DidOpenTextDocumentParams{TextDocument: TextDocumentItem{URI: uri, LanguageID: "cue", Version: 1, Text: src}}, false)
	c.diagnostics()
	line := strings.Split(src, "\n")[7]
	for word, want := range map[string]string{"#Get": "vela-source:/package/vela/kube.cue", "context.name": "vela-source:/context/component.cue"} {
		at := strings.Index(line, word) + len(word) - 1
		m := c.response(c.send("textDocument/definition", TextDocumentPositionParams{TextDocument: TextDocumentIdentifier{URI: uri}, Position: Position{Line: 7, Character: uint32(at)}}, true))
		var locs []Location
		require.NoError(t, json.Unmarshal(m["result"], &locs), string(m["result"]))
		require.Len(t, locs, 1, word)
		assert.Equal(t, want, locs[0].URI, word)

		m = c.response(c.send(MethodSource, SourceParams{URI: want}, true))
		var r SourceResult
		require.NoError(t, json.Unmarshal(m["result"], &r))
		got := strings.Split(r.Text, "\n")[locs[0].Range.Start.Line]
		assert.Contains(t, got[locs[0].Range.Start.Character:], strings.TrimPrefix(word[strings.LastIndex(word, ".")+1:], "."), word)
	}
}

// After field:, completion offers the types a parameter can declare.
func TestCompleteFieldType(t *testing.T) {
	src := strings.Replace(fixedDef, "template: {\n", "template: {\n\tparameter: {\n\t\ttimeout: \n\t}\n", 1)
	c := newClient(t)
	c.send("textDocument/didOpen", DidOpenTextDocumentParams{TextDocument: TextDocumentItem{URI: uri, LanguageID: "cue", Version: 1, Text: src}}, false)
	c.diagnostics()
	line := 6
	col := len(strings.Split(src, "\n")[line])
	m := c.response(c.send("textDocument/completion", CompletionParams{TextDocument: TextDocumentIdentifier{URI: uri}, Position: Position{Line: uint32(line), Character: uint32(col)}}, true))
	var list CompletionList
	require.NoError(t, json.Unmarshal(m["result"], &list))
	var labels []string
	for _, it := range list.Items {
		labels = append(labels, it.Label)
	}
	assert.Contains(t, labels, "string")
	assert.Contains(t, labels, "*default | type")
}

func TestNewApplicationRequests(t *testing.T) {
	c := newClient(t)
	m := c.response(c.send(MethodComponentTypes, struct{}{}, true))
	var types ComponentTypesResult
	require.NoError(t, json.Unmarshal(m["result"], &types))
	var names []string
	for _, ty := range types.Types {
		names = append(names, ty.Name)
	}
	assert.Contains(t, names, "webservice")

	m = c.response(c.send(MethodNewApplication, NewApplicationParams{Name: "shop", Type: "webservice"}, true))
	var app NewApplicationResult
	require.NoError(t, json.Unmarshal(m["result"], &app))
	assert.Contains(t, app.Snippet, "type: webservice")

	m = c.response(c.send(MethodNewApplication, NewApplicationParams{Name: "shop", Type: "nope"}, true))
	assert.Contains(t, string(m["error"]), "no component type nope")
}

func TestApplicationLensesAndAdd(t *testing.T) {
	app := "apiVersion: core.oam.dev/v1beta1\nkind: Application\nmetadata:\n  name: shop\nspec:\n  components:\n    - name: web\n      type: webservice\n      properties:\n        image: nginx\n"
	appURI := "file:///defs/shop.yaml"
	c := newClient(t)
	c.send("textDocument/didOpen", DidOpenTextDocumentParams{TextDocument: TextDocumentItem{URI: appURI, LanguageID: "yaml", Version: 1, Text: app}}, false)
	c.diagnostics()
	m := c.response(c.send("textDocument/codeLens", CodeLensParams{TextDocument: TextDocumentIdentifier{URI: appURI}}, true))
	var lenses []CodeLens
	require.NoError(t, json.Unmarshal(m["result"], &lenses))
	var titles []string
	for _, l := range lenses {
		titles = append(titles, l.Command.Title)
		assert.Equal(t, CommandAddToApplication, l.Command.Command)
	}
	assert.Equal(t, []string{"Add policy", "Add workflow step", "Add component", "Add trait"}, titles)

	m = c.response(c.send(MethodAddToApplication, AddToApplicationParams{TextDocument: TextDocumentIdentifier{URI: appURI}, Line: 6, Kind: "trait", Type: "scaler"}, true))
	var add AddToApplicationResult
	require.NoError(t, json.Unmarshal(m["result"], &add), string(m["error"]))
	assert.Contains(t, add.Snippet, "traits:")
	assert.Contains(t, add.Snippet, "- type: scaler")
}

// The Testing view's New Test: the kinds of test a definition has, and a
// case of one, asked from the definition or from a test file of it.
func TestNewTestOfAKind(t *testing.T) {
	dir := t.TempDir()
	def := filepath.Join(dir, "web.cue")
	require.NoError(t, os.WriteFile(def, []byte(fixedDef), 0o600))
	test := filepath.Join(dir, "web_test.cue")
	require.NoError(t, os.WriteFile(test, []byte("import \"vela/test\"\n\nx: test.#ComponentRender & {definition: \"web\"}\n"), 0o600))
	c := newClient(t)
	c.drain()

	m := c.response(c.send(MethodTestKinds, TestKindsParams{TextDocument: TextDocumentIdentifier{URI: "file://" + test}}, true))
	var kinds TestKindsResult
	require.NoError(t, json.Unmarshal(m["result"], &kinds))
	var fns []string
	for _, k := range kinds.Kinds {
		fns = append(fns, k.Function)
	}
	assert.Equal(t, []string{"#ComponentRender", "#ComponentStatus"}, fns)

	m = c.response(c.send(MethodNewTest, NewTestParams{TextDocument: TextDocumentIdentifier{URI: "file://" + test}, Function: "#ComponentStatus"}, true))
	var r NewTestResult
	require.NoError(t, json.Unmarshal(m["result"], &r), string(m["error"]))
	assert.Equal(t, test, r.Path)
	assert.Contains(t, r.Cases, "test.#ComponentStatus & {")
	assert.NotContains(t, r.Cases, "import")
}

// A test file has a code lens to add a case, above its first line.
func TestTestFileLens(t *testing.T) {
	testURI := "file:///defs/web_test.cue"
	c := newClient(t)
	c.drain()
	c.send("textDocument/didOpen", DidOpenTextDocumentParams{TextDocument: TextDocumentItem{URI: testURI, LanguageID: "cue", Version: 1, Text: "import \"vela/test\"\n"}}, false)
	m := c.response(c.send("textDocument/codeLens", CodeLensParams{TextDocument: TextDocumentIdentifier{URI: testURI}}, true))
	var lenses []CodeLens
	require.NoError(t, json.Unmarshal(m["result"], &lenses))
	require.Len(t, lenses, 1)
	assert.Equal(t, uint32(0), lenses[0].Range.Start.Line)
	assert.Equal(t, CommandNewTestCase, lenses[0].Command.Command)
	assert.Equal(t, []interface{}{testURI}, lenses[0].Command.Arguments)
}

// After parameter., every parameter is offered, not only those of the
// field's type; with a let nothing uses yet in the template.
func TestCompleteParameterMembers(t *testing.T) {
	src := "\"web\": {\n\ttype: \"component\"\n\tattributes: workload: definition: {apiVersion: \"apps/v1\", kind: \"Deployment\"}\n}\ntemplate: {\n\tlet tag = \"v1\"\n\toutput: {\n\t\tapiVersion: \"apps/v1\"\n\t\tkind: \"Deployment\"\n\t\tmetadata: name: parameter.\n\t}\n\tparameter: {\n\t\timage: string\n\t\treplicas: *1 | int\n\t}\n}\n"
	c := newClient(t)
	c.drain()
	c.send("textDocument/didOpen", DidOpenTextDocumentParams{TextDocument: TextDocumentItem{URI: uri, LanguageID: "cue", Version: 1, Text: src}}, false)
	line := strings.Split(src, "\n")[9]
	m := c.response(c.send("textDocument/completion", CompletionParams{TextDocument: TextDocumentIdentifier{URI: uri}, Position: Position{Line: 9, Character: uint32(len(line))}}, true))
	var list CompletionList
	require.NoError(t, json.Unmarshal(m["result"], &list))
	var labels []string
	for _, it := range list.Items {
		labels = append(labels, it.Label)
	}
	assert.Equal(t, []string{"image", "replicas"}, labels)
}
