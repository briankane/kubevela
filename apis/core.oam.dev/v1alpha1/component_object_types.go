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

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"

	"github.com/oam-dev/kubevela/apis/core.oam.dev/common"
)

// Component is one component's rendered resources for one cluster, signed by the hub that
// rendered them. vela-agent in the target cluster verifies, applies, tracks and health-checks
// them, and reports back through status.
// +kubebuilder:object:root=true
// +kubebuilder:resource:categories={oam}
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="CLUSTER",type=string,JSONPath=`.spec.target.cluster`
// +kubebuilder:printcolumn:name="READY",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="REASON",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].reason`
// +kubebuilder:printcolumn:name="REVISION",type=integer,JSONPath=`.spec.revision`
// +kubebuilder:printcolumn:name="OBSERVED",type=integer,JSONPath=`.status.observedRevision`
// +kubebuilder:printcolumn:name="AGE",type=date,JSONPath=`.metadata.creationTimestamp`
// +kubebuilder:printcolumn:name="MESSAGE",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].message`,priority=1
// +genclient
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
type Component struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   ComponentSpec   `json:"spec"`
	Status ComponentStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// ComponentList contains a list of Component.
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
type ComponentList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Component `json:"items"`
}

// ComponentSpec is everything the hub signs. vela-agent refuses a spec whose signature does
// not verify, so every field here is covered by spec.signature.
// +kubebuilder:validation:XValidation:rule="(has(self.delete) && self.delete) || has(self.payload)",message="payload is required unless delete is set"
type ComponentSpec struct {
	// Target is the cluster this Component is addressed to.
	Target ComponentTarget `json:"target"`

	// Revision increases with every change the hub dispatches. vela-agent refuses a
	// revision lower than the last one it applied.
	// +kubebuilder:validation:Minimum=1
	Revision int64 `json:"revision"`

	// Delete marks a tombstone: vela-agent garbage-collects what this Component tracks.
	// +optional
	Delete bool `json:"delete,omitempty"`

	// Resources lists what the payload holds, so the applied set is readable without it.
	// +optional
	Resources []ComponentResource `json:"resources,omitempty"`

	// Apply holds the apply-time policies the hub resolved for this component.
	// +optional
	Apply ComponentApply `json:"apply,omitempty"`

	// Health is evaluated against the live resources.
	// +optional
	Health ComponentHealth `json:"health,omitempty"`

	// PostRender holds the PostDispatch traits vela-agent renders against the live
	// workload once it is healthy.
	// +optional
	PostRender *ComponentPostRender `json:"postRender,omitempty"`

	// Observe names live fields vela-agent reports in status.observed (or sealed, in
	// status.sealedObserved) for the hub to read.
	// +optional
	Observe []ComponentObservation `json:"observe,omitempty"`

	// Payload locates the rendered resources. Absent on a tombstone.
	// +optional
	Payload *ComponentPayload `json:"payload,omitempty"`

	// Signature is the hub's signature over the rest of the spec.
	Signature ComponentSignature `json:"signature"`
}

// ComponentTarget is the cluster a Component is addressed to.
type ComponentTarget struct {
	// +kubebuilder:validation:MinLength=1
	Cluster string `json:"cluster"`
}

// ComponentResource identifies one rendered resource and its content hash.
type ComponentResource struct {
	APIVersion string `json:"apiVersion"`
	Kind       string `json:"kind"`
	// +optional
	Namespace string `json:"namespace,omitempty"`
	Name      string `json:"name"`
	// Role is how health CUE refers to the resource: "output", or "outputs.<name>".
	// +optional
	Role string `json:"role,omitempty"`
	// Definition is the definition that rendered the resource.
	Definition ComponentResourceDefinition `json:"definition"`
	// SHA256 is the hash of the resource as it appears in the payload.
	SHA256 string `json:"sha256"`
}

