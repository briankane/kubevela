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
	"encoding/json"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/oam-dev/kubevela/pkg/definition/analysis"
	velaschema "github.com/oam-dev/kubevela/pkg/schema"
)

// parameterSchemaOf is the OpenAPI schema KubeVela publishes for a made
// definition's parameter, as the controller and the designer read it.
func parameterSchemaOf(t *testing.T, f File) map[string]interface{} {
	t.Helper()
	tmpl, ok := analysis.TemplateSource(f.Name, []byte(f.Text))
	require.True(t, ok, f.Text)
	s, err := velaschema.ParsePropertiesToSchema(context.Background(), tmpl.Body)
	require.NoError(t, err, f.Text)
	b, err := s.MarshalJSON()
	require.NoError(t, err)
	var out map[string]interface{}
	require.NoError(t, json.Unmarshal(b, &out))
	return out
}

func schemaAt(t *testing.T, s map[string]interface{}, path ...string) map[string]interface{} {
	t.Helper()
	for _, p := range path {
		next, ok := s[p].(map[string]interface{})
		require.True(t, ok, "%s in %v", p, s)
		s = next
	}
	return s
}

// Each kind of field a CRD's schema has gives a parameter whose published
// schema says what the CRD does: its type, bounds, enum and default.
func TestEachKindHasASchema(t *testing.T) {
	src, err := os.ReadFile("testdata/kinds.yaml")
	require.NoError(t, err)
	files, err := Generate(src, "widget", []Choice{{Path: []string{"backend"}, To: "trait:widget-backend"}, {Path: []string{"ratio"}, To: "both:widget-ratio"}})
	require.NoError(t, err)
	require.Len(t, files, 3)
	for _, f := range files {
		checksClean(t, f)
	}
	p := schemaAt(t, parameterSchemaOf(t, files[0]), "properties")
	for name, want := range map[string]map[string]interface{}{
		"replicas": {"type": "integer", "minimum": float64(1), "maximum": float64(9), "default": float64(1)},
		"weight":   {"type": "integer", "minimum": float64(0), "exclusiveMinimum": true},
		"ratio":    {"type": "number", "minimum": float64(0), "maximum": float64(1), "default": 0.5},
		"scale":    {"type": "number", "default": 1.5},
		"mode":     {"type": "string", "default": "safe"},
		"size":     {"type": "string", "pattern": "^[0-9]+Gi$", "default": "1Gi"},
		"name":     {"type": "string", "minLength": float64(3), "maxLength": float64(9), "default": "abc"},
		"code":     {"type": "string", "minLength": float64(2)},
		"enabled":  {"type": "boolean", "default": true},
	} {
		got := schemaAt(t, p, name)
		for k, v := range want {
			assert.Equal(t, v, got[k], "%s.%s: %v", name, k, got)
		}
	}
	assert.ElementsMatch(t, []interface{}{"fast", "safe"}, schemaAt(t, p, "mode")["enum"])
	assert.NotNil(t, schemaAt(t, p, "port")["default"], "int or string keeps its default: %v", schemaAt(t, p, "port"))
	assert.Equal(t, "string", schemaAt(t, p, "labels", "additionalProperties")["type"])
	assert.Equal(t, "integer", schemaAt(t, p, "limits", "additionalProperties")["type"])
	assert.Equal(t, "object", schemaAt(t, p, "config")["type"])
	assert.Equal(t, "string", schemaAt(t, p, "args", "items")["type"])
	assert.Equal(t, float64(65535), schemaAt(t, p, "ports", "items")["maximum"])
	rule := schemaAt(t, p, "rules", "items")
	assert.Contains(t, rule["required"], "host")
	assert.Equal(t, float64(1), schemaAt(t, rule, "properties", "weight")["default"])

	trait := schemaAt(t, parameterSchemaOf(t, fileNamed(t, files, "widget-backend.cue")), "properties", "backend", "properties")
	assert.Equal(t, float64(30), schemaAt(t, trait, "timeout")["default"])
	assert.Equal(t, "1.2", schemaAt(t, trait, "tls", "properties", "minVersion")["default"])
	override := schemaAt(t, parameterSchemaOf(t, fileNamed(t, files, "widget-ratio.cue")), "properties", "ratio")
	assert.Equal(t, float64(1), override["maximum"], "an override keeps the bounds, without the default")
}
