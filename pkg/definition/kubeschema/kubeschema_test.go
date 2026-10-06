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

package kubeschema

import (
	"testing"

	"cuelang.org/go/cue"
	"cuelang.org/go/cue/cuecontext"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// widgets is an OpenAPI v3 document as /openapi/v3/apis/example.com/v1 serves
// it, using each feature the conversion has to handle.
const widgets = `{
  "openapi": "3.0.0",
  "components": {"schemas": {
    "com.example.v1.Widget": {
      "type": "object",
      "description": "A Widget.",
      "required": ["spec"],
      "properties": {
        "apiVersion": {"type": "string"},
        "kind": {"type": "string"},
        "metadata": {"allOf": [{"$ref": "#/components/schemas/io.k8s.apimachinery.pkg.apis.meta.v1.ObjectMeta"}], "default": {}},
        "spec": {"allOf": [{"$ref": "#/components/schemas/com.example.v1.WidgetSpec"}]}
      },
      "x-kubernetes-group-version-kind": [{"group": "example.com", "version": "v1", "kind": "Widget"}]
    },
    "com.example.v1.WidgetSpec": {
      "type": "object",
      "required": ["size"],
      "properties": {
        "size": {"type": "integer", "format": "int32"},
        "policy": {"type": "string", "enum": ["Always", "Never"]},
        "port": {"x-kubernetes-int-or-string": true},
        "quantity": {"oneOf": [{"type": "string"}, {"type": "number"}]},
        "labels": {"type": "object", "additionalProperties": {"type": "string"}},
        "raw": {"type": "object", "x-kubernetes-preserve-unknown-fields": true},
        "note": {"type": "string", "nullable": true},
        "children": {"type": "array", "items": {"$ref": "#/components/schemas/com.example.v1.WidgetSpec"}}
      }
    },
    "io.k8s.apimachinery.pkg.apis.meta.v1.ObjectMeta": {
      "type": "object",
      "properties": {"name": {"type": "string"}, "labels": {"type": "object", "additionalProperties": {"type": "string"}}}
    }
  }}
}`

const gadgetCRD = `apiVersion: apiextensions.k8s.io/v1
kind: CustomResourceDefinition
metadata: {name: gadgets.example.com}
spec:
  group: example.com
  names: {kind: Gadget, plural: gadgets}
  scope: Namespaced
  versions:
  - name: v1alpha1
    served: true
    storage: true
    schema:
      openAPIV3Schema:
        type: object
        properties:
          spec:
            type: object
            required: [color]
            properties:
              color: {type: string}
`

var widget = GVK{Group: "example.com", Version: "v1", Kind: "Widget"}

// unify checks value against the kind's schema the way the analysis does.
func unify(t *testing.T, s *Schemas, gvk GVK, value string) error {
	t.Helper()
	src, ok := s.CUE(gvk)
	require.True(t, ok, "no schema for %v", gvk)
	v := cuecontext.New().CompileString(src + "\nx: " + Root(gvk) + " & " + value)
	require.NoError(t, v.LookupPath(cue.ParsePath(Root(gvk))).Err(), "the schema itself compiles")
	return v.LookupPath(cue.ParsePath("x")).Validate()
}

func TestDocumentKinds(t *testing.T) {
	s := New()
	require.NoError(t, s.AddDocument([]byte(widgets)))
	assert.True(t, s.Has(widget))
	assert.False(t, s.Has(GVK{Group: "example.com", Version: "v1", Kind: "WidgetSpec"}), "only kinds are indexed")

	for name, tc := range map[string]struct {
		value string
		bad   string
	}{
		"a valid widget":          {value: `{apiVersion: "example.com/v1", kind: "Widget", metadata: name: "w", spec: {size: 3, policy: "Always", port: 80, quantity: "1Gi", labels: a: "b", raw: anything: 1, note: null, children: [{size: 1}]}}`},
		"int-or-string as string": {value: `{spec: {size: 1, port: "http"}}`},
		"number quantity":         {value: `{spec: {size: 1, quantity: 2}}`},
		"misspelt field":          {value: `{spec: {size: 1, sise: 2}}`, bad: "sise"},
		"wrong type":              {value: `{spec: {size: "big"}}`, bad: "size"},
		"value not in the enum":   {value: `{spec: {size: 1, policy: "Sometimes"}}`, bad: "policy"},
		"misspelt in a ref":       {value: `{metadata: nmae: "w"}`, bad: "nmae"},
		"misspelt in a recursion": {value: `{spec: {size: 1, children: [{size: 1, sise: 2}]}}`, bad: "sise"},
		"non-string label":        {value: `{spec: {size: 1, labels: a: 1}}`, bad: "labels"},
	} {
		t.Run(name, func(t *testing.T) {
			err := unify(t, s, widget, tc.value)
			if tc.bad == "" {
				assert.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.bad)
		})
	}
}

// An abstract value is not an error: a template's fields are filled later.
func TestRequiredFieldsAreOnlyEnforcedWhenConcrete(t *testing.T) {
	s := New()
	require.NoError(t, s.AddDocument([]byte(widgets)))
	assert.NoError(t, unify(t, s, widget, `{spec: {policy: "Never"}}`))

	src, _ := s.CUE(widget)
	v := cuecontext.New().CompileString(src + "\nx: " + Root(widget) + " & {spec: {policy: \"Never\"}}")
	err := v.LookupPath(cue.ParsePath("x")).Validate(cue.Concrete(true))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "size")
}

func TestCRDKinds(t *testing.T) {
	s := New()
	require.NoError(t, s.AddCRD([]byte(gadgetCRD)))
	gadget := GVK{Group: "example.com", Version: "v1alpha1", Kind: "Gadget"}
	require.True(t, s.Has(gadget))
	assert.Equal(t, gadget, s.KindOf("gadgets.example.com"))
	assert.NoError(t, unify(t, s, gadget, `{apiVersion: "example.com/v1alpha1", kind: "Gadget", metadata: name: "g", spec: color: "red"}`))
	err := unify(t, s, gadget, `{spec: colour: "red"}`)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "colour")
}

func TestLaterSourcesWin(t *testing.T) {
	s := New()
	require.NoError(t, s.AddCRD([]byte(gadgetCRD)))
	require.NoError(t, s.AddCRD([]byte(`apiVersion: apiextensions.k8s.io/v1
kind: CustomResourceDefinition
metadata: {name: gadgets.example.com}
spec:
  group: example.com
  names: {kind: Gadget, plural: gadgets}
  scope: Namespaced
  versions:
  - name: v1alpha1
    served: true
    storage: true
    schema:
      openAPIV3Schema:
        type: object
        properties:
          spec: {type: object, properties: {colour: {type: string}}}
`)))
	gadget := GVK{Group: "example.com", Version: "v1alpha1", Kind: "Gadget"}
	assert.NoError(t, unify(t, s, gadget, `{spec: colour: "red"}`))
}

func TestParseAPIVersion(t *testing.T) {
	assert.Equal(t, GVK{Group: "apps", Version: "v1", Kind: "Deployment"}, ParseGVK("apps/v1", "Deployment"))
	assert.Equal(t, GVK{Version: "v1", Kind: "Service"}, ParseGVK("v1", "Service"))
}
