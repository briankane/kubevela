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

const wholesaleSpec = `"cache": {
	type: "component"
	attributes: workload: definition: {
		apiVersion: "shop.example.com/v1"
		kind:       "Cache"
	}
}
template: {
	output: {
		apiVersion: "shop.example.com/v1"
		kind:       "Cache"
		metadata: name: context.name
		spec: parameter
	}
	parameter: {
		// +usage=Redis version to run
		version: string
		// +usage=Keeps the data across restarts
		persistence?: {
			// +usage=Size of the volume, in Gi
			size?: string & =~"^[0-9]+Gi$"
		}
		replicas: *1 | int & >=1 & <=9
	}
}
`

const wiredFields = `web: {
	type: "component"
	attributes: workload: type: "deployments.apps"
}
template: {
	output: {
		apiVersion: "apps/v1"
		kind:       "Deployment"
		spec: {
			replicas: parameter.replicas
			template: spec: containers: [{image: parameter.image, resources: parameter.resources}]
			if parameter.paused != _|_ {
				paused: parameter.paused
			}
		}
	}
	parameter: {
		image: string
		// +usage=Replicas of the Deployment
		replicas: *1 | int
		resources?: {...}
		paused?: bool
	}
}
`

func TestMoveToTraitOffered(t *testing.T) {
	at := func(src, marker string) (string, bool) {
		return MoveToTraitAt("c.cue", src, strings.Index(src, marker)+1)
	}
	p, ok := at(wholesaleSpec, "persistence?:")
	assert.True(t, ok)
	assert.Equal(t, "persistence", p)
	_, ok = at(wholesaleSpec, "version: string")
	assert.False(t, ok, "a required parameter: a trait could leave it unset")
	_, ok = at(wholesaleSpec, "size?:")
	assert.False(t, ok, "only a top-level parameter")
	_, ok = at(wholesaleSpec, "spec: parameter")
	assert.False(t, ok, "not a parameter")
}

func TestMoveToTraitWholesale(t *testing.T) {
	edits, trait, err := MoveToTrait("cache.cue", wholesaleSpec, "persistence", "cache-persistence")
	require.NoError(t, err)
	got := applyRangeEdits(wholesaleSpec, edits)
	assert.NotContains(t, got, "persistence")
	assert.NotContains(t, got, "Keeps the data")
	assert.Contains(t, got, "\t\t// +usage=Redis version to run\n\t\tversion: string\n\t\treplicas: *1 | int & >=1 & <=9\n")
	for _, want := range []string{
		`"cache-persistence": {`,
		`appliesToWorkloads: ["caches.shop.example.com"]`,
		"patch: spec: parameter",
		"// +usage=Keeps the data across restarts\n\t\tpersistence?: {",
		`size?: string & =~"^[0-9]+Gi$"`,
	} {
		assert.Contains(t, trait, want)
	}
	for _, src := range []string{got, trait} {
		for _, d := range Analyze("x.cue", []byte(src)).Diagnostics {
			assert.NotEqual(t, SeverityError, d.Severity, "checks clean: %s", d.Message)
		}
	}
}

func TestMoveToTraitWired(t *testing.T) {
	edits, trait, err := MoveToTrait("web.cue", wiredFields, "replicas", "web-replicas")
	require.NoError(t, err)
	got := applyRangeEdits(wiredFields, edits)
	assert.NotContains(t, got, "replicas")
	assert.Contains(t, trait, `appliesToWorkloads: ["deployments.apps"]`)
	assert.Contains(t, trait, "patch: spec: replicas: parameter.replicas")
	assert.Contains(t, trait, "// +usage=Replicas of the Deployment\n\t\treplicas: *1 | int")

	_, _, err = MoveToTrait("web.cue", wiredFields, "resources", "web-resources")
	assert.ErrorContains(t, err, "in a list", "a patch cannot say which element")
	_, _, err = MoveToTrait("web.cue", wiredFields, "paused", "web-paused")
	assert.ErrorContains(t, err, "conditionally")
	_, _, err = MoveToTrait("web.cue", wiredFields, "image", "web-image")
	assert.ErrorContains(t, err, "required")
}

// The trait imports what the moved parameter uses, and the component drops an import nothing else uses.
func TestMoveToTraitImports(t *testing.T) {
	src := "import \"strings\"\n\n" + strings.Replace(wholesaleSpec, "replicas: *1 | int & >=1 & <=9", "replicas: *1 | int & >=1 & <=9\n\t\tname?: strings.MinRunes(3)", 1)
	edits, trait, err := MoveToTrait("cache.cue", src, "name", "cache-name")
	require.NoError(t, err)
	assert.Contains(t, trait, "import \"strings\"\n")
	got := applyRangeEdits(src, edits)
	assert.NotContains(t, got, "import \"strings\"", "nothing else in the component uses it")
	for _, s := range []string{got, trait} {
		for _, d := range Analyze("x.cue", []byte(s)).Diagnostics {
			assert.NotEqual(t, SeverityError, d.Severity, "checks clean: %s", d.Message)
		}
	}

	kept := strings.Replace(src, "version: string", "version: strings.MinRunes(1)", 1)
	edits, _, err = MoveToTrait("cache.cue", kept, "name", "cache-name")
	require.NoError(t, err)
	assert.Contains(t, applyRangeEdits(kept, edits), "import \"strings\"", "version still uses it")
}
