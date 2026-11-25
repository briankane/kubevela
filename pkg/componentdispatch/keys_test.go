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

package componentdispatch

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/oam-dev/kubevela/apis/core.oam.dev/v1alpha1"
)

func fakeClient(t *testing.T) client.Client {
	scheme := runtime.NewScheme()
	require.NoError(t, clientgoscheme.AddToScheme(scheme))
	return fake.NewClientBuilder().WithScheme(scheme).Build()
}

func TestSigningKeysAreCreatedOnce(t *testing.T) {
	ctx, cli := context.Background(), fakeClient(t)
	first, err := EnsureSigningKeys(ctx, cli, "vela-system")
	require.NoError(t, err)
	require.NotEmpty(t, first.Active)
	require.Len(t, first.Keys, 1)

	again, err := EnsureSigningKeys(ctx, cli, "vela-system")
	require.NoError(t, err)
	require.Equal(t, first.Active, again.Active)
	require.Equal(t, first.Keys, again.Keys, "the stored key is reused, not regenerated")

	c := &v1alpha1.Component{ObjectMeta: metav1.ObjectMeta{Name: "c", Namespace: "ns"}, Spec: v1alpha1.ComponentSpec{Revision: 1}}
	require.NoError(t, again.Signer().Sign(c))
	require.NoError(t, first.Trusted().Verify(c))
}

func TestRotationKeepsTheOldKeyTrustedUntilRetired(t *testing.T) {
	ctx, cli := context.Background(), fakeClient(t)
	before, err := EnsureSigningKeys(ctx, cli, "vela-system")
	require.NoError(t, err)
	signedBefore := &v1alpha1.Component{ObjectMeta: metav1.ObjectMeta{Name: "c", Namespace: "ns"}, Spec: v1alpha1.ComponentSpec{Revision: 1}}
	require.NoError(t, before.Signer().Sign(signedBefore))

	after, err := RotateSigningKey(ctx, cli, "vela-system")
	require.NoError(t, err)
	require.NotEqual(t, before.Active, after.Active)
	require.Len(t, after.Keys, 2)
	require.NoError(t, after.Trusted().Verify(signedBefore), "the old key is still trusted")

	err = RetireSigningKey(ctx, cli, "vela-system", after.Active)
	require.ErrorContains(t, err, "active")

	require.NoError(t, RetireSigningKey(ctx, cli, "vela-system", before.Active))
	retired, err := EnsureSigningKeys(ctx, cli, "vela-system")
	require.NoError(t, err)
	require.Len(t, retired.Keys, 1)
	require.ErrorIs(t, retired.Trusted().Verify(signedBefore), ErrUntrustedKey)
}

func TestTrustSecretRoundTrip(t *testing.T) {
	ring, err := EnsureSigningKeys(context.Background(), fakeClient(t), "vela-system")
	require.NoError(t, err)
	secret := &corev1.Secret{Data: TrustSecretData(ring)}
	keys, err := TrustedKeysFromSecret(secret)
	require.NoError(t, err)
	require.Equal(t, ring.Trusted(), keys)

	secret.Data["bad"] = []byte("short")
	_, err = TrustedKeysFromSecret(secret)
	require.ErrorContains(t, err, `"bad"`)
}

func TestNoTrustedKeysSaysSo(t *testing.T) {
	c := &v1alpha1.Component{Spec: v1alpha1.ComponentSpec{Signature: v1alpha1.ComponentSignature{KeyID: "hub-1", Value: "c2ln"}}}
	err := TrustedKeys{}.Verify(c)
	require.ErrorIs(t, err, ErrUntrustedKey)
	require.ErrorContains(t, err, "trusts no hub keys")
}

func activeSince(t *testing.T, cli client.Client) time.Time {
	secret := &corev1.Secret{}
	require.NoError(t, cli.Get(context.Background(), client.ObjectKey{Namespace: "vela-system", Name: SigningKeysSecret}, secret))
	since, err := time.Parse(time.RFC3339, secret.Annotations[AnnotationActiveSince])
	require.NoError(t, err, "the active key says when it became active")
	return since
}

func setKeysAnnotation(t *testing.T, cli client.Client, key, value string) {
	secret := &corev1.Secret{}
	require.NoError(t, cli.Get(context.Background(), client.ObjectKey{Namespace: "vela-system", Name: SigningKeysSecret}, secret))
	if value == "" {
		delete(secret.Annotations, key)
	} else {
		metav1.SetMetaDataAnnotation(&secret.ObjectMeta, key, value)
	}
	require.NoError(t, cli.Update(context.Background(), secret))
}

func TestTheActiveKeySaysWhenItBecameActive(t *testing.T) {
	ctx, cli := context.Background(), fakeClient(t)
	_, err := EnsureSigningKeys(ctx, cli, "vela-system")
	require.NoError(t, err)
	require.WithinDuration(t, time.Now(), activeSince(t, cli), time.Minute)

	setKeysAnnotation(t, cli, AnnotationActiveSince, time.Now().Add(-48*time.Hour).UTC().Format(time.RFC3339))
	_, err = RotateSigningKey(ctx, cli, "vela-system")
	require.NoError(t, err)
	require.WithinDuration(t, time.Now(), activeSince(t, cli), time.Minute, "a rotation restarts the clock")

	setKeysAnnotation(t, cli, AnnotationActiveSince, time.Now().Add(-48*time.Hour).UTC().Format(time.RFC3339))
	setKeysAnnotation(t, cli, AnnotationRotateSigningKey, "now")
	rotated, err := RotateIfRequested(ctx, cli, "vela-system")
	require.NoError(t, err)
	require.True(t, rotated)
	require.WithinDuration(t, time.Now(), activeSince(t, cli), time.Minute, "so does a requested one")
}

