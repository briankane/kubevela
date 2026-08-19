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

// A read has to say where the value went, not just what was read. Once a
// property is assembled from more than one source, "which field did I read" on
// its own cannot be mapped back to anything.
func TestConsumedReadsCarryTheDestinationProperty(t *testing.T) {
	r := require.New(t)
	h := handlerFor(nil, v1beta1.ApplicationSource{Name: "db", Type: "dbinfo"})

	h.recordSourceResolution(sourceKindComponent, "web", "webservice", "local",
		map[string]cuedefinition.SourceResolutionStatus{
			"db": {
				Name:  "db",
				Phase: sourcePhaseResolved,
				// host is assembled from two fields; image from one.
				ConsumedFields: map[string]interface{}{"addr": "db.internal", "port": 5432, "img": "pg:16"},
				Reads: []cuedefinition.SourceRead{
					{SourceAttr: "addr", Property: "host", Value: "db.internal"},
					{SourceAttr: "port", Property: "host", Value: 5432},
					{SourceAttr: "img", Property: "image", Value: "pg:16"},
				},
			},
		})

	values := h.sourceStatusList()[0].ConsumedBy[0].Values
	r.Len(values, 3)
	// Sorted by property then field, so the report is stable across reconciles
	// rather than following Go's map iteration order.
	r.Equal("host", values[0].Property)
	r.Equal("addr", values[0].SourceAttr)
	r.Equal("host", values[1].Property)
	r.Equal("port", values[1].SourceAttr)
	r.Equal("image", values[2].Property)
	r.Equal("img", values[2].SourceAttr)
	r.JSONEq(`"db.internal"`, string(values[0].Value.Raw))
	r.JSONEq(`5432`, string(values[1].Value.Raw))
}

// A chained source reads on its own behalf. Attributing those reads to whichever
// component triggered the chain would hide the chain and misreport the component.
func TestChainedSourceReadsAreNotClaimedByTheComponent(t *testing.T) {
	r := require.New(t)
	h := handlerFor(nil, v1beta1.ApplicationSource{Name: "atlas", Type: "atlas"})

	h.recordSourceResolution(sourceKindComponent, "web", "webservice", "local",
		map[string]cuedefinition.SourceResolutionStatus{
			"atlas": {
				Name:           "atlas",
				Phase:          sourcePhaseResolved,
				ConsumedFields: map[string]interface{}{"clusterName": "eu-west-1"},
				Reads: []cuedefinition.SourceRead{
					// read by the chained source "config", not by the component
					{SourceAttr: "clusterName", Property: "path", Value: "eu-west-1",
						ReaderKind: "source", ReaderName: "config"},
				},
			},
		})

	r.Empty(h.sourceStatusList()[0].ConsumedBy[0].Values,
		"the component made no reads of its own, so it must claim none")
}

// A sensitive field must stay redacted when the read is the struct above it.
// Marks are schema paths - "db.password" - and an expression may substitute a
// whole collection, so a read of "db" carries the password with it. Checking
// only whether the read sits at or below a mark misses that entirely and writes
// the secret into a status anyone with get on Applications can read.
func TestSensitiveValuesSurviveAWholeStructRead(t *testing.T) {
	r := require.New(t)
	h := handlerFor(nil, v1beta1.ApplicationSource{Name: "creds", Type: "dbcreds"})

	h.recordSourceResolution(sourceKindComponent, "web", "webservice", "local",
		map[string]cuedefinition.SourceResolutionStatus{
			"creds": {
				Name: "creds", Phase: sourcePhaseResolved,
				SensitivePaths: []string{"db.password", "members.token"},
				ConsumedFields: map[string]interface{}{"db": "x"},
				Reads: []cuedefinition.SourceRead{
					{SourceAttr: "db", Property: "settings", Value: map[string]interface{}{
						"host": "db.internal", "password": "hunter2",
					}},
					{SourceAttr: "members", Property: "team", Value: []interface{}{
						map[string]interface{}{"name": "ana", "token": "t-secret"},
					}},
				},
			},
		})

	values := h.sourceStatusList()[0].ConsumedBy[0].Values
	for _, rd := range values {
		raw := string(rd.Value.Raw)
		r.NotContains(raw, "hunter2", "a password under a read struct must not reach status")
		r.NotContains(raw, "t-secret", "a token inside a read list must not reach status either")
		r.Contains(raw, "***")
	}
	// Redaction is surgical: what was not marked still shows.
	r.Contains(string(values[0].Value.Raw)+string(values[1].Value.Raw), "db.internal")
	r.Contains(string(values[0].Value.Raw)+string(values[1].Value.Raw), "ana")
}
