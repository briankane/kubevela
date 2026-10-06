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

package addon

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func offlineAddon(t *testing.T, files map[string]string) *InstallPackage {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "my-addon")
	for name, text := range files {
		require.NoError(t, os.MkdirAll(filepath.Dir(filepath.Join(dir, name)), 0o750))
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(text), 0o600))
	}
	pkg, err := LoadLocalInstallPackage("my-addon", dir)
	require.NoError(t, err)
	return pkg
}

func TestRenderAppOffline(t *testing.T) {
	t.Run("a CUE template", func(t *testing.T) {
		pkg := offlineAddon(t, map[string]string{
			"metadata.yaml": "name: my-addon\nversion: 1.0.0\n",
			"parameter.cue": "parameter: image: *\"nginx\" | string\n",
			"template.cue": `package main
output: {
	apiVersion: "core.oam.dev/v1beta1"
	kind:       "Application"
	spec: components: [{name: "web", type: "webservice", properties: image: parameter.image}]
}
outputs: ns: {apiVersion: "v1", kind: "Namespace", metadata: name: "extra"}
`,
		})
		app, aux, err := RenderAppOffline(pkg, map[string]interface{}{"image": "busybox"})
		require.NoError(t, err)
		require.Len(t, app.Spec.Components, 1)
		assert.Contains(t, string(app.Spec.Components[0].Properties.Raw), "busybox")
		require.Len(t, aux, 1)
		assert.Equal(t, "Namespace", aux[0].GetKind())
	})
	t.Run("a legacy addon deployed to runtime clusters", func(t *testing.T) {
		pkg := offlineAddon(t, map[string]string{
			"metadata.yaml": "name: my-addon\nversion: 1.0.0\ndeployTo:\n  runtimeCluster: true\n",
			"template.yaml": "apiVersion: core.oam.dev/v1beta1\nkind: Application\nmetadata:\n  name: my-addon\nspec:\n  components: []\n",
		})
		_, _, err := RenderAppOffline(pkg, nil)
		assert.ErrorIs(t, err, ErrRenderNeedsCluster)
	})
	t.Run("a legacy addon on the control plane", func(t *testing.T) {
		pkg := offlineAddon(t, map[string]string{
			"metadata.yaml": "name: my-addon\nversion: 1.0.0\n",
			"template.yaml": "apiVersion: core.oam.dev/v1beta1\nkind: Application\nmetadata:\n  name: my-addon\nspec:\n  components: []\n",
		})
		_, _, err := RenderAppOffline(pkg, nil)
		assert.NoError(t, err)
	})
}
