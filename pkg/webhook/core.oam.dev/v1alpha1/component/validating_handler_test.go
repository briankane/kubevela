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
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
	admissionv1 "k8s.io/api/admission/v1"
	authenticationv1 "k8s.io/api/authentication/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	"github.com/oam-dev/kubevela/apis/core.oam.dev/v1alpha1"
	"github.com/oam-dev/kubevela/pkg/utils/common"
)

const hubSA = "system:serviceaccount:vela-system:kubevela-vela-core"

func handler(t *testing.T, producers ...string) *ValidatingHandler {
	p, err := ParseProducers(producers)
	require.NoError(t, err)
	return &ValidatingHandler{Decoder: admission.NewDecoder(common.Scheme), Producers: p}
}

func comp(rev int64, finalizers ...string) *v1alpha1.Component {
	return &v1alpha1.Component{
		ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "default", Finalizers: finalizers},
		Spec:       v1alpha1.ComponentSpec{Target: v1alpha1.ComponentTarget{Cluster: "local"}, Revision: rev},
	}
}

func raw(t *testing.T, c *v1alpha1.Component) runtime.RawExtension {
	if c == nil {
		return runtime.RawExtension{}
	}
	b, err := json.Marshal(c)
	require.NoError(t, err)
	return runtime.RawExtension{Raw: b}
}

func request(t *testing.T, op admissionv1.Operation, user authenticationv1.UserInfo, old, obj *v1alpha1.Component) admission.Request {
	return admission.Request{AdmissionRequest: admissionv1.AdmissionRequest{
		Operation: op, UserInfo: user, Object: raw(t, obj), OldObject: raw(t, old),
		Name: "web", Namespace: "default",
	}}
}

func TestProducersMayWrite(t *testing.T) {
	h := handler(t, hubSA, "group:vela-hubs")
	for name, user := range map[string]authenticationv1.UserInfo{
		"by username": {Username: hubSA},
		"by group":    {Username: "hub-prod", Groups: []string{"system:authenticated", "vela-hubs"}},
	} {
		t.Run(name, func(t *testing.T) {
			for _, req := range []admission.Request{
				request(t, admissionv1.Create, user, nil, comp(1)),
				request(t, admissionv1.Update, user, comp(1), comp(2)),
				request(t, admissionv1.Delete, user, comp(2), nil),
			} {
				resp := h.Handle(context.Background(), req)
				require.True(t, resp.Allowed, "%s: %v", req.Operation, resp.Result)
			}
		})
	}
}

func TestOthersMayNotWriteTheSpec(t *testing.T) {
	h := handler(t, hubSA)
	alice := authenticationv1.UserInfo{Username: "alice", Groups: []string{"system:authenticated"}}
	for name, req := range map[string]admission.Request{
		"create":      request(t, admissionv1.Create, alice, nil, comp(1)),
		"spec update": request(t, admissionv1.Update, alice, comp(1), comp(2)),
	} {
		t.Run(name, func(t *testing.T) {
			resp := h.Handle(context.Background(), req)
			require.False(t, resp.Allowed)
			require.Contains(t, resp.Result.Message, `"alice"`)
			require.Contains(t, resp.Result.Message, "producer")
		})
	}
}

// Anyone RBAC lets delete a Component may, the namespace and garbage collectors among them:
// without a signed tombstone vela-agent leaves its resources in place, and the hub recreates it.
func TestOthersMayDelete(t *testing.T) {
	h := handler(t, hubSA)
	gc := authenticationv1.UserInfo{Username: "system:serviceaccount:kube-system:namespace-controller"}
	resp := h.Handle(context.Background(), request(t, admissionv1.Delete, gc, comp(1), nil))
	require.True(t, resp.Allowed, "%v", resp.Result)
}

// vela-agent adds and removes its finalizer with an ordinary update.
func TestOthersMayChangeMetadataOnly(t *testing.T) {
	h := handler(t, hubSA)
	agent := authenticationv1.UserInfo{Username: "system:serviceaccount:vela-system:vela-agent"}
	resp := h.Handle(context.Background(), request(t, admissionv1.Update, agent, comp(1), comp(1, "component.oam.dev/resource-tracker")))
	require.True(t, resp.Allowed, "%v", resp.Result)
}

func TestNoProducersRefusesEveryone(t *testing.T) {
	h := handler(t)
	resp := h.Handle(context.Background(), request(t, admissionv1.Create, authenticationv1.UserInfo{Username: hubSA}, nil, comp(1)))
	require.False(t, resp.Allowed)
	require.Contains(t, resp.Result.Message, "no producers are configured")
}

func TestParseProducers(t *testing.T) {
	p, err := ParseProducers([]string{" " + hubSA + " ", "group:vela-hubs", ""})
	require.NoError(t, err)
	require.True(t, p.Allows(authenticationv1.UserInfo{Username: hubSA}))
	require.True(t, p.Allows(authenticationv1.UserInfo{Username: "x", Groups: []string{"vela-hubs"}}))
	require.False(t, p.Allows(authenticationv1.UserInfo{Username: "group:vela-hubs"}), "a group entry never matches a username")

	_, err = ParseProducers([]string{"group:"})
	require.ErrorContains(t, err, "group:")
}
