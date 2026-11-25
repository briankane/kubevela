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
	"crypto/ed25519"
	"crypto/rand"
	"testing"

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	kstatus "sigs.k8s.io/cli-utils/pkg/kstatus/status"

	"github.com/oam-dev/kubevela/apis/core.oam.dev/v1alpha1"
	"github.com/oam-dev/kubevela/pkg/componentdispatch"
)

// stage is one condition as a test expects it: status and reason.
type stage struct {
	status metav1.ConditionStatus
	reason string
}

var (
	done       = func(reason string) stage { return stage{metav1.ConditionTrue, reason} }
	notDone    = func(reason string) stage { return stage{metav1.ConditionFalse, reason} }
	notReached = stage{metav1.ConditionUnknown, ReasonNotReached}
)

// stages is every condition the Component reports, by type.
func (h *hub) stages(name string) map[string]stage {
	c := h.get(name)
	out := map[string]stage{}
	for _, cond := range c.Status.Conditions {
		require.Equal(h.t, c.Generation, cond.ObservedGeneration, "%s describes the current generation", cond.Type)
		out[cond.Type] = stage{cond.Status, cond.Reason}
	}
	return out
}

// kstatusOf is what kstatus makes of the Component.
func (h *hub) kstatusOf(name string) kstatus.Status {
	obj, err := runtime.DefaultUnstructuredConverter.ToUnstructured(h.get(name))
	require.NoError(h.t, err)
	res, err := kstatus.Compute(&unstructured.Unstructured{Object: obj})
	require.NoError(h.t, err)
	return res.Status
}

func TestReportsEveryStageOfAReadyComponent(t *testing.T) {
	h := newHub(t)
	h.dispatch(h.component("backend", 1, ready(h, "yes"), nil))
	h.settle("backend")

	require.Equal(t, map[string]stage{
		ConditionVerified:        done(ConditionVerified),
		ConditionApplied:         done(ConditionApplied),
		ConditionWorkloadHealthy: done(ConditionWorkloadHealthy),
		ConditionPostRendered:    done(ReasonNoneDeclared),
		ConditionTraitsHealthy:   done(ReasonNoneDeclared),
		ConditionCollected:       done(ConditionCollected),
		ConditionHealthy:         done(ConditionHealthy),
		ConditionReady:           done(ConditionReady),
		ConditionReconciling:     notDone(ConditionReady),
		ConditionStalled:         notDone(ConditionReady),
	}, h.stages("backend"))
	c := h.get("backend")
	require.Equal(t, c.Generation, c.Status.ObservedGeneration)
	require.Equal(t, kstatus.CurrentStatus, h.kstatusOf("backend"))
}

func TestReportsAnUnhealthyWorkloadAsReconciling(t *testing.T) {
	h := newHub(t)
	h.dispatch(h.component("backend", 1, ready(h, "no"), nil))
	h.settle("backend")

	require.Equal(t, map[string]stage{
		ConditionVerified:        done(ConditionVerified),
		ConditionApplied:         done(ConditionApplied),
		ConditionWorkloadHealthy: notDone(ReasonWorkloadUnhealthy),
		ConditionPostRendered:    notReached,
		ConditionTraitsHealthy:   notReached,
		ConditionCollected:       notReached,
		ConditionHealthy:         notDone(ReasonWorkloadUnhealthy),
		ConditionReady:           notDone(ReasonWorkloadUnhealthy),
		ConditionReconciling:     done(ReasonWorkloadUnhealthy),
		ConditionStalled:         notDone(ReasonWorkloadUnhealthy),
	}, h.stages("backend"))
	ready := h.condition("backend", ConditionReady)
	require.Contains(t, ready.Message, "ready: no", "the workload's health message")
	require.Equal(t, kstatus.InProgressStatus, h.kstatusOf("backend"))
}

func TestARefusalLeavesNothingTrueFromTheLastRevision(t *testing.T) {
	h := newHub(t)
	h.dispatch(h.component("backend", 1, ready(h, "yes"), nil))
	h.settle("backend")
	require.Equal(t, kstatus.CurrentStatus, h.kstatusOf("backend"))

	_, other, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	trusted := h.signer
	h.signer = componentdispatch.Signer{KeyID: "unknown", Key: other}
	h.dispatch(h.component("backend", 2, ready(h, "yes"), nil))
	h.signer = trusted
	h.settle("backend")

	require.Equal(t, map[string]stage{
		ConditionVerified:        notDone(ReasonUntrustedKey),
		ConditionApplied:         notReached,
		ConditionWorkloadHealthy: notReached,
		ConditionPostRendered:    notReached,
		ConditionTraitsHealthy:   notReached,
		ConditionCollected:       notReached,
		ConditionHealthy:         notReached,
		ConditionReady:           notDone(ReasonUntrustedKey),
		ConditionReconciling:     notDone(ReasonUntrustedKey),
		ConditionStalled:         done(ReasonUntrustedKey),
	}, h.stages("backend"))
	require.Equal(t, kstatus.FailedStatus, h.kstatusOf("backend"))
}

