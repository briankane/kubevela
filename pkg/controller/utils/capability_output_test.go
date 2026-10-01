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

package utils

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/oam-dev/kubevela/apis/core.oam.dev/common"
	"github.com/oam-dev/kubevela/apis/core.oam.dev/v1beta1"
	"github.com/oam-dev/kubevela/apis/types"
	common2 "github.com/oam-dev/kubevela/pkg/utils/common"
)

func schemaConfigMaps(t *testing.T, k8sClient client.Client, names ...string) []corev1.ConfigMap {
	t.Helper()
	var cms []corev1.ConfigMap
	for _, name := range names {
		var cm corev1.ConfigMap
		require.NoError(t, k8sClient.Get(context.Background(), client.ObjectKey{Namespace: "vela-system", Name: name}, &cm), name)
		cms = append(cms, cm)
	}
	return cms
}

func TestComponentDefinitionStoresOutputSchemas(t *testing.T) {
	ctx := context.Background()
	def := &v1beta1.ComponentDefinition{
		TypeMeta:   metav1.TypeMeta{APIVersion: "core.oam.dev/v1beta1", Kind: v1beta1.ComponentDefinitionKind},
		ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "vela-system", UID: "def-uid"},
		Spec: v1beta1.ComponentDefinitionSpec{Schematic: &common.Schematic{CUE: &common.CUE{Template: `
parameter: {
	image: string
	port:  *80 | int
}
output: {
	apiVersion: "apps/v1"
	kind:       "Deployment"
	spec: template: spec: containers: [{image: parameter.image}]
}
outputs: svc: {
	apiVersion: "v1"
	kind:       "Service"
	spec: ports: [{port: parameter.port}]
}
`}}},
	}
	rev := &v1beta1.DefinitionRevision{
		TypeMeta:   metav1.TypeMeta{APIVersion: "core.oam.dev/v1beta1", Kind: v1beta1.DefinitionRevisionKind},
		ObjectMeta: metav1.ObjectMeta{Name: "web-v1", Namespace: "vela-system", UID: "rev-uid"},
		Spec:       v1beta1.DefinitionRevisionSpec{ComponentDefinition: *def},
	}
	k8sClient := fake.NewClientBuilder().WithScheme(common2.Scheme).WithObjects(rev).Build()

	capability := NewCapabilityComponentDef(def)
	_, err := capability.StoreOpenAPISchema(ctx, k8sClient, "vela-system", "web", "web-v1")
	require.NoError(t, err)

	for _, cm := range schemaConfigMaps(t, k8sClient, "component-schema-web", "component-schema-web-v1") {
		var output map[string]any
		require.NoError(t, json.Unmarshal([]byte(cm.Data[types.OutputSchema]), &output), cm.Name)
		assert.Contains(t, output["properties"], "spec", cm.Name)
		assert.Contains(t, output["properties"], "status", cm.Name)

		var outputs map[string]map[string]any
		require.NoError(t, json.Unmarshal([]byte(cm.Data[types.OutputsSchema]), &outputs), cm.Name)
		require.Contains(t, outputs, "svc", cm.Name)
		assert.Contains(t, outputs["svc"]["properties"], "spec", cm.Name)
	}
}

func TestTraitDefinitionStoresOutputsSchema(t *testing.T) {
	ctx := context.Background()
	def := &v1beta1.TraitDefinition{
		TypeMeta:   metav1.TypeMeta{APIVersion: "core.oam.dev/v1beta1", Kind: v1beta1.TraitDefinitionKind},
		ObjectMeta: metav1.ObjectMeta{Name: "expose", Namespace: "vela-system", UID: "def-uid"},
		Spec: v1beta1.TraitDefinitionSpec{Schematic: &common.Schematic{CUE: &common.CUE{Template: `
parameter: port: int
outputs: service: {
	apiVersion: "v1"
	kind:       "Service"
	spec: ports: [{port: parameter.port}]
}
`}}},
	}
	rev := &v1beta1.DefinitionRevision{
		TypeMeta:   metav1.TypeMeta{APIVersion: "core.oam.dev/v1beta1", Kind: v1beta1.DefinitionRevisionKind},
		ObjectMeta: metav1.ObjectMeta{Name: "expose-v1", Namespace: "vela-system", UID: "rev-uid"},
		Spec:       v1beta1.DefinitionRevisionSpec{TraitDefinition: *def},
	}
	k8sClient := fake.NewClientBuilder().WithScheme(common2.Scheme).WithObjects(rev).Build()

	capability := NewCapabilityTraitDef(def)
	_, err := capability.StoreOpenAPISchema(ctx, k8sClient, "vela-system", "expose", "expose-v1")
	require.NoError(t, err)

	for _, cm := range schemaConfigMaps(t, k8sClient, "trait-schema-expose", "trait-schema-expose-v1") {
		assert.NotContains(t, cm.Data, types.OutputSchema, cm.Name)
		var outputs map[string]map[string]any
		require.NoError(t, json.Unmarshal([]byte(cm.Data[types.OutputsSchema]), &outputs), cm.Name)
		require.Contains(t, outputs, "service", cm.Name)
		assert.Contains(t, outputs["service"]["properties"], "status", cm.Name)
	}
}

func TestOutputSchemaDataLeavesOutWhatTheTemplateLacks(t *testing.T) {
	ctx := context.Background()
	assert.Empty(t, outputSchemaData(ctx, "x", `parameter: a: string`))
	assert.Empty(t, outputSchemaData(ctx, "x", `output: {`), "a template that does not parse stores no output keys")
}
