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

package writer

import (
	"context"
	"encoding/json"
	"testing"

	jsonpatch "github.com/evanphx/json-patch/v5"
	"github.com/stretchr/testify/require"
	admissionv1 "k8s.io/api/admission/v1"
	authenticationv1 "k8s.io/api/authentication/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

const core = "system:serviceaccount:vela-system:kubevela-vela-core"

func object(spec string, annotations, labels map[string]string) []byte {
	u := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "core.oam.dev/v1beta1", "kind": "ComponentDefinition",
		"metadata": map[string]any{"name": "d", "namespace": "vela-system"},
		"spec":     map[string]any{"schematic": spec},
	}}
	u.SetAnnotations(annotations)
	u.SetLabels(labels)
	raw, _ := json.Marshal(u.Object)
	return raw
}

// stamped applies the handler's patch and returns the resulting annotations.
func stamped(t *testing.T, op admissionv1.Operation, user authenticationv1.UserInfo, obj, old []byte) map[string]string {
	t.Helper()
	req := admission.Request{AdmissionRequest: admissionv1.AdmissionRequest{
		Operation: op, UserInfo: user,
		Object: runtime.RawExtension{Raw: obj}, OldObject: runtime.RawExtension{Raw: old},
	}}
	resp := (&Handler{ControllerUsername: core}).Handle(context.Background(), req)
	require.True(t, resp.Allowed, resp.Result)
	patched := obj
	if len(resp.Patches) > 0 {
		raw, err := json.Marshal(resp.Patches)
		require.NoError(t, err)
		p, err := jsonpatch.DecodePatch(raw)
		require.NoError(t, err)
		patched, err = p.Apply(obj)
		require.NoError(t, err)
	}
	u := &unstructured.Unstructured{}
	require.NoError(t, json.Unmarshal(patched, &u.Object))
	return u.GetAnnotations()
}

func TestStamp(t *testing.T) {
	alice := authenticationv1.UserInfo{Username: "alice", Groups: []string{"payments-leads", "system:authenticated"}}
	bob := authenticationv1.UserInfo{Username: "bob", Groups: []string{"team-a"}}
	aliceStamp := map[string]string{AnnotationWriter: "alice", AnnotationWriterGroups: "payments-leads,system:authenticated"}

	t.Run("create stamps the request's user and every group", func(t *testing.T) {
		require.Equal(t, aliceStamp, stamped(t, admissionv1.Create, alice, object("v1", nil, nil), nil))
	})
	t.Run("a claimed stamp is overwritten", func(t *testing.T) {
		claimed := map[string]string{AnnotationWriter: "platform-admin", AnnotationWriterGroups: "system:masters", "keep": "me"}
		got := stamped(t, admissionv1.Create, bob, object("v1", claimed, nil), nil)
		require.Equal(t, map[string]string{AnnotationWriter: "bob", AnnotationWriterGroups: "team-a", "keep": "me"}, got)
	})
	t.Run("a spec change restamps", func(t *testing.T) {
		got := stamped(t, admissionv1.Update, bob, object("v2", aliceStamp, nil), object("v1", aliceStamp, nil))
		require.Equal(t, "bob", got[AnnotationWriter])
	})
	t.Run("a metadata-only update keeps the stamp", func(t *testing.T) {
		touched := map[string]string{AnnotationWriter: "bob", AnnotationWriterGroups: "system:masters", "note": "x"}
		got := stamped(t, admissionv1.Update, bob, object("v1", touched, nil), object("v1", aliceStamp, nil))
		require.Equal(t, map[string]string{AnnotationWriter: "alice", AnnotationWriterGroups: "payments-leads,system:authenticated", "note": "x"}, got)
	})
	t.Run("a metadata-only update of an unstamped object stays unstamped", func(t *testing.T) {
		got := stamped(t, admissionv1.Update, bob, object("v1", map[string]string{AnnotationWriter: "bob"}, nil), object("v1", nil, nil))
		require.Empty(t, got)
	})
	t.Run("vela-core dispatching for an Application", func(t *testing.T) {
		labels := map[string]string{"app.oam.dev/name": "addon-github", "app.oam.dev/namespace": "vela-system"}
		got := stamped(t, admissionv1.Create, authenticationv1.UserInfo{Username: core, Groups: []string{"system:serviceaccounts"}},
			object("v1", nil, labels), nil)
		require.Equal(t, map[string]string{AnnotationWriter: "application:vela-system/addon-github"}, got)
	})
	t.Run("vela-core for nothing it names", func(t *testing.T) {
		got := stamped(t, admissionv1.Create, authenticationv1.UserInfo{Username: core}, object("v1", nil, nil), nil)
		require.Equal(t, map[string]string{AnnotationWriter: "vela-core"}, got)
	})
}