// ComponentApply holds the apply-time policies, in the shapes an Application declares them.
type ComponentApply struct {
	// +optional
	ApplyOnce *ApplyOncePolicySpec `json:"applyOnce,omitempty"`
	// +optional
	GarbageCollect *GarbageCollectPolicySpec `json:"garbageCollect,omitempty"`
	// +optional
	SharedResource *SharedResourcePolicySpec `json:"sharedResource,omitempty"`
	// +optional
	TakeOver *TakeOverPolicySpec `json:"takeOver,omitempty"`
	// +optional
	ReadOnly *ReadOnlyPolicySpec `json:"readOnly,omitempty"`
	// +optional
	ResourceUpdate *ResourceUpdatePolicySpec `json:"resourceUpdate,omitempty"`
	// AdoptFrom is the owner key (<kind>/<namespace>/<name>) whose resources this Component
	// takes over in place, as when it replaces the Application that applied them.
	// +optional
	AdoptFrom string `json:"adoptFrom,omitempty"`
}

// ComponentHealth is the rendered health of a component: its workload, then its traits.
type ComponentHealth struct {
	// Context holds the render-time values health CUE reads as context.*.
	// +optional
	Context map[string]string `json:"context,omitempty"`
	// Workload is absent when the workload is not applied (a trait manages it).
	// +optional
	Workload *ComponentHealthSubject `json:"workload,omitempty"`
	// +optional
	Traits []ComponentTraitHealth `json:"traits,omitempty"`
}

// HealthSnippets are one definition's health, customStatus and details CUE.
type HealthSnippets struct {
	// +optional
	HealthPolicy string `json:"healthPolicy,omitempty"`
	// +optional
	CustomStatus string `json:"customStatus,omitempty"`
	// +optional
	Details string `json:"details,omitempty"`
}

// ComponentHealthSubject is a workload or trait whose health is evaluated.
type ComponentHealthSubject struct {
	HealthSnippets `json:",inline"`
	// Parameter is what the health CUE reads as parameter.
	// +optional
	// +kubebuilder:pruning:PreserveUnknownFields
	Parameter *runtime.RawExtension `json:"parameter,omitempty"`
	// Ancestors are the snippets of the definitions this one extends, nearest parent first.
	// +optional
	Ancestors []HealthSnippets `json:"ancestors,omitempty"`
}

// ComponentTraitHealth is a trait's health subject, with its type.
type ComponentTraitHealth struct {
	Type                   string `json:"type"`
	ComponentHealthSubject `json:",inline"`
}

// ComponentPostRender is what vela-agent needs to render a component's PostDispatch traits.
type ComponentPostRender struct {
	// Context is what the templates read as context, without output and outputs, which
	// vela-agent fills from the workload it applied and its live status.
	// +optional
	// +kubebuilder:pruning:PreserveUnknownFields
	Context *runtime.RawExtension `json:"context,omitempty"`
	// Namespace, when set, is where every rendered resource goes, as a topology policy's
	// namespace override places it. Unset, a resource whose template names no namespace
	// goes to the context's.
	// +optional
	Namespace string `json:"namespace,omitempty"`
	// +kubebuilder:validation:MinItems=1
	Traits []ComponentPostRenderTrait `json:"traits"`
}

// ComponentPostRenderTrait is one PostDispatch trait: its templates, its parameter, and the
// metadata the hub stamps on what it renders.
type ComponentPostRenderTrait struct {
	// Type is the trait-type label its resources carry: the installed definition's name.
	Type string `json:"type"`
	// Templates are the trait's own template, named as the Application names the trait, then
	// those of the definitions it extends, nearest parent first.
	// +kubebuilder:validation:MinItems=1
	Templates []ComponentTemplate `json:"templates"`
	// Parameter is what the templates read as parameter, with every expression resolved.
	// +optional
	// +kubebuilder:pruning:PreserveUnknownFields
	Parameter *runtime.RawExtension `json:"parameter,omitempty"`
	// Labels and Annotations are set on each resource the trait renders, after the labels
	// vela-agent derives from the context and the name it gives a resource with none.
	// +optional
	Labels map[string]string `json:"labels,omitempty"`
	// +optional
	Annotations map[string]string `json:"annotations,omitempty"`
	// FilterAnnotationKeys are annotations a rendered resource never carries, even when its
	// template sets them: the hub's filtered keys for this Application.
	// +optional
	FilterAnnotationKeys []string `json:"filterAnnotationKeys,omitempty"`
}

