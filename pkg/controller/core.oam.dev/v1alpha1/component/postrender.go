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

package component

import (
	"context"
	"fmt"
	"slices"
	"strings"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/oam-dev/kubevela/apis/core.oam.dev/v1alpha1"
	"github.com/oam-dev/kubevela/pkg/componentdispatch"
	"github.com/oam-dev/kubevela/pkg/oam"
	"github.com/oam-dev/kubevela/pkg/resourcekeeper"
)

// postRender renders spec.postRender against the workload once it is healthy, applies what it
// renders through the keeper that applied the payload, and returns the index the rest of the
// reconcile reads: the signed one, plus each post-rendered resource. Until it renders, the
// post-rendered resources already applied keep their entries from the last status. A render
// that fails is reported on the Component, not retried as an error: what was applied stays.
func (r *Reconciler) postRender(ctx context.Context, comp *v1alpha1.Component, keeper resourcekeeper.ResourceKeeper,
	objs []*unstructured.Unstructured) ([]v1alpha1.ComponentResource, error) {
	index := slices.Clone(comp.Spec.Resources)
	previous := append(index, previousPostRendered(comp)...)
	if h := comp.Status.Health; h == nil || !(h.WorkloadHealthy || h.Healthy) {
		setCondition(comp, ConditionPostRendered, metav1.ConditionFalse, "WaitingForWorkload", "waiting for the workload to be healthy")
		return previous, nil
	}
	workload, outputs := renderedOutputs(comp.Spec.Resources, objs)
	if workload == nil {
		r.postRenderFailed(comp, ReasonNoWorkload, "PostDispatch traits render against the workload, and this Component applies none")
		return previous, nil
	}
	live := &unstructured.Unstructured{}
	live.SetGroupVersionKind(workload.GroupVersionKind())
	if err := r.Client.Get(ctx, client.ObjectKeyFromObject(workload), live); err != nil {
		return previous, fmt.Errorf("reading the workload to render PostDispatch traits: %w", err)
	}
	rendered, err := componentdispatch.PostRender(ctx, comp.Spec.PostRender, withLiveStatus(workload, live), outputs)
	if err != nil {
		r.postRenderFailed(comp, ReasonRenderFailed, err.Error())
		return previous, nil
	}
	apply := make([]*unstructured.Unstructured, 0, len(rendered))
	for _, p := range rendered {
		if cluster := oam.GetCluster(p.Object); cluster != "" {
			r.postRenderFailed(comp, ReasonRenderFailed, fmt.Sprintf("%s %s names cluster %q: a Component's resources apply only where it is delivered",
				p.Object.GetKind(), p.Object.GetName(), cluster))
			return previous, nil
		}
		entry, err := componentdispatch.IndexEntry(p.Object)
		if err != nil {
			return previous, err
		}
		entry.Role, entry.Definition = p.Role, p.Definition
		index = append(index, entry)
		apply = append(apply, p.Object)
	}
	if err := keeper.Dispatch(ctx, apply, nil); err != nil {
		r.postRenderFailed(comp, applyFailure(err), err.Error())
		return previous, nil
	}
	setCondition(comp, ConditionPostRendered, metav1.ConditionTrue, ConditionPostRendered, "")
	return index, nil
}

func (r *Reconciler) postRenderFailed(comp *v1alpha1.Component, reason, message string) {
	setCondition(comp, ConditionPostRendered, metav1.ConditionFalse, reason, message)
	if r.Recorder != nil {
		r.Recorder.Event(comp, corev1.EventTypeWarning, "PostRender"+reason, message)
	}
}

// previousPostRendered is what the last status reported of the PostDispatch traits' resources.
func previousPostRendered(comp *v1alpha1.Component) []v1alpha1.ComponentResource {
	types := map[string]bool{}
	for _, t := range comp.Spec.PostRender.Traits {
		if len(t.Templates) > 0 {
			types[t.Templates[0].Name] = true
		}
	}
	var out []v1alpha1.ComponentResource
	for _, res := range comp.Status.Resources {
		if res.Definition.Kind == v1alpha1.DefinitionTrait && types[res.Definition.Type] {
			out = append(out, v1alpha1.ComponentResource{APIVersion: res.APIVersion, Kind: res.Kind, Namespace: res.Namespace,
				Name: res.Name, Role: res.Role, Definition: res.Definition})
		}
	}
	return out
}

// renderedOutputs finds the workload and the component's own outputs in the payload, by the
// role the index gives each.
func renderedOutputs(index []v1alpha1.ComponentResource, objs []*unstructured.Unstructured) (*unstructured.Unstructured, map[string]*unstructured.Unstructured) {
	type key struct{ apiVersion, kind, namespace, name string }
	byID := make(map[key]*unstructured.Unstructured, len(objs))
	for _, o := range objs {
		byID[key{o.GetAPIVersion(), o.GetKind(), o.GetNamespace(), o.GetName()}] = o
	}
	var workload *unstructured.Unstructured
	outputs := map[string]*unstructured.Unstructured{}
	for _, res := range index {
		obj := byID[key{res.APIVersion, res.Kind, res.Namespace, res.Name}]
		if obj == nil || res.Definition.Kind != v1alpha1.DefinitionComponent {
			continue
		}
		switch {
		case res.Role == componentdispatch.RoleOutput:
			workload = obj
		case strings.HasPrefix(res.Role, componentdispatch.RoleOutputsPrefix):
			outputs[strings.TrimPrefix(res.Role, componentdispatch.RoleOutputsPrefix)] = obj
		}
	}
	return workload, outputs
}

// withLiveStatus is the workload as rendered, carrying the live status: what a PostDispatch trait
// reads as context.output.
func withLiveStatus(rendered, live *unstructured.Unstructured) *unstructured.Unstructured {
	out := rendered.DeepCopy()
	if status, ok := live.Object["status"]; ok {
		out.Object["status"] = status
	}
	return out
}
