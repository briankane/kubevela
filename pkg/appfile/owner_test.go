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
	"testing"

	"github.com/kubevela/pkg/cue/cuex"
	"github.com/kubevela/workflow/pkg/cue/process"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/oam-dev/kubevela/apis/core.oam.dev/v1beta1"
	"github.com/oam-dev/kubevela/pkg/cue/definition"
	velaprocess "github.com/oam-dev/kubevela/pkg/cue/process"
)

// ownerEngine records the owner each Complete ran with.
type ownerEngine struct {
	definition.AbstractEngine
	seen *[]*cuex.Owner
}

func (e ownerEngine) Complete(ctx process.Context, _ string, _ interface{}) error {
	if o, ok := cuex.OwnerFrom(ctx.GetCtx()); ok {
		*e.seen = append(*e.seen, &o)
	} else {
		*e.seen = append(*e.seen, nil)
	}
	return nil
}

func TestEvalContextCompilesAsTheDefinition(t *testing.T) {
	stamp := map[string]string{"cue.oam.dev/writer": "alice", "cue.oam.dev/writer-groups": "leads,system:authenticated"}
	var seen []*cuex.Owner
	comp := &Component{
		Name: "web",
		FullTemplate: &Template{ComponentDefinition: &v1beta1.ComponentDefinition{
			ObjectMeta: metav1.ObjectMeta{Name: "webservice", Namespace: "team-a", Annotations: stamp}}},
		engine: ownerEngine{seen: &seen},
	}
	tr := &Trait{
		Name: "scaler",
		FullTemplate: &Template{TraitDefinition: &v1beta1.TraitDefinition{
			ObjectMeta: metav1.ObjectMeta{Name: "scaler", Namespace: "vela-system"}}},
		engine: ownerEngine{seen: &seen},
	}
	bare := &Trait{Name: "from-a-file", FullTemplate: &Template{}, engine: ownerEngine{seen: &seen}}

	base := context.WithValue(context.Background(), struct{}{}, "base")
	pCtx := velaprocess.NewContext(velaprocess.ContextData{Ctx: base})
	require.NoError(t, comp.EvalContext(pCtx))
	require.NoError(t, tr.EvalContext(pCtx))
	require.NoError(t, bare.EvalContext(pCtx))

	require.Equal(t, []*cuex.Owner{
		{Kind: "ComponentDefinition", Namespace: "team-a", Name: "webservice",
			Writer: &cuex.Writer{User: "alice", Groups: []string{"leads", "system:authenticated"}}},
		{Kind: "TraitDefinition", Namespace: "vela-system", Name: "scaler"},
		nil,
	}, seen)
	require.Equal(t, base, pCtx.GetCtx(), "the process context keeps its own context afterwards")
}
