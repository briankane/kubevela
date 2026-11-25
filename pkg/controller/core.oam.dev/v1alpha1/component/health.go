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
	"encoding/json"
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/util/jsonpath"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/oam-dev/kubevela/apis/core.oam.dev/v1alpha1"
	"github.com/oam-dev/kubevela/pkg/componentdispatch"
	"github.com/oam-dev/kubevela/pkg/componenthealth"
	"github.com/oam-dev/kubevela/pkg/cue/definition/health"
)

const outputsPrefix = "outputs."

// reportHealth evaluates spec.health and spec.statusPaths against the live resources and
// records the results on comp.Status.
func (r *Reconciler) reportHealth(ctx context.Context, comp *v1alpha1.Component, index []v1alpha1.ComponentResource) {
	spec, err := healthSpec(comp, index)
	if err == nil {
		comp.Status.Health, err = componenthealth.Evaluate(ctx, r.Client, spec)
	}
	if err != nil {
		comp.Status.Health = nil
	}
	setHealthStages(comp, err)
	r.observe(ctx, comp, index)
}

// healthSpec maps spec.health onto the resources it reads: the workload's are those with no
// trait, a trait's are those it rendered, and each is found by its role.
func healthSpec(comp *v1alpha1.Component, index []v1alpha1.ComponentResource) (componenthealth.Spec, error) {
	h := comp.Spec.Health
	spec := componenthealth.Spec{Context: h.Context}
	if h.Workload != nil {
		subject, err := healthSubject(*h.Workload, index, "")
		if err != nil {
			return spec, fmt.Errorf("workload: %w", err)
		}
		spec.Workload = &subject
	}
	for _, t := range h.Traits {
		subject, err := healthSubject(t.ComponentHealthSubject, index, t.Type)
		if err != nil {
			return spec, fmt.Errorf("trait %s: %w", t.Type, err)
		}
		spec.Traits = append(spec.Traits, componenthealth.Trait{Type: t.Type, Subject: subject})
	}
	return spec, nil
}

func healthSubject(s v1alpha1.ComponentHealthSubject, resources []v1alpha1.ComponentResource, trait string) (componenthealth.Subject, error) {
	request := health.StatusRequest{Health: s.HealthPolicy, Custom: s.CustomStatus, Details: s.Details}
	for _, a := range s.Ancestors {
		request.Ancestors = append(request.Ancestors, health.Snippets{Health: a.HealthPolicy, Custom: a.CustomStatus, Details: a.Details})
	}
	if s.Parameter != nil && len(s.Parameter.Raw) > 0 {
		if err := json.Unmarshal(s.Parameter.Raw, &request.Parameter); err != nil {
			return componenthealth.Subject{}, fmt.Errorf("parameter: %w", err)
		}
	}
	subject := componenthealth.Subject{Status: request}
	for _, res := range resources {
		if res.Definition.Trait() != trait {
			continue
		}
		ref := componenthealth.ResourceRef{APIVersion: res.APIVersion, Kind: res.Kind, Namespace: res.Namespace, Name: res.Name}
		switch {
		case res.Role == "output" && trait == "":
			subject.Output = &ref
		case strings.HasPrefix(res.Role, outputsPrefix):
			if subject.Outputs == nil {
				subject.Outputs = map[string]componenthealth.ResourceRef{}
			}
			subject.Outputs[strings.TrimPrefix(res.Role, outputsPrefix)] = ref
		}
	}
	return subject, nil
}

// observe reads each spec.observe field from its live resource into status.observed, or
// sealed into status.sealedObserved when sensitive. A field that is absent, a resource that
// cannot be read, or a sensitive value with no sealing key to seal it to, is left out: the
// hub reads a missing field as not there yet.
func (r *Reconciler) observe(ctx context.Context, comp *v1alpha1.Component, index []v1alpha1.ComponentResource) {
	comp.Status.Observed, comp.Status.SealedObserved, comp.Status.SealedWith = nil, nil, ""
	plain := map[string]map[string]interface{}{}
	sealed := map[string]map[string]string{}
	live := map[string]*unstructured.Unstructured{}
	var sealing componentdispatch.SealingKeys
	keyID := comp.Spec.Signature.KeyID
	for _, o := range comp.Spec.Observe {
		obj, ok := live[o.Resource]
		if !ok {
			obj = r.liveResource(ctx, index, o.Resource)
			live[o.Resource] = obj
		}
		if obj == nil {
			continue
		}
		v, found := lookup(obj.Object, o.Path)
		if !found {
			continue
		}
		if !o.Sensitive {
			if plain[o.Resource] == nil {
				plain[o.Resource] = map[string]interface{}{}
			}
			plain[o.Resource][o.Name] = v
			continue
		}
		if sealing == nil {
			keys, err := r.Keys.SealingKeys(ctx)
			if err != nil || keys == nil {
				keys = componentdispatch.SealingKeys{}
			}
			sealing = keys
		}
		raw, err := json.Marshal(v)
		if err != nil {
			continue
		}
		box, err := sealing.Seal(keyID, raw)
		if err != nil {
			r.Recorder.Eventf(comp, corev1.EventTypeWarning, "CannotSeal", "%s %s left out: %v", o.Resource, o.Name, err)
			continue
		}
		if sealed[o.Resource] == nil {
			sealed[o.Resource] = map[string]string{}
		}
		sealed[o.Resource][o.Name] = box
	}
	for role, fields := range plain {
		raw, err := json.Marshal(fields)
		if err != nil {
			continue
		}
		if comp.Status.Observed == nil {
			comp.Status.Observed = map[string]runtime.RawExtension{}
		}
		comp.Status.Observed[role] = runtime.RawExtension{Raw: raw}
	}
	if len(sealed) > 0 {
		comp.Status.SealedObserved, comp.Status.SealedWith = sealed, keyID
	}
}

// liveResource reads the resource with role, preferring the workload's over a trait's of the
// same role; nil if there is none or it cannot be read.
func (r *Reconciler) liveResource(ctx context.Context, resources []v1alpha1.ComponentResource, role string) *unstructured.Unstructured {
	var res *v1alpha1.ComponentResource
	for i := range resources {
		if resources[i].Role != role {
			continue
		}
		if res == nil || (res.Definition.Kind == v1alpha1.DefinitionTrait && resources[i].Definition.Kind == v1alpha1.DefinitionComponent) {
			res = &resources[i]
		}
	}
	if res == nil {
		return nil
	}
	obj := &unstructured.Unstructured{}
	obj.SetGroupVersionKind(schema.FromAPIVersionAndKind(res.APIVersion, res.Kind))
	if err := r.Get(ctx, client.ObjectKey{Namespace: res.Namespace, Name: res.Name}, obj); err != nil {
		return nil
	}
	return obj
}

// lookup reads path from obj. An empty path is the whole object, without its managed fields.
func lookup(obj map[string]interface{}, path string) (interface{}, bool) {
	if path == "" {
		whole := (&unstructured.Unstructured{Object: obj}).DeepCopy()
		unstructured.RemoveNestedField(whole.Object, "metadata", "managedFields")
		return whole.Object, true
	}
	jp := jsonpath.New("statusPath").AllowMissingKeys(true)
	if err := jp.Parse("{" + path + "}"); err != nil {
		return nil, false
	}
	results, err := jp.FindResults(obj)
	if err != nil || len(results) == 0 || len(results[0]) == 0 {
		return nil, false
	}
	return results[0][0].Interface(), true
}
