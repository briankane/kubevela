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
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	kerrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"

	pkgmulticluster "github.com/kubevela/pkg/multicluster"
)

const (
	// SigningKeysSecret holds the hub's private signing keys, in the hub's system namespace.
	SigningKeysSecret = "vela-component-signing-keys"
	// TrustSecret holds the hub public keys a cluster trusts, in its system namespace.
	TrustSecret = "vela-component-trust"
	// AnnotationRotateSigningKey on the signing keys Secret asks for a rotation: the hub adds a
	// key, makes it the active one, and removes the annotation.
	AnnotationRotateSigningKey = "component.oam.dev/rotate-signing-key"
	// AnnotationRevokeSigningKey on the signing keys Secret names a key to stop trusting at once,
	// whatever still carries it. The hub signs with a new key first if it is the active one, and
	// removes the annotation.
	AnnotationRevokeSigningKey = "component.oam.dev/revoke-signing-key"
	// AnnotationRemovedKeys on the signing keys Secret lists, comma-separated, the keys the hub
	// retired or revoked that some cluster may still trust, so their leaving is not reported as
	// foreign.
	AnnotationRemovedKeys = "component.oam.dev/removed-keys"
	// AnnotationActiveSince on the signing keys Secret is when the active key became active, in
	// RFC 3339.
	AnnotationActiveSince = "component.oam.dev/active-since"
	// activeKey names, in the signing keys Secret, the key ID new signatures use. Key IDs
	// cannot collide with it: they always start with "hub-".
	activeKey = "active"
)

// KeyRing is the hub's signing keys: the active one signs, all of them are trusted.
type KeyRing struct {
	Active string
	Keys   map[string]ed25519.PrivateKey
	// Removed is the keys the hub retired or revoked that some cluster may still trust.
	Removed map[string]bool
	// ActiveSince is when the active key became active; zero if the Secret does not say.
	ActiveSince time.Time
}

// Signer signs with the active key.
func (k *KeyRing) Signer() Signer {
	return Signer{KeyID: k.Active, Key: k.Keys[k.Active]}
}

// Trusted is the public half of every key in the ring.
func (k *KeyRing) Trusted() TrustedKeys {
	out := make(TrustedKeys, len(k.Keys))
	for id, priv := range k.Keys {
		out[id] = priv.Public().(ed25519.PublicKey)
	}
	return out
}

// HubKeys reads the hub's signing keys on each call, so a rotation applies from the next one.
type HubKeys struct {
	Client client.Client
	// Reader reads the keys Secret past the manager's cache.
	Reader    client.Reader
	Namespace string
}

// Ring is the hub's signing keys, generating the first key if there is none. The keys are
// read from the hub, whatever cluster ctx names.
func (k HubKeys) Ring(ctx context.Context) (*KeyRing, error) {
	return EnsureSigningKeys(pkgmulticluster.WithCluster(ctx, pkgmulticluster.Local), ReadThrough{Client: k.Client, Reader: k.Reader}, k.Namespace)
}

// ReadThrough is a client whose Gets go to Reader.
type ReadThrough struct {
	client.Client
	Reader client.Reader
}

// Get reads through Reader.
func (r ReadThrough) Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	return r.Reader.Get(ctx, key, obj, opts...)
}

// EnsureSigningKeys loads the hub's signing keys, generating the first one if there are none.
func EnsureSigningKeys(ctx context.Context, cli client.Client, namespace string) (*KeyRing, error) {
	secret := &corev1.Secret{}
	err := cli.Get(ctx, client.ObjectKey{Namespace: namespace, Name: SigningKeysSecret}, secret)
	if kerrors.IsNotFound(err) {
		secret = &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: SigningKeysSecret}}
		if err := addActiveKey(secret); err != nil {
			return nil, err
		}
		if err := cli.Create(ctx, secret); err != nil {
			if !kerrors.IsAlreadyExists(err) {
				return nil, fmt.Errorf("creating %s/%s: %w", namespace, SigningKeysSecret, err)
			}
			// another replica created it first: use theirs
			return EnsureSigningKeys(ctx, cli, namespace)
		}
	} else if err != nil {
		return nil, fmt.Errorf("reading %s/%s: %w", namespace, SigningKeysSecret, err)
	}
	return ringFromSecret(secret)
}

// RotateSigningKey adds a new key and makes it the active one. The old key stays trusted
// until RetireSigningKey removes it.
func RotateSigningKey(ctx context.Context, cli client.Client, namespace string) (*KeyRing, error) {
	if _, err := EnsureSigningKeys(ctx, cli, namespace); err != nil {
		return nil, err
	}
	var ring *KeyRing
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		secret := &corev1.Secret{}
		if err := cli.Get(ctx, client.ObjectKey{Namespace: namespace, Name: SigningKeysSecret}, secret); err != nil {
			return err
		}
		if err := addActiveKey(secret); err != nil {
			return err
		}
		if err := cli.Update(ctx, secret); err != nil {
			return err
		}
		var err error
		ring, err = ringFromSecret(secret)
		return err
	})
	return ring, err
}

