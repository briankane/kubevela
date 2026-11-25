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

package componentdispatch

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"

	"github.com/oam-dev/kubevela/apis/core.oam.dev/v1alpha1"
	"github.com/oam-dev/kubevela/pkg/oam"
	"github.com/oam-dev/kubevela/pkg/oam/util"
)

func rawJSON(t *testing.T, v interface{}) *runtime.RawExtension {
	t.Helper()
	b, err := json.Marshal(v)
	require.NoError(t, err)
	return &runtime.RawExtension{Raw: b}
}

// postRenderContext is a component render context as the hub ships it.
func postRenderContext(t *testing.T) *runtime.RawExtension {
	return rawJSON(t, map[string]interface{}{
		"name": "web", "namespace": "shop-ns", "appName": "shop", "appRevision": "shop-v3",
		"appRevisionNum": 3, "cluster": "worker", "components": []interface{}{},
	})
}

func liveWorkload() *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "apps/v1", "kind": "Deployment",
		"metadata": map[string]interface{}{"name": "web", "namespace": "shop-ns"},
		"spec":     map[string]interface{}{"replicas": int64(2)},
		"status":   map[string]interface{}{"readyReplicas": int64(2)},
	}}
}

const announceTemplate = `
import "strings"

outputs: notice: {
	apiVersion: "v1"
	kind:       "ConfigMap"
	metadata: name: context.name + "-notice"
	data: {
		ready:   "\(context.output.status.readyReplicas)"
		message: strings.ToUpper(parameter.message)
		trait:   context.traitType
	}
}
parameter: message: string
`

func announce(t *testing.T, template string) v1alpha1.ComponentPostRenderTrait {
	return v1alpha1.ComponentPostRenderTrait{
		Type:        "announce",
		Templates:   []v1alpha1.ComponentTemplate{{Name: "announce", Template: template}},
		Parameter:   rawJSON(t, map[string]interface{}{"message": "ready"}),
		Labels:      map[string]string{"app.oam.dev/app-revision-hash": "abc123"},
		Annotations: map[string]string{"example.com/owner": "team-a"},
	}
}

func TestPostRenderReadsTheLiveWorkload(t *testing.T) {
	spec := &v1alpha1.ComponentPostRender{Context: postRenderContext(t), Traits: []v1alpha1.ComponentPostRenderTrait{announce(t, announceTemplate)}}
	out, err := PostRender(context.Background(), spec, liveWorkload(), nil)
	require.NoError(t, err)
	require.Len(t, out, 1)

	obj := out[0].Object
	require.Equal(t, "web-notice", obj.GetName())
	require.Equal(t, "shop-ns", obj.GetNamespace(), "the context's namespace, for a template that names none")
	data, _, _ := unstructured.NestedStringMap(obj.Object, "data")
	require.Equal(t, map[string]string{"ready": "2", "message": "READY", "trait": "announce"}, data)

	labels := obj.GetLabels()
	require.Equal(t, "announce", labels[oam.TraitTypeLabel])
	require.Equal(t, "notice", labels[oam.TraitResource])
	require.Equal(t, "shop", labels[oam.LabelAppName])
	require.Equal(t, "web", labels[oam.LabelAppComponent])
	require.Equal(t, "abc123", labels["app.oam.dev/app-revision-hash"])
	require.Equal(t, "team-a", obj.GetAnnotations()["example.com/owner"])

	require.Equal(t, "outputs.notice", out[0].Role)
	require.Equal(t, v1alpha1.ComponentResourceDefinition{Kind: v1alpha1.DefinitionTrait, Type: "announce"}, out[0].Definition)
}

func TestPostRenderNamesAnUnnamedOutputAsTheHubDoes(t *testing.T) {
	unnamed := `
outputs: notice: {
	apiVersion: "v1"
	kind:       "ConfigMap"
	data: ready: "\(context.output.status.readyReplicas)"
}
`
	trait := announce(t, unnamed)
	trait.Parameter = nil
	spec := &v1alpha1.ComponentPostRender{Context: postRenderContext(t), Traits: []v1alpha1.ComponentPostRenderTrait{trait}}
	out, err := PostRender(context.Background(), spec, liveWorkload(), nil)
	require.NoError(t, err)
	require.Len(t, out, 1)

	named := out[0].Object.DeepCopy()
	named.SetName("")
	named.SetNamespace("")
	named.SetAnnotations(nil)
	util.RemoveLabels(named, []string{oam.LabelAppRevision, "app.oam.dev/app-revision-hash"})
	require.Equal(t, util.GenTraitName("web", named, "announce"), out[0].Object.GetName())
}

