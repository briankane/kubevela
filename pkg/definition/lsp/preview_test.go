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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/oam-dev/kubevela/pkg/definition/preview"
)

const previewDef = `"my-worker": {
	type: "component"
	attributes: workload: definition: {apiVersion: "apps/v1", kind: "Deployment"}
}
template: {
	output: {
		apiVersion: "apps/v1"
		kind:       "Deployment"
		metadata: name: context.name
		spec: template: spec: containers: [{name: context.name, image: parameter.image}]
	}
	parameter: image: string
}
`

func TestPreviewOutput(t *testing.T) {
	c := newClient(t)
	id := c.send(MethodPreviewOutput, PreviewOutputParams{
		TextDocument: TextDocumentIdentifier{URI: uri},
		Text:         previewDef,
		Values:       "parameter: {image: nginx:1.25}\n",
	}, true)
	m := c.response(id)
	require.Empty(t, string(m["error"]))
	var r preview.Result
	require.NoError(t, json.Unmarshal(m["result"], &r))
	assert.Equal(t, "component", r.Type)
	require.Len(t, r.Objects, 1)
	assert.Contains(t, r.Objects[0].YAML, "image: nginx:1.25", "the text sent is rendered, not the file on disk")
	assert.Contains(t, r.Objects[0].YAML, "name: my-worker")
}

// A field of the preview is traced to what it reads, as JSON numbers index lists.
func TestProvenance(t *testing.T) {
	c := newClient(t)
	m := c.response(c.send(MethodProvenance, ProvenanceParams{
		TextDocument: TextDocumentIdentifier{URI: uri},
		Text:         previewDef,
		Values:       "parameter: {image: nginx:1.25}\n",
		Location:     preview.Location{Object: "output", Path: []interface{}{"spec", "template", "spec", "containers", float64(0), "image"}},
	}, true))
	require.Empty(t, string(m["error"]))
	var o preview.Origin
	require.NoError(t, json.Unmarshal(m["result"], &o))
	assert.Equal(t, preview.Origin{Kind: "parameter", Ref: "parameter.image", Set: true, Line: 9, Column: 58, RefLine: 11}, o)
}

func TestPreviewOutputNamesWhatIsMissing(t *testing.T) {
	c := newClient(t)
	m := c.response(c.send(MethodPreviewOutput, PreviewOutputParams{TextDocument: TextDocumentIdentifier{URI: uri}, Text: previewDef}, true))
	var r preview.Result
	require.NoError(t, json.Unmarshal(m["result"], &r))
	assert.Contains(t, r.Error, "image")
}

func TestPreviewValues(t *testing.T) {
	c := newClient(t)
	m := c.response(c.send(MethodPreviewValues, PreviewValuesParams{TextDocument: TextDocumentIdentifier{URI: uri}, Text: previewDef}, true))
	require.Empty(t, string(m["error"]))
	var r PreviewValuesResult
	require.NoError(t, json.Unmarshal(m["result"], &r))
	assert.Contains(t, r.YAML, "image: null # required string")
	assert.Contains(t, r.YAML, "name: my-worker")
}
