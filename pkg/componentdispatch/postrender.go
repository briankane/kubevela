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
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"cuelang.org/go/cue/ast"
	"cuelang.org/go/cue/cuecontext"
	"cuelang.org/go/cue/parser"
	"github.com/kubevela/workflow/pkg/cue/model"
	"github.com/kubevela/workflow/pkg/cue/process"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"

	"github.com/oam-dev/kubevela/apis/core.oam.dev/v1alpha1"
	"github.com/oam-dev/kubevela/pkg/cue/definition"
	velaprocess "github.com/oam-dev/kubevela/pkg/cue/process"
	"github.com/oam-dev/kubevela/pkg/definition/inherit"
	"github.com/oam-dev/kubevela/pkg/oam"
	"github.com/oam-dev/kubevela/pkg/oam/util"
)

// Roles name a resource in a Component's index the way health CUE refers to it.
const (
	RoleOutput        = "output"
	RoleOutputsPrefix = "outputs."
)

// Component conditions and reasons the hub reads as well as vela-agent.
const (
	// ConditionPostRendered reports whether vela-agent rendered the PostDispatch traits.
	ConditionPostRendered = "PostRendered"
	// ConditionReady is True once every stage of the revision has passed; otherwise its reason
	// and message are the first stage's that has not.
	ConditionReady = "Ready"
	// ReasonWorkloadUnhealthy and ReasonTraitUnhealthy are a stage held up by health, which the
	// health report already words.
	ReasonWorkloadUnhealthy = "WorkloadUnhealthy"
	ReasonTraitUnhealthy    = "TraitUnhealthy"
)

// PostRendered is one resource a PostDispatch trait rendered, with how the Component's
// index records it.
type PostRendered struct {
	Object     *unstructured.Unstructured
	Role       string
	Definition v1alpha1.ComponentResourceDefinition
}

// standardLibrary is the CUE standard library's top-level packages, the only imports a
// PostDispatch trait rendered by vela-agent may use.
var standardLibrary = map[string]bool{
	"crypto": true, "encoding": true, "html": true, "list": true, "math": true, "net": true, "path": true,
	"regexp": true, "strconv": true, "strings": true, "struct": true, "text": true, "time": true, "uuid": true,
}

// CheckPostRenderable refuses templates vela-agent will not render: one that imports anything
// beyond the CUE standard library (provider packages run with vela-agent's permissions), or that
// declares processing (an HTTP call from the cluster).
func CheckPostRenderable(templates []v1alpha1.ComponentTemplate) error {
	for _, t := range templates {
		f, err := parser.ParseFile(t.Name, t.Template)
		if err != nil {
			return fmt.Errorf("parsing %s: %w", t.Name, err)
		}
		for _, spec := range f.Imports {
			path, err := strconv.Unquote(spec.Path.Value)
			if err != nil {
				return fmt.Errorf("%s: import %s: %w", t.Name, spec.Path.Value, err)
			}
			if !standardLibrary[strings.SplitN(path, "/", 2)[0]] {
				return fmt.Errorf("%s imports %q: vela-agent renders PostDispatch traits with the CUE standard library only", t.Name, path)
			}
		}
		for _, decl := range f.Decls {
			if field, ok := decl.(*ast.Field); ok {
				if name, _, _ := ast.LabelName(field.Label); name == "processing" {
					return fmt.Errorf("%s uses processing: vela-agent does not make calls for a PostDispatch trait", t.Name)
				}
			}
		}
	}
	return nil
}

