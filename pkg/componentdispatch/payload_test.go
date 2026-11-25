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
	"crypto/rand"
	"encoding/base64"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/oam-dev/kubevela/apis/core.oam.dev/v1alpha1"
)

func configMap(name string, data map[string]interface{}) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "v1",
		"kind":       "ConfigMap",
		"metadata":   map[string]interface{}{"name": name, "namespace": "team-a"},
		"data":       data,
	}}
}

func TestPayloadRoundTrip(t *testing.T) {
	objs := []*unstructured.Unstructured{
		configMap("a", map[string]interface{}{"k": "v"}),
		configMap("b", map[string]interface{}{"k": "w"}),
	}
	data, digest, err := EncodePayload(objs)
	require.NoError(t, err)
	require.Len(t, digest, 64)

	got, err := DecodePayload(data, digest)
	require.NoError(t, err)
	require.Equal(t, objs, got)
}

func TestPayloadDigestMismatchIsRefused(t *testing.T) {
	data, digest, err := EncodePayload([]*unstructured.Unstructured{configMap("a", nil)})
	require.NoError(t, err)
	other, _, err := EncodePayload([]*unstructured.Unstructured{configMap("b", nil)})
	require.NoError(t, err)

	_, err = DecodePayload(other, digest)
	require.ErrorIs(t, err, ErrPayloadDigest)
	_, err = DecodePayload(data, strings.Repeat("0", 64))
	require.ErrorIs(t, err, ErrPayloadDigest)
}

func TestOversizedPayloadIsRefused(t *testing.T) {
	noise := make([]byte, MaxPayloadBytes)
	_, err := rand.Read(noise)
	require.NoError(t, err)
	_, _, err = EncodePayload([]*unstructured.Unstructured{configMap("big", map[string]interface{}{"blob": base64.StdEncoding.EncodeToString(noise)})})
	require.ErrorIs(t, err, ErrPayloadTooLarge)
	require.ErrorContains(t, err, "KiB")
}

func TestResourceIndexMustMatchThePayload(t *testing.T) {
	a, b := configMap("a", map[string]interface{}{"k": "v"}), configMap("b", nil)
	index := func(objs ...*unstructured.Unstructured) []v1alpha1.ComponentResource {
		var out []v1alpha1.ComponentResource
		for _, o := range objs {
			r, err := IndexEntry(o)
			require.NoError(t, err)
			out = append(out, r)
		}
		return out
	}
	require.NoError(t, CheckIndex([]*unstructured.Unstructured{a, b}, index(a, b)))

	changed := configMap("a", map[string]interface{}{"k": "changed"})
	err := CheckIndex([]*unstructured.Unstructured{changed, b}, index(a, b))
	require.ErrorIs(t, err, ErrIndexMismatch)
	require.ErrorContains(t, err, "ConfigMap team-a/a")

	err = CheckIndex([]*unstructured.Unstructured{a}, index(a, b))
	require.ErrorIs(t, err, ErrIndexMismatch, "an indexed resource missing from the payload")
	err = CheckIndex([]*unstructured.Unstructured{a, b}, index(a))
	require.ErrorIs(t, err, ErrIndexMismatch, "a payload resource missing from the index")
}

func TestIndexEntryKeepsRoleAndTraitBlank(t *testing.T) {
	r, err := IndexEntry(configMap("a", nil))
	require.NoError(t, err)
	require.Equal(t, v1alpha1.ComponentResource{APIVersion: "v1", Kind: "ConfigMap", Namespace: "team-a", Name: "a", SHA256: r.SHA256}, r)
	require.Len(t, r.SHA256, 64)
}

func TestPayloadKeepsExplicitNulls(t *testing.T) {
	obj := configMap("a", map[string]interface{}{"k": "v"})
	obj.Object["immutable"] = nil
	data, digest, err := EncodePayload([]*unstructured.Unstructured{obj})
	require.NoError(t, err)
	got, err := DecodePayload(data, digest)
	require.NoError(t, err)
	require.Contains(t, got[0].Object, "immutable", "the payload carries the resource as rendered")
	require.NoError(t, CheckIndex(got, func() []v1alpha1.ComponentResource {
		r, err := IndexEntry(obj)
		require.NoError(t, err)
		return []v1alpha1.ComponentResource{r}
	}()))
}
