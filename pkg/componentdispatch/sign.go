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

// Package componentdispatch holds what the hub and vela-agent must agree on byte for byte:
// how a Component is signed and verified, and how its payload is encoded.
package componentdispatch

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"

	utiljson "k8s.io/apimachinery/pkg/util/json"

	"github.com/oam-dev/kubevela/apis/core.oam.dev/v1alpha1"
)

var (
	// ErrUnsigned is a Component with no signature.
	ErrUnsigned = errors.New("component is not signed")
	// ErrUntrustedKey is a signature by a key this cluster does not trust.
	ErrUntrustedKey = errors.New("component is signed by an untrusted key")
	// ErrBadSignature is a signature that does not match the Component.
	ErrBadSignature = errors.New("component signature does not verify")
)

// messagePrefix separates Component signatures from anything else the same key might sign.
const messagePrefix = "core.oam.dev/v1alpha1 Component\n"

// Signer signs Components with one hub key.
type Signer struct {
	KeyID string
	Key   ed25519.PrivateKey
}

// Sign sets c.Spec.Signature over c's namespace, name and the rest of its spec.
func (s Signer) Sign(c *v1alpha1.Component) error {
	msg, err := signingMessage(c)
	if err != nil {
		return err
	}
	c.Spec.Signature = v1alpha1.ComponentSignature{
		KeyID: s.KeyID,
		Value: base64.StdEncoding.EncodeToString(ed25519.Sign(s.Key, msg)),
	}
	return nil
}

// TrustedKeys are the hub public keys a cluster accepts, by key ID.
type TrustedKeys map[string]ed25519.PublicKey

// Verify checks c's signature under the key it names.
func (k TrustedKeys) Verify(c *v1alpha1.Component) error {
	sig := c.Spec.Signature
	if sig.KeyID == "" || sig.Value == "" {
		return ErrUnsigned
	}
	key, ok := k[sig.KeyID]
	if !ok {
		if len(k) == 0 {
			return fmt.Errorf("%w: %q (this cluster trusts no hub keys yet)", ErrUntrustedKey, sig.KeyID)
		}
		return fmt.Errorf("%w: %q", ErrUntrustedKey, sig.KeyID)
	}
	value, err := base64.StdEncoding.DecodeString(sig.Value)
	if err != nil {
		return fmt.Errorf("%w: signature is not base64", ErrBadSignature)
	}
	msg, err := signingMessage(c)
	if err != nil {
		return err
	}
	if !ed25519.Verify(key, msg, value) {
		return fmt.Errorf("%w under key %q", ErrBadSignature, sig.KeyID)
	}
	return nil
}

// SameSpec reports whether two Components ask for the same thing: their specs, without
// signatures, are equal in canonical JSON.
func SameSpec(a, b *v1alpha1.Component) (bool, error) {
	ma, err := canonicalSpec(a)
	if err != nil {
		return false, err
	}
	mb, err := canonicalSpec(b)
	if err != nil {
		return false, err
	}
	return bytes.Equal(ma, mb), nil
}

func canonicalSpec(c *v1alpha1.Component) ([]byte, error) {
	spec := c.Spec.DeepCopy()
	spec.Signature = v1alpha1.ComponentSignature{}
	return canonicalJSON(spec)
}

// signingMessage is what a signature covers: the Component's namespace, name and spec
// without its signature, in canonical JSON.
func signingMessage(c *v1alpha1.Component) ([]byte, error) {
	spec := c.Spec.DeepCopy()
	spec.Signature = v1alpha1.ComponentSignature{}
	body, err := canonicalJSON(struct {
		Namespace string                  `json:"namespace"`
		Name      string                  `json:"name"`
		Spec      *v1alpha1.ComponentSpec `json:"spec"`
	}{c.Namespace, c.Name, spec})
	if err != nil {
		return nil, fmt.Errorf("encoding component %s/%s for signing: %w", c.Namespace, c.Name, err)
	}
	return append([]byte(messagePrefix), body...), nil
}

// canonicalJSON encodes v as the API server would hand it back: decoded the way
// apimachinery decodes JSON (whole numbers as int64), null fields dropped, then encoded with
// sorted keys and no whitespace. Raw fields such as a health parameter come back from the
// server rewritten, so signing their bytes as written would not survive the round trip.
func canonicalJSON(v interface{}) ([]byte, error) {
	data, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	var generic interface{}
	if err := utiljson.Unmarshal(data, &generic); err != nil {
		return nil, err
	}
	return json.Marshal(dropNulls(generic))
}

// dropNulls removes null-valued fields from every object in v. List elements are kept.
func dropNulls(v interface{}) interface{} {
	switch t := v.(type) {
	case map[string]interface{}:
		for k, e := range t {
			if e == nil {
				delete(t, k)
				continue
			}
			t[k] = dropNulls(e)
		}
	case []interface{}:
		for i, e := range t {
			t[i] = dropNulls(e)
		}
	}
	return v
}
