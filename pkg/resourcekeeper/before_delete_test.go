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

package resourcekeeper

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	kerrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/oam-dev/kubevela/apis/core.oam.dev/v1alpha1"
	"github.com/oam-dev/kubevela/pkg/resourcetracker"
	"github.com/oam-dev/kubevela/pkg/utils/common"
)

func beforeDeleteConfigMap(name string) *unstructured.Unstructured {
	cm := &unstructured.Unstructured{}
	cm.SetAPIVersion("v1")
	cm.SetKind("ConfigMap")
	cm.SetNamespace("default")
	cm.SetName(name)
	return cm
}

// gate is a BeforeDelete hook that holds every delete until opened, and records what it saw.
type gate struct {
	open bool
	seen []string
}

func (g *gate) hook(_ context.Context, obj *unstructured.Unstructured) (bool, error) {
	g.seen = append(g.seen, obj.GetName())
	return g.open, nil
}

func owner(gen int64, deleting bool) resourcetracker.Tracked {
	return resourcetracker.NewBase(resourcetracker.Owner{Kind: "Hub", Namespace: "default", Name: "shop", UID: "uid-shop", Generation: gen, Deleting: deleting})
}

func getCM(t *testing.T, cli client.Client, name string) bool {
	err := cli.Get(context.Background(), client.ObjectKey{Namespace: "default", Name: name}, &corev1.ConfigMap{})
	if kerrors.IsNotFound(err) {
		return false
	}
	require.NoError(t, err)
	return true
}

func TestGarbageCollectionWaitsForBeforeDelete(t *testing.T) {
	defer func(p float64) { MarkWithProbability = p }(MarkWithProbability)
	MarkWithProbability = 1.0
	ctx := context.Background()
	cli := fake.NewClientBuilder().WithScheme(common.Scheme).Build()
	g := &gate{}
	keeper := func(gen int64) ResourceKeeper {
		rk, err := New(ctx, cli, owner(gen, false), Policies{}, Options{BeforeDelete: g.hook})
		require.NoError(t, err)
		return rk
	}

	require.NoError(t, keeper(1).Dispatch(ctx, []*unstructured.Unstructured{beforeDeleteConfigMap("kept"), beforeDeleteConfigMap("dropped")}, nil))
	require.NoError(t, keeper(2).Dispatch(ctx, []*unstructured.Unstructured{beforeDeleteConfigMap("kept")}, nil))

	for i := 0; i < 3; i++ {
		finished, _, err := keeper(2).GarbageCollect(ctx)
		require.NoError(t, err)
		require.False(t, finished, "not finished while the hook holds the delete")
		require.True(t, getCM(t, cli, "dropped"))
	}
	require.Contains(t, g.seen, "dropped")
	require.NotContains(t, g.seen, "kept", "only what is being deleted reaches the hook")

	g.open = true
	finished := false
	for i := 0; i < 5 && !finished; i++ {
		var err error
		finished, _, err = keeper(2).GarbageCollect(ctx)
		require.NoError(t, err)
	}
	require.True(t, finished)
	require.False(t, getCM(t, cli, "dropped"))
	require.True(t, getCM(t, cli, "kept"))
}

func TestExplicitDeleteReportsPending(t *testing.T) {
	ctx := context.Background()
	cli := fake.NewClientBuilder().WithScheme(common.Scheme).Build()
	g := &gate{}
	rk, err := New(ctx, cli, owner(1, false), Policies{}, Options{BeforeDelete: g.hook})
	require.NoError(t, err)
	require.NoError(t, rk.Dispatch(ctx, []*unstructured.Unstructured{beforeDeleteConfigMap("gone")}, nil))

	err = rk.Delete(ctx, []*unstructured.Unstructured{beforeDeleteConfigMap("gone")})
	require.True(t, errors.Is(err, ErrDeletePending), "%v", err)
	require.True(t, getCM(t, cli, "gone"))

	g.open = true
	require.NoError(t, rk.Delete(ctx, []*unstructured.Unstructured{beforeDeleteConfigMap("gone")}))
	require.False(t, getCM(t, cli, "gone"))
}

// Handing a shared resource to the next sharer deletes nothing, so it does not wait.
func TestBeforeDeleteSkipsSharedHandover(t *testing.T) {
	defer func(p float64) { MarkWithProbability = p }(MarkWithProbability)
	MarkWithProbability = 1.0
	ctx := context.Background()
	cli := fake.NewClientBuilder().WithScheme(common.Scheme).Build()
	shared := Policies{SharedResource: &v1alpha1.SharedResourcePolicySpec{Rules: []v1alpha1.SharedResourcePolicyRule{{
		Selector: v1alpha1.ResourcePolicyRuleSelector{ResourceTypes: []string{"ConfigMap"}},
	}}}}
	g := &gate{}
	other, err := New(ctx, cli, resourcetracker.NewBase(resourcetracker.Owner{Kind: "Hub", Namespace: "default", Name: "other", UID: "uid-other", Generation: 1}), shared, Options{})
	require.NoError(t, err)
	mine := func(gen int64, deleting bool) ResourceKeeper {
		rk, err := New(ctx, cli, owner(gen, deleting), shared, Options{BeforeDelete: g.hook})
		require.NoError(t, err)
		return rk
	}
	require.NoError(t, mine(1, false).Dispatch(ctx, []*unstructured.Unstructured{beforeDeleteConfigMap("both")}, nil))
	require.NoError(t, other.Dispatch(ctx, []*unstructured.Unstructured{beforeDeleteConfigMap("both")}, nil))

	finished := false
	for i := 0; i < 5 && !finished; i++ {
		finished, _, err = mine(1, true).GarbageCollect(ctx)
		require.NoError(t, err)
	}
	require.True(t, finished)
	require.Empty(t, g.seen)
	require.True(t, getCM(t, cli, "both"))
}
