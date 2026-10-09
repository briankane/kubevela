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
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/oam-dev/kubevela/pkg/definition/analysis"
)

func caches(t *testing.T) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/caches.yaml")
	require.NoError(t, err)
	return b
}

func TestRead(t *testing.T) {
	info, err := Read(caches(t))
	require.NoError(t, err)
	assert.Equal(t, "Cache", info.Kind)
	assert.Equal(t, "shop.example.com", info.Group)
	assert.Equal(t, "v1", info.Version, "the storage version")
	assert.Equal(t, "caches", info.Plural)
	assert.True(t, info.Ready, "status has conditions")

	names := map[string]Field{}
	for _, f := range info.Fields {
		names[f.Name] = f
	}
	assert.Equal(t, []string{"version", "engine", "replicas", "persistence", "resources", "tolerations", "podLabels", "args", "config", "name"}, fieldNames(info.Fields), "in the schema's order")
	assert.True(t, names["version"].Required)
	assert.Equal(t, `"redis" | "valkey"`, names["engine"].Type)
	assert.Equal(t, `"redis"`, names["engine"].Default)
	assert.Equal(t, "Replicas of the cache", names["replicas"].Description)
	assert.Equal(t, []string{"enabled", "size"}, fieldNames(names["persistence"].Children))
	assert.Equal(t, []string{"persistence", "size"}, names["persistence"].Children[1].Path)
	assert.Equal(t, "[...{…}]", names["tolerations"].Type, "a list of structs")
	assert.NotEmpty(t, names["resources"].Hint, "often a trait")
	assert.NotEmpty(t, names["tolerations"].Hint)
	assert.Empty(t, names["engine"].Hint)
}

func fieldNames(fs []Field) []string {
	var out []string
	for _, f := range fs {
		out = append(out, f.Name)
	}
	return out
}

func TestGenerateAComponent(t *testing.T) {
	files, err := Generate(caches(t), "cache", nil)
	require.NoError(t, err)
	require.Len(t, files, 1)
	c := files[0]
	assert.Equal(t, "cache.cue", c.Name)
	for _, want := range []string{
		`"cache": {`,
		`type:        "component"`,
		`workload: definition: {`,
		`apiVersion: "shop.example.com/v1"`,
		`kind:       "Cache"`,
		"metadata: name: context.name",
		"spec: parameter",
		"// +usage=Redis version to run\n",
		"version: string\n",
		`engine: *"redis" | "valkey"`,
		"replicas: *1 | int & >=1 & <=9",
		`size?: string & =~"^[0-9]+Gi$"`,
		"limits?: [string]: int | string",
		`operator?: "Exists" | "Equal"`,
		"config?: {...}",
		"name?: strings.MinRunes(3) & strings.MaxRunes(40)",
		`import "strings"`,
		"healthPolicy:",
		`c.type == "Ready"`,
	} {
		assert.Contains(t, c.Text, want)
	}
	res := analysis.Analyze("cache.cue", []byte(c.Text))
	for _, d := range res.Diagnostics {
		assert.NotEqual(t, analysis.SeverityError, d.Severity, "the component checks clean: %s", d.Message)
	}
}

func TestGenerateTraits(t *testing.T) {
	files, err := Generate(caches(t), "cache", []Choice{
		{Path: []string{"resources"}, To: "trait:cache-scheduling"},
		{Path: []string{"tolerations"}, To: "trait:cache-scheduling"},
		{Path: []string{"persistence"}, To: "trait:cache-persistence"},
		{Path: []string{"persistence", "enabled"}, To: "omit"},
		{Path: []string{"config"}, To: "omit"},
	})
	require.NoError(t, err)
	require.Len(t, files, 3)
	component, persistence, scheduling := files[0], files[1], files[2]
	assert.Equal(t, "cache-persistence.cue", persistence.Name)
	assert.Equal(t, "cache-scheduling.cue", scheduling.Name)

	assert.NotContains(t, component.Text, "resources")
	assert.NotContains(t, component.Text, "persistence")
	assert.NotContains(t, component.Text, "config")
	assert.Contains(t, component.Text, "version: string")

	for _, want := range []string{
		`type:        "trait"`,
		`appliesToWorkloads: ["caches.shop.example.com"]`,
		"// +patchKey=key\n\t\t\ttolerations: parameter.tolerations",
		"limits?: [string]: int | string",
		`operator?: "Exists" | "Equal"`,
	} {
		assert.Contains(t, scheduling.Text, want)
	}
	assert.Contains(t, persistence.Text, "patch: spec: parameter", "nothing in it needs a marker")
	assert.Contains(t, persistence.Text, `size?: string & =~"^[0-9]+Gi$"`)
	assert.NotContains(t, persistence.Text, "enabled", "left out")
	assert.True(t, strings.Contains(persistence.Text, "persistence?: {"), "nested as in spec")
	for _, f := range files {
		for _, d := range analysis.Analyze(f.Name, []byte(f.Text)).Diagnostics {
			assert.NotEqual(t, analysis.SeverityError, d.Severity, "%s checks clean: %s", f.Name, d.Message)
		}
	}
}

func TestRequiredStaysInTheComponent(t *testing.T) {
	_, err := Generate(caches(t), "cache", []Choice{{Path: []string{"version"}, To: "trait:cache-version"}})
	assert.ErrorContains(t, err, "version is required")
}

func TestHealthCondition(t *testing.T) {
	files, err := GenerateWith(caches(t), "cache", nil, Options{Condition: "Programmed"})
	require.NoError(t, err)
	assert.Contains(t, files[0].Text, `c.type == "Programmed"`)
	assert.NotContains(t, files[0].Text, `"Ready"`)

	files, err = GenerateWith(caches(t), "cache", nil, Options{Condition: "-"})
	require.NoError(t, err)
	assert.NotContains(t, files[0].Text, "healthPolicy", "none asked for")
}
