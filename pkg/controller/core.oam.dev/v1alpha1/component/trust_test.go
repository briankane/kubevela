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
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/oam-dev/kubevela/pkg/componentdispatch"
	"github.com/oam-dev/kubevela/pkg/monitor/metrics"
)

func trustNamespace(t *testing.T) string {
	ns := strings.ToLower(strings.ReplaceAll(t.Name(), "/", "-"))
	require.NoError(t, testClient.Create(context.Background(), &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}))
	return ns
}

func localOnly(context.Context) ([]string, error) { return nil, nil }

// writeOnly is the pusher's client in these tests. vela-core's client reads from a cache of
// the hub alone, whatever cluster the context names, so the pusher must read through Reader.
type writeOnly struct{ client.Client }

func (writeOnly) Get(context.Context, client.ObjectKey, client.Object, ...client.GetOption) error {
	return errors.New("the trust push must not read through the cached client")
}

func newPusher(ns string, events record.EventRecorder) *TrustPusher {
	return &TrustPusher{Client: writeOnly{testClient}, Reader: testClient, Namespace: ns, Clusters: localOnly, Recorder: events}
}

func TestPushedKeysAreWhatTheAgentTrusts(t *testing.T) {
	ctx, ns := context.Background(), trustNamespace(t)
	source := SecretKeySource{Reader: testClient, Namespace: ns}
	keys, err := source.TrustedKeys(ctx)
	require.NoError(t, err)
	require.Empty(t, keys, "nothing pushed yet: nothing trusted")

	pusher := newPusher(ns, record.NewFakeRecorder(10))
	require.NoError(t, pusher.Push(ctx))
	ring, err := componentdispatch.EnsureSigningKeys(ctx, testClient, ns)
	require.NoError(t, err)
	keys, err = source.TrustedKeys(ctx)
	require.NoError(t, err)
	require.Equal(t, ring.Trusted(), keys)

	rotated, err := componentdispatch.RotateSigningKey(ctx, testClient, ns)
	require.NoError(t, err)
	require.NoError(t, pusher.Push(ctx))
	keys, err = source.TrustedKeys(ctx)
	require.NoError(t, err)
	require.Equal(t, rotated.Trusted()[rotated.Active], keys[rotated.Active], "the new key is trusted once pushed")
}

func TestAKeyTheHubDidNotIssueIsRemoved(t *testing.T) {
	ctx, ns := context.Background(), trustNamespace(t)
	events := record.NewFakeRecorder(10)
	pusher := newPusher(ns, events)
	require.NoError(t, pusher.Push(ctx))

	trust := &corev1.Secret{}
	require.NoError(t, testClient.Get(ctx, client.ObjectKey{Namespace: ns, Name: componentdispatch.TrustSecret}, trust))
	intruder, _, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	trust.Data["hub-intruder"] = intruder
	require.NoError(t, testClient.Update(ctx, trust))

	require.NoError(t, pusher.Push(ctx))
	require.NoError(t, testClient.Get(ctx, client.ObjectKey{Namespace: ns, Name: componentdispatch.TrustSecret}, trust))
	require.NotContains(t, trust.Data, "hub-intruder")
	event := <-events.Events
	require.Contains(t, event, "Warning")
	require.Contains(t, event, "hub-intruder")
	require.Contains(t, event, "local")
}

// An operator asks for a rotation by annotating the signing keys Secret: the next push adds a
// key, makes it the active one and trusts both everywhere.
func TestRotatesTheSigningKeyWhenAsked(t *testing.T) {
	ctx, ns := context.Background(), trustNamespace(t)
	pusher := newPusher(ns, record.NewFakeRecorder(10))
	require.NoError(t, pusher.Push(ctx))
	before, err := componentdispatch.EnsureSigningKeys(ctx, testClient, ns)
	require.NoError(t, err)

	signing := &corev1.Secret{}
	require.NoError(t, testClient.Get(ctx, client.ObjectKey{Namespace: ns, Name: componentdispatch.SigningKeysSecret}, signing))
	metav1.SetMetaDataAnnotation(&signing.ObjectMeta, componentdispatch.AnnotationRotateSigningKey, "2026-11-22")
	require.NoError(t, testClient.Update(ctx, signing))
	require.NoError(t, pusher.Push(ctx))

	after, err := componentdispatch.EnsureSigningKeys(ctx, testClient, ns)
	require.NoError(t, err)
	require.NotEqual(t, before.Active, after.Active, "a new key signs")
	require.Contains(t, after.Keys, before.Active, "the old key stays trusted while Components carry it")
	require.NoError(t, testClient.Get(ctx, client.ObjectKey{Namespace: ns, Name: componentdispatch.SigningKeysSecret}, signing))
	require.NotContains(t, signing.Annotations, componentdispatch.AnnotationRotateSigningKey, "one request, one rotation")
	keys, err := SecretKeySource{Reader: testClient, Namespace: ns}.TrustedKeys(ctx)
	require.NoError(t, err)
	require.Len(t, keys, 2)
}