func TestPostRenderPlacesEverythingInAnOverrideNamespace(t *testing.T) {
	placed := `
outputs: notice: {
	apiVersion: "v1"
	kind:       "ConfigMap"
	metadata: {name: "notice", namespace: "elsewhere"}
}
`
	trait := announce(t, placed)
	trait.Parameter = nil
	spec := &v1alpha1.ComponentPostRender{Context: postRenderContext(t), Namespace: "prod", Traits: []v1alpha1.ComponentPostRenderTrait{trait}}
	out, err := PostRender(context.Background(), spec, liveWorkload(), nil)
	require.NoError(t, err)
	require.Equal(t, "prod", out[0].Object.GetNamespace())
}

func TestPostRenderGivesANamespaceNoNamespace(t *testing.T) {
	space := `
outputs: space: {
	apiVersion: "v1"
	kind:       "Namespace"
	metadata: name: "shop-extra"
}
`
	trait := announce(t, space)
	trait.Parameter = nil
	spec := &v1alpha1.ComponentPostRender{Context: postRenderContext(t), Traits: []v1alpha1.ComponentPostRenderTrait{trait}}
	out, err := PostRender(context.Background(), spec, liveWorkload(), nil)
	require.NoError(t, err)
	require.Empty(t, out[0].Object.GetNamespace(), "the context's namespace goes only on what is namespaced")
}

func TestPostRenderDropsFilteredAnnotationsAnOutputSetsItself(t *testing.T) {
	annotated := `
outputs: notice: {
	apiVersion: "v1"
	kind:       "ConfigMap"
	metadata: {
		name: "notice"
		annotations: {
			"example.com/own":    "kept"
			"example.com/hidden": "dropped"
			"app.oam.dev/last-applied-configuration": "-"
		}
	}
}
`
	trait := announce(t, annotated)
	trait.Parameter = nil
	trait.FilterAnnotationKeys = []string{"example.com/hidden"}
	spec := &v1alpha1.ComponentPostRender{Context: postRenderContext(t), Traits: []v1alpha1.ComponentPostRenderTrait{trait}}
	out, err := PostRender(context.Background(), spec, liveWorkload(), nil)
	require.NoError(t, err)
	require.Equal(t, map[string]string{
		"example.com/own":   "kept",
		"example.com/owner": "team-a",
		"app.oam.dev/last-applied-configuration": "-",
	}, out[0].Object.GetAnnotations())
}

func TestPostRenderRendersAnInheritanceChain(t *testing.T) {
	parent := `
outputs: notice: {
	apiVersion: "v1"
	kind:       "ConfigMap"
	metadata: name: context.name + "-notice"
	data: from: "parent"
}
`
	child := `
$super: properties: {}
outputs: notice: data: ready: "\(context.output.status.readyReplicas)"
`
	trait := v1alpha1.ComponentPostRenderTrait{Type: "announce-plus", Templates: []v1alpha1.ComponentTemplate{
		{Name: "announce-plus", Template: child}, {Name: "announce-base", Template: parent}}}
	spec := &v1alpha1.ComponentPostRender{Context: postRenderContext(t), Traits: []v1alpha1.ComponentPostRenderTrait{trait}}
	out, err := PostRender(context.Background(), spec, liveWorkload(), nil)
	require.NoError(t, err)
	require.Len(t, out, 1)
	data, _, _ := unstructured.NestedStringMap(out[0].Object.Object, "data")
	require.Equal(t, map[string]string{"from": "parent", "ready": "2"}, data)
}

func TestPostRenderableAllowsTheStandardLibraryOnly(t *testing.T) {
	require.NoError(t, CheckPostRenderable([]v1alpha1.ComponentTemplate{{Name: "announce", Template: announceTemplate}}))

	err := CheckPostRenderable([]v1alpha1.ComponentTemplate{{Name: "reader", Template: `
import "vela/kube"

outputs: x: kube.#Read & {}
`}})
	require.ErrorContains(t, err, `reader imports "vela/kube"`)

	err = CheckPostRenderable([]v1alpha1.ComponentTemplate{{Name: "announce"}, {Name: "caller", Template: `
processing: http: {method: "GET", url: "https://example.com"}
outputs: x: {}
`}})
	require.ErrorContains(t, err, "caller uses processing")
}

func TestPostRenderRefusesWhatItCannotRender(t *testing.T) {
	trait := announce(t, `
import "vela/kube"

outputs: x: {}
`)
	spec := &v1alpha1.ComponentPostRender{Context: postRenderContext(t), Traits: []v1alpha1.ComponentPostRenderTrait{trait}}
	_, err := PostRender(context.Background(), spec, liveWorkload(), nil)
	require.ErrorContains(t, err, `imports "vela/kube"`)
}
