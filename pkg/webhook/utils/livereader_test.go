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

package utils

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/oam-dev/kubevela/apis/core.oam.dev/v1beta1"
)

// counter records how often a client was asked to read, so a test can say not
// just what an answer was but where it came from.
type counter struct {
	client.Client
	gets, lists int
}

func (c *counter) Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	c.gets++
	return c.Client.Get(ctx, key, obj, opts...)
}

func (c *counter) List(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
	c.lists++
	return c.Client.List(ctx, list, opts...)
}

func componentDef(namespace, name string) *v1beta1.ComponentDefinition {
	return &v1beta1.ComponentDefinition{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name},
	}
}

func readerScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	require.NoError(t, v1beta1.AddToScheme(s))
	return s
}

// The cache answers whatever it can. Admission fires on every write, including
// the ones a controller makes while reconciling, so a live read per request is
// paid for by the whole write path.
func TestAHitInTheCacheNeverReachesTheAPIServer(t *testing.T) {
	s := readerScheme(t)
	cached := &counter{Client: fake.NewClientBuilder().WithScheme(s).
		WithObjects(componentDef("team-a", "base")).Build()}
	live := &counter{Client: fake.NewClientBuilder().WithScheme(s).
		WithObjects(componentDef("team-a", "base")).Build()}

	cli := ReadThroughCache(cached, live)

	require.NoError(t, cli.Get(context.Background(),
		types.NamespacedName{Namespace: "team-a", Name: "base"}, &v1beta1.ComponentDefinition{}))
	require.Equal(t, 1, cached.gets)
	require.Zero(t, live.gets, "the cache had the answer")
}

// A miss is the case the live read exists for: a parent written milliseconds
// before its child has not reached the cache, and refusing the child for naming
// a definition that is plainly there is the bug this fixes.
func TestAMissInTheCacheIsRetriedLive(t *testing.T) {
	s := readerScheme(t)
	cached := &counter{Client: fake.NewClientBuilder().WithScheme(s).Build()}
	live := &counter{Client: fake.NewClientBuilder().WithScheme(s).
		WithObjects(componentDef("team-a", "base")).Build()}

	cli := ReadThroughCache(cached, live)

	got := &v1beta1.ComponentDefinition{}
	require.NoError(t, cli.Get(context.Background(),
		types.NamespacedName{Namespace: "team-a", Name: "base"}, got))
	require.Equal(t, "base", got.Name)
	require.Equal(t, 1, live.gets)
}

// Listing revisions is how a pinned `@v1` is resolved, and an empty list is the
// same miss as a NotFound.
func TestAnEmptyListIsRetriedLiveAndANonEmptyOneIsNot(t *testing.T) {
	s := readerScheme(t)
	live := &counter{Client: fake.NewClientBuilder().WithScheme(s).
		WithObjects(componentDef("team-a", "base")).Build()}

	empty := &counter{Client: fake.NewClientBuilder().WithScheme(s).Build()}
	list := &v1beta1.ComponentDefinitionList{}
	require.NoError(t, ReadThroughCache(empty, live).List(context.Background(), list))
	require.Len(t, list.Items, 1)
	require.Equal(t, 1, live.lists)

	live.lists = 0
	full := &counter{Client: fake.NewClientBuilder().WithScheme(s).
		WithObjects(componentDef("team-a", "base")).Build()}
	list = &v1beta1.ComponentDefinitionList{}
	require.NoError(t, ReadThroughCache(full, live).List(context.Background(), list))
	require.Len(t, list.Items, 1)
	require.Zero(t, live.lists, "the cache had entries")
}

// With no live client there is nothing to fall back to, and the cached one is
// used unwrapped so a test supplying a fake gets exactly what it supplied.
func TestNoLiveClientMeansTheCachedOneIsUsedAsIs(t *testing.T) {
	cached := fake.NewClientBuilder().WithScheme(readerScheme(t)).Build()
	require.Equal(t, cached, ReadThroughCache(cached, nil))
}