// A key that is no longer active is retired, and leaves every cluster's trust, once no
// Component is signed with it or carries values sealed to it.
func TestRetiresAKeyNoComponentCarries(t *testing.T) {
	h := newHub(t)
	ctx, ns := context.Background(), h.ns
	events := record.NewFakeRecorder(50)
	pusher := newPusher(ns, events)
	require.NoError(t, pusher.Push(ctx))
	old, err := componentdispatch.EnsureSigningKeys(ctx, testClient, ns)
	require.NoError(t, err)
	ring, err := componentdispatch.RotateSigningKey(ctx, testClient, ns)
	require.NoError(t, err)

	c, _ := h.component("backend", 1, ready(h, "yes"), nil)
	c.Spec.Signature.KeyID = old.Active
	require.NoError(t, testClient.Create(ctx, c))
	require.NoError(t, pusher.Push(ctx))
	current, err := componentdispatch.EnsureSigningKeys(ctx, testClient, ns)
	require.NoError(t, err)
	require.Contains(t, current.Keys, old.Active, "a Component is still signed with it")

	c = h.get("backend")
	c.Spec.Signature.KeyID = ring.Active
	require.NoError(t, testClient.Update(ctx, c))
	c.Status.SealedWith = old.Active
	require.NoError(t, testClient.Status().Update(ctx, c))
	require.NoError(t, pusher.Push(ctx))
	current, err = componentdispatch.EnsureSigningKeys(ctx, testClient, ns)
	require.NoError(t, err)
	require.Contains(t, current.Keys, old.Active, "values are still sealed to it")

	c = h.get("backend")
	c.Status.SealedWith = ring.Active
	require.NoError(t, testClient.Status().Update(ctx, c))
	require.NoError(t, pusher.Push(ctx))
	current, err = componentdispatch.EnsureSigningKeys(ctx, testClient, ns)
	require.NoError(t, err)
	require.NotContains(t, current.Keys, old.Active, "retired")
	keys, err := SecretKeySource{Reader: testClient, Namespace: ns}.TrustedKeys(ctx)
	require.NoError(t, err)
	require.NotContains(t, keys, old.Active, "and no longer trusted")
	close(events.Events)
	for event := range events.Events {
		require.NotContains(t, event, "Warning", "a key the hub retired is not foreign")
	}
}

// With a maximum key age, the push rotates a key that has been active for longer.
func TestRotatesAKeyOlderThanTheMaximumAge(t *testing.T) {
	ctx, ns := context.Background(), trustNamespace(t)
	events := record.NewFakeRecorder(10)
	pusher := newPusher(ns, events)
	require.NoError(t, pusher.Push(ctx))
	before, err := componentdispatch.EnsureSigningKeys(ctx, testClient, ns)
	require.NoError(t, err)

	signing := &corev1.Secret{}
	require.NoError(t, testClient.Get(ctx, client.ObjectKey{Namespace: ns, Name: componentdispatch.SigningKeysSecret}, signing))
	metav1.SetMetaDataAnnotation(&signing.ObjectMeta, componentdispatch.AnnotationActiveSince, time.Now().Add(-48*time.Hour).UTC().Format(time.RFC3339))
	require.NoError(t, testClient.Update(ctx, signing))
	require.NoError(t, pusher.Push(ctx))
	same, err := componentdispatch.EnsureSigningKeys(ctx, testClient, ns)
	require.NoError(t, err)
	require.Equal(t, before.Active, same.Active, "without a maximum age, keys rotate only when asked")

	pusher.MaxKeyAge = 24 * time.Hour
	require.NoError(t, pusher.Push(ctx))
	after, err := componentdispatch.EnsureSigningKeys(ctx, testClient, ns)
	require.NoError(t, err)
	require.NotEqual(t, before.Active, after.Active)
	require.Contains(t, <-events.Events, "RotatedSigningKey")
}

