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
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"

	"github.com/oam-dev/kubevela/apis/core.oam.dev/v1alpha1"
)

func newSigner(t *testing.T, keyID string) (Signer, ed25519.PublicKey) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	return Signer{KeyID: keyID, Key: priv}, pub
}

func signedComponent(t *testing.T, s Signer) *v1alpha1.Component {
	c := &v1alpha1.Component{
		TypeMeta:   metav1.TypeMeta{APIVersion: v1alpha1.SchemeGroupVersion.String(), Kind: v1alpha1.ComponentKind},
		ObjectMeta: metav1.ObjectMeta{Name: "shop-backend-local", Namespace: "team-a"},
		Spec: v1alpha1.ComponentSpec{
			Target:    v1alpha1.ComponentTarget{Cluster: "local"},
			Revision:  3,
			Resources: []v1alpha1.ComponentResource{{APIVersion: "v1", Kind: "ConfigMap", Namespace: "team-a", Name: "settings", Role: "output", SHA256: "abc"}},
			Apply:     v1alpha1.ComponentApply{TakeOver: &v1alpha1.TakeOverPolicySpec{}},
			Health: v1alpha1.ComponentHealth{Workload: &v1alpha1.ComponentHealthSubject{
				HealthSnippets: v1alpha1.HealthSnippets{HealthPolicy: "isHealth: true"},
				Parameter:      &runtime.RawExtension{Raw: []byte(`{"replicas": 2.0, "image": "nginx", "tag": null}`)},
			}},
			Payload: &v1alpha1.ComponentPayload{SecretRef: v1alpha1.ComponentSecretReference{Name: "p"}, Format: PayloadFormat, SHA256: "def"},
		},
	}
	require.NoError(t, s.Sign(c))
	return c
}

func TestSignedComponentVerifies(t *testing.T) {
	s, pub := newSigner(t, "hub-1")
	c := signedComponent(t, s)
	require.Equal(t, "hub-1", c.Spec.Signature.KeyID)
	require.NotEmpty(t, c.Spec.Signature.Value)
	require.NoError(t, TrustedKeys{"hub-1": pub}.Verify(c))
}

// The API server stores a Component as JSON and hands back its own encoding: keys sorted,
// whitespace gone, 2.0 written as 2, null fields dropped. The signature must survive that.
func TestSignatureSurvivesTheAPIServer(t *testing.T) {
	s, pub := newSigner(t, "hub-1")
	c := signedComponent(t, s)

	data, err := json.Marshal(c)
	require.NoError(t, err)
	stored := &unstructured.Unstructured{}
	require.NoError(t, stored.UnmarshalJSON(data))
	unstructured.RemoveNestedField(stored.Object, "spec", "health", "workload", "parameter", "tag")
	served, err := stored.MarshalJSON()
	require.NoError(t, err)
	back := &v1alpha1.Component{}
	require.NoError(t, json.Unmarshal(served, back))
	require.NotEqual(t, string(c.Spec.Health.Workload.Parameter.Raw), string(back.Spec.Health.Workload.Parameter.Raw), "the round trip rewrote the raw parameter")

	require.NoError(t, TrustedKeys{"hub-1": pub}.Verify(back))
}

func TestTamperingFailsVerification(t *testing.T) {
	s, pub := newSigner(t, "hub-1")
	keys := TrustedKeys{"hub-1": pub}
	for name, tamper := range map[string]func(*v1alpha1.Component){
		"target":    func(c *v1alpha1.Component) { c.Spec.Target.Cluster = "other" },
		"revision":  func(c *v1alpha1.Component) { c.Spec.Revision = 2 },
		"tombstone": func(c *v1alpha1.Component) { c.Spec.Delete = true },
		"resources": func(c *v1alpha1.Component) { c.Spec.Resources[0].Name = "other" },
		"apply":     func(c *v1alpha1.Component) { c.Spec.Apply.ReadOnly = &v1alpha1.ReadOnlyPolicySpec{} },
		"health":    func(c *v1alpha1.Component) { c.Spec.Health.Workload.HealthPolicy = "isHealth: false" },
		"parameter": func(c *v1alpha1.Component) {
			c.Spec.Health.Workload.Parameter.Raw = []byte(`{"replicas":3,"image":"nginx"}`)
		},
		"payload":    func(c *v1alpha1.Component) { c.Spec.Payload.SHA256 = "000" },
		"namespace":  func(c *v1alpha1.Component) { c.Namespace = "team-b" },
		"name":       func(c *v1alpha1.Component) { c.Name = "other" },
		"key id":     func(c *v1alpha1.Component) { c.Spec.Signature.KeyID = "hub-2" },
		"unsigned":   func(c *v1alpha1.Component) { c.Spec.Signature = v1alpha1.ComponentSignature{} },
		"bad base64": func(c *v1alpha1.Component) { c.Spec.Signature.Value = "%%%" },
	} {
		t.Run(name, func(t *testing.T) {
			c := signedComponent(t, s)
			tamper(c)
			require.Error(t, keys.Verify(c))
		})
	}
}

func TestVerifyNamesWhatFailed(t *testing.T) {
	s, pub := newSigner(t, "hub-1")
	c := signedComponent(t, s)

	err := TrustedKeys{"hub-2": pub}.Verify(c)
	require.ErrorIs(t, err, ErrUntrustedKey)
	require.ErrorContains(t, err, "hub-1")

	c.Spec.Signature = v1alpha1.ComponentSignature{}
	require.ErrorIs(t, TrustedKeys{"hub-1": pub}.Verify(c), ErrUnsigned)

	c = signedComponent(t, s)
	c.Spec.Revision++
	require.ErrorIs(t, TrustedKeys{"hub-1": pub}.Verify(c), ErrBadSignature)
}

func TestEitherKeyVerifiesDuringRotation(t *testing.T) {
	old, oldPub := newSigner(t, "hub-1")
	next, nextPub := newSigner(t, "hub-2")
	keys := TrustedKeys{"hub-1": oldPub, "hub-2": nextPub}
	require.NoError(t, keys.Verify(signedComponent(t, old)))
	require.NoError(t, keys.Verify(signedComponent(t, next)))

	signedByNext := signedComponent(t, next)
	signedByNext.Spec.Signature.KeyID = "hub-1"
	require.ErrorIs(t, keys.Verify(signedByNext), ErrBadSignature, "a signature only verifies under the key it names")
}