func TestReportsAStaleTrackerAsStalled(t *testing.T) {
	h := newHub(t)
	h.dispatch(h.component("backend", 1, ready(h, "yes"), nil))
	h.settle("backend")
	c := h.get("backend")
	c.Finalizers = nil
	require.NoError(t, testClient.Update(context.Background(), c))
	require.NoError(t, testClient.Delete(context.Background(), c))
	h.dispatch(h.component("backend", 2, ready(h, "yes"), nil))
	h.settle("backend")

	got := h.stages("backend")
	require.Equal(t, done(ConditionVerified), got[ConditionVerified], "signature and target are checked first")
	require.Equal(t, notDone(ReasonStaleTracker), got[ConditionApplied])
	require.Equal(t, notReached, got[ConditionWorkloadHealthy])
	require.Equal(t, done(ReasonStaleTracker), got[ConditionStalled])
	require.Equal(t, kstatus.FailedStatus, h.kstatusOf("backend"))
}

func TestReportsEachPostRenderStage(t *testing.T) {
	h := newHub(t)
	h.dispatch(h.component("backend", 1, ready(h, "yes"), announce(h, announceTemplate)))
	h.settle("backend")
	got := h.stages("backend")
	require.Equal(t, done(ConditionPostRendered), got[ConditionPostRendered])
	require.Equal(t, done(ConditionTraitsHealthy), got[ConditionTraitsHealthy])
	require.Equal(t, done(ConditionReady), got[ConditionReady])

	unhealthyTrait := func(c *v1alpha1.Component) {
		announce(h, announceTemplate)(c)
		c.Spec.Health.Traits[0].HealthPolicy = `isHealth: false`
		c.Spec.Health.Traits[0].CustomStatus = `message: "not yet"`
	}
	h.dispatch(h.component("backend", 2, ready(h, "yes"), unhealthyTrait))
	h.settle("backend")
	got = h.stages("backend")
	require.Equal(t, done(ConditionWorkloadHealthy), got[ConditionWorkloadHealthy])
	require.Equal(t, done(ConditionPostRendered), got[ConditionPostRendered])
	require.Equal(t, notDone(ReasonTraitUnhealthy), got[ConditionTraitsHealthy])
	require.Equal(t, notDone(ReasonTraitUnhealthy), got[ConditionHealthy])
	require.Equal(t, notDone(ReasonTraitUnhealthy), got[ConditionReady])
	require.Contains(t, h.condition("backend", ConditionReady).Message, "announce: not yet", "names the trait")
	require.Equal(t, kstatus.InProgressStatus, h.kstatusOf("backend"))

	h.dispatch(h.component("backend", 3, ready(h, "yes"), announce(h, `
import "vela/kube"

outputs: notice: {}
`)))
	h.settle("backend")
	got = h.stages("backend")
	require.Equal(t, notDone("RenderFailed"), got[ConditionPostRendered])
	require.Equal(t, notReached, got[ConditionTraitsHealthy])
	require.Equal(t, done("RenderFailed"), got[ConditionStalled])
	require.Equal(t, kstatus.FailedStatus, h.kstatusOf("backend"))
}

func TestReportsATombstoneAsDeleting(t *testing.T) {
	h := newHub(t)
	h.dispatch(h.component("backend", 1, ready(h, "yes"), nil))
	h.settle("backend")
	h.dispatch(h.component("backend", 2, nil, func(c *v1alpha1.Component) {
		c.Spec.Delete = true
		c.Spec.Payload = nil
		c.Spec.Health = v1alpha1.ComponentHealth{}
		c.Spec.Observe = nil
	}))
	h.settle("backend")

	got := h.stages("backend")
	require.Equal(t, done(ConditionVerified), got[ConditionVerified])
	require.Equal(t, notDone(ReasonDeleted), got[ConditionApplied])
	require.Equal(t, notReached, got[ConditionWorkloadHealthy])
	require.Equal(t, notDone(ReasonDeleted), got[ConditionReady])
	require.Equal(t, notDone(ReasonDeleted), got[ConditionStalled])
}
