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

package application

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/validation"

	"github.com/oam-dev/kubevela/apis/core.oam.dev/v1alpha1"
	"github.com/oam-dev/kubevela/pkg/componentdispatch"
	"github.com/oam-dev/kubevela/pkg/cue/definition"
	"github.com/oam-dev/kubevela/pkg/multicluster"
	"github.com/oam-dev/kubevela/pkg/oam"
)

const (
	payloadSuffix      = "-payload"
	controlPlaneSuffix = "-cp"
	outputRole         = componentdispatch.RoleOutput
	outputsRolePrefix  = componentdispatch.RoleOutputsPrefix
)

// componentTarget names one Component: one component of an Application, placed on one
// cluster, in the namespace it lives in there.
type componentTarget struct {
	app, appNamespace string
	component         string
	replicaKey        string
	cluster           string
	namespace         string
	// controlPlaneFor and controlPlaneNamespace are set on a hub Component holding the
	// control-plane-only resources of the component placed on that cluster, in that namespace.
	controlPlaneFor, controlPlaneNamespace string
}

// name is <app>-<component>[-<replica>]-<cluster>, then 8 hex of a hash over every part of the
// target. A hub control-plane Component puts the placement's cluster and namespace in place of
// the cluster, and ends -cp. The readable parts are trimmed to keep the name a valid label value,
// which vela-agent stamps on everything it applies; the hash keeps placements apart however
// their dash-joined parts read.
func (t componentTarget) name() string {
	parts := []string{t.app, t.component}
	if t.replicaKey != "" {
		parts = append(parts, t.replicaKey)
	}
	suffix := ""
	if t.controlPlaneFor == "" {
		parts = append(parts, t.cluster)
	} else {
		parts = append(parts, t.controlPlaneFor, t.controlPlaneNamespace)
		suffix = controlPlaneSuffix
	}
	sum := sha256.Sum256([]byte(strings.Join([]string{t.appNamespace, t.app, t.component, t.replicaKey, t.cluster,
		t.controlPlaneFor, t.controlPlaneNamespace}, "/")))
	hash := "-" + hex.EncodeToString(sum[:4])
	readable := strings.Join(parts, "-")
	if limit := validation.LabelValueMaxLength - len(hash) - len(suffix); len(readable) > limit {
		readable = strings.TrimRight(readable[:limit], "-.")
	}
	return readable + hash + suffix
}

// controlPlane is the hub Component for this one's control-plane-only resources. It lives
// with the Application, where those resources are rendered.
func (t componentTarget) controlPlane() componentTarget {
	cp := t
	cp.cluster = multicluster.ClusterLocalName
	cp.namespace = t.appNamespace
	cp.controlPlaneFor, cp.controlPlaneNamespace = t.cluster, t.namespace
	return cp
}

func (t componentTarget) labels() map[string]string {
	labels := map[string]string{
		oam.LabelAppName:          t.app,
		oam.LabelAppNamespace:     t.appNamespace,
		oam.LabelAppComponent:     t.component,
		oam.LabelComponentCluster: t.cluster,
	}
	if t.replicaKey != "" {
		labels[oam.LabelReplicaKey] = t.replicaKey
	}
	if t.controlPlaneFor != "" {
		labels[oam.LabelControlPlaneFor] = t.controlPlaneFor
	}
	return labels
}

// renderedComponent is one component rendered for one placement, before packaging.
type renderedComponent struct {
	target componentTarget
	// workload is nil when it is not applied (a trait manages it).
	workload *unstructured.Unstructured
	traits   []*unstructured.Unstructured
	// terraform components are not dispatched: their health is evaluated in Go.
	terraform bool
	// componentType is the component's ComponentDefinition.
	componentType string
	// postRender is what vela-agent renders once the workload is healthy; nil for none.
	postRender *v1alpha1.ComponentPostRender

	context        map[string]string
	workloadHealth *v1alpha1.ComponentHealthSubject
	traitHealth    []renderedTrait
	apply          v1alpha1.ComponentApply
	// reads are other components' reads of this one, each below the producer:
	// ["output", ...] or ["outputs", <name>, ...].
	reads [][]string
}

