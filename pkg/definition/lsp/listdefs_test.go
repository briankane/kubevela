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

package lsp

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"

	"github.com/oam-dev/kubevela/apis/core.oam.dev/v1beta1"
)

// Every kind of definition an Application names is read from the cluster,
// sources among them: a source only the cluster has is one an Application
// may bind.
func TestListDefinitions(t *testing.T) {
	kinds := []string{"ComponentDefinition", "TraitDefinition", "PolicyDefinition", "WorkflowStepDefinition", "SourceDefinition"}
	listKinds := map[schema.GroupVersionResource]string{}
	var objs []runtime.Object
	for _, k := range kinds {
		gvr := v1beta1.SchemeGroupVersion.WithResource(strings.ToLower(k) + "s")
		listKinds[gvr] = k + "List"
		u := &unstructured.Unstructured{}
		u.SetAPIVersion(v1beta1.SchemeGroupVersion.String())
		u.SetKind(k)
		u.SetNamespace("vela-system")
		u.SetName(strings.ToLower(k))
		objs = append(objs, u)
	}
	client := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), listKinds, objs...)
	var got []string
	for _, d := range listDefinitionsWith(client) {
		got = append(got, d.GetKind())
	}
	assert.ElementsMatch(t, kinds, got)
}
