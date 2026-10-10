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
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/oam-dev/kubevela/pkg/definition/analysis"
)

// builtins are KubeVela's own traits named.
func builtins(t *testing.T, names ...string) []Definition {
	t.Helper()
	var out []Definition
	for _, n := range names {
		found := false
		for _, d := range analysis.BuiltinDefinitions() {
			if d.Type == "trait" && d.Name == n {
				out = append(out, Definition{Name: d.Name, Source: d.Source, CUE: d.CUE})
				found = true
			}
		}
		require.True(t, found, "no built-in trait %s", n)
	}
	return out
}

// cachePersistence is a workspace trait setting a Cache's persistence.
const cachePersistence = `"cache-persistence": {
	type: "trait"
	attributes: {
		appliesToWorkloads: ["caches.shop.example.com"]
		podDisruptive: false
	}
}
template: {
	patch: spec: {
		if parameter.persistence != _|_ {
			persistence: {
				if parameter.persistence.enabled != _|_ {
					enabled: parameter.persistence.enabled
				}
				if parameter.persistence.size != _|_ {
					size: parameter.persistence.size
				}
			}
		}
	}
	parameter: persistence?: {enabled?: bool, size?: string}
}
`

func TestExistingTraits(t *testing.T) {
	defs := append(builtins(t, "scaler", "resource", "labels", "topologyspreadconstraints", "hpa"),
		Definition{Name: "cache-persistence", Source: "workspace", CUE: cachePersistence, Path: "/ws/cache-persistence.cue"},
		Definition{Name: "nope", Source: "workspace", CUE: `nope: {type: "trait", attributes: appliesToWorkloads: ["x"]}
template: patch: spec: nothing: parameter.n
`})
	members, err := ReadSetWith(caches(t), defs)
	require.NoError(t, err)
	byName := map[string]Existing{}
	for _, e := range members[0].Existing {
		byName[e.Name] = e
	}
	assert.Equal(t, Existing{Name: "scaler", Source: "built-in", Fields: [][]string{{"replicas"}}}, byName["scaler"], "it patches spec.replicas, which a Cache has")
	assert.Equal(t, Existing{Name: "cache-persistence", Source: "workspace", Fields: [][]string{{"persistence"}}, Applies: true, Editable: true}, byName["cache-persistence"], "the fields under one part where they part")
	for _, n := range []string{"resource", "topologyspreadconstraints", "nope"} {
		assert.NotContains(t, byName, n, "it patches what a Cache does not have")
	}
	assert.NotContains(t, byName, "labels", "it applies to every workload already")
	assert.NotContains(t, byName, "hpa", "it patches nothing")
}

// A trait this package made, patching the spec with its parameter, fits the CRD it was made from.
func TestExistingTraitMadeHere(t *testing.T) {
	files, err := Generate(caches(t), "cache", []Choice{{Path: []string{"persistence"}, To: "trait:cache-persistence"}})
	require.NoError(t, err)
	trait := fileNamed(t, files, "cache-persistence.cue")
	require.Contains(t, trait.Text, "patch: spec: parameter")
	members, err := ReadSetWith(caches(t), []Definition{{Name: "cache-persistence", Source: "workspace", CUE: trait.Text}})
	require.NoError(t, err)
	require.Len(t, members[0].Existing, 1)
	assert.Equal(t, [][]string{{"persistence"}}, members[0].Existing[0].Fields)
}

func TestUseExistingTrait(t *testing.T) {
	scaling := Definition{Name: "scaling", Source: "workspace", Path: "/ws/scaling.cue", CUE: `scaling: {
	type: "trait"
	attributes: {
		// Workloads with a replicas field.
		appliesToWorkloads: ["deployments.apps"]
	}
}
template: {
	patch: spec: replicas: parameter.replicas
	parameter: replicas: *1 | int
}
`}
	defs := append(builtins(t, "scaler"), scaling)
	plans := []Plan{{Kind: "Cache", Role: "component", Name: "cache", Choices: []Choice{{Path: []string{"replicas"}, To: "existing:scaling"}}}}
	files, err := GenerateSetWith(caches(t), plans, defs)
	require.NoError(t, err)
	require.Len(t, files, 2)
	assert.NotContains(t, files[0].Text, "replicas", "left out of the component")
	assert.Equal(t, "/ws/scaling.cue", files[1].Path)
	assert.Contains(t, files[1].Text, `appliesToWorkloads: ["deployments.apps", "caches.shop.example.com"]`)
	assert.Contains(t, files[1].Text, "// Workloads with a replicas field.", "the rest of the file as it was")

	plans[0].Choices[0].To = "existing:scaler"
	_, err = GenerateSetWith(caches(t), plans, defs)
	assert.ErrorContains(t, err, "add it to scaler's appliesToWorkloads", "a trait not in the workspace is never edited")
}

// cacheAndQueue are the Cache and Queue CRDs, which share their persistence.
func cacheAndQueue(t *testing.T) []byte {
	t.Helper()
	queues, err := os.ReadFile("testdata/queues.yaml")
	require.NoError(t, err)
	return append(append(caches(t), []byte("---\n")...), queues...)
}

func TestSharedFields(t *testing.T) {
	members, err := ReadSet(cacheAndQueue(t))
	require.NoError(t, err)
	assert.Equal(t, []Shared{{Path: []string{"persistence"}, With: []string{"Queue"}}}, members[0].Shared, "replicas is an int in one and a string in the other")
	assert.Equal(t, []Shared{{Path: []string{"persistence"}, With: []string{"Cache"}}}, members[1].Shared)
}

func TestSharedTrait(t *testing.T) {
	to := []Choice{{Path: []string{"persistence"}, To: "trait:persistence"}}
	files, err := GenerateSet(cacheAndQueue(t), []Plan{
		{Kind: "Cache", Role: "component", Name: "cache", Choices: to},
		{Kind: "Queue", Role: "component", Name: "queue", Choices: to},
	})
	require.NoError(t, err)
	var names []string
	for _, f := range files {
		names = append(names, f.Name)
	}
	assert.Equal(t, []string{"cache.cue", "persistence.cue", "queue.cue"}, names)
	trait := fileNamed(t, files, "persistence.cue")
	checksClean(t, trait)
	assert.Contains(t, trait.Text, `appliesToWorkloads: ["caches.shop.example.com", "queues.shop.example.com"]`)
	assert.Contains(t, trait.Text, "Sets part of the spec of a Cache or a Queue.")

	_, err = GenerateSet(cacheAndQueue(t), []Plan{
		{Kind: "Cache", Role: "component", Name: "cache", Choices: []Choice{{Path: []string{"replicas"}, To: "trait:scale"}}},
		{Kind: "Queue", Role: "component", Name: "queue", Choices: []Choice{{Path: []string{"replicas"}, To: "trait:scale"}}},
	})
	assert.ErrorContains(t, err, "trait scale would set different fields of a Cache and a Queue")
}
