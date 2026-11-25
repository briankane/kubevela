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
	"encoding/json"
	"fmt"

	"cuelang.org/go/cue"
	"cuelang.org/go/cue/cuecontext"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"

	"github.com/kubevela/workflow/pkg/cue/process"

	"github.com/oam-dev/kubevela/apis/core.oam.dev/v1alpha1"
	"github.com/oam-dev/kubevela/apis/types"
	"github.com/oam-dev/kubevela/pkg/appfile"
	"github.com/oam-dev/kubevela/pkg/componentdispatch"
	"github.com/oam-dev/kubevela/pkg/controller/core.oam.dev/v1beta1/application/assemble"
	velaprocess "github.com/oam-dev/kubevela/pkg/cue/process"
	"github.com/oam-dev/kubevela/pkg/oam"
	"github.com/oam-dev/kubevela/pkg/sources"
)

// postRenderSpec is what vela-agent needs to render wl's PostDispatch traits against the
// workload it applies, or nil when wl has none. workload is wl's rendered workload. A trait
// vela-agent cannot render as the hub would is refused, naming it.
func (h *AppHandler) postRenderSpec(af *appfile.Appfile, wl *appfile.Component, workload *unstructured.Unstructured,
	overrideNamespace string) (*v1alpha1.ComponentPostRender, error) {
	if len(wl.PostDispatchTraits) == 0 {
		return nil, nil
	}
	if workload == nil {
		return nil, fmt.Errorf("component %s: its PostDispatch traits render against its workload, which it does not apply", wl.Name)
	}
	ctxData, err := postRenderContext(wl.Ctx)
	if err != nil {
		return nil, fmt.Errorf("component %s: %w", wl.Name, err)
	}
	spec := &v1alpha1.ComponentPostRender{Context: ctxData, Namespace: overrideNamespace}
	for _, tr := range wl.PostDispatchTraits {
		trait, err := h.postRenderTrait(af, wl, workload, tr)
		if err != nil {
			return nil, fmt.Errorf("component %s, trait %s: %w", wl.Name, tr.Name, err)
		}
		spec.Traits = append(spec.Traits, trait)
	}
	return spec, nil
}

func (h *AppHandler) postRenderTrait(af *appfile.Appfile, wl *appfile.Component, workload *unstructured.Unstructured,
	tr *appfile.Trait) (v1alpha1.ComponentPostRenderTrait, error) {
	var out v1alpha1.ComponentPostRenderTrait
	if def := tr.FullTemplate.TraitDefinition; def != nil {
		switch {
		case def.Spec.ControlPlaneOnly:
			return out, fmt.Errorf("a PostDispatch trait that is controlPlaneOnly cannot be dispatched: vela-agent on the hub has no live workload to render it against")
		case def.Spec.PodDisruptive:
			return out, fmt.Errorf("a PostDispatch trait that is podDisruptive cannot be dispatched: it changes the workload, which vela-agent renders it after")
		case def.Spec.WorkloadRefPath != "":
			return out, fmt.Errorf("a PostDispatch trait with a workloadRefPath cannot be dispatched yet")
		}
	}
	if wl.Patch != nil && wl.Patch.LookupPath(cue.ParsePath("traits")).Exists() {
		return out, fmt.Errorf("a policy patches this component's trait outputs, which vela-agent renders its PostDispatch traits without")
	}
	out.Type = tr.GetTypeLabel()
	out.Templates = []v1alpha1.ComponentTemplate{{Name: tr.Name, Template: tr.Template}}
	for _, a := range tr.FullTemplate.Ancestors {
		out.Templates = append(out.Templates, v1alpha1.ComponentTemplate{Name: a.Name, Template: a.Template})
	}
	if err := componentdispatch.CheckPostRenderable(out.Templates); err != nil {
		return out, err
	}
	params, err := sources.ResolveSourceExpressions(wl.Ctx, tr.Params, sources.SurfaceTrait)
	if err != nil {
		return out, err
	}
	if params == nil && tr.Params != nil {
		params = tr.Params
	}
	if params != nil {
		raw, err := json.Marshal(params)
		if err != nil {
			return out, fmt.Errorf("encoding the parameter: %w", err)
		}
		out.Parameter = &runtime.RawExtension{Raw: raw}
	}
	out.Labels, out.Annotations, err = h.traitContract(af, wl, workload, out.Type)
	out.FilterAnnotationKeys = appfile.FilteredAnnotationKeys(af.AppAnnotations)
	return out, err
}

// traitContract is the labels and annotations the hub stamps on a trait's output after naming
// it, found by stamping a stand-in output through the same code: SetOAMContract, then
// PrepareBeforeApply, less the cluster label as the payload's resources go. The trait-type label
// is left to vela-agent.
func (h *AppHandler) traitContract(af *appfile.Appfile, wl *appfile.Component, workload *unstructured.Unstructured,
	typeLabel string) (map[string]string, map[string]string, error) {
	stand := &unstructured.Unstructured{Object: map[string]interface{}{"apiVersion": "v1", "kind": "ConfigMap",
		"metadata": map[string]interface{}{"name": "stand-in"}}}
	stand.SetLabels(map[string]string{oam.TraitTypeLabel: typeLabel})
	manifest := &types.ComponentManifest{Name: wl.Name, ComponentOutput: workload.DeepCopy(),
		ComponentOutputsAndTraits: []*unstructured.Unstructured{stand}}
	if err := af.SetOAMContract(manifest); err != nil {
		return nil, nil, err
	}
	if _, _, err := assemble.PrepareBeforeApply(manifest, h.currentAppRev, DisableAllComponentRevision); err != nil {
		return nil, nil, err
	}
	labels := stand.GetLabels()
	delete(labels, oam.TraitTypeLabel)
	dropClusterLabel(labels)
	return labels, stand.GetAnnotations(), nil
}

// postRenderContext is the component's render context as a trait reads it, without what
// vela-agent fills in itself: the workload, the outputs, and the trait being rendered.
func postRenderContext(pctx process.Context) (*runtime.RawExtension, error) {
	file, err := pctx.BaseContextFile()
	if err != nil {
		return nil, err
	}
	v := cuecontext.New().CompileString(file).LookupPath(cue.ParsePath("context"))
	if err := v.Err(); err != nil {
		return nil, fmt.Errorf("reading the render context: %w", err)
	}
	var data map[string]interface{}
	if err := v.Decode(&data); err != nil {
		return nil, fmt.Errorf("reading the render context: %w", err)
	}
	for _, k := range []string{"output", "outputs", velaprocess.ContextTraitType} {
		delete(data, k)
	}
	raw, err := json.Marshal(data)
	if err != nil {
		return nil, err
	}
	return &runtime.RawExtension{Raw: raw}, nil
}