// renderedTrait is a trait's health, with the type label its resources carry.
type renderedTrait struct {
	name, typeLabel  string
	controlPlaneOnly bool
	health           v1alpha1.ComponentHealthSubject
}

// componentPackage is what one Component carries: its resources, indexed by role, and their health.
type componentPackage struct {
	target     componentTarget
	objects    []*unstructured.Unstructured
	index      []v1alpha1.ComponentResource
	health     v1alpha1.ComponentHealth
	apply      v1alpha1.ComponentApply
	observe    []v1alpha1.ComponentObservation
	postRender *v1alpha1.ComponentPostRender
}

// packages splits a rendered component into the Components that carry it. A component on
// the hub is one Component. One on another cluster leaves its control-plane-only trait
// resources to a second Component on the hub, since vela-agent there cannot reach the hub.
func (r renderedComponent) packages() ([]componentPackage, error) {
	if r.terraform {
		return nil, fmt.Errorf("component %q is a Terraform component, which cannot be dispatched as a Component yet", r.target.component)
	}
	split := r.target.cluster != multicluster.ClusterLocalName
	main := componentPackage{target: r.target, apply: r.apply, postRender: r.postRender,
		health: v1alpha1.ComponentHealth{Context: r.context, Workload: r.workloadHealth}}
	cp := componentPackage{target: r.target.controlPlane(), apply: r.apply, health: v1alpha1.ComponentHealth{Context: r.context}}

	names := map[string]string{}
	controlPlaneOnly := map[string]bool{}
	for _, t := range r.traitHealth {
		names[t.typeLabel] = t.name
		controlPlaneOnly[t.typeLabel] = t.controlPlaneOnly
		dest := &main
		if split && t.controlPlaneOnly {
			dest = &cp
		}
		dest.health.Traits = append(dest.health.Traits, v1alpha1.ComponentTraitHealth{Type: t.name, ComponentHealthSubject: t.health})
	}

	own := v1alpha1.ComponentResourceDefinition{Kind: v1alpha1.DefinitionComponent, Type: r.componentType}
	if r.workload != nil {
		if err := main.add(r.workload, outputRole, own); err != nil {
			return nil, err
		}
	}
	for _, obj := range r.traits {
		typeLabel := obj.GetLabels()[oam.TraitTypeLabel]
		role := ""
		if res := obj.GetLabels()[oam.TraitResource]; res != "" {
			role = outputsRolePrefix + res
		}
		def, dest := own, &main
		if typeLabel != definition.AuxiliaryWorkload {
			def = v1alpha1.ComponentResourceDefinition{Kind: v1alpha1.DefinitionTrait, Type: typeLabel}
			if name, ok := names[typeLabel]; ok {
				def.Type = name
			}
			if split && controlPlaneOnly[typeLabel] {
				dest = &cp
			}
		}
		if err := dest.add(obj, role, def); err != nil {
			return nil, err
		}
	}

	main.observeAll(r.reads)
	cp.observeAll(r.reads)
	out := []componentPackage{main}
	if len(cp.objects) > 0 || len(cp.health.Traits) > 0 {
		out = append(out, cp)
	}
	return out, nil
}

// observeAll asks for every read of a resource this package carries. A read of a Secret is sensitive.
func (p *componentPackage) observeAll(reads [][]string) {
	kinds := map[string]string{}
	for _, r := range p.index {
		if _, dup := kinds[r.Role]; !dup || r.Definition.Kind == v1alpha1.DefinitionComponent {
			kinds[r.Role] = r.Kind
		}
	}
	seen := map[string]bool{}
	add := func(role string, fields []string) {
		name := observationName(fields)
		if seen[role+" "+name] {
			return
		}
		seen[role+" "+name] = true
		p.observe = append(p.observe, v1alpha1.ComponentObservation{
			Resource: role, Name: name, Path: observationPath(fields), Sensitive: kinds[role] == "Secret",
		})
	}
	first := len(p.observe)
	for _, read := range reads {
		role, fields, ok := observedRead(read)
		if _, carried := kinds[role]; ok && carried {
			add(role, fields)
		}
	}
	rest := p.observe[first:]
	sort.Slice(rest, func(i, j int) bool {
		if rest[i].Resource != rest[j].Resource {
			return rest[i].Resource < rest[j].Resource
		}
		return rest[i].Name < rest[j].Name
	})
}

