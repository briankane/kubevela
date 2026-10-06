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

package analysis

import (
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// completeYAML completes doc, the file at rel under dir, at the "|" in it.
func completeYAML(t *testing.T, dir, rel, doc string) []string {
	t.Helper()
	cursor := strings.Index(doc, "|")
	require.GreaterOrEqual(t, cursor, 0)
	doc = doc[:cursor] + doc[cursor+1:]
	got, ok := CompleteYAMLFile(filepath.Join(dir, rel), doc, cursor, Options{})
	require.True(t, ok, "%s is a YAML file KubeVela reads", rel)
	var out []string
	for _, c := range got {
		out = append(out, c.Label)
	}
	sort.Strings(out)
	return out
}

func TestCompleteAddonMetadataYAML(t *testing.T) {
	dir := addonDir(t)
	assert.Contains(t, completeYAML(t, dir, "metadata.yaml", "name: x\n|"), "version")
	assert.Equal(t, []string{"dependencies", "deployTo", "description"}, completeYAML(t, dir, "metadata.yaml", "name: x\nde|"))
	assert.Equal(t, []string{"disableControlPlane", "runtimeCluster", "runtime_cluster"}, completeYAML(t, dir, "metadata.yaml", "deployTo:\n  |"))
	assert.Equal(t, []string{"kubernetes", "vela"}, completeYAML(t, dir, "metadata.yaml", "system:\n  vela: \">=1.9\"\n  |"))
	assert.Equal(t, []string{"name", "version"}, completeYAML(t, dir, "metadata.yaml", "dependencies:\n  - |"))
	assert.Equal(t, []string{"name", "version"}, completeYAML(t, dir, "metadata.yaml", "dependencies:\n- name: fluxcd\n  |"))
	assert.Equal(t, []string{"false", "true"}, completeYAML(t, dir, "metadata.yaml", "invisible: |"))
}

func TestCompleteUISchemaYAML(t *testing.T) {
	dir := addonDir(t)
	rel := "schemas/component-uischema-web.yaml"
	assert.Contains(t, completeYAML(t, dir, rel, "- |"), "jsonKey")
	assert.Contains(t, completeYAML(t, dir, rel, "- jsonKey: image\n  |"), "uiType")
	assert.Contains(t, completeYAML(t, dir, rel, "- jsonKey: image\n  validate:\n    |"), "required")
	assert.Equal(t, []string{"!=", "==", "in"}, completeYAML(t, dir, rel, "- jsonKey: a\n  conditions:\n  - jsonKey: b\n    op: |"))
	assert.Contains(t, completeYAML(t, dir, rel, "- jsonKey: a\n  subParameters:\n  - jsonKey: b\n    |"), "uiType", "a parameter's own parameters")
}

func TestCompletePackageYAML(t *testing.T) {
	dir := t.TempDir()
	doc := "apiVersion: cue.oam.dev/v1alpha1\nkind: Package\nmetadata:\n  name: x\nspec:\n  |"
	assert.Equal(t, []string{"path", "provider", "templates"}, completeYAML(t, dir, "x-package.yaml", doc))
	doc = "apiVersion: cue.oam.dev/v1alpha1\nkind: Package\nspec:\n  provider:\n    protocol: |"
	assert.Equal(t, []string{"grpc", "http", "https"}, completeYAML(t, dir, "x-package.yaml", doc))
	// In a stream, the document the cursor is in decides.
	doc = "apiVersion: cue.oam.dev/v1alpha1\nkind: Package\n---\napiVersion: v1\nkind: ConfigMap\n|"
	assert.Contains(t, completeYAML(t, dir, "x.yaml", doc), "data", "a Kubernetes object's fields, from its kind")
}

func TestCompleteResourceYAML(t *testing.T) {
	dir := addonDir(t)
	doc := "apiVersion: apps/v1\nkind: Deployment\nmetadata:\n  name: x\nspec:\n  |"
	got := completeYAML(t, dir, "resources/deploy.yaml", doc)
	assert.Contains(t, got, "replicas")
	assert.Contains(t, got, "template")
}

func TestCompleteYAMLNotOurs(t *testing.T) {
	_, ok := CompleteYAMLFile(filepath.Join(t.TempDir(), "values.yaml"), "a: 1\n", 5, Options{})
	assert.False(t, ok, "YAML of no kind KubeVela reads")
}

func TestYAMLDocsAndHover(t *testing.T) {
	dir := addonDir(t)
	got, ok := CompleteYAMLFile(filepath.Join(dir, "metadata.yaml"), "name: x\ninvis", len("name: x\ninvis"), Options{})
	require.True(t, ok)
	require.Len(t, got, 1)
	assert.Contains(t, got[0].Doc, "Hides the addon")

	doc := "- jsonKey: image\n  uiType: ImageInput\n"
	h, ok := HoverYAMLFile(filepath.Join(dir, "schemas", "component-uischema-web.yaml"), doc, strings.Index(doc, "uiType")+2, Options{})
	require.True(t, ok)
	assert.Contains(t, h, "uiType: string")
	assert.Contains(t, h, "widget")
}
