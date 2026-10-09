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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const exploreDef = `worker: {
	type: "component"
	attributes: workload: definition: {apiVersion: "apps/v1", kind: "Deployment"}
}
template: {
	parameter: {
		image:    string
		tier:     *"web" | "worker" | "batch"
		replicas: *1 | int & >=1 & <=10
		debug:    *false | bool
		note?:    string
		limits: cpu: *"500m" | string
		probe?: {
			path:  string
			host?: string
		}
	}
	output: {
		apiVersion: "apps/v1"
		kind:       "Deployment"
		metadata: {
			name: context.name
			labels: tier: parameter.tier
			if parameter.note != _|_ {
				annotations: note: parameter.note
			}
		}
		spec: {
			replicas: parameter.replicas
			if parameter.tier == "batch" {
				// A typo the base never reaches.
				template: spec: containers: [{image: parameter.imag}]
			}
			template: spec: containers: [{image: parameter.image, resources: limits: cpu: parameter.limits.cpu}]
		}
	}
}
`

func TestExplore(t *testing.T) {
	e, err := Explore(context.Background(), Request{Path: "worker.cue", Source: []byte(exploreDef), Values: []byte("parameter:\n  image: nginx\n  replicas: 3\n")})
	require.NoError(t, err)
	assert.Empty(t, e.Base.Error)
	require.NotEmpty(t, e.Base.Objects)

	byName := map[string]Variant{}
	var names []string
	for _, v := range e.Variants {
		byName[v.Name] = v
		names = append(names, v.Name)
	}
	assert.ElementsMatch(t, []string{
		`tier: "worker"`,
		`tier: "batch"`,
		"replicas: 1 (its minimum)",
		"replicas: 10 (its maximum)",
		"replicas unset (its default)",
		"debug: true",
		`note: "example"`,
	}, names)

	assert.Contains(t, byName[`tier: "batch"`].Error, "imag", "the branch the base never reaches")
	assert.Empty(t, byName[`tier: "worker"`].Error)
	assert.Contains(t, byName["replicas: 10 (its maximum)"].Values, "replicas: 10")
	assert.Contains(t, byName[`note: "example"`].Objects[0].YAML, "note: example")
	assert.NotContains(t, byName["replicas unset (its default)"].Values, "replicas")
}

func TestExploreCapsVariants(t *testing.T) {
	e, err := Explore(context.Background(), Request{Path: "worker.cue", Source: []byte(exploreDef), Values: []byte("parameter:\n  image: nginx\n")})
	require.NoError(t, err)
	assert.LessOrEqual(t, len(e.Variants), maxVariants)
	assert.False(t, e.Truncated)
}

// Unsetting a parameter the values set to its default would change nothing, so it is not tried.
func TestExploreSkipsUnsettingADefault(t *testing.T) {
	e, err := Explore(context.Background(), Request{Path: "worker.cue", Source: []byte(exploreDef), Values: []byte("parameter:\n  image: nginx\n  tier: web\n  replicas: 3\n")})
	require.NoError(t, err)
	for _, v := range e.Variants {
		assert.NotEqual(t, "tier unset (its default)", v.Name)
	}
	var names []string
	for _, v := range e.Variants {
		names = append(names, v.Name)
	}
	assert.Contains(t, names, "replicas unset (its default)", "3 is not the default")
}