// observedRead is the resource role a read names, and the fields to observe for it: up to
// the first list index, or field JSONPath cannot spell, whose parent is observed whole.
func observedRead(read []string) (role string, fields []string, ok bool) {
	switch {
	case len(read) >= 1 && read[0] == outputRole:
		role, fields = outputRole, read[1:]
	case len(read) >= 2 && read[0] == "outputs":
		role, fields = outputsRolePrefix+read[1], read[2:]
	default:
		return "", nil, false
	}
	for i, f := range fields {
		if f == "" || strings.ContainsAny(f, "[]{}\\ ") || isIndex(f) {
			return role, fields[:i], true
		}
	}
	return role, fields, true
}

func isIndex(s string) bool {
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return s != ""
}

// observationName names an observation by its fields, as a JSON list, so the hub can put
// the value back where it was read from.
func observationName(fields []string) string {
	if fields == nil {
		fields = []string{}
	}
	b, _ := json.Marshal(fields)
	return string(b)
}

// observationPath is fields as a JSONPath, dots in a field escaped; empty for the whole resource.
func observationPath(fields []string) string {
	var b strings.Builder
	for _, f := range fields {
		b.WriteString(".")
		b.WriteString(strings.ReplaceAll(f, ".", `\.`))
	}
	return b.String()
}

// add indexes obj without its cluster label (see dropClusterLabel).
func (p *componentPackage) add(obj *unstructured.Unstructured, role string, def v1alpha1.ComponentResourceDefinition) error {
	obj = obj.DeepCopy()
	if labels := obj.GetLabels(); dropClusterLabel(labels) {
		obj.SetLabels(labels)
	}
	entry, err := componentdispatch.IndexEntry(obj)
	if err != nil {
		return err
	}
	entry.Role, entry.Definition = role, def
	p.objects = append(p.objects, obj)
	p.index = append(p.index, entry)
	return nil
}

// build makes the unsigned Component at revision, and the payload Secret it names.
func (p componentPackage) build(revision int64) (*v1alpha1.Component, *corev1.Secret, error) {
	data, digest, err := componentdispatch.EncodePayload(p.objects)
	if err != nil {
		return nil, nil, fmt.Errorf("packaging component %s: %w", p.target.name(), err)
	}
	name := p.target.name()
	secret := &corev1.Secret{
		TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "Secret"},
		ObjectMeta: metav1.ObjectMeta{Name: name + payloadSuffix, Namespace: p.target.namespace, Labels: p.target.labels()},
		Type:       corev1.SecretTypeOpaque,
		Data:       map[string][]byte{componentdispatch.PayloadKey: data},
	}
	comp := &v1alpha1.Component{
		TypeMeta:   metav1.TypeMeta{APIVersion: v1alpha1.SchemeGroupVersion.String(), Kind: v1alpha1.ComponentKind},
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: p.target.namespace, Labels: p.target.labels()},
		Spec: v1alpha1.ComponentSpec{
			Target:     v1alpha1.ComponentTarget{Cluster: p.target.cluster},
			Revision:   revision,
			Resources:  p.index,
			Apply:      p.apply,
			Health:     p.health,
			Observe:    p.observe,
			PostRender: p.postRender,
			Payload: &v1alpha1.ComponentPayload{
				SecretRef: v1alpha1.ComponentSecretReference{Name: secret.Name},
				Format:    componentdispatch.PayloadFormat,
				SHA256:    digest,
			},
		},
	}
	return comp, secret, nil
}

