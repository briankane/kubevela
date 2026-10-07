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
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const formDef = `"api": {
	type:        "component"
	description: "An API server"
	attributes: workload: type: "autodetects.core.oam.dev"
}
template: {
	output: {apiVersion: "apps/v1", kind: "Deployment"}
	parameter: {
		// +usage=The image to run
		image: string
		// +usage=The port it listens on
		port: *8080 | int
		env?: [...{name: string, value?: string}]
	}
}
`

const formUISchema = `- jsonKey: image
  label: Container image
  sort: 1
- jsonKey: port
  uiType: Number
`

// A definition's parameter is given as the controller publishes it for
// VelaUX's forms, with a UI schema the workspace has for it.
func TestDefinitionSchema(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "api.cue"), []byte(formDef), 0o600))
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "addon", "schemas"), 0o750))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "addon", "schemas", "component-uischema-api.yaml"), []byte(formUISchema), 0o600))
	c := newClient(t)
	c.response(c.send("initialize", map[string]interface{}{"rootUri": "file://" + dir}, true))
	c.send("initialized", map[string]interface{}{}, false)

	// The workspace is indexed after initialized: ask until it has been.
	var m map[string]json.RawMessage
	for end := time.Now().Add(10 * time.Second); time.Now().Before(end); time.Sleep(50 * time.Millisecond) {
		m = c.response(c.send(MethodDefinitionSchema, DefinitionSchemaParams{Type: "component", Name: "api"}, true))
		if len(m["error"]) == 0 {
			break
		}
	}
	var r DefinitionSchemaResult
	require.NoError(t, json.Unmarshal(m["result"], &r), string(m["error"]))
	assert.Equal(t, "An API server", r.Description)
	assert.Equal(t, "workspace", r.Source)

	var s struct {
		Required   []string `json:"required"`
		Properties map[string]struct {
			Type        string      `json:"type"`
			Default     interface{} `json:"default"`
			Description string      `json:"description"`
		} `json:"properties"`
	}
	require.NoError(t, json.Unmarshal(r.Schema, &s))
	assert.Contains(t, s.Required, "image")
	assert.Equal(t, "string", s.Properties["image"].Type)
	assert.Equal(t, "The image to run", s.Properties["image"].Description)
	assert.EqualValues(t, 8080, s.Properties["port"].Default)
	assert.Equal(t, "array", s.Properties["env"].Type)

	var ui []map[string]interface{}
	require.NoError(t, json.Unmarshal(r.UISchema, &ui))
	assert.Equal(t, "Container image", ui[0]["label"])

	m = c.response(c.send(MethodDefinitionSchema, DefinitionSchemaParams{Type: "component", Name: "nope"}, true))
	assert.Contains(t, string(m["error"]), "nope")
}
