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

// Package appkeeper builds a ResourceKeeper for an Application: it is the Application's
// implementation of the kind-agnostic library in pkg/resourcekeeper. It reads the resource
// policies from the Application's spec with pkg/policy, which merges several policies of one
// type, and passes them to the keeper as data.
package appkeeper

import (
	"context"
	"testing"

	"github.com/oam-dev/kubevela/apis/core.oam.dev/v1beta1"
	"github.com/oam-dev/kubevela/pkg/oam"
	"github.com/oam-dev/kubevela/pkg/utils/common"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

// Component reads find a producer's objects through ComponentResources, so what
// the keeper records when it dispatches has to carry what a read looks them up
// by: component, trait, placement and object reference. Recorded here through
// Dispatch, as the controller records it, rather than built by hand.
func TestComponentResourcesRecordsWhatReadsNeed(t *testing.T) {
	ctx := context.Background()
	cli := fake.NewClientBuilder().WithScheme(common.Scheme).Build()
	app := &v1beta1.Application{ObjectMeta: metav1.ObjectMeta{Name: "shop", Namespace: "shop", Generation: 1}}
	keeper, err := New(ctx, cli, app)
	require.NoError(t, err)

	object := func(kind, name string, labels map[string]string) *unstructured.Unstructured {
		u := &unstructured.Unstructured{}
		u.SetGroupVersionKind(corev1.SchemeGroupVersion.WithKind(kind))
		u.SetName(name)
		u.SetNamespace("shop")
		u.SetLabels(labels)
		return u
	}
	workload := object("ConfigMap", "db", map[string]string{oam.LabelAppComponent: "db"})
	svc := object("Service", "db-svc", map[string]string{
		oam.LabelAppComponent: "db", oam.TraitTypeLabel: "expose", oam.TraitResource: "svc"})
	require.NoError(t, keeper.Dispatch(ctx, []*unstructured.Unstructured{workload, svc}, nil))

	got := map[string]v1beta1.ManagedResource{}
	for _, mr := range keeper.ComponentResources("db") {
		got[mr.Name] = mr
	}
	require.Len(t, got, 2)
	for name, want := range map[string]struct{ kind, trait string }{"db": {"ConfigMap", ""}, "db-svc": {"Service", "expose"}} {
		mr := got[name]
		require.Equal(t, "db", mr.Component, name)
		require.Equal(t, want.trait, mr.Trait, name)
		require.Equal(t, "v1", mr.APIVersion, name)
		require.Equal(t, want.kind, mr.Kind, name)
		require.Equal(t, "shop", mr.Namespace, name)
		require.Contains(t, []string{"", "local"}, mr.Cluster, name)
		require.False(t, mr.Deleted, name)
	}

	// A read tells a trait's resource from the workload by this label, on the live object.
	live := &unstructured.Unstructured{}
	live.SetGroupVersionKind(corev1.SchemeGroupVersion.WithKind("Service"))
	require.NoError(t, cli.Get(ctx, client.ObjectKey{Namespace: "shop", Name: "db-svc"}, live))
	require.Equal(t, "svc", live.GetLabels()[oam.TraitResource])
}
