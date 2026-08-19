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
	"testing"

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/oam-dev/kubevela/apis/core.oam.dev/v1beta1"
	cuedefinition "github.com/oam-dev/kubevela/pkg/cue/definition"
	"github.com/oam-dev/kubevela/pkg/oam"
)

func handlerFor(annotations map[string]string, sources ...v1beta1.ApplicationSource) *AppHandler {
	return &AppHandler{app: &v1beta1.Application{
		ObjectMeta: metav1.ObjectMeta{Name: "app", Namespace: "default", Annotations: annotations},
		Spec:       v1beta1.ApplicationSpec{Sources: sources},
	}}
}

// The Application-level list answers "did my data arrive", once per binding
// rather than once per binding per component.
func TestSourceStatusListReportsEveryBindingOnce(t *testing.T) {
	r := require.New(t)
	h := handlerFor(nil,
		v1beta1.ApplicationSource{Name: "registry", Type: "configmap"},
		v1beta1.ApplicationSource{Name: "unread", Type: "configmap"},
	)

	// Two components read the same binding. That is one binding, two consumers.
	for _, comp := range []string{"web", "api"} {
		h.recordSourceResolution(sourceKindComponent, comp, "webservice", "local",
			map[string]cuedefinition.SourceResolutionStatus{
				"registry": {
					Name: "registry", Type: "configmap", Phase: sourcePhaseResolved,
					Config: "configmap-local-default-abc", ExpiresAt: "2026-08-19T15:00:00Z",
					ConsumedFields: map[string]interface{}{"data.image": "nginx:1.27"},
				},
			})
	}

	out := h.sourceStatusList()
	r.Len(out, 2, "one row per declared binding, in spec order")
	r.Equal("registry", out[0].Name)
	r.Equal(sourcePhaseResolved, out[0].Phase)
	r.Equal("configmap-local-default-abc", out[0].Config)
	r.Len(out[0].ConsumedBy, 2, "both readers recorded against the one binding")
	r.Equal(sourceKindComponent, out[0].ConsumedBy[0].DefinitionKind)
	r.Equal("webservice", out[0].ConsumedBy[0].Type)

	// A binding nothing read is reported as Unused rather than omitted; silence
	// would be ambiguous with a failure.
	r.Equal("unread", out[1].Name)
	r.Equal(sourcePhaseUnused, out[1].Phase)
	r.Empty(out[1].ConsumedBy)
}

// A workflow step has no status of its own to carry this, so it has to land here.
func TestSourceStatusListRecordsNonComponentReaders(t *testing.T) {
	r := require.New(t)
	h := handlerFor(nil, v1beta1.ApplicationSource{Name: "registry", Type: "configmap"})

	h.recordSourceResolution(sourceKindWorkflowStep, "notify", "notification", "",
		map[string]cuedefinition.SourceResolutionStatus{
			"registry": {Name: "registry", Phase: sourcePhaseResolved,
				ConsumedFields: map[string]interface{}{"data.channel": "#deploys"}},
		})

	out := h.sourceStatusList()
	r.Len(out[0].ConsumedBy, 1)
	r.Equal(sourceKindWorkflowStep, out[0].ConsumedBy[0].DefinitionKind)
	r.Equal("notify", out[0].ConsumedBy[0].Name)
	r.Empty(out[0].ConsumedBy[0].Cluster, "a workflow step is not placed in a cluster")
}

// A failure seen by any reader is the one worth surfacing, even if another
// reader resolved the same binding happily from cache.
func TestSourceStatusListPrefersAFailure(t *testing.T) {
	r := require.New(t)
	h := handlerFor(nil, v1beta1.ApplicationSource{Name: "registry", Type: "configmap"})

	h.recordSourceResolution(sourceKindComponent, "web", "webservice", "local",
		map[string]cuedefinition.SourceResolutionStatus{
			"registry": {Name: "registry", Phase: sourcePhaseResolved,
				ConsumedFields: map[string]interface{}{"data.image": "nginx"}},
		})
	h.recordSourceResolution(sourceKindComponent, "api", "webservice", "remote",
		map[string]cuedefinition.SourceResolutionStatus{
			"registry": {Name: "registry", Phase: sourcePhaseFailed, Message: "fetch timed out",
				ConsumedFields: map[string]interface{}{"data.image": "nginx"}},
		})

	out := h.sourceStatusList()
	r.Equal(sourcePhaseFailed, out[0].Phase)
	r.Equal("fetch timed out", out[0].Message)
}

func TestSourceStatusAutoUpdateIsResolvedNotDeclared(t *testing.T) {
	r := require.New(t)
	yes, no := true, false

	on := handlerFor(nil, v1beta1.ApplicationSource{Name: "a", AutoUpdate: &yes}).sourceStatusList()
	r.NotNil(on[0].AutoUpdate)
	r.True(*on[0].AutoUpdate)

	off := handlerFor(nil, v1beta1.ApplicationSource{Name: "a", AutoUpdate: &no}).sourceStatusList()
	r.False(*off[0].AutoUpdate)

	// A pin beats the binding. The bool says false; the message says why, since a
	// bool alone cannot distinguish pinned from opted-out from gate-off.
	pinned := handlerFor(map[string]string{oam.AnnotationPublishVersion: "v1"},
		v1beta1.ApplicationSource{Name: "a", AutoUpdate: &yes}).sourceStatusList()
	r.False(*pinned[0].AutoUpdate)
	r.Contains(pinned[0].Message, "publishVersion")
}