func TestRequestsARotationOnceTheActiveKeyIsTooOld(t *testing.T) {
	ctx, cli := context.Background(), fakeClient(t)
	_, err := EnsureSigningKeys(ctx, cli, "vela-system")
	require.NoError(t, err)
	requested := func() bool {
		secret := &corev1.Secret{}
		require.NoError(t, cli.Get(ctx, client.ObjectKey{Namespace: "vela-system", Name: SigningKeysSecret}, secret))
		_, ok := secret.Annotations[AnnotationRotateSigningKey]
		return ok
	}

	require.NoError(t, RequestRotationIfOlder(ctx, cli, "vela-system", 24*time.Hour))
	require.False(t, requested(), "a young key stays")

	setKeysAnnotation(t, cli, AnnotationActiveSince, time.Now().Add(-48*time.Hour).UTC().Format(time.RFC3339))
	require.NoError(t, RequestRotationIfOlder(ctx, cli, "vela-system", 24*time.Hour))
	require.True(t, requested(), "an old one is rotated the way an operator asks for it")

	setKeysAnnotation(t, cli, AnnotationRotateSigningKey, "")
	setKeysAnnotation(t, cli, AnnotationActiveSince, "")
	require.NoError(t, RequestRotationIfOlder(ctx, cli, "vela-system", 24*time.Hour))
	require.False(t, requested(), "a key of unknown age is not rotated at once")
	require.WithinDuration(t, time.Now(), activeSince(t, cli), time.Minute, "its clock starts now")
}

func TestRevokesTheKeyAnOperatorNames(t *testing.T) {
	ctx, cli := context.Background(), fakeClient(t)
	first, err := EnsureSigningKeys(ctx, cli, "vela-system")
	require.NoError(t, err)
	second, err := RotateSigningKey(ctx, cli, "vela-system")
	require.NoError(t, err)

	revoked, err := RevokeIfRequested(ctx, cli, "vela-system")
	require.NoError(t, err)
	require.Empty(t, revoked, "nothing asked")

	setKeysAnnotation(t, cli, AnnotationRevokeSigningKey, first.Active)
	revoked, err = RevokeIfRequested(ctx, cli, "vela-system")
	require.NoError(t, err)
	require.Equal(t, first.Active, revoked)
	ring, err := EnsureSigningKeys(ctx, cli, "vela-system")
	require.NoError(t, err)
	require.NotContains(t, ring.Keys, first.Active, "gone, whatever still carries it")
	require.Equal(t, second.Active, ring.Active)

	setKeysAnnotation(t, cli, AnnotationRevokeSigningKey, second.Active)
	revoked, err = RevokeIfRequested(ctx, cli, "vela-system")
	require.NoError(t, err)
	require.Equal(t, second.Active, revoked)
	ring, err = EnsureSigningKeys(ctx, cli, "vela-system")
	require.NoError(t, err)
	require.NotContains(t, ring.Keys, second.Active)
	require.NotEqual(t, second.Active, ring.Active, "revoking the active key signs with a new one")

	setKeysAnnotation(t, cli, AnnotationRevokeSigningKey, "hub-nope")
	_, err = RevokeIfRequested(ctx, cli, "vela-system")
	require.ErrorContains(t, err, "hub-nope")
	secret := &corev1.Secret{}
	require.NoError(t, cli.Get(ctx, client.ObjectKey{Namespace: "vela-system", Name: SigningKeysSecret}, secret))
	require.NotContains(t, secret.Annotations, AnnotationRevokeSigningKey, "one request, one answer")
}

func TestTheKeysTheHubRemovedAreRecorded(t *testing.T) {
	ctx, cli := context.Background(), fakeClient(t)
	first, err := EnsureSigningKeys(ctx, cli, "vela-system")
	require.NoError(t, err)
	second, err := RotateSigningKey(ctx, cli, "vela-system")
	require.NoError(t, err)
	require.NoError(t, RetireSigningKey(ctx, cli, "vela-system", first.Active))
	setKeysAnnotation(t, cli, AnnotationRevokeSigningKey, second.Active)
	_, err = RevokeIfRequested(ctx, cli, "vela-system")
	require.NoError(t, err)

	ring, err := EnsureSigningKeys(ctx, cli, "vela-system")
	require.NoError(t, err)
	require.Equal(t, map[string]bool{first.Active: true, second.Active: true}, ring.Removed)

	require.NoError(t, ForgetRemovedKeys(ctx, cli, "vela-system", map[string]bool{first.Active: true}))
	ring, err = EnsureSigningKeys(ctx, cli, "vela-system")
	require.NoError(t, err)
	require.Equal(t, map[string]bool{second.Active: true}, ring.Removed, "only what every cluster has dropped is forgotten")
}
