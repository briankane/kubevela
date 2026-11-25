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
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/oam-dev/kubevela/apis/core.oam.dev/v1alpha1"
	"github.com/oam-dev/kubevela/apis/core.oam.dev/v1beta1"
	"github.com/oam-dev/kubevela/pkg/appfile"
)

// agentWaitFixture is an Application whose one dispatched Component vela-agent has not reported
// on, after the full reconcile that dispatched it.
type agentWaitFixture struct {
	t     *testing.T
	cli   client.Client
	r     *Reconciler
	app   *v1beta1.Application
	comp  *v1alpha1.Component
	reads *componentGetCounter
}

func newAgentWait(t *testing.T) *agentWaitFixture {
	f := &agentWaitFixture{t: t}
	target := componentTarget{app: "shop", appNamespace: "default", component: "web", cluster: "spoke", namespace: "prod"}
	f.comp = &v1alpha1.Component{
		ObjectMeta: metav1.ObjectMeta{Name: target.name(), Namespace: "prod"},
		Spec:       v1alpha1.ComponentSpec{Revision: 2},
		Status:     v1alpha1.ComponentStatus{ObservedRevision: 1},
	}
	scheme := runtime.NewScheme()
	require.NoError(t, v1alpha1.AddToScheme(scheme))
	f.cli = fake.NewClientBuilder().WithScheme(scheme).WithObjects(f.comp).WithStatusSubresource(f.comp).Build()
	require.NoError(t, f.cli.Status().Update(context.Background(), f.comp))
	require.NoError(t, f.cli.Get(context.Background(), client.ObjectKeyFromObject(f.comp), f.comp))
	f.app = &v1beta1.Application{ObjectMeta: metav1.ObjectMeta{
		Name: "shop", Namespace: "default", UID: "uid-1", Generation: 3, Annotations: map[string]string{"team": "a"},
	}}
	f.reads = &componentGetCounter{Reader: f.cli}
	f.r = &Reconciler{
		ClusterReader:                f.reads,
		ComponentAgentRenderInterval: 5 * time.Second,
		ComponentAgentHealthInterval: time.Second,
		agentWaits:                   &sync.Map{},
	}
	f.fullReconcile(800 * time.Millisecond)
	return f
}

// fullReconcile reports the Component unhealthy, as a full reconcile does, and returns its requeue.
func (f *agentWaitFixture) fullReconcile(backoff time.Duration) time.Duration {
	h := &AppHandler{app: f.app, agentRenderInterval: f.r.ComponentAgentRenderInterval}
	target := componentTarget{app: "shop", appNamespace: "default", component: "web", cluster: "spoke", namespace: "prod"}
	// As deliver leaves them: the reconcile's copy carries the status it read, the Component it
	// built and dispatched carries neither status nor resourceVersion.
	h.rememberComponent(target, f.comp)
	built := f.comp.DeepCopy()
	built.ResourceVersion, built.Status = "", v1alpha1.ComponentStatus{}
	h.reportComponentHealth(&appfile.Component{Name: "web", FullTemplate: &appfile.Template{}}, "spoke", target,
		[]deliveredComponent{{target: target, component: built}})
	return f.r.requeueOnAgent(f.app, h, backoff)
}

// healthOnly is whether the next reconcile of the Application reads only its Components.
func (f *agentWaitFixture) healthOnly() bool {
	return f.r.waitOnlyOnAgent(context.Background(), f.app.DeepCopy())
}

func TestChecksHealthOnlyUntilVelaAgentReports(t *testing.T) {
	f := newAgentWait(t)
	require.True(t, f.healthOnly(), "nothing changed")
	require.EqualValues(t, 1, f.reads.gets.Load(), "one read of the Component, nothing else")
	require.True(t, f.healthOnly())

	f.comp.Status.ObservedRevision = 2
	require.NoError(t, f.cli.Status().Update(context.Background(), f.comp))
	require.False(t, f.healthOnly(), "vela-agent reported")
	require.False(t, f.healthOnly(), "the wait is forgotten until a full reconcile remembers it again")
}

func TestRequeuesForAHealthCheckWhileWaitingOnVelaAgent(t *testing.T) {
	f := newAgentWait(t)
	require.Equal(t, time.Second, f.fullReconcile(800*time.Millisecond), "the health interval")
	require.True(t, f.healthOnly())

	require.Equal(t, 40*time.Second, f.fullReconcile(40*time.Second), "a long wait is paced by the backoff alone")
	require.False(t, f.healthOnly(), "and remembers nothing")
}

func TestRendersAgainWhenTheApplicationChanges(t *testing.T) {
	for name, change := range map[string]func(*v1beta1.Application){
		"its spec":        func(a *v1beta1.Application) { a.Generation++ },
		"an annotation":   func(a *v1beta1.Application) { a.Annotations["app.oam.dev/restart-workflow"] = "true" },
		"recreated":       func(a *v1beta1.Application) { a.UID = "uid-2" },
		"being deleted":   func(a *v1beta1.Application) { a.DeletionTimestamp = &metav1.Time{Time: time.Now()} },
		"nothing it read": nil,
	} {
		t.Run(name, func(t *testing.T) {
			f := newAgentWait(t)
			if change == nil {
				require.True(t, f.healthOnly())
				return
			}
			change(f.app)
			require.False(t, f.healthOnly())
		})
	}
}

func TestRendersAgainEveryRenderInterval(t *testing.T) {
	f := newAgentWait(t)
	f.r.ComponentAgentRenderInterval = time.Millisecond
	f.fullReconcile(0)
	time.Sleep(5 * time.Millisecond)
	require.False(t, f.healthOnly())
}

func TestForgetsAWaitWhenTheApplicationIsGone(t *testing.T) {
	f := newAgentWait(t)
	f.r.forgetAgentWait(client.ObjectKeyFromObject(f.app))
	require.False(t, f.healthOnly())
}

func TestRendersEveryTimeWithoutDispatch(t *testing.T) {
	f := newAgentWait(t)
	f.r.agentWaits = nil
	require.Equal(t, 5*time.Second, f.fullReconcile(800*time.Millisecond), "the render interval alone")
	require.False(t, f.healthOnly())
}
