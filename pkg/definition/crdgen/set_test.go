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
	"os"
	"testing"

	"github.com/kubevela/pkg/cue/cuex"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"sigs.k8s.io/yaml"

	"github.com/oam-dev/kubevela/pkg/definition/analysis"
	"github.com/oam-dev/kubevela/pkg/definition/preview"
)

func TestMain(m *testing.M) {
	// The tests run without a cluster.
	cuex.EnableExternalPackageForDefaultCompiler = false
	cuex.EnableExternalPackageWatchForDefaultCompiler = false
	os.Exit(m.Run())
}

// cacheSet is the Cache CRD and two CRDs that belong to a Cache, with a ConfigMap among them.
func cacheSet(t *testing.T) []byte {
	t.Helper()
	related, err := os.ReadFile("testdata/related.yaml")
	require.NoError(t, err)
	return append(append(caches(t), []byte("---\n")...), related...)
}

func TestReadSet(t *testing.T) {
	members, err := ReadSet(cacheSet(t))
	require.NoError(t, err)
	var kinds []string
	refs := map[string][]Ref{}
	for _, m := range members {
		kinds = append(kinds, m.Kind)
		refs[m.Kind] = m.Refs
	}
	assert.Equal(t, []string{"Cache", "CacheBackup", "CacheUser"}, kinds, "the CRDs, in order")
	assert.Empty(t, refs["Cache"])
	assert.Equal(t, []Ref{{Path: []string{"cacheRef"}, Of: "Cache"}}, refs["CacheBackup"], "by its name")
	assert.Equal(t, []Ref{{Path: []string{"cluster"}, Of: "Cache"}}, refs["CacheUser"], "by its description")

	_, err = ReadSet([]byte("kind: ConfigMap\n"))
	assert.ErrorContains(t, err, "no CustomResourceDefinition")
}

// renderTrait is what a trait renders with parameter, for a component named sessions.
func renderTrait(t *testing.T, f File, parameter map[string]interface{}, observed map[string]interface{}) preview.Result {
	t.Helper()
	workload := map[string]interface{}{"apiVersion": "shop.example.com/v1", "kind": "Cache", "metadata": map[string]interface{}{"name": "sessions"}}
	v := map[string]interface{}{"parameter": parameter, "context": map[string]string{"name": "sessions"}, "workload": workload}
	if observed != nil {
		v["observed"] = map[string]interface{}{"outputs": observed}
	}
	values, err := yaml.Marshal(v)
	require.NoError(t, err)
	r := preview.Render(context.Background(), preview.Request{Path: f.Name, Source: []byte(f.Text), Values: values})
	require.Empty(t, r.Error, f.Text)
	return r
}

func objectsOf(t *testing.T, r preview.Result) map[string]map[string]interface{} {
	t.Helper()
	out := map[string]map[string]interface{}{}
	for _, o := range r.Objects {
		var m map[string]interface{}
		require.NoError(t, yaml.Unmarshal([]byte(o.YAML), &m))
		out[o.Name] = m
	}
	return out
}

func fileNamed(t *testing.T, files []File, name string) File {
	t.Helper()
	for _, f := range files {
		if f.Name == name {
			return f
		}
	}
	require.Failf(t, "no file", "%s is not among the files made", name)
	return File{}
}

func checksClean(t *testing.T, f File) {
	t.Helper()
	for _, d := range analysis.Analyze(f.Name, []byte(f.Text)).Diagnostics {
		assert.NotEqual(t, analysis.SeverityError, d.Severity, "%s: %s\n%s", f.Name, d.Message, f.Text)
	}
}

