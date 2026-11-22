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

package appfile

import (
	"context"

	"github.com/kubevela/pkg/cue/cuex"
	"github.com/kubevela/pkg/cue/cuex/access"
	"github.com/kubevela/workflow/pkg/cue/process"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// Owner is the definition the template came from, as a CueX call check sees
// it, or nil for a template with no named definition, such as one read from a
// file.
func (t *Template) Owner() *cuex.Owner {
	if t == nil {
		return nil
	}
	var def client.Object
	var kind string
	switch {
	case t.ComponentDefinition != nil:
		def, kind = t.ComponentDefinition, "ComponentDefinition"
	case t.WorkloadDefinition != nil:
		def, kind = t.WorkloadDefinition, "WorkloadDefinition"
	case t.TraitDefinition != nil:
		def, kind = t.TraitDefinition, "TraitDefinition"
	case t.PolicyDefinition != nil:
		def, kind = t.PolicyDefinition, "PolicyDefinition"
	case t.WorkflowStepDefinition != nil:
		def, kind = t.WorkflowStepDefinition, "WorkflowStepDefinition"
	case t.SourceDefinition != nil:
		def, kind = t.SourceDefinition, "SourceDefinition"
	default:
		return nil
	}
	return OwnerOf(kind, def)
}

// OwnerOf is def as the owner of its template's compilations, or nil for one
// with no name, such as a definition read from a file.
func OwnerOf(kind string, def client.Object) *cuex.Owner {
	if def.GetName() == "" {
		return nil
	}
	return &cuex.Owner{Kind: kind, Namespace: def.GetNamespace(), Name: def.GetName(), Writer: access.WriterOf(def.GetAnnotations())}
}

// CompileAs returns ctx with owner as its compilations' owner, or ctx as it is
// for no owner.
func CompileAs(ctx context.Context, owner *cuex.Owner) context.Context {
	if owner == nil {
		return ctx
	}
	return cuex.WithOwner(ctx, *owner)
}

// asOwner has pCtx's compilations run as t's definition until the returned
// func puts its context back. Traits share their component's process context,
// so each sets its own owner for its render only.
func asOwner(pCtx process.Context, t *Template) func() {
	owner := t.Owner()
	if owner == nil {
		return func() {}
	}
	prev := pCtx.GetCtx()
	pCtx.SetCtx(CompileAs(prev, owner))
	return func() { pCtx.SetCtx(prev) }
}