// RotateIfRequested rotates the signing key when the keys Secret carries
// AnnotationRotateSigningKey, removing the annotation in the same write. It reports whether it
// rotated.
func RotateIfRequested(ctx context.Context, cli client.Client, namespace string) (bool, error) {
	rotated := false
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		rotated = false
		secret := &corev1.Secret{}
		err := cli.Get(ctx, client.ObjectKey{Namespace: namespace, Name: SigningKeysSecret}, secret)
		if kerrors.IsNotFound(err) {
			return nil
		}
		if err != nil {
			return err
		}
		if _, ok := secret.Annotations[AnnotationRotateSigningKey]; !ok {
			return nil
		}
		delete(secret.Annotations, AnnotationRotateSigningKey)
		if err := addActiveKey(secret); err != nil {
			return err
		}
		if err := cli.Update(ctx, secret); err != nil {
			return err
		}
		rotated = true
		return nil
	})
	return rotated, err
}

// RequestRotationIfOlder asks for a rotation, as an operator would with
// AnnotationRotateSigningKey, once the active key has been active for longer than maxAge. A
// key that does not say when it became active is treated as active from now.
func RequestRotationIfOlder(ctx context.Context, cli client.Client, namespace string, maxAge time.Duration) error {
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		secret := &corev1.Secret{}
		err := cli.Get(ctx, client.ObjectKey{Namespace: namespace, Name: SigningKeysSecret}, secret)
		if kerrors.IsNotFound(err) {
			return nil
		}
		if err != nil {
			return err
		}
		since, err := time.Parse(time.RFC3339, secret.Annotations[AnnotationActiveSince])
		switch {
		case err != nil:
			metav1.SetMetaDataAnnotation(&secret.ObjectMeta, AnnotationActiveSince, time.Now().UTC().Format(time.RFC3339))
		case time.Since(since) > maxAge:
			metav1.SetMetaDataAnnotation(&secret.ObjectMeta, AnnotationRotateSigningKey, "max-age "+maxAge.String())
		default:
			return nil
		}
		return cli.Update(ctx, secret)
	})
}

// RevokeIfRequested removes the key AnnotationRevokeSigningKey names, adding a new active key
// first if it is the active one, and removes the annotation in the same write. It returns the
// key it revoked, if any; a key the Secret does not hold is an error, and the request is spent.
func RevokeIfRequested(ctx context.Context, cli client.Client, namespace string) (string, error) {
	var revoked string
	var unknown error
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		revoked, unknown = "", nil
		secret := &corev1.Secret{}
		err := cli.Get(ctx, client.ObjectKey{Namespace: namespace, Name: SigningKeysSecret}, secret)
		if kerrors.IsNotFound(err) {
			return nil
		}
		if err != nil {
			return err
		}
		id, ok := secret.Annotations[AnnotationRevokeSigningKey]
		if !ok {
			return nil
		}
		delete(secret.Annotations, AnnotationRevokeSigningKey)
		if _, held := secret.Data[id]; !held || id == activeKey {
			unknown = fmt.Errorf("cannot revoke component signing key %q: %s/%s holds no such key", id, namespace, SigningKeysSecret)
			return cli.Update(ctx, secret)
		}
		if ActiveKeyID(secret) == id {
			if err := addActiveKey(secret); err != nil {
				return err
			}
		}
		delete(secret.Data, id)
		markRemoved(secret, id)
		if err := cli.Update(ctx, secret); err != nil {
			return err
		}
		revoked = id
		return nil
	})
	if err != nil {
		return "", err
	}
	return revoked, unknown
}

// RetireSigningKey removes a key that is no longer active.
func RetireSigningKey(ctx context.Context, cli client.Client, namespace, keyID string) error {
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		secret := &corev1.Secret{}
		if err := cli.Get(ctx, client.ObjectKey{Namespace: namespace, Name: SigningKeysSecret}, secret); err != nil {
			return err
		}
		if string(secret.Data[activeKey]) == keyID {
			return fmt.Errorf("key %q is the active signing key; rotate before retiring it", keyID)
		}
		if _, ok := secret.Data[keyID]; !ok {
			return nil
		}
		delete(secret.Data, keyID)
		markRemoved(secret, keyID)
		return cli.Update(ctx, secret)
	})
}