// A revoked key leaves every cluster's trust on the next push, even while Components carry it.
func TestRevokesAKeyAtOnce(t *testing.T) {
	h := newHub(t)
	ctx, ns := context.Background(), h.ns
	events := record.NewFakeRecorder(50)
	pusher := newPusher(ns, events)
	require.NoError(t, pusher.Push(ctx))
	old, err := componentdispatch.EnsureSigningKeys(ctx, testClient, ns)
	require.NoError(t, err)
	_, err = componentdispatch.RotateSigningKey(ctx, testClient, ns)
	require.NoError(t, err)
	c, _ := h.component("backend", 1, ready(h, "yes"), nil)
	c.Spec.Signature.KeyID = old.Active
	require.NoError(t, testClient.Create(ctx, c))
	require.NoError(t, pusher.Push(ctx))

	signing := &corev1.Secret{}
	require.NoError(t, testClient.Get(ctx, client.ObjectKey{Namespace: ns, Name: componentdispatch.SigningKeysSecret}, signing))
	metav1.SetMetaDataAnnotation(&signing.ObjectMeta, componentdispatch.AnnotationRevokeSigningKey, old.Active)
	require.NoError(t, testClient.Update(ctx, signing))
	require.NoError(t, pusher.Push(ctx))

	keys, err := SecretKeySource{Reader: testClient, Namespace: ns}.TrustedKeys(ctx)
	require.NoError(t, err)
	require.NotContains(t, keys, old.Active, "no longer trusted, though a Component carries it")
	close(events.Events)
	var reasons []string
	for event := range events.Events {
		require.NotContains(t, event, "ForeignTrustedKey", "a key the hub revoked is not foreign")
		reasons = append(reasons, event)
	}
	require.Contains(t, strings.Join(reasons, "\n"), "RevokedSigningKey")
}

// A key one leader removed and did not push before it stopped is no more foreign to the next.
func TestAKeyAnEarlierLeaderRemovedIsNotForeign(t *testing.T) {
	ctx, ns := context.Background(), trustNamespace(t)
	require.NoError(t, newPusher(ns, record.NewFakeRecorder(10)).Push(ctx))
	old, err := componentdispatch.EnsureSigningKeys(ctx, testClient, ns)
	require.NoError(t, err)
	_, err = componentdispatch.RotateSigningKey(ctx, testClient, ns)
	require.NoError(t, err)
	require.NoError(t, newPusher(ns, record.NewFakeRecorder(10)).Push(ctx))
	require.NoError(t, componentdispatch.RetireSigningKey(ctx, testClient, ns, old.Active))

	events := record.NewFakeRecorder(10)
	require.NoError(t, newPusher(ns, events).Push(ctx))
	keys, err := SecretKeySource{Reader: testClient, Namespace: ns}.TrustedKeys(ctx)
	require.NoError(t, err)
	require.NotContains(t, keys, old.Active)
	close(events.Events)
	for event := range events.Events {
		require.NotContains(t, event, "ForeignTrustedKey")
	}
	signing := &corev1.Secret{}
	require.NoError(t, testClient.Get(ctx, client.ObjectKey{Namespace: ns, Name: componentdispatch.SigningKeysSecret}, signing))
	require.NotContains(t, signing.Annotations, componentdispatch.AnnotationRemovedKeys, "forgotten once every cluster has dropped it")
}

// More than two keys means a rotation started before the last one retired its key: the push
// warns, naming the keys and what holds them, and the key count and age are metrics.
func TestReportsKeysPilingUp(t *testing.T) {
	h := newHub(t)
	ctx, ns := context.Background(), h.ns
	events := record.NewFakeRecorder(50)
	pusher := newPusher(ns, events)
	require.NoError(t, pusher.Push(ctx))
	first, err := componentdispatch.EnsureSigningKeys(ctx, testClient, ns)
	require.NoError(t, err)
	second, err := componentdispatch.RotateSigningKey(ctx, testClient, ns)
	require.NoError(t, err)
	c, _ := h.component("backend", 1, ready(h, "yes"), nil)
	c.Spec.Signature.KeyID = first.Active
	require.NoError(t, testClient.Create(ctx, c))
	c = h.get("backend")
	c.Status.SealedWith = second.Active
	require.NoError(t, testClient.Status().Update(ctx, c))
	require.NoError(t, pusher.Push(ctx))
	require.Equal(t, 2.0, testutil.ToFloat64(metrics.ComponentSigningKeys))

	_, err = componentdispatch.RotateSigningKey(ctx, testClient, ns)
	require.NoError(t, err)
	signing := &corev1.Secret{}
	require.NoError(t, testClient.Get(ctx, client.ObjectKey{Namespace: ns, Name: componentdispatch.SigningKeysSecret}, signing))
	metav1.SetMetaDataAnnotation(&signing.ObjectMeta, componentdispatch.AnnotationActiveSince, time.Now().Add(-time.Hour).UTC().Format(time.RFC3339))
	require.NoError(t, testClient.Update(ctx, signing))
	require.NoError(t, pusher.Push(ctx))
	require.Equal(t, 3.0, testutil.ToFloat64(metrics.ComponentSigningKeys))
	require.InDelta(t, time.Hour.Seconds(), testutil.ToFloat64(metrics.ComponentSigningKeyAge), 60)

	close(events.Events)
	var warning string
	for event := range events.Events {
		if strings.Contains(event, "SigningKeysPilingUp") {
			warning = event
		}
	}
	require.Contains(t, warning, "Warning")
	require.Contains(t, warning, first.Active)
	require.Contains(t, warning, second.Active)
	require.Contains(t, warning, "backend", "names the Component holding it")
}
