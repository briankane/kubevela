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

package preview

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const addonTemplate = `package main

output: {
	apiVersion: "core.oam.dev/v1beta1"
	kind:       "Application"
	spec: components: [{name: "web", type: "webservice", properties: image: parameter.image}]
}
outputs: ns: {apiVersion: "v1", kind: "Namespace", metadata: name: "extra"}
`

func previewAddon(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "my-addon")
	require.NoError(t, os.MkdirAll(dir, 0o750))
	for name, text := range map[string]string{
		"metadata.yaml": "name: my-addon\nversion: 1.0.0\n",
		"parameter.cue": "parameter: {\n\t// +usage=Image to run\n\timage: *\"nginx\" | string\n\t// +usage=Replicas to run\n\treplicas: int\n}\n",
		"template.cue":  addonTemplate,
	} {
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(text), 0o600))
	}
	return dir
}

func TestPreviewAddon(t *testing.T) {
	dir := previewAddon(t)
	tmpl := filepath.Join(dir, "template.cue")

	res := Render(context.Background(), Request{Path: tmpl, Source: []byte(addonTemplate), Values: []byte("parameter:\n  image: busybox\n")})
	require.Empty(t, res.Error)
	assert.Equal(t, "addon", res.Type)
	require.Len(t, res.Objects, 2)
	assert.Contains(t, res.Objects[0].Name, "Application")
	assert.Contains(t, res.Objects[0].YAML, "busybox")
	assert.Contains(t, res.Objects[1].Name, "Namespace extra")

	edited := strings.Replace(addonTemplate, `name: "web"`, `name: "edited"`, 1)
	res = Render(context.Background(), Request{Path: tmpl, Source: []byte(edited)})
	require.Empty(t, res.Error)
	assert.Contains(t, res.Objects[0].YAML, "edited", "the editor's text, not the file on disk")

	res = Render(context.Background(), Request{Path: tmpl, Source: []byte(addonTemplate), Values: []byte("name: small\nparameter:\n  image: a\n---\nname: big\nparameter:\n  image: b\n")})
	require.Len(t, res.Inputs, 2)
	assert.Equal(t, "small", res.Inputs[0].Name)
	assert.Contains(t, res.Inputs[1].Objects[0].YAML, "image: b")
}

func TestAddonSkeleton(t *testing.T) {
	dir := previewAddon(t)
	got, err := Skeleton(context.Background(), filepath.Join(dir, "template.cue"), []byte(addonTemplate))
	require.NoError(t, err)
	assert.Contains(t, got, "my-addon")
	assert.Contains(t, got, `image: "nginx"`)
	assert.Contains(t, got, "replicas: null # required int")
	assert.NotContains(t, got, "context:", "an addon's render takes its parameters only")
}
