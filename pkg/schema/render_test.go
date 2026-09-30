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

package schema

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	uischema "github.com/oam-dev/kubevela/pkg/utils/schema"
)

func generate(t *testing.T, src string) *ParameterSchemas {
	t.Helper()
	ps, err := GenerateParameterSchemas(context.Background(), src)
	require.NoError(t, err)
	return ps
}

func uiParam(t *testing.T, ps uischema.UISchema, key string) *uischema.UIParameter {
	t.Helper()
	for _, p := range ps {
		if p.JSONKey == key {
			return p
		}
	}
	t.Fatalf("no UI parameter %q", key)
	return nil
}

func TestGenerateConditionalFields(t *testing.T) {
	ps := generate(t, `parameter: {
	type: "something" | "other" | "third"
	if type == "something" { key: string }
	if type != "something" { aDifferentKey: string }
}`)
	assert.Equal(t, "Select", uiParam(t, ps.UI, "type").UIType)
	assert.Equal(t, []uischema.Condition{{JSONKey: "type", Value: "something"}}, uiParam(t, ps.UI, "key").Conditions)
	assert.Equal(t, []uischema.Condition{{JSONKey: "type", Op: "in", Value: []any{"other", "third"}}},
		uiParam(t, ps.UI, "aDifferentKey").Conditions)

	assert.Equal(t, []string{"type"}, ps.OpenAPI.Required)
	require.NotNil(t, ps.OpenAPI.Discriminator)
	assert.Equal(t, "type", ps.OpenAPI.Discriminator.PropertyName)
	require.Len(t, ps.OpenAPI.OneOf, 3)
	assert.Equal(t, []string{"key"}, ps.OpenAPI.OneOf[0].Value.Required)
	assert.Equal(t, []string{"aDifferentKey"}, ps.OpenAPI.OneOf[1].Value.Required)
}

func TestGenerateNestedAndBoolConditions(t *testing.T) {
	ps := generate(t, `parameter: {
	enabled: *false | bool
	if enabled { port: *80 | int }
	storage: {
		kind: "pvc" | "emptyDir"
		if kind == "pvc" { size: string }
	}
}`)
	assert.Equal(t, []uischema.Condition{{JSONKey: "enabled", Value: true}}, uiParam(t, ps.UI, "port").Conditions)
	storage := uiParam(t, ps.UI, "storage")
	assert.Equal(t, []uischema.Condition{{JSONKey: "kind", Value: "pvc"}}, uiParam(t, storage.SubParameters, "size").Conditions)
}

func TestGenerateRecursiveDefinition(t *testing.T) {
	ps := generate(t, "#T: {n?: #T, v?: string}\nparameter: a: #T")
	n := ps.OpenAPI.Properties["a"].Value.Properties["n"].Value
	require.NotNil(t, n.AdditionalProperties.Has)
	assert.True(t, *n.AdditionalProperties.Has)
}

func TestGenerateShapesTheEncoderRejects(t *testing.T) {
	ps := generate(t, `
import "list"

#Port: {
	// +usage=Container port
	port:     int & >=1 & <=65535
	protocol: *"TCP" | "UDP"
}
parameter: {
	replicas: *1 | int & >=1
	level:    *2 | 1 | 3
	ne:       int & !=0
	either:   string | int
	ports:    *[{port: 80}] | [...#Port]
	items:    [...string] & list.MinItems(1)
}`)
	replicas := ps.OpenAPI.Properties["replicas"].Value
	assert.Equal(t, int64(1), replicas.Default)
	assert.Equal(t, 1.0, *replicas.Min)
	assert.NotContains(t, ps.OpenAPI.Required, "replicas", "a defaulted field need not be supplied")

	assert.Equal(t, "Select", uiParam(t, ps.UI, "level").UIType)
	assert.Equal(t, []any{int64(0)}, ps.OpenAPI.Properties["ne"].Value.Not.Value.Enum)
	for _, b := range ps.OpenAPI.Properties["either"].Value.OneOf {
		assert.NotNil(t, b.Value.Type)
	}
	ports := ps.OpenAPI.Properties["ports"].Value
	assert.Equal(t, []any{map[string]any{"port": int64(80)}}, ports.Default)
	assert.Equal(t, "Container port", ports.Items.Value.Properties["port"].Value.Description)
	assert.Equal(t, uint64(1), ps.OpenAPI.Properties["items"].Value.MinItems)
	assert.Equal(t, "Strings", uiParam(t, ps.UI, "items").UIType)
}

func TestGenerateDeclarationOrder(t *testing.T) {
	ps := generate(t, `parameter: {zeta: string, alpha?: int, mid: *true | bool}`)
	var keys []string
	for _, p := range ps.UI {
		keys = append(keys, p.JSONKey)
	}
	assert.Equal(t, []string{"zeta", "alpha", "mid"}, keys)
}
