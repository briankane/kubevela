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
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const parentSrc = `"web": {
	type: "component"
	attributes: workload: type: "autodetects.core.oam.dev"
}
template: {
	output: {apiVersion: "apps/v1", kind: "Deployment"}
	parameter: {
		// +usage=Image to run
		image: string
	}
}
`

const childSrc = `"tenant-web": {
	type:    "component"
	extends: "web"
}
template: {
	$super: properties: imge: "nginx"
}
`

const helloPackageYAML = `apiVersion: cue.oam.dev/v1alpha1
kind: Package
metadata: {name: hello}
spec:
  path: ext/hello
  templates:
    hello.cue: |
      package hello
      #Say: {
        #do: "say"
        #provider: "hello"
        $params: name: string
      }
`

const usesHelloSrc = `import "ext/hello"

"greeter": {
	type: "component"
	attributes: workload: type: "autodetects.core.oam.dev"
}
template: {
	_greet: hello.#Say & {$params: name: "x"}
	output: {apiVersion: "v1", kind: "ConfigMap"}
}
`

// diagnosticsUntil reads published diagnostics for uri until ok accepts them.
func diagnosticsUntil(t *testing.T, c *client, uri string, ok func([]Diagnostic) bool) []Diagnostic {
	t.Helper()
	var last []Diagnostic
	for end := time.Now().Add(10 * time.Second); time.Now().Before(end); {
		p := c.diagnostics()
		if p.URI != uri {
			continue
		}
		last = p.Diagnostics
		if ok(last) {
			return last
		}
	}
	return last
}

func errorsOf(diags []Diagnostic) []Diagnostic {
	var out []Diagnostic
	for _, d := range diags {
		if d.Severity == SeverityError {
			out = append(out, d)
		}
	}
	return out
}

func messages(diags []Diagnostic) string {
	var out []string
	for _, d := range diags {
		out = append(out, d.Message)
	}
	return strings.Join(out, "\n")
}

func TestWorkspaceIndexFindsParentsAndPackages(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "web.cue"), []byte(parentSrc), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "hello-package.yaml"), []byte(helloPackageYAML), 0o600))
	c := newClient(t)
	c.response(c.send("initialize", map[string]interface{}{"rootUri": "file://" + dir}, true))
	c.send("initialized", map[string]interface{}{}, false)

	child := "file://" + filepath.Join(dir, "tenant-web.cue")
	c.send("textDocument/didOpen", DidOpenTextDocumentParams{TextDocument: TextDocumentItem{URI: child, LanguageID: "cue", Version: 1, Text: childSrc}}, false)
	got := diagnosticsUntil(t, c, child, func(d []Diagnostic) bool { return strings.Contains(messages(d), "web takes no parameter imge") })
	assert.Contains(t, messages(got), "web takes no parameter imge", "the parent is found in the workspace")

	greeter := "file://" + filepath.Join(dir, "greeter.cue")
	c.send("textDocument/didOpen", DidOpenTextDocumentParams{TextDocument: TextDocumentItem{URI: greeter, LanguageID: "cue", Version: 1, Text: usesHelloSrc}}, false)
	unresolved := func(d []Diagnostic) bool { return strings.Contains(messages(errorsOf(d)), "ext/hello") }
	got = diagnosticsUntil(t, c, greeter, func(d []Diagnostic) bool { return !unresolved(d) })
	assert.False(t, unresolved(got), "the custom provider's package is found in the workspace")
}

func TestListsTheWorkspaceDefinitions(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "web.cue"), []byte(parentSrc), 0o600))
	c := newClient(t)
	c.response(c.send("initialize", map[string]interface{}{"rootUri": "file://" + dir}, true))
	c.send("initialized", map[string]interface{}{}, false)
	var names []string
	for end := time.Now().Add(10 * time.Second); time.Now().Before(end) && len(names) == 0; time.Sleep(50 * time.Millisecond) {
		m := c.response(c.send(MethodDefinitions, DefinitionsParams{Type: "component"}, true))
		var r DefinitionsResult
		require.NoError(t, json.Unmarshal(m["result"], &r))
		names = r.Names
	}
	assert.Equal(t, []string{"web"}, names)
}