// ComponentTemplate is one definition's CUE template.
type ComponentTemplate struct {
	// Name is the definition's name, which render errors cite.
	Name     string `json:"name"`
	Template string `json:"template"`
}

// ComponentObservation names one live field to report, under its resource's role and name.
type ComponentObservation struct {
	// Resource is the role of a resource in spec.resources: "output", or "outputs.<name>".
	Resource string `json:"resource"`
	Name     string `json:"name"`
	// Path is a JSONPath into the live resource, e.g. ".status.readyReplicas".
	Path string `json:"path"`
	// Sensitive values are sealed to the hub's sealing key and reported in
	// status.sealedObserved, never in plain text.
	// +optional
	Sensitive bool `json:"sensitive,omitempty"`
}

// ComponentPayload locates the compressed rendered resources.
type ComponentPayload struct {
	SecretRef ComponentSecretReference `json:"secretRef"`
	// Format names the encoding, e.g. "gzip+json/v1".
	Format string `json:"format"`
	// SHA256 is the hash of the payload data; vela-agent refuses a payload that differs.
	SHA256 string `json:"sha256"`
}

// ComponentSecretReference names a Secret in the Component's namespace.
type ComponentSecretReference struct {
	Name string `json:"name"`
}

// ComponentSignature is the hub's signature over the spec.
type ComponentSignature struct {
	KeyID string `json:"keyID"`
	Value string `json:"value"`
}

// ComponentStatus is what vela-agent reports.
type ComponentStatus struct {
	// ObservedGeneration is the metadata.generation this status describes.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
	// ObservedRevision is the last revision vela-agent verified and applied.
	// +optional
	ObservedRevision int64 `json:"observedRevision,omitempty"`
	// +optional
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`
	// Health is in the shape of an Application's status.services entry.
	// +optional
	Health *common.ApplicationComponentStatus `json:"health,omitempty"`
	// Observed holds the fields spec.observe asked for that are not sensitive: by resource
	// role, an object of name to value.
	// +optional
	// +kubebuilder:pruning:PreserveUnknownFields
	Observed map[string]runtime.RawExtension `json:"observed,omitempty"`
	// SealedObserved holds the sensitive ones: by resource role, then name, the value's JSON
	// sealed to the sealing key of SealedWith.
	// +optional
	SealedObserved map[string]map[string]string `json:"sealedObserved,omitempty"`
	// SealedWith is the hub key ID whose sealing key the sealed values are sealed to: the key
	// that signed the spec they were observed for.
	// +optional
	SealedWith string `json:"sealedWith,omitempty"`
	// Resources are the resources vela-agent applied and tracks for the observed revision, in
	// the cluster the Component is delivered to. Tools list an Application's resources from
	// here, since the hub may not be able to reach that cluster's ResourceTrackers.
	// +optional
	Resources []ComponentAppliedResource `json:"resources,omitempty"`
}

// ComponentAppliedResource identifies one resource vela-agent applied.
type ComponentAppliedResource struct {
	APIVersion string `json:"apiVersion"`
	Kind       string `json:"kind"`
	// +optional
	Namespace string `json:"namespace,omitempty"`
	Name      string `json:"name"`
	// Role is the resource's role in spec.resources: "output", or "outputs.<name>".
	// +optional
	Role string `json:"role,omitempty"`
	// Definition is the definition that rendered the resource.
	Definition ComponentResourceDefinition `json:"definition"`
}

// Kinds of definition a ComponentResourceDefinition names.
const (
	DefinitionComponent = "Component"
	DefinitionTrait     = "Trait"
)

// ComponentResourceDefinition is the definition that rendered a resource: the component's own
// type for its workload and auxiliary outputs, or a trait's type.
type ComponentResourceDefinition struct {
	// +kubebuilder:validation:Enum=Component;Trait
	Kind string `json:"kind"`
	// Type is the ComponentDefinition or TraitDefinition name.
	Type string `json:"type"`
}

// Trait is the type of the trait that rendered the resource; empty for a component's own.
func (d ComponentResourceDefinition) Trait() string {
	if d.Kind == DefinitionTrait {
		return d.Type
	}
	return ""
}
