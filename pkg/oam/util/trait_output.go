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

package util

import (
	"maps"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/oam-dev/kubevela/pkg/oam"
)

// The rules below finish what a template rendered, wherever it renders: the Application
// controller, and vela-agent rendering a dispatched component's PostDispatch traits.

// NameTraitOutput names a trait's output that its template left unnamed, from the component
// and a hash of the output without its app-revision label.
func NameTraitOutput(trait *unstructured.Unstructured, component string) {
	if len(trait.GetName()) > 0 {
		return
	}
	hashed := trait.DeepCopy()
	RemoveLabels(hashed, []string{oam.LabelAppRevision})
	trait.SetName(GenTraitName(component, hashed, trait.GetLabels()[oam.TraitTypeLabel]))
}

// InheritAnnotations merges inherited into obj's annotations, then drops every key in drop.
// The app.oam.dev/last-applied-configuration an object sets itself wins over an inherited one,
// and an inherited opt-out of it is dropped: it is a statement an object makes about itself.
func InheritAnnotations(obj *unstructured.Unstructured, inherited map[string]string, drop []string) {
	ann := obj.GetAnnotations()
	if ann == nil && inherited == nil {
		return
	}
	if ann == nil {
		ann = map[string]string{}
	}
	own := ann[oam.AnnotationLastAppliedConfig]
	maps.Copy(ann, inherited)
	for _, k := range drop {
		delete(ann, k)
	}
	switch {
	case own != "":
		ann[oam.AnnotationLastAppliedConfig] = own
	case oam.IsSkipLastAppliedConfig(ann[oam.AnnotationLastAppliedConfig]):
		delete(ann, oam.AnnotationLastAppliedConfig)
	}
	obj.SetAnnotations(ann)
}

// SetDefaultNamespace puts obj in namespace when its template named none, unless obj is a
// Namespace.
func SetDefaultNamespace(obj *unstructured.Unstructured, namespace string) {
	if obj.GroupVersionKind() == corev1.SchemeGroupVersion.WithKind("Namespace") {
		return
	}
	if len(obj.GetNamespace()) == 0 {
		obj.SetNamespace(namespace)
	}
}
