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
	"context"
	"maps"
	"time"

	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/oam-dev/kubevela/apis/core.oam.dev/v1alpha1"
	"github.com/oam-dev/kubevela/apis/core.oam.dev/v1beta1"
	"github.com/oam-dev/kubevela/pkg/multicluster"
)

// agentWait is what a render left an Application waiting on: vela-agent's report on these
// Components. Until it lapses, a reconcile that finds the Application and the Components as
// they were reads only the Components.
type agentWait struct {
	uid         types.UID
	generation  int64
	annotations map[string]string
	until       time.Time
	components  []waitedComponent
}

// waitedComponent is a Component as the render left it: the revision it read or wrote, and the
// status vela-agent had last written.
type waitedComponent struct {
	cluster  string
	key      client.ObjectKey
	revision int64
	status   v1alpha1.ComponentStatus
}

// requeueOnAgent is when to reconcile app after a render that ran its workflow, from the
// workflow's backoff. While h found vela-agent yet to report and the next render is due within
// the render interval, it remembers what app waits on and returns the health interval; a longer
// wait is paced by the backoff alone, so a cluster whose vela-agent is down is not read every
// health interval.
func (r *Reconciler) requeueOnAgent(app *v1beta1.Application, h *AppHandler, backoff time.Duration) time.Duration {
	key := client.ObjectKeyFromObject(app)
	next := h.workflowRequeue(backoff)
	if r.agentWaits == nil {
		return next
	}
	if !h.waitingOnAgent.Load() || next > r.agentRenderInterval() || len(h.waitedOn) == 0 {
		r.forgetAgentWait(key)
		return next
	}
	r.agentWaits.Store(key, agentWait{
		uid:         app.UID,
		generation:  app.Generation,
		annotations: maps.Clone(app.Annotations),
		until:       time.Now().Add(next),
		components:  h.waitedOn,
	})
	return min(next, r.agentHealthInterval())
}

// waitOnlyOnAgent reads the Components app waits on, and reports whether nothing has changed
// that a render would see: app as it was when the wait was remembered, each Component at the
// same revision with the same status, the wait not lapsed. Anything else forgets the wait, and
// the caller renders.
func (r *Reconciler) waitOnlyOnAgent(ctx context.Context, app *v1beta1.Application) bool {
	if r.agentWaits == nil {
		return false
	}
	key := client.ObjectKeyFromObject(app)
	v, ok := r.agentWaits.Load(key)
	if !ok {
		return false
	}
	w := v.(agentWait)
	if app.UID != w.uid || app.Generation != w.generation || !maps.Equal(app.Annotations, w.annotations) ||
		app.DeletionTimestamp != nil || !time.Now().Before(w.until) || r.ClusterReader == nil {
		r.forgetAgentWait(key)
		return false
	}
	for _, c := range w.components {
		live := &v1alpha1.Component{}
		if err := r.ClusterReader.Get(multicluster.ContextWithClusterName(ctx, c.cluster), c.key, live); err != nil ||
			live.Spec.Revision != c.revision || !equality.Semantic.DeepEqual(live.Status, c.status) {
			r.forgetAgentWait(key)
			return false
		}
	}
	return true
}

// forgetAgentWait drops what the Application named by key was waiting on.
func (r *Reconciler) forgetAgentWait(key client.ObjectKey) {
	if r.agentWaits != nil {
		r.agentWaits.Delete(key)
	}
}

func (r *Reconciler) agentRenderInterval() time.Duration {
	if r.ComponentAgentRenderInterval > 0 {
		return r.ComponentAgentRenderInterval
	}
	return defaultComponentAgentRenderInterval
}

func (r *Reconciler) agentHealthInterval() time.Duration {
	if r.ComponentAgentHealthInterval > 0 {
		return r.ComponentAgentHealthInterval
	}
	return defaultComponentAgentHealthInterval
}
