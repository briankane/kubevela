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
	"context"
	"encoding/json"
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"

	"github.com/oam-dev/kubevela/apis/core.oam.dev/v1beta1"
)

// revisionOf is the ApplicationRevision an Application's workflow last ran,
// or, before one has, its latest.
func revisionOf(app unstructured.Unstructured) string {
	if name, _, _ := unstructured.NestedString(app.Object, "status", "workflow", "appRevision"); name != "" {
		return name
	}
	name, _, _ := unstructured.NestedString(app.Object, "status", "latestRevision", "name")
	return name
}

// definitionFromRevision is the definition of a type and name as a revision
// recorded it when the Application rendered with it.
func definitionFromRevision(rev v1beta1.ApplicationRevision, typ, name string) (*unstructured.Unstructured, bool) {
	var obj runtime.Object
	var kind string
	switch typ {
	case "component":
		if d, ok := rev.Spec.ComponentDefinitions[name]; ok && d != nil {
			obj, kind = d, v1beta1.ComponentDefinitionKind
		}
	case "trait":
		if d, ok := rev.Spec.TraitDefinitions[name]; ok && d != nil {
			obj, kind = d, v1beta1.TraitDefinitionKind
		}
	case "workflow-step":
		if d, ok := rev.Spec.WorkflowStepDefinitions[name]; ok && d != nil {
			obj, kind = d, v1beta1.WorkflowStepDefinitionKind
		}
	case "policy":
		if d, ok := rev.Spec.PolicyDefinitions[name]; ok {
			obj, kind = &d, v1beta1.PolicyDefinitionKind
		}
	}
	if obj == nil {
		return nil, false
	}
	raw, err := runtime.DefaultUnstructuredConverter.ToUnstructured(obj)
	if err != nil {
		return nil, false
	}
	out := &unstructured.Unstructured{Object: raw}
	// A revision's snapshot carries no type meta of its own.
	out.SetGroupVersionKind(v1beta1.SchemeGroupVersion.WithKind(kind))
	return out, true
}

// readRevisionDefinition reads a definition as the Application's current
// revision recorded it, and names the revision.
func readRevisionDefinition(cfg *rest.Config, namespace, app, typ, name string) (*unstructured.Unstructured, string, error) {
	client, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return nil, "", err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*clusterTimeout)
	defer cancel()
	a, err := client.Resource(v1beta1.SchemeGroupVersion.WithResource("applications")).Namespace(namespace).Get(ctx, app, metav1.GetOptions{})
	if err != nil {
		return nil, "", err
	}
	revName := revisionOf(*a)
	if revName == "" {
		return nil, "", fmt.Errorf("%s/%s has no revision yet", namespace, app)
	}
	u, err := client.Resource(v1beta1.SchemeGroupVersion.WithResource("applicationrevisions")).Namespace(namespace).Get(ctx, revName, metav1.GetOptions{})
	if err != nil {
		return nil, "", err
	}
	// The typed revision decompresses a compressed spec as it is read.
	raw, err := json.Marshal(u.Object)
	if err != nil {
		return nil, "", err
	}
	var rev v1beta1.ApplicationRevision
	if err := json.Unmarshal(raw, &rev); err != nil {
		return nil, "", err
	}
	obj, ok := definitionFromRevision(rev, typ, name)
	if !ok {
		return nil, revName, fmt.Errorf("%s records no %s definition %s", revName, typ, name)
	}
	return obj, revName, nil
}
