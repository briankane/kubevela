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

const provenanceDef = `import "strings"

web: {
	type: "component"
	attributes: workload: definition: {apiVersion: "apps/v1", kind: "Deployment"}
}
template: {
	parameter: {
		image: string
		port:  *80 | int
		team:  string
	}
	_labels: team: parameter.team
	output: {
		apiVersion: "apps/v1"
		kind:       "Deployment"
		metadata: {
			name:   context.name
			labels: _labels
		}
		spec: {
			replicas: 1
			template: spec: containers: [{
				name:  strings.ToLower("Web")
				image: parameter.image
				ports: [{containerPort: parameter.port}]
			}]
		}
	}
	outputs: service: {
		apiVersion: "v1"
		kind:       "Service"
		metadata: name: "svc-" + context.name
	}
}
`

const provenanceValues = `parameter:
  image: nginx
  team: shop
`

func TestProvenance(t *testing.T) {
	at := func(object string, path ...interface{}) Origin {
		o, err := Provenance(context.Background(), Request{Path: "web.cue", Source: []byte(provenanceDef), Values: []byte(provenanceValues)}, Location{Object: object, Path: path})
		require.NoError(t, err)
		return o
	}
	image := at("output", "spec", "template", "spec", "containers", 0, "image")
	assert.Equal(t, "parameter", image.Kind)
	assert.Equal(t, "parameter.image", image.Ref)
	assert.True(t, image.Set, "the values set it")
	assert.Equal(t, 24, image.Line, "the field's line in the file, 0-based")
	assert.Equal(t, 8, image.RefLine, "the parameter's line")

	port := at("output", "spec", "template", "spec", "containers", 0, "ports", 0, "containerPort")
	assert.Equal(t, "parameter.port", port.Ref)
	assert.False(t, port.Set, "its default")

	name := at("output", "metadata", "name")
	assert.Equal(t, "context", name.Kind)
	assert.Equal(t, "context.name", name.Ref)

	team := at("output", "metadata", "labels", "team")
	assert.Equal(t, "parameter", team.Kind)
	assert.Equal(t, "parameter.team", team.Ref)
	assert.Equal(t, []string{"_labels.team"}, team.Via)

	replicas := at("output", "spec", "replicas")
	assert.Equal(t, "literal", replicas.Kind)
	assert.Equal(t, 21, replicas.Line)

	svc := at("outputs.service", "metadata", "name")
	assert.Equal(t, "expression", svc.Kind)
	assert.Equal(t, `"svc-" + context.name`, svc.Expression)
	assert.Equal(t, []string{"context.name"}, svc.Uses)

	call := at("output", "spec", "template", "spec", "containers", 0, "name")
	assert.Equal(t, "expression", call.Kind)
	assert.Equal(t, `strings.ToLower("Web")`, call.Expression)

	_, err := Provenance(context.Background(), Request{Path: "web.cue", Source: []byte(provenanceDef), Values: []byte(provenanceValues)}, Location{Object: "output", Path: []interface{}{"spec", "nope"}})
	assert.Error(t, err, "a field the template does not have")
}

const provenanceTrait = `scaler: {
	type: "trait"
	attributes: appliesToWorkloads: ["deployments.apps"]
}
template: {
	parameter: replicas: *1 | int
	patch: spec: replicas: parameter.replicas
}
`

// A trait's workload field is its patch's, or the workload's own.
func TestProvenanceOfAPatch(t *testing.T) {
	at := func(path ...interface{}) Origin {
		o, err := Provenance(context.Background(), Request{Path: "scaler.cue", Source: []byte(provenanceTrait), Values: []byte("parameter:\n  replicas: 3\n")}, Location{Object: "workload", Path: path})
		require.NoError(t, err)
		return o
	}
	replicas := at("spec", "replicas")
	assert.Equal(t, "parameter", replicas.Kind)
	assert.Equal(t, "parameter.replicas", replicas.Ref)
	assert.True(t, replicas.Set)
	assert.Equal(t, 6, replicas.Line)
	assert.Equal(t, "workload", at("metadata", "name").Kind)
}