func TestAddonFiles(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "my-addon")
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "definitions"), 0o750))
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "schemas"), 0o750))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "metadata.yaml"), []byte("name: my-addon\nversion: 1.0.0\n"), 0o600))
	open := func(rel, text string) string {
		c := newClient(t)
		u := "file://" + filepath.Join(dir, rel)
		c.send("textDocument/didOpen", DidOpenTextDocumentParams{TextDocument: TextDocumentItem{URI: u, LanguageID: "cue", Version: 1, Text: text}}, false)
		return messages(c.diagnostics().Diagnostics)
	}
	assert.Contains(t, open("template.cue", "package main\n\noutput: {apiVersion: \"core.oam.dev/v1beta1\", kind: \"Deployment\"}\n"), "Application")
	assert.Contains(t, open("schemas/component-uischema-web.yaml", "- jsonKey: a\n  uitype: Input\n"), "uitype")
	assert.Contains(t, open("definitions/web.cue", "\"web\": {\n\ttype: \"component\"\n\tattributs: {}\n}\ntemplate: output: {apiVersion: \"v1\", kind: \"ConfigMap\"}\n"), "attributs", "an addon's definitions are checked as definitions")
	assert.Empty(t, open("values.yaml", "a: [unclosed\n"), "other YAML is not ours to check")
}

func TestPackageFiles(t *testing.T) {
	dir := t.TempDir()
	c := newClient(t)
	bad := strings.Replace(helloPackageYAML, "path: ext/hello", "path: vela/hello", 1)
	u := "file://" + filepath.Join(dir, "hello-package.yaml")
	c.send("textDocument/didOpen", DidOpenTextDocumentParams{TextDocument: TextDocumentItem{URI: u, LanguageID: "yaml", Version: 1, Text: bad}}, false)
	assert.Contains(t, messages(c.diagnostics().Diagnostics), "vela/")

	m := c.response(c.send(MethodNewPackage, NewPackageParams{Name: "greeter", Path: "ext/greeter", Protocol: "https"}, true))
	var r NewPackageResult
	require.NoError(t, json.Unmarshal(m["result"], &r))
	assert.Contains(t, r.YAML, "protocol: https")
}

func TestAddonCompletionAndHover(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "my-addon")
	require.NoError(t, os.MkdirAll(dir, 0o750))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "metadata.yaml"), []byte("name: my-addon\nversion: 1.0.0\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "parameter.cue"), []byte("parameter: {\n\t// +usage=Image to run\n\timage: string\n}\n"), 0o600))
	c := newClient(t)
	u := "file://" + filepath.Join(dir, "template.cue")
	text := "package main\n\noutput: spec: components: [{name: parameter.image}]\n"
	c.send("textDocument/didOpen", DidOpenTextDocumentParams{TextDocument: TextDocumentItem{URI: u, LanguageID: "cue", Version: 1, Text: text}}, false)
	c.diagnostics()

	line := "output: spec: components: [{name: parameter."
	m := c.response(c.send("textDocument/completion", CompletionParams{TextDocument: TextDocumentIdentifier{URI: u}, Position: Position{Line: 2, Character: uint32(len(line))}}, true))
	var list CompletionList
	require.NoError(t, json.Unmarshal(m["result"], &list))
	var labels []string
	for _, it := range list.Items {
		labels = append(labels, it.Label)
	}
	assert.Contains(t, labels, "image")

	m = c.response(c.send("textDocument/hover", HoverParams{TextDocument: TextDocumentIdentifier{URI: u}, Position: Position{Line: 2, Character: uint32(len(line) + 2)}}, true))
	var h Hover
	require.NoError(t, json.Unmarshal(m["result"], &h))
	assert.Contains(t, h.Contents.Value, "Image to run")
}

func TestYAMLCompletion(t *testing.T) {
	c := newClient(t)
	u := "file://" + filepath.Join(t.TempDir(), "greeter-package.yaml")
	text := "apiVersion: cue.oam.dev/v1alpha1\nkind: Package\nspec:\n  provider:\n    protocol: \n"
	c.send("textDocument/didOpen", DidOpenTextDocumentParams{TextDocument: TextDocumentItem{URI: u, LanguageID: "yaml", Version: 1, Text: text}}, false)
	c.diagnostics()
	m := c.response(c.send("textDocument/completion", CompletionParams{TextDocument: TextDocumentIdentifier{URI: u}, Position: Position{Line: 4, Character: uint32(len("    protocol: "))}}, true))
	var list CompletionList
	require.NoError(t, json.Unmarshal(m["result"], &list))
	var labels []string
	for _, it := range list.Items {
		labels = append(labels, it.Label)
	}
	assert.ElementsMatch(t, []string{"grpc", "http", "https"}, labels)
}
