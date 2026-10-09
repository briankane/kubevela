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

package crdgen

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"sigs.k8s.io/yaml"

	"github.com/oam-dev/kubevela/pkg/definition/preview"
)

// renderComponent is what a generated component renders with parameter.
func renderComponent(t *testing.T, f File, parameter map[string]interface{}) map[string]interface{} {
	t.Helper()
	values, err := yaml.Marshal(map[string]interface{}{"parameter": parameter, "context": map[string]string{"name": "sessions"}})
	require.NoError(t, err)
	r := preview.Render(context.Background(), preview.Request{Path: f.Name, Source: []byte(f.Text), Values: values})
	require.Empty(t, r.Error)
	require.Len(t, r.Objects, 1)
	var out map[string]interface{}
	require.NoError(t, yaml.Unmarshal([]byte(r.Objects[0].YAML), &out))
	return out
}

// patch is a workload as a generated trait leaves it, as KubeVela's trait engine patches it.
func patch(t *testing.T, f File, workload, parameter map[string]interface{}) map[string]interface{} {
	t.Helper()
	values, err := yaml.Marshal(map[string]interface{}{"parameter": parameter, "workload": workload})
	require.NoError(t, err)
	r := preview.Render(context.Background(), preview.Request{Path: f.Name, Source: []byte(f.Text), Values: values})
	require.Empty(t, r.Error, f.Text)
	require.NotNil(t, r.Workload)
	var out map[string]interface{}
	require.NoError(t, yaml.Unmarshal([]byte(r.Workload.YAML), &out))
	return out
}

func spec(o map[string]interface{}) map[string]interface{} {
	return o["spec"].(map[string]interface{})
}

func TestReadsMergeSemantics(t *testing.T) {
	info, err := Read(caches(t))
	require.NoError(t, err)
	merge := map[string]string{}
	for _, f := range info.Fields {
		merge[f.Name] = f.Merge
	}
	assert.Equal(t, "merges by key", merge["tolerations"])
	assert.Equal(t, "replaced whole", merge["podLabels"])
	assert.Equal(t, "replaced whole", merge["args"])
	assert.Equal(t, "", merge["engine"])
}

// A trait adds to a list another trait filled, by the CRD's key, and replaces an atomic map or list.
func TestTraitPatchesByTheCRDsSemantics(t *testing.T) {
	files, err := Generate(caches(t), "cache", []Choice{
		{Path: []string{"tolerations"}, To: "trait:cache-scheduling"},
		{Path: []string{"podLabels"}, To: "trait:cache-scheduling"},
		{Path: []string{"args"}, To: "trait:cache-scheduling"},
	})
	require.NoError(t, err)
	trait := files[1]
	assert.Contains(t, trait.Text, "// +patchKey=key")
	assert.Contains(t, trait.Text, "// +patchStrategy=replace\n\t\t\targs:")
	assert.Contains(t, trait.Text, "// +patchStrategy=retainKeys\n\t\t\tpodLabels:")

	workload := renderComponent(t, files[0], map[string]interface{}{"version": "7.2"})
	spec(workload)["tolerations"] = []interface{}{map[string]interface{}{"key": "zone", "operator": "Exists"}}
	spec(workload)["podLabels"] = map[string]interface{}{"team": "data"}
	spec(workload)["args"] = []interface{}{"--old"}

	got := spec(patch(t, trait, workload, map[string]interface{}{
		"tolerations": []interface{}{map[string]interface{}{"key": "dedicated", "operator": "Equal", "effect": "NoSchedule"}},
		"podLabels":   map[string]interface{}{"tier": "cache"},
		"args":        []interface{}{"--new"},
	}))
	assert.Len(t, got["tolerations"], 2, "both, merged by key: %v", got["tolerations"])
	assert.Equal(t, map[string]interface{}{"tier": "cache"}, got["podLabels"], "replaced whole")
	assert.Equal(t, []interface{}{"--new"}, got["args"], "replaced whole")

	untouched := spec(patch(t, trait, workload, map[string]interface{}{}))
	assert.Equal(t, map[string]interface{}{"team": "data"}, untouched["podLabels"], "an unset field patches nothing")
}

// A field the component sets is replaced by the trait that overrides it.
func TestTraitOverridesTheComponent(t *testing.T) {
	files, err := Generate(caches(t), "cache", []Choice{
		{Path: []string{"replicas"}, To: "both:cache-scaling"},
		{Path: []string{"version"}, To: "both:cache-scaling"},
	})
	require.NoError(t, err)
	component, trait := files[0], files[1]
	assert.Contains(t, component.Text, "replicas: *1 | int & >=1 & <=9", "the component still sets it")
	assert.Contains(t, component.Text, "version: string", "a required field may be overridden")
	assert.Contains(t, trait.Text, "replicas?: int & >=1 & <=9")
	assert.Contains(t, trait.Text, "// +patchStrategy=retainKeys")

	workload := renderComponent(t, component, map[string]interface{}{"version": "7.2", "replicas": 3})
	assert.EqualValues(t, 3, spec(workload)["replicas"])
	got := spec(patch(t, trait, workload, map[string]interface{}{"replicas": 7, "version": "7.4"}))
	assert.EqualValues(t, 7, got["replicas"], "overridden")
	assert.Equal(t, "7.4", got["version"])
	kept := spec(patch(t, trait, workload, map[string]interface{}{"replicas": 7}))
	assert.Equal(t, "7.2", kept["version"], "not given: the component's stays")
}
