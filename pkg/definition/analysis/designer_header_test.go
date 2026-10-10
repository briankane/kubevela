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
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func fieldAt(fs []HeaderField, path string) (HeaderField, bool) {
	for _, f := range fs {
		if strings.Join(f.Path, ".") == path {
			return f, true
		}
	}
	return HeaderField{}, false
}

func TestHeaderFields(t *testing.T) {
	trait := HeaderFields("trait")
	for path, kind := range map[string]string{
		"description":                        "string",
		"extends":                            "string",
		"attributes.appliesToWorkloads":      "strings",
		"attributes.podDisruptive":           "bool",
		"attributes.stage":                   "enum",
		"attributes.status.healthPolicy":     "cue",
		"attributes.restrictions.namespaces": "strings",
		"attributes.restrictions.quota":      "other",
		"labels":                             "map",
	} {
		f, ok := fieldAt(trait, path)
		if assert.True(t, ok, path) {
			assert.Equal(t, kind, f.Kind, path)
		}
	}
	f, _ := fieldAt(trait, "attributes.appliesToWorkloads")
	assert.Equal(t, "workloads", f.Suggest)
	assert.Equal(t, "Applies to workloads", f.Label)
	assert.NotEmpty(t, f.Doc, "from the CRD")
	_, ok := fieldAt(trait, "attributes.extends")
	assert.False(t, ok, "shown at the top level")

	component := HeaderFields("component")
	_, ok = fieldAt(component, "attributes.workload.definition.kind")
	assert.True(t, ok)
	_, ok = fieldAt(component, "attributes.appliesToWorkloads")
	assert.False(t, ok, "a trait's")
	policy := HeaderFields("policy")
	f, _ = fieldAt(policy, "attributes.priority")
	assert.Equal(t, "int", f.Kind)
}

const designedTrait = `// The trait's own comment.
"cache-backup": {
	type:        "trait"
	description: "Adds a CacheBackup."
	attributes: appliesToWorkloads: ["caches.shop.example.com"]
	labels: {
		// Hidden until it is ready.
		"custom.definition.oam.dev/ui-hidden": "true"
	}
}
template: {
	patch: spec: schedule: parameter.schedule
	parameter: {
		// +usage=When to back up
		schedule: string
	}
}
`

func valueAt(h Header, path string) HeaderValue {
	for _, v := range h.Fields {
		if strings.Join(v.Path, ".") == path {
			return v
		}
	}
	return HeaderValue{}
}

func TestReadHeader(t *testing.T) {
	h, ok := ReadHeader("t.cue", []byte(designedTrait))
	require.True(t, ok)
	assert.Equal(t, "cache-backup", h.Name)
	assert.Equal(t, "trait", h.Type)
	assert.Equal(t, "Adds a CacheBackup.", valueAt(h, "description").Value)
	assert.Equal(t, []string{"caches.shop.example.com"}, valueAt(h, "attributes.appliesToWorkloads").Value)
	assert.Equal(t, map[string]string{"custom.definition.oam.dev/ui-hidden": "true"}, valueAt(h, "labels").Value)
	assert.False(t, valueAt(h, "attributes.podDisruptive").Set)
	require.NotNil(t, valueAt(h, "description").Range)
	assert.Equal(t, 4, valueAt(h, "description").Range.Start.Line)

	h, _ = ReadHeader("t.cue", []byte(strings.Replace(designedTrait, `description: "Adds a CacheBackup."`, `description: "Adds " + "one."`, 1)))
	assert.True(t, valueAt(h, "description").Computed, "not a literal")
}

// apply is src with an edit made.
func apply(t *testing.T, src string, e RangeEdit) string {
	t.Helper()
	lines := strings.SplitAfter(src, "\n")
	offset := func(p Position) int {
		n := 0
		for i := 0; i < p.Line-1; i++ {
			n += len(lines[i])
		}
		return n + p.Column - 1
	}
	return src[:offset(e.Range.Start)] + e.NewText + src[offset(e.Range.End):]
}

func TestEditHeader(t *testing.T) {
	e, err := EditHeader("t.cue", []byte(designedTrait), []string{"attributes", "podDisruptive"}, true)
	require.NoError(t, err)
	out := apply(t, designedTrait, e)
	assert.Contains(t, out, "attributes: {\n\t\tappliesToWorkloads: [\"caches.shop.example.com\"]\n\t\tpodDisruptive: true\n\t}", "a: b: c is braced once it holds two")
	assert.Contains(t, out, "// The trait's own comment.\n\"cache-backup\": {", "the comment above is kept")
	assert.Contains(t, out, "// Hidden until it is ready.", "comments inside are kept")
	assert.Contains(t, out, "template: {\n\tpatch: spec: schedule: parameter.schedule\n", "the template is untouched")
	h, ok := ReadHeader("t.cue", []byte(out))
	require.True(t, ok)
	assert.Equal(t, true, valueAt(h, "attributes.podDisruptive").Value)

	e, err = EditHeader("t.cue", []byte(out), []string{"attributes", "appliesToWorkloads"}, []interface{}{"caches.shop.example.com", "queues.shop.example.com"})
	require.NoError(t, err)
	out = apply(t, out, e)
	assert.Contains(t, out, `appliesToWorkloads: ["caches.shop.example.com", "queues.shop.example.com"]`)

	e, err = EditHeader("t.cue", []byte(out), []string{"attributes", "restrictions", "namespaces"}, []string{"tenant-*"})
	require.NoError(t, err)
	out = apply(t, out, e)
	h, _ = ReadHeader("t.cue", []byte(out))
	assert.Equal(t, []string{"tenant-*"}, valueAt(h, "attributes.restrictions.namespaces").Value)

	e, err = EditHeader("t.cue", []byte(out), []string{"attributes", "restrictions", "namespaces"}, nil)
	require.NoError(t, err)
	out = apply(t, out, e)
	assert.NotContains(t, out, "restrictions", "an emptied struct goes too")

	e, err = EditHeader("t.cue", []byte(out), []string{"annotations"}, map[string]interface{}{"definition.oam.dev/icon": "db"})
	require.NoError(t, err)
	out = apply(t, out, e)
	assert.Contains(t, out, "\"definition.oam.dev/icon\": \"db\"")
	h, _ = ReadHeader("t.cue", []byte(out))
	assert.Equal(t, map[string]string{"definition.oam.dev/icon": "db"}, valueAt(h, "annotations").Value)
	assert.Empty(t, Analyze("t.cue", []byte(out)).Diagnostics, out)

	_, err = EditHeader("t.cue", []byte(out), []string{"attributes", "status", "healthPolicy"}, "x")
	assert.ErrorContains(t, err, "not a header field")
}