// PostRender renders a Component's PostDispatch traits against the workload vela-agent applied,
// with its live status, and the other outputs it applied. It renders as the hub renders a trait:
// the same context, template engine, labels and naming, then the metadata the hub shipped.
func PostRender(ctx context.Context, spec *v1alpha1.ComponentPostRender, workload *unstructured.Unstructured,
	outputs map[string]*unstructured.Unstructured) ([]PostRendered, error) {
	data, err := decodeObject(spec.Context)
	if err != nil {
		return nil, fmt.Errorf("context: %w", err)
	}
	str := func(k string) string { s, _ := data[k].(string); return s }
	pctx := process.NewContext(process.ContextData{Ctx: ctx, Data: data, Name: str(velaprocess.ContextName),
		Namespace: str(velaprocess.ContextNamespace), WorkflowName: str(velaprocess.ContextWorkflowName),
		PublishVersion: str(velaprocess.ContextPublishVersion)})
	cctx := cuecontext.New()
	base, err := model.NewBase(cctx.Encode(workload.Object))
	if err != nil {
		return nil, err
	}
	if err := pctx.SetBase(base); err != nil {
		return nil, err
	}
	for name, obj := range outputs {
		ins, err := model.NewOther(cctx.Encode(obj.Object))
		if err != nil {
			return nil, err
		}
		if err := pctx.AppendAuxiliaries(process.Auxiliary{Ins: ins, Name: name}); err != nil {
			return nil, err
		}
	}

	var out []PostRendered
	for _, trait := range spec.Traits {
		if len(trait.Templates) == 0 {
			return nil, fmt.Errorf("trait %s has no template", trait.Type)
		}
		if err := CheckPostRenderable(trait.Templates); err != nil {
			return nil, err
		}
		rendered, err := renderTrait(pctx, trait)
		if err != nil {
			return nil, err
		}
		for _, obj := range rendered {
			finish(obj, trait, spec.Namespace, str(velaprocess.ContextNamespace))
			out = append(out, PostRendered{Object: obj, Role: RoleOutputsPrefix + obj.GetLabels()[oam.TraitResource],
				Definition: v1alpha1.ComponentResourceDefinition{Kind: v1alpha1.DefinitionTrait, Type: trait.Templates[0].Name}})
		}
	}
	return out, nil
}

// renderTrait completes one trait in pctx and returns its outputs with the labels and name the
// hub gives a trait's outputs before stamping its metadata.
func renderTrait(pctx process.Context, trait v1alpha1.ComponentPostRenderTrait) ([]*unstructured.Unstructured, error) {
	ancestors := make([]inherit.Level, 0, len(trait.Templates)-1)
	for _, t := range trait.Templates[1:] {
		ancestors = append(ancestors, inherit.Level{Name: t.Name, Template: t.Template})
	}
	own := trait.Templates[0]
	var params interface{}
	if trait.Parameter != nil && len(trait.Parameter.Raw) > 0 {
		p, err := decodeObject(trait.Parameter)
		if err != nil {
			return nil, fmt.Errorf("trait %s parameter: %w", own.Name, err)
		}
		params = p
	}
	_, before := pctx.Output()
	pctx.PushData(velaprocess.ContextTraitType, own.Name)
	engine := definition.NewTraitAbstractEngineWithTypeLabel(own.Name, trait.Type, ancestors...)
	if err := engine.Complete(pctx, own.Template, params); err != nil {
		return nil, err
	}
	_, after := pctx.Output()
	objs, err := definition.TraitOutputs(pctx, after[len(before):])
	if err != nil {
		return nil, fmt.Errorf("trait %s %w", own.Name, err)
	}
	component, _ := pctx.GetData(velaprocess.ContextName).(string)
	for _, obj := range objs {
		util.NameTraitOutput(obj, component)
	}
	return objs, nil
}

// finish stamps what the hub stamps on a trait's output after naming it: its contract labels,
// the annotations it inherits less the filtered keys, then the namespace.
func finish(obj *unstructured.Unstructured, trait v1alpha1.ComponentPostRenderTrait, override, contextNamespace string) {
	util.AddLabels(obj, trait.Labels)
	util.InheritAnnotations(obj, trait.Annotations, trait.FilterAnnotationKeys)
	if override != "" {
		obj.SetNamespace(override)
		return
	}
	util.SetDefaultNamespace(obj, contextNamespace)
}

// decodeObject reads a JSON object keeping numbers as written, so an integer in the context or
// a parameter stays an integer in CUE.
func decodeObject(raw *runtime.RawExtension) (map[string]interface{}, error) {
	out := map[string]interface{}{}
	if raw == nil || len(raw.Raw) == 0 {
		return out, nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw.Raw))
	dec.UseNumber()
	if err := dec.Decode(&out); err != nil {
		return nil, err
	}
	return out, nil
}
