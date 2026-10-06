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

const globalPolicySrc = `"platform-defaults": {
	type: "policy"
	attributes: {scope: "Application", global: true}
}
template: output: ctx: {
	region: "eu-west-1"
	tier:   "gold"
}
`

const componentSrc = "\"web\": {\n\ttype: \"component\"\n}\ntemplate: output: {\n\tapiVersion: \"v1\"\n\tkind:       \"ConfigMap\"\n\tdata: r: context.custom.\n}\n"

// customLabels asks for completions after context.custom. until they match
// want, as the workspace is indexed in the background.
func customLabels(t *testing.T, c *client, compURI string, want []string) []string {
	t.Helper()
	var got []string
	for end := time.Now().Add(10 * time.Second); time.Now().Before(end); time.Sleep(50 * time.Millisecond) {
		m := c.response(c.send("textDocument/completion", CompletionParams{
			TextDocument: TextDocumentIdentifier{URI: compURI},
			Position:     Position{Line: 6, Character: uint32(len("\tdata: r: context.custom."))},
		}, true))
		var list CompletionList
		require.NoError(t, json.Unmarshal(m["result"], &list))
		got = itemLabels(list)
		if assert.ObjectsAreEqual(want, got) {
			return got
		}
	}
	return got
}

func TestOffersWhatGlobalPoliciesPublish(t *testing.T) {
	dir := t.TempDir()
	policy := filepath.Join(dir, "policies", "platform-defaults.cue")
	require.NoError(t, os.MkdirAll(filepath.Dir(policy), 0o750))
	require.NoError(t, os.WriteFile(policy, []byte(globalPolicySrc), 0o600))
	compURI := "file://" + filepath.Join(dir, "web.cue")

	c := newClient(t)
	c.response(c.send("initialize", map[string]interface{}{"rootUri": "file://" + dir}, true))
	c.send("initialized", map[string]interface{}{}, false)
	c.send("textDocument/didOpen", DidOpenTextDocumentParams{TextDocument: TextDocumentItem{URI: compURI, LanguageID: "cue", Version: 1, Text: componentSrc}}, false)
	c.diagnostics()

	assert.Equal(t, []string{"region", "tier"}, customLabels(t, c, compURI, []string{"region", "tier"}), "indexed from disk")

	// An unsaved edit to the policy is seen at once.
	c.send("textDocument/didOpen", DidOpenTextDocumentParams{TextDocument: TextDocumentItem{
		URI: "file://" + policy, LanguageID: "cue", Version: 1, Text: strings.Replace(globalPolicySrc, "tier:", "zone: \"a\"\n\ttier:", 1),
	}}, false)
	c.diagnostics()
	assert.Equal(t, []string{"region", "tier", "zone"}, customLabels(t, c, compURI, []string{"region", "tier", "zone"}))

	// A deleted policy publishes nothing.
	c.send("textDocument/didClose", DidCloseTextDocumentParams{TextDocument: TextDocumentIdentifier{URI: "file://" + policy}}, false)
	c.diagnostics()
	require.NoError(t, os.Remove(policy))
	c.send("workspace/didChangeWatchedFiles", DidChangeWatchedFilesParams{Changes: []FileEvent{{URI: "file://" + policy, Type: FileChangeDeleted}}}, false)
	assert.Empty(t, customLabels(t, c, compURI, nil))
}
