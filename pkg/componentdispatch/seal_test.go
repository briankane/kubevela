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
	"bytes"
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
)

func TestSealedValuesOpenOnlyWithTheRing(t *testing.T) {
	ctx := context.Background()
	ring, err := EnsureSigningKeys(ctx, fakeClient(t), "vela-system")
	require.NoError(t, err)
	other, err := EnsureSigningKeys(ctx, fakeClient(t), "vela-system")
	require.NoError(t, err)

	sealed, err := ring.Sealing().Seal(ring.Active, []byte(`"s3cret"`))
	require.NoError(t, err)
	require.NotContains(t, sealed, "s3cret")

	opened, err := ring.Open(ring.Active, sealed)
	require.NoError(t, err)
	require.Equal(t, `"s3cret"`, string(opened))

	_, err = other.Open(ring.Active, sealed)
	require.ErrorIs(t, err, ErrUnknownSealingKey, "another hub has no such key")
	other.Keys[ring.Active] = other.Keys[other.Active]
	_, err = other.Open(ring.Active, sealed)
	require.ErrorIs(t, err, ErrCannotOpen, "another hub's key of the same name cannot open it")

	tampered := []byte(sealed)
	tampered[len(tampered)-3] ^= 0x01
	_, err = ring.Open(ring.Active, string(tampered))
	require.Error(t, err)

	_, err = ring.Sealing().Seal("hub-unknown", []byte(`"x"`))
	require.ErrorIs(t, err, ErrUnknownSealingKey)
}

// The sealing key is derived from the signing seed, so it is stable across restarts and
// replicas, and is not the signing key.
func TestSealingKeysAreDerivedAndSeparate(t *testing.T) {
	ctx := context.Background()
	cli := fakeClient(t)
	ring, err := EnsureSigningKeys(ctx, cli, "vela-system")
	require.NoError(t, err)
	again, err := EnsureSigningKeys(ctx, cli, "vela-system")
	require.NoError(t, err)
	require.Equal(t, ring.Sealing(), again.Sealing())

	seal := ring.Sealing()[ring.Active]
	require.NotNil(t, seal)
	require.False(t, bytes.Equal(seal[:], ring.Trusted()[ring.Active]), "the sealing key is not the signing key")
}

func TestTrustSecretCarriesSealingKeys(t *testing.T) {
	ctx := context.Background()
	cli := fakeClient(t)
	_, err := EnsureSigningKeys(ctx, cli, "vela-system")
	require.NoError(t, err)
	ring, err := RotateSigningKey(ctx, cli, "vela-system")
	require.NoError(t, err)

	secret := &corev1.Secret{Data: TrustSecretData(ring)}
	require.Len(t, secret.Data, 4, "a signing and a sealing key for each of two keys")
	trusted, err := TrustedKeysFromSecret(secret)
	require.NoError(t, err)
	require.Equal(t, ring.Trusted(), trusted)
	sealing, err := SealingKeysFromSecret(secret)
	require.NoError(t, err)
	require.Equal(t, ring.Sealing(), sealing)

	secret.Data["hub-x"+sealingSuffix] = []byte("short")
	_, err = SealingKeysFromSecret(secret)
	require.ErrorContains(t, err, `"hub-x"`)
}
