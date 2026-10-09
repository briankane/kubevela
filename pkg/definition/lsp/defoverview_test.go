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

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/oam-dev/kubevela/pkg/definition"
)

const scalerCUE = `scaler: {
	type: "trait"
	description: "Scales a workload"
	attributes: appliesToWorkloads: ["deployments.apps"]
}
template: {
	parameter: replicas: *1 | int
	patch: spec: replicas: parameter.replicas
}
`

// applied is the object vela def apply writes for src, as the cluster
// holds it: with a namespace, a revision and an addon's label.
func applied(t *testing.T, src string, revision int64, addon string) unstructured.Unstructured {
	t.Helper()
	def := definition.Definition{Unstructured: unstructured.Unstructured{}}
	require.NoError(t, def.FromCUEString(src, nil))
	def.SetNamespace("vela-system")
	if addon != "" {
		def.SetLabels(map[string]string{"addons.oam.dev/name": addon})
	}
	require.NoError(t, unstructured.SetNestedField(def.Object, revision, "status", "latestRevision", "revision"))
	return def.Unstructured
}

// A workspace definition is the same as the cluster's when what vela def
// apply would write matches it, however the file is laid out.
func TestSameDefinition(t *testing.T) {
	cluster := applied(t, scalerCUE, 2, "")
	same, err := sameDefinition([]byte(strings.ReplaceAll(scalerCUE, "\t", "  ")), cluster)
	require.NoError(t, err)
	assert.True(t, same, "layout alone is no change")

	same, err = sameDefinition([]byte(strings.Replace(scalerCUE, "*1 | int", "*2 | int", 1)), cluster)
	require.NoError(t, err)
	assert.False(t, same, "a default changed")

	same, err = sameDefinition([]byte(strings.Replace(scalerCUE, "Scales a workload", "Scales it", 1)), cluster)
	require.NoError(t, err)
	assert.False(t, same, "the description changed")
}

func TestDefinitionsOverview(t *testing.T) {
	workspace := []workspaceDefinition{
		{Name: "scaler", Type: "trait", Path: "/w/scaler.cue", Src: []byte(scalerCUE)},
		{Name: "tenant-web", Type: "component", Path: "/w/tenant-web.cue", Src: []byte("x")},
		{Name: "gateway", Type: "trait", Path: "/w/gateway.cue", Src: []byte(strings.Replace(strings.Replace(scalerCUE, "scaler: {", "gateway: {", 1), "*1 | int", "*2 | int", 1))},
	}
	gateway := applied(t, strings.Replace(scalerCUE, "scaler: {", "gateway: {", 1), 1, "")
	cluster := []unstructured.Unstructured{applied(t, scalerCUE, 3, ""), applied(t, strings.Replace(scalerCUE, "scaler: {", "expose: {", 1), 1, "ingress"), gateway}
	got := definitionsOverview(workspace, cluster)
	by := map[string]DefinitionOverview{}
	for _, d := range got {
		by[d.Type+"/"+d.Name] = d
	}
	assert.Equal(t, DefinitionOverview{Name: "scaler", Type: "trait", Path: "/w/scaler.cue", Namespace: "vela-system", Revision: 3, State: "same"}, by["trait/scaler"])
	assert.Equal(t, DefinitionOverview{Name: "tenant-web", Type: "component", Path: "/w/tenant-web.cue", State: "workspace"}, by["component/tenant-web"])
	assert.Equal(t, DefinitionOverview{Name: "expose", Type: "trait", Namespace: "vela-system", Revision: 1, Addon: "ingress", State: "cluster"}, by["trait/expose"])
	assert.Equal(t, "changed", by["trait/gateway"].State, "the file's default differs from the cluster's")
	assert.Len(t, got, 4)
}

// The server answers with its workspace's definitions beside its cluster's.
func TestDefinitionsOverviewRequest(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "scaler.cue"), []byte(scalerCUE), 0o600))
	cluster := func() (Cluster, error) {
		c, err := velaCluster()
		c.Definitions = []unstructured.Unstructured{applied(t, scalerCUE, 2, "")}
		return c, err
	}
	c, _, _ := openWith(t, cluster, "auto", root, "other.cue", "a: 1\n")
	var got DefinitionsOverviewResult
	require.NoError(t, json.Unmarshal(c.response(c.send(MethodDefinitionsOverview, DefinitionsOverviewParams{}, true))["result"], &got))
	assert.Equal(t, "k3d-test", got.Context)
	require.Len(t, got.Definitions, 1)
	assert.Equal(t, DefinitionOverview{Name: "scaler", Type: "trait", Path: filepath.Join(root, "scaler.cue"), Namespace: "vela-system", Revision: 2, State: "same"}, got.Definitions[0])
}
