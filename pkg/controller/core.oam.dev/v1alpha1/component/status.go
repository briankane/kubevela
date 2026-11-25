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
	"fmt"
	"strings"

	kerrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/oam-dev/kubevela/apis/core.oam.dev/v1alpha1"
)

// stages are the stage conditions in the order a revision passes them.
var stages = []string{
	ConditionVerified, ConditionApplied, ConditionWorkloadHealthy,
	ConditionPostRendered, ConditionTraitsHealthy, ConditionCollected,
}

// stalledReasons are the reasons a stage cannot pass until something else changes: the hub's
// spec, this cluster's trust, or a tracker someone must delete.
var stalledReasons = map[string]bool{
	ReasonUnsigned: true, ReasonUntrustedKey: true, ReasonBadSignature: true, ReasonWrongCluster: true,
	ReasonStaleRevision: true, ReasonStaleTracker: true, ReasonApplyRejected: true,
	ReasonEvaluationFailed: true, ReasonRenderFailed: true, ReasonNoWorkload: true,
}

// More reasons on the stage conditions.
const (
	ReasonApplyFailed      = "ApplyFailed"
	ReasonApplyRejected    = "ApplyRejected"
	ReasonEvaluationFailed = "EvaluationFailed"
	ReasonRenderFailed     = "RenderFailed"
	ReasonNoWorkload       = "NoWorkload"
	ReasonCollecting       = "Collecting"
	ReasonDeleting         = "Deleting"
)

// writeStatus summarises the stages, then writes the status.
func (r *Reconciler) writeStatus(ctx context.Context, comp *v1alpha1.Component) error {
	summarise(comp)
	return r.Status().Update(ctx, comp)
}

// summarise completes the conditions from the stages this reconcile reached: every stage after
// the first that has not passed is NotReached, since it says nothing about this revision. Then
// it derives Healthy, and the Ready, Reconciling and Stalled conditions kstatus reads.
func summarise(comp *v1alpha1.Component) {
	comp.Status.ObservedGeneration = comp.Generation
	var stop *metav1.Condition
	for _, t := range stages {
		c := meta.FindStatusCondition(comp.Status.Conditions, t)
		if stop != nil || c == nil {
			setCondition(comp, t, metav1.ConditionUnknown, ReasonNotReached, "")
			if stop == nil {
				stop = meta.FindStatusCondition(comp.Status.Conditions, t).DeepCopy()
			}
			continue
		}
		if c.Status != metav1.ConditionTrue {
			stop = c.DeepCopy()
		}
	}

	workload := meta.FindStatusCondition(comp.Status.Conditions, ConditionWorkloadHealthy)
	traits := meta.FindStatusCondition(comp.Status.Conditions, ConditionTraitsHealthy)
	switch {
	case workload.Status == metav1.ConditionTrue && traits.Status == metav1.ConditionTrue:
		message := ""
		if comp.Status.Health != nil {
			message = comp.Status.Health.Message
		}
		setCondition(comp, ConditionHealthy, metav1.ConditionTrue, ConditionHealthy, message)
	case workload.Status == metav1.ConditionFalse:
		setCondition(comp, ConditionHealthy, metav1.ConditionFalse, workload.Reason, workload.Message)
	case traits.Status == metav1.ConditionFalse:
		setCondition(comp, ConditionHealthy, metav1.ConditionFalse, traits.Reason, traits.Message)
	default:
		setCondition(comp, ConditionHealthy, metav1.ConditionUnknown, ReasonNotReached, "")
	}

	if stop == nil {
		setCondition(comp, ConditionReady, metav1.ConditionTrue, ConditionReady, "")
		setCondition(comp, ConditionReconciling, metav1.ConditionFalse, ConditionReady, "")
		setCondition(comp, ConditionStalled, metav1.ConditionFalse, ConditionReady, "")
		return
	}
	setCondition(comp, ConditionReady, metav1.ConditionFalse, stop.Reason, stop.Message)
	stalled := stalledReasons[stop.Reason]
	// A collected tombstone has nothing left to do: the hub deletes it on seeing it observed.
	reconciling := !stalled && stop.Reason != ReasonDeleted
	setCondition(comp, ConditionReconciling, conditionStatus(reconciling), stop.Reason, stop.Message)
	setCondition(comp, ConditionStalled, conditionStatus(stalled), stop.Reason, stop.Message)
}

func conditionStatus(b bool) metav1.ConditionStatus {
	if b {
		return metav1.ConditionTrue
	}
	return metav1.ConditionFalse
}

// applyFailure is the reason for an apply that failed: rejected when the API server will refuse
// it again until the spec changes, failed otherwise.
func applyFailure(err error) string {
	if kerrors.IsInvalid(err) || kerrors.IsForbidden(err) || kerrors.IsBadRequest(err) {
		return ReasonApplyRejected
	}
	return ReasonApplyFailed
}

// setHealthStages records the workload's and the traits' health from comp.Status.Health, or the
// error evaluating it.
func setHealthStages(comp *v1alpha1.Component, err error) {
	if err != nil {
		setCondition(comp, ConditionWorkloadHealthy, metav1.ConditionUnknown, ReasonEvaluationFailed, err.Error())
		return
	}
	h := comp.Status.Health
	if h.WorkloadHealthy || h.Healthy {
		setCondition(comp, ConditionWorkloadHealthy, metav1.ConditionTrue, ConditionWorkloadHealthy, h.Message)
	} else {
		setCondition(comp, ConditionWorkloadHealthy, metav1.ConditionFalse, ReasonWorkloadUnhealthy, h.Message)
	}
	if len(comp.Spec.Health.Traits) == 0 {
		setCondition(comp, ConditionTraitsHealthy, metav1.ConditionTrue, ReasonNoneDeclared, "")
		return
	}
	var unhealthy []string
	for _, t := range h.Traits {
		if !t.Healthy {
			unhealthy = append(unhealthy, fmt.Sprintf("%s: %s", t.Type, t.Message))
		}
	}
	if len(unhealthy) > 0 {
		setCondition(comp, ConditionTraitsHealthy, metav1.ConditionFalse, ReasonTraitUnhealthy, strings.Join(unhealthy, "; "))
		return
	}
	setCondition(comp, ConditionTraitsHealthy, metav1.ConditionTrue, ConditionTraitsHealthy, "")
}
