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

	"github.com/oam-dev/kubevela/pkg/definition/crdgen"
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

// A selection is evaluated where it is written, with the values sent.
func TestEvaluate(t *testing.T) {
	c := newClient(t)
	line := uint32(9)
	at := strings.Index(strings.Split(previewDef, "\n")[line], "parameter.image")
	m := c.response(c.send(MethodEvaluate, EvaluateParams{
		TextDocument: TextDocumentIdentifier{URI: uri},
		Text:         previewDef,
		Values:       "parameter: {image: nginx:1.25}\n",
		Range:        Range{Start: Position{Line: line, Character: uint32(at)}, End: Position{Line: line, Character: uint32(at + len("parameter.image"))}},
	}, true))
	require.Empty(t, string(m["error"]))
	var e preview.Evaluation
	require.NoError(t, json.Unmarshal(m["result"], &e))
	assert.Equal(t, preview.Evaluation{Value: `"nginx:1.25"`, Concrete: true}, e)
}

func TestPreviewTest(t *testing.T) {
	c := newClient(t)
	m := c.response(c.send(MethodPreviewTest, PreviewTestParams{TextDocument: TextDocumentIdentifier{URI: uri}, Text: previewDef, Values: "parameter: {image: nginx:1.25}\n", Name: "renders nginx"}, true))
	require.Empty(t, string(m["error"]))
	var r PreviewTestResult
	require.NoError(t, json.Unmarshal(m["result"], &r))
	assert.Contains(t, r.Cases, `"renders nginx": test.#ComponentRender & {`)
	assert.Contains(t, r.Cases, `image: "nginx:1.25"`)
}

func TestExplore(t *testing.T) {
	c := newClient(t)
	m := c.response(c.send(MethodExplore, PreviewOutputParams{TextDocument: TextDocumentIdentifier{URI: uri}, Text: previewDef, Values: "parameter: {image: nginx:1.25}\n"}, true))
	require.Empty(t, string(m["error"]))
	var e preview.Exploration
	require.NoError(t, json.Unmarshal(m["result"], &e))
	assert.Empty(t, e.Base.Error)
	assert.NotEmpty(t, e.Base.Objects)
	assert.Empty(t, e.Variants, "a required string has nothing to vary")
}

func TestComponentFromCRD(t *testing.T) {
	src, err := os.ReadFile("../crdgen/testdata/caches.yaml")
	require.NoError(t, err)
	c := newClient(t)
	m := c.response(c.send(MethodCRDFields, CRDParams{Text: string(src)}, true))
	require.Empty(t, string(m["error"]))
	var info crdgen.Info
	require.NoError(t, json.Unmarshal(m["result"], &info))
	assert.Equal(t, "Cache", info.Kind)

	m = c.response(c.send(MethodComponentFromCRD, ComponentFromCRDParams{Text: string(src), Name: "cache", Choices: []crdgen.Choice{{Path: []string{"resources"}, To: "trait:cache-resources"}}}, true))
	require.Empty(t, string(m["error"]))
	var r ComponentFromCRDResult
	require.NoError(t, json.Unmarshal(m["result"], &r))
	require.Len(t, r.Files, 2)
	assert.Equal(t, "cache-resources.cue", r.Files[1].Name)

	m = c.response(c.send(MethodCRDFields, CRDParams{Text: "kind: ConfigMap\n"}, true))
	assert.Contains(t, string(m["error"]), "not a CustomResourceDefinition")
}

func TestDefinitionsFromCRDs(t *testing.T) {
	cache, err := os.ReadFile("../crdgen/testdata/caches.yaml")
	require.NoError(t, err)
	related, err := os.ReadFile("../crdgen/testdata/related.yaml")
	require.NoError(t, err)
	src := string(cache) + "---\n" + string(related)
	c := newClient(t)
	m := c.response(c.send(MethodCRDSet, CRDParams{Text: src}, true))
	require.Empty(t, string(m["error"]))
	var members []crdgen.Member
	require.NoError(t, json.Unmarshal(m["result"], &members))
	require.Len(t, members, 3)
	assert.Equal(t, "Cache", members[1].Refs[0].Of)

	m = c.response(c.send(MethodDefinitionsFromCRDs, DefinitionsFromCRDsParams{Text: src, Plans: []crdgen.Plan{
		{Kind: "Cache", Role: "component", Name: "cache"},
		{Kind: "CacheBackup", Role: "trait", Name: "cache-backup", Of: "Cache", Ref: []string{"cacheRef"}},
	}}, true))
	require.Empty(t, string(m["error"]))
	var r ComponentFromCRDResult
	require.NoError(t, json.Unmarshal(m["result"], &r))
	require.Len(t, r.Files, 2)
	assert.Equal(t, "cache-backup.cue", r.Files[1].Name)
}

// workspaceScaling is a workspace trait patching the replicas a Cache has.
const workspaceScaling = `scaling: {
	type: "trait"
	attributes: appliesToWorkloads: ["deployments.apps"]
}
template: {
	patch: spec: replicas: parameter.replicas
	parameter: replicas: *1 | int
}
`

func TestDefinitionsFromCRDsUseExistingTraits(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "scaling.cue")
	require.NoError(t, os.WriteFile(path, []byte(workspaceScaling), 0o600))
	cache, err := os.ReadFile("../crdgen/testdata/caches.yaml")
	require.NoError(t, err)
	c := newClient(t)
	c.response(c.send("initialize", map[string]interface{}{"rootUri": "file://" + dir}, true))
	c.send("initialized", map[string]interface{}{}, false)

	existing := func() map[string]crdgen.Existing {
		m := c.response(c.send(MethodCRDSet, CRDParams{Text: string(cache)}, true))
		require.Empty(t, string(m["error"]))
		var members []crdgen.Member
		require.NoError(t, json.Unmarshal(m["result"], &members))
		out := map[string]crdgen.Existing{}
		for _, e := range members[0].Existing {
			out[e.Name] = e
		}
		return out
	}
	require.Eventually(t, func() bool { _, ok := existing()["scaling"]; return ok }, 10*time.Second, 50*time.Millisecond, "the workspace's trait is offered")
	assert.True(t, existing()["scaling"].Editable)
	assert.Contains(t, existing(), "scaler", "a built-in one too")

	m := c.response(c.send(MethodDefinitionsFromCRDs, DefinitionsFromCRDsParams{Text: string(cache), Plans: []crdgen.Plan{
		{Kind: "Cache", Role: "component", Name: "cache", Choices: []crdgen.Choice{{Path: []string{"replicas"}, To: "existing:scaling"}}},
	}}, true))
	require.Empty(t, string(m["error"]))
	var r ComponentFromCRDResult
	require.NoError(t, json.Unmarshal(m["result"], &r))
	require.Len(t, r.Files, 2)
	assert.Equal(t, path, r.Files[1].Path)
	assert.Contains(t, r.Files[1].Text, `"deployments.apps", "caches.shop.example.com"`)
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
