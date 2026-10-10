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
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const headerTrait = `scaling: {
	type: "trait"
	attributes: appliesToWorkloads: ["deployments.apps"]
}
template: {
	patch: spec: replicas: parameter.replicas
	parameter: {
		// +usage=How many
		replicas: *1 | int
	}
}
`

func TestDefinitionHeader(t *testing.T) {
	c := newClient(t)
	m := c.response(c.send(MethodDefinitionHeader, DefinitionHeaderParams{TextDocument: TextDocumentIdentifier{URI: uri}, Text: headerTrait}, true))
	require.Empty(t, string(m["error"]))
	var h DefinitionHeaderResult
	require.NoError(t, json.Unmarshal(m["result"], &h))
	assert.Equal(t, "trait", h.Type)
	var applies HeaderValue
	for _, f := range h.Fields {
		if strings.Join(f.Path, ".") == "attributes.appliesToWorkloads" {
			applies = f
		}
	}
	assert.True(t, applies.Set)
	require.NotNil(t, applies.Range)
	assert.Equal(t, uint32(2), applies.Range.Start.Line, "0-based")
	assert.Contains(t, h.Suggestions["workloads"], "deployments.apps", "from the built-in components")
	assert.Contains(t, h.Suggestions["traits"], "scaler")

	m = c.response(c.send(MethodEditDefinitionHeader, EditDefinitionHeaderParams{TextDocument: TextDocumentIdentifier{URI: uri}, Text: headerTrait, Path: []string{"description"}, Value: "Scales it."}, true))
	require.Empty(t, string(m["error"]))
	var edits []TextEdit
	require.NoError(t, json.Unmarshal(m["result"], &edits))
	require.Len(t, edits, 1)
	assert.Contains(t, edits[0].NewText, `description: "Scales it."`)
}

func TestAddonMetadataFields(t *testing.T) {
	c := newClient(t)
	m := c.response(c.send(MethodAddonMetadataFields, struct{}{}, true))
	require.Empty(t, string(m["error"]))
	var fs []HeaderValue
	require.NoError(t, json.Unmarshal(m["result"], &fs))
	require.NotEmpty(t, fs)
	assert.Equal(t, []string{"name"}, fs[0].Path)
}
