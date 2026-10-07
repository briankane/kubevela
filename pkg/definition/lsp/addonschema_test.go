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

// An addon folder gives its metadata, its parameters' schema and its UI schema.
func TestAddonSchema(t *testing.T) {
	dir := t.TempDir()
	write := func(name, text string) {
		require.NoError(t, os.MkdirAll(filepath.Dir(filepath.Join(dir, name)), 0o750))
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(text), 0o600))
	}
	write("metadata.yaml", "name: shop-extras\nversion: 1.2.0\ndescription: Extras for the shop\ndependencies:\n  - name: fluxcd\n")
	write("parameter.cue", "parameter: {\n\t// +usage=Where to install it\n\tnamespace: *\"shop\" | string\n\tcacheReplicas?: int\n}\n")
	write("template.cue", "output: {type: \"k8s-objects\"}\n")
	write("schemas/addon-uischema-shop-extras.yaml", "- jsonKey: namespace\n  label: Namespace\n")

	c := newClient(t)
	c.drain()
	m := c.response(c.send(MethodAddonSchema, AddonSchemaParams{Folder: dir}, true))
	var r AddonSchemaResult
	require.NoError(t, json.Unmarshal(m["result"], &r), string(m["error"]))
	assert.Equal(t, "shop-extras", r.Name)
	assert.Equal(t, "1.2.0", r.Version)
	assert.Equal(t, "Extras for the shop", r.Description)
	assert.Equal(t, []string{"fluxcd"}, r.Dependencies)

	var s struct {
		Properties map[string]struct {
			Default     interface{} `json:"default"`
			Description string      `json:"description"`
		} `json:"properties"`
	}
	require.NoError(t, json.Unmarshal(r.Schema, &s))
	assert.Equal(t, "shop", s.Properties["namespace"].Default)
	assert.Equal(t, "Where to install it", s.Properties["namespace"].Description)
	assert.Contains(t, s.Properties, "cacheReplicas")
	assert.Contains(t, string(r.UISchema), "Namespace")

	m = c.response(c.send(MethodAddonSchema, AddonSchemaParams{Folder: filepath.Join(dir, "nope")}, true))
	assert.Contains(t, string(m["error"]), "metadata.yaml")
}

// An addon whose parameters the generator refuses still gives a form.
func TestAddonSchemaTheGeneratorRefuses(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "metadata.yaml"), []byte("name: x\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "parameter.cue"), []byte("parameter: {\n\treplicas: *1 | int & >=1\n}\n"), 0o600))
	r, err := addonSchema(dir)
	require.NoError(t, err)
	assert.Empty(t, r.SchemaError)
	assert.Contains(t, string(r.Schema), `"replicas"`)
}
