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
	"testing"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	ctrlEvent "sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/oam-dev/kubevela/apis/core.oam.dev/v1beta1"
	"github.com/oam-dev/kubevela/pkg/componentdispatch"
	"github.com/oam-dev/kubevela/pkg/oam"
)

func signingKeys(active string, annotations map[string]string) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Namespace: "vela-system", Name: componentdispatch.SigningKeysSecret, Annotations: annotations},
		Data:       map[string][]byte{"active": []byte(active), active: make([]byte, 32)},
	}
}

// A new active key, or a key leaving, sends dispatched Applications to re-sign; the hub's own
// stamps and a request for a rotation do not.
func TestReSignsWhenTheActiveKeyChanges(t *testing.T) {
	update := func(old, new *corev1.Secret) bool {
		return activeKeyChanged().Update(ctrlEvent.TypedUpdateEvent[*corev1.Secret]{ObjectOld: old, ObjectNew: new})
	}
	require.True(t, update(signingKeys("hub-1", nil), signingKeys("hub-2", nil)))
	require.False(t, update(signingKeys("hub-1", nil), signingKeys("hub-1", map[string]string{componentdispatch.AnnotationRotateSigningKey: "now"})))
	revoked := signingKeys("hub-2", nil)
	withOld := signingKeys("hub-2", nil)
	withOld.Data["hub-1"] = make([]byte, 32)
	require.True(t, update(withOld, revoked), "a key leaving re-signs whatever it signed")
	require.False(t, update(revoked, withOld), "a key arriving signs nothing until it is active")
	require.False(t, activeKeyChanged().Create(ctrlEvent.TypedCreateEvent[*corev1.Secret]{Object: signingKeys("hub-1", nil)}), "a new Secret has signed nothing yet")
}

func TestReSignsEveryDispatchedApplication(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, clientgoscheme.AddToScheme(scheme))
	require.NoError(t, v1beta1.AddToScheme(scheme))
	app := func(ns, name string, dispatched bool) client.Object {
		a := &v1beta1.Application{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name}}
		if dispatched {
			a.Annotations = map[string]string{oam.AnnotationDispatch: oam.DispatchComponent}
		}
		return a
	}
	r := &Reconciler{Client: fake.NewClientBuilder().WithScheme(scheme).WithObjects(
		app("shop", "web", true), app("shop", "legacy", false), app("billing", "api", true)).Build()}

	requests := r.dispatchedApplications(context.Background(), signingKeys("hub-2", nil))
	require.ElementsMatch(t, []reconcile.Request{
		{NamespacedName: client.ObjectKey{Namespace: "shop", Name: "web"}},
		{NamespacedName: client.ObjectKey{Namespace: "billing", Name: "api"}},
	}, requests)
}
