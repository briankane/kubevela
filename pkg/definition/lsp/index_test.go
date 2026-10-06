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
	got = diagnosticsUntil(t, c, greeter, func(d []Diagnostic) bool { return !strings.Contains(messages(d), "ext/hello") })
	assert.NotContains(t, messages(got), "ext/hello", "the custom provider's package is found in the workspace")
}