// A trait of a related CRD adds the object, pointing back at the component by its reference.
func TestRelatedTrait(t *testing.T) {
	files, err := GenerateSet(cacheSet(t), []Plan{
		{Kind: "Cache", Role: "component", Name: "cache"},
		{Kind: "CacheBackup", Role: "trait", Name: "cache-backup", Of: "Cache", Ref: []string{"cacheRef"}},
		{Kind: "CacheUser", Role: "skip"},
	})
	require.NoError(t, err)
	require.Len(t, files, 2)
	trait := fileNamed(t, files, "cache-backup.cue")
	checksClean(t, trait)
	assert.Contains(t, trait.Text, `appliesToWorkloads: ["caches.shop.example.com"]`)
	assert.NotContains(t, trait.Text, "cacheRef?:", "the reference is not a parameter")
	assert.Contains(t, trait.Text, "schedule: string", "required stays required")
	assert.Contains(t, trait.Text, "retention: *7 | int")

	r := renderTrait(t, trait, map[string]interface{}{"schedule": "0 3 * * *"}, nil)
	objects := objectsOf(t, r)
	backup := objects["outputs.backup"]
	require.NotNil(t, backup, "objects: %v", objects)
	assert.Equal(t, "CacheBackup", backup["kind"])
	assert.Equal(t, "shop.example.com/v1", backup["apiVersion"])
	assert.Equal(t, "sessions-backup", backup["metadata"].(map[string]interface{})["name"])
	assert.Equal(t, map[string]interface{}{
		"cacheRef":  map[string]interface{}{"name": "sessions"},
		"schedule":  "0 3 * * *",
		"retention": float64(7),
	}, backup["spec"])

	require.NotNil(t, r.Status)
	assert.Empty(t, r.Status.Error)
	assert.False(t, r.Status.Healthy, "no Ready condition yet")
	ready := map[string]interface{}{"backup": map[string]interface{}{"status": map[string]interface{}{"conditions": []interface{}{map[string]interface{}{"type": "Ready", "status": "True"}}}}}
	st := renderTrait(t, trait, map[string]interface{}{"schedule": "0 3 * * *"}, ready).Status
	assert.True(t, st.Healthy, "%+v", st)
}

// A trait may add one object per entry of a list, each named by its entry.
func TestRelatedTraitMany(t *testing.T) {
	files, err := GenerateSet(cacheSet(t), []Plan{
		{Kind: "CacheUser", Role: "trait", Name: "cache-users", Of: "Cache", Ref: []string{"cluster"}, Many: true},
	})
	require.NoError(t, err)
	trait := fileNamed(t, files, "cache-users.cue")
	checksClean(t, trait)
	assert.Contains(t, trait.Text, "users: [...{")

	r := renderTrait(t, trait, map[string]interface{}{"users": []interface{}{
		map[string]interface{}{"name": "orders"},
		map[string]interface{}{"name": "audit", "access": "readwrite"},
	}}, nil)
	objects := objectsOf(t, r)
	require.Len(t, objects, 2, "objects: %v", objects)
	audit := objects["outputs.user-audit"]
	require.NotNil(t, audit, "objects: %v", objects)
	assert.Equal(t, "audit", audit["metadata"].(map[string]interface{})["name"])
	assert.Equal(t, map[string]interface{}{"cluster": map[string]interface{}{"name": "sessions"}, "access": "readwrite"}, audit["spec"])
	assert.Equal(t, "read", objects["outputs.user-orders"]["spec"].(map[string]interface{})["access"], "the default")
}

// A related object may instead name the component in a label, and apply to any workload.
func TestRelatedTraitByLabel(t *testing.T) {
	files, err := GenerateSet(cacheSet(t), []Plan{
		{Kind: "CacheUser", Role: "trait", Name: "cache-user", Label: "shop.example.com/cache", Options: Options{Condition: "-"}},
	})
	require.NoError(t, err)
	trait := fileNamed(t, files, "cache-user.cue")
	checksClean(t, trait)
	assert.NotContains(t, trait.Text, "appliesToWorkloads")
	objects := objectsOf(t, renderTrait(t, trait, map[string]interface{}{"cluster": map[string]interface{}{"name": "other"}}, nil))
	user := objects["outputs.cache-user"]
	require.NotNil(t, user, "objects: %v", objects)
	assert.Equal(t, map[string]interface{}{"shop.example.com/cache": "sessions"}, user["metadata"].(map[string]interface{})["labels"])
}

func TestGenerateSetRefuses(t *testing.T) {
	_, err := GenerateSet(cacheSet(t), []Plan{{Kind: "CacheBackup", Role: "trait", Name: "b"}})
	assert.ErrorContains(t, err, "points at the component")
	_, err = GenerateSet(cacheSet(t), []Plan{{Kind: "Nope", Role: "component", Name: "n"}})
	assert.ErrorContains(t, err, "Nope")
	_, err = GenerateSet(cacheSet(t), []Plan{
		{Kind: "Cache", Role: "component", Name: "cache"},
		{Kind: "CacheUser", Role: "component", Name: "cache"},
	})
	assert.ErrorContains(t, err, "cache.cue")
}