// observedResources rebuilds a Component's resources from what vela-agent observed of
// them: each one's identity, with every observed field set where it was read from. open
// decrypts a sealed value.
func observedResources(c *v1alpha1.Component, open func(sealed string) ([]byte, error)) (workloads, outputs []*unstructured.Unstructured, err error) {
	values := map[string]map[string]interface{}{}
	for role, raw := range c.Status.Observed {
		dec := json.NewDecoder(strings.NewReader(string(raw.Raw)))
		dec.UseNumber()
		fields := map[string]interface{}{}
		if err := dec.Decode(&fields); err != nil {
			return nil, nil, fmt.Errorf("component %s/%s observed %s: %w", c.Namespace, c.Name, role, err)
		}
		values[role] = fields
	}
	for role, sealed := range c.Status.SealedObserved {
		for name, box := range sealed {
			raw, err := open(box)
			if err != nil {
				return nil, nil, fmt.Errorf("component %s/%s observed %s %s: %w", c.Namespace, c.Name, role, name, err)
			}
			dec := json.NewDecoder(strings.NewReader(string(raw)))
			dec.UseNumber()
			var v interface{}
			if err := dec.Decode(&v); err != nil {
				return nil, nil, fmt.Errorf("component %s/%s observed %s %s: %w", c.Namespace, c.Name, role, name, err)
			}
			if values[role] == nil {
				values[role] = map[string]interface{}{}
			}
			values[role][name] = v
		}
	}

	seen := map[string]bool{}
	for _, r := range c.Spec.Resources {
		if r.Role == "" || seen[r.Role] {
			continue
		}
		seen[r.Role] = true
		u := &unstructured.Unstructured{Object: map[string]interface{}{}}
		if err := setObserved(u, values[r.Role]); err != nil {
			return nil, nil, fmt.Errorf("component %s/%s observed %s: %w", c.Namespace, c.Name, r.Role, err)
		}
		u.SetAPIVersion(r.APIVersion)
		u.SetKind(r.Kind)
		u.SetName(r.Name)
		u.SetNamespace(r.Namespace)
		labels := u.GetLabels()
		if labels == nil {
			labels = map[string]string{}
		}
		if res, ok := strings.CutPrefix(r.Role, outputsRolePrefix); ok {
			labels[oam.TraitResource] = res
			u.SetLabels(labels)
			outputs = append(outputs, u)
			continue
		}
		if rk := c.Labels[oam.LabelReplicaKey]; rk != "" {
			labels[oam.LabelReplicaKey] = rk
		}
		u.SetLabels(labels)
		workloads = append(workloads, u)
	}
	return workloads, outputs, nil
}

// setObserved sets each observed value at the fields its name lists, parents before their
// children, so a field read on its own overrides the copy inside a parent read whole.
func setObserved(u *unstructured.Unstructured, observed map[string]interface{}) error {
	type field struct {
		path  []string
		value interface{}
	}
	fields := make([]field, 0, len(observed))
	for name, v := range observed {
		var path []string
		if err := json.Unmarshal([]byte(name), &path); err != nil {
			return fmt.Errorf("observation %q is not named by its fields: %w", name, err)
		}
		fields = append(fields, field{path: path, value: v})
	}
	sort.Slice(fields, func(i, j int) bool {
		if len(fields[i].path) != len(fields[j].path) {
			return len(fields[i].path) < len(fields[j].path)
		}
		return strings.Join(fields[i].path, "\x00") < strings.Join(fields[j].path, "\x00")
	})
	for _, f := range fields {
		if len(f.path) == 0 {
			whole, ok := f.value.(map[string]interface{})
			if !ok {
				return fmt.Errorf("the whole resource was observed as a %T", f.value)
			}
			u.Object = runtime.DeepCopyJSON(whole)
			continue
		}
		if err := unstructured.SetNestedField(u.Object, runtime.DeepCopyJSONValue(f.value), f.path...); err != nil {
			return err
		}
	}
	return nil
}

// dropClusterLabel removes a cluster label naming a cluster from labels, and reports whether it
// did: a Component's resources are applied in the cluster the Component is delivered to, and
// vela-agent's keeper would route by the label.
func dropClusterLabel(labels map[string]string) bool {
	if labels[oam.LabelAppCluster] == "" {
		return false
	}
	delete(labels, oam.LabelAppCluster)
	return true
}
