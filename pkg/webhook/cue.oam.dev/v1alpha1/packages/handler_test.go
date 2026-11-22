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

package packages

import (
	"context"
	"encoding/json"
	"testing"

	cuev1alpha1 "github.com/kubevela/pkg/apis/cue/v1alpha1"
	"github.com/stretchr/testify/require"
	admissionv1 "k8s.io/api/admission/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	"github.com/oam-dev/kubevela/pkg/utils/common"
)

func pkgWith(namespaces ...string) *cuev1alpha1.Package {
	p := &cuev1alpha1.Package{
		TypeMeta:   metav1.TypeMeta{APIVersion: cuev1alpha1.GroupVersion.String(), Kind: "Package"},
		ObjectMeta: metav1.ObjectMeta{Name: "githubwrite", Namespace: "vela-system"},
		Spec:       cuev1alpha1.PackageSpec{Path: "example.com/githubwrite"},
	}
	if namespaces != nil {
		p.Spec.Restrictions = &cuev1alpha1.PackageRestrictions{CallRule: cuev1alpha1.CallRule{Namespaces: namespaces}}
	}
	return p
}

func request(t *testing.T, op admissionv1.Operation, obj, old *cuev1alpha1.Package) admission.Request {
	raw := func(p *cuev1alpha1.Package) runtime.RawExtension {
		if p == nil {
			return runtime.RawExtension{}
		}
		b, err := json.Marshal(p)
		require.NoError(t, err)
		return runtime.RawExtension{Raw: b}
	}
	return admission.Request{AdmissionRequest: admissionv1.AdmissionRequest{
		Operation: op, Resource: metav1.GroupVersionResource(gvr), Object: raw(obj), OldObject: raw(old),
	}}
}

func TestValidatingHandler(t *testing.T) {
	h := &ValidatingHandler{Decoder: admission.NewDecoder(common.Scheme)}
	ctx := context.Background()

	t.Run("restrictions that read are admitted", func(t *testing.T) {
		require.True(t, h.Handle(ctx, request(t, admissionv1.Create, pkgWith("team-*"), nil)).Allowed)
		require.True(t, h.Handle(ctx, request(t, admissionv1.Create, pkgWith(), nil)).Allowed)
	})
	t.Run("restrictions that cannot be read are refused, saying where", func(t *testing.T) {
		resp := h.Handle(ctx, request(t, admissionv1.Create, pkgWith("team-["), nil))
		require.False(t, resp.Allowed)
		require.Contains(t, resp.Result.Message, `spec.restrictions.namespaces[0]: "team-[" is not a namespace name or glob`)
		require.False(t, h.Handle(ctx, request(t, admissionv1.Update, pkgWith("team-["), pkgWith("team-*"))).Allowed)
	})
	t.Run("a stored problem goes through an update that leaves the spec alone", func(t *testing.T) {
		old := pkgWith("team-[")
		changed := pkgWith("team-[")
		changed.Labels = map[string]string{"owner": "platform"}
		require.True(t, h.Handle(ctx, request(t, admissionv1.Update, changed, old)).Allowed)
	})
}
