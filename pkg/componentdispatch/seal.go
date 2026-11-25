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
	"crypto/ecdh"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"sort"
	"strings"

	"golang.org/x/crypto/nacl/box"
	corev1 "k8s.io/api/core/v1"
)

const (
	// sealingSuffix marks a sealing key's entry in the trust Secret, beside the signing key
	// of the same ID.
	sealingSuffix = ".seal"
	// sealingInfo separates the sealing key derived from a signing seed from any other use
	// of that seed.
	sealingInfo = "kubevela component outputs v1"
)

var (
	// ErrUnknownSealingKey is a seal to, or an open with, a key the ring does not hold.
	ErrUnknownSealingKey = errors.New("unknown sealing key")
	// ErrCannotOpen is a sealed value that does not open with the named key.
	ErrCannotOpen = errors.New("sealed value does not open")
)

// SealingKeys are the public halves of the hub's sealing keys, by signing key ID. vela-agent
// seals sensitive values to them, so that only the hub can read them.
type SealingKeys map[string]*[32]byte

// Seal encrypts value to the sealing key keyID, as an anonymous NaCl box, base64-encoded.
func (k SealingKeys) Seal(keyID string, value []byte) (string, error) {
	pub, ok := k[keyID]
	if !ok {
		return "", fmt.Errorf("%w %q", ErrUnknownSealingKey, keyID)
	}
	sealed, err := box.SealAnonymous(nil, value, pub, rand.Reader)
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(sealed), nil
}

// Sealing is the public half of each key's sealing key.
func (k *KeyRing) Sealing() SealingKeys {
	out := make(SealingKeys, len(k.Keys))
	for id := range k.Keys {
		pub, _, err := k.sealingKey(id)
		if err == nil {
			out[id] = pub
		}
	}
	return out
}

// Open decrypts a value sealed to the sealing key keyID.
func (k *KeyRing) Open(keyID, sealed string) ([]byte, error) {
	if _, ok := k.Keys[keyID]; !ok {
		return nil, fmt.Errorf("%w %q", ErrUnknownSealingKey, keyID)
	}
	pub, priv, err := k.sealingKey(keyID)
	if err != nil {
		return nil, err
	}
	raw, err := base64.StdEncoding.DecodeString(sealed)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrCannotOpen, err)
	}
	opened, ok := box.OpenAnonymous(nil, raw, pub, priv)
	if !ok {
		return nil, fmt.Errorf("%w with key %q", ErrCannotOpen, keyID)
	}
	return opened, nil
}

// sealingKey derives the X25519 sealing key for a signing key from its seed.
func (k *KeyRing) sealingKey(keyID string) (pub, priv *[32]byte, err error) {
	signing := k.Keys[keyID]
	scalar, err := hkdf.Key(sha256.New, signing.Seed(), nil, sealingInfo, 32)
	if err != nil {
		return nil, nil, err
	}
	key, err := ecdh.X25519().NewPrivateKey(scalar)
	if err != nil {
		return nil, nil, err
	}
	pub, priv = new([32]byte), new([32]byte)
	copy(pub[:], key.PublicKey().Bytes())
	copy(priv[:], key.Bytes())
	return pub, priv, nil
}

// SealingKeysFromSecret reads the sealing keys a trust Secret holds.
func SealingKeysFromSecret(secret *corev1.Secret) (SealingKeys, error) {
	keys := SealingKeys{}
	var bad []string
	for name, raw := range secret.Data {
		id, ok := strings.CutSuffix(name, sealingSuffix)
		if !ok {
			continue
		}
		if len(raw) != 32 {
			bad = append(bad, fmt.Sprintf("%q", id))
			continue
		}
		keys[id] = (*[32]byte)(raw)
	}
	if len(bad) > 0 {
		sort.Strings(bad)
		return nil, errors.New("trust secret holds sealing keys that are not X25519 public keys: " + fmt.Sprint(bad))
	}
	return keys, nil
}

// TrustEntryKeyID is the signing key a trust Secret entry belongs to: the entry itself, or the
// key whose sealing key it holds.
func TrustEntryKeyID(entry string) string {
	return strings.TrimSuffix(entry, sealingSuffix)
}