// ForgetRemovedKeys drops ids from the removed keys, once no cluster trusts them.
func ForgetRemovedKeys(ctx context.Context, cli client.Client, namespace string, ids map[string]bool) error {
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		secret := &corev1.Secret{}
		if err := cli.Get(ctx, client.ObjectKey{Namespace: namespace, Name: SigningKeysSecret}, secret); err != nil {
			return err
		}
		removed := removedKeys(secret)
		for id := range ids {
			delete(removed, id)
		}
		setRemoved(secret, removed)
		return cli.Update(ctx, secret)
	})
}

func removedKeys(secret *corev1.Secret) map[string]bool {
	removed := map[string]bool{}
	for _, id := range strings.Split(secret.Annotations[AnnotationRemovedKeys], ",") {
		if id != "" {
			removed[id] = true
		}
	}
	return removed
}

func markRemoved(secret *corev1.Secret, id string) {
	removed := removedKeys(secret)
	removed[id] = true
	setRemoved(secret, removed)
}

func setRemoved(secret *corev1.Secret, removed map[string]bool) {
	if len(removed) == 0 {
		delete(secret.Annotations, AnnotationRemovedKeys)
		return
	}
	ids := make([]string, 0, len(removed))
	for id := range removed {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	metav1.SetMetaDataAnnotation(&secret.ObjectMeta, AnnotationRemovedKeys, strings.Join(ids, ","))
}

// ActiveKeyID is the ID of the key the signing keys Secret signs with.
func ActiveKeyID(secret *corev1.Secret) string {
	return string(secret.Data[activeKey])
}

// addActiveKey adds a new key to the signing keys Secret and makes it the active one, from now.
func addActiveKey(secret *corev1.Secret) error {
	id, priv, err := newKey()
	if err != nil {
		return err
	}
	if secret.Data == nil {
		secret.Data = map[string][]byte{}
	}
	secret.Data[id] = priv.Seed()
	secret.Data[activeKey] = []byte(id)
	metav1.SetMetaDataAnnotation(&secret.ObjectMeta, AnnotationActiveSince, time.Now().UTC().Format(time.RFC3339))
	return nil
}

func ringFromSecret(secret *corev1.Secret) (*KeyRing, error) {
	ring := &KeyRing{Active: string(secret.Data[activeKey]), Keys: map[string]ed25519.PrivateKey{}, Removed: removedKeys(secret)}
	if since, err := time.Parse(time.RFC3339, secret.Annotations[AnnotationActiveSince]); err == nil {
		ring.ActiveSince = since
	}
	for id, seed := range secret.Data {
		if id == activeKey {
			continue
		}
		if len(seed) != ed25519.SeedSize {
			return nil, fmt.Errorf("%s/%s: key %q is not an Ed25519 seed", secret.Namespace, secret.Name, id)
		}
		ring.Keys[id] = ed25519.NewKeyFromSeed(seed)
	}
	if _, ok := ring.Keys[ring.Active]; !ok {
		return nil, fmt.Errorf("%s/%s: active key %q is missing", secret.Namespace, secret.Name, ring.Active)
	}
	return ring, nil
}

func newKey() (string, ed25519.PrivateKey, error) {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return "", nil, fmt.Errorf("generating a signing key: %w", err)
	}
	suffix := make([]byte, 3)
	if _, err := rand.Read(suffix); err != nil {
		return "", nil, err
	}
	return fmt.Sprintf("hub-%s-%s", time.Now().UTC().Format("20060102"), hex.EncodeToString(suffix)), priv, nil
}

// TrustSecretData is the trust Secret's data for a key ring: each key ID to its raw Ed25519
// public key, and <ID>.seal to the raw X25519 public key vela-agent seals values to.
func TrustSecretData(ring *KeyRing) map[string][]byte {
	trusted, sealing := ring.Trusted(), ring.Sealing()
	out := make(map[string][]byte, len(trusted)+len(sealing))
	for id, pub := range trusted {
		out[id] = []byte(pub)
	}
	for id, pub := range sealing {
		out[id+sealingSuffix] = pub[:]
	}
	return out
}

// TrustedKeysFromSecret reads the keys a trust Secret holds.
func TrustedKeysFromSecret(secret *corev1.Secret) (TrustedKeys, error) {
	keys := make(TrustedKeys, len(secret.Data))
	var bad []string
	for id, raw := range secret.Data {
		if strings.HasSuffix(id, sealingSuffix) {
			continue
		}
		if len(raw) != ed25519.PublicKeySize {
			bad = append(bad, fmt.Sprintf("%q", id))
			continue
		}
		keys[id] = ed25519.PublicKey(raw)
	}
	if len(bad) > 0 {
		sort.Strings(bad)
		return nil, errors.New("trust secret holds keys that are not Ed25519 public keys: " + fmt.Sprint(bad))
	}
	return keys, nil
}
