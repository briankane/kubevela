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

// Package packages validates CueX Packages at admission.
package packages

import (
	"context"
	"fmt"
	"net/http"

	cuev1alpha1 "github.com/kubevela/pkg/apis/cue/v1alpha1"
	"github.com/kubevela/pkg/cue/cuex/access"
	admissionv1 "k8s.io/api/admission/v1"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

// Path is where the handler is served.
const Path = "/validating-cue-oam-dev-v1alpha1-packages"

var gvr = schema.GroupVersionResource{Group: cuev1alpha1.Group, Version: cuev1alpha1.Version, Resource: "packages"}

// ValidatingHandler refuses a Package whose restrictions cannot be read.
type ValidatingHandler struct {
	Decoder admission.Decoder
}

var _ admission.Handler = &ValidatingHandler{}

// Handle validates a created or updated Package. An update that leaves the spec
// as it was is admitted, so a Package stored before this check can still be
// relabelled or deleted.
func (h *ValidatingHandler) Handle(_ context.Context, req admission.Request) admission.Response {
	if req.Resource.String() != gvr.String() {
		return admission.Errored(http.StatusBadRequest, fmt.Errorf("expect resource to be %s", gvr))
	}
	if req.Operation != admissionv1.Create && req.Operation != admissionv1.Update {
		return admission.ValidationResponse(true, "")
	}
	obj := &cuev1alpha1.Package{}
	if err := h.Decoder.Decode(req, obj); err != nil {
		return admission.Errored(http.StatusBadRequest, err)
	}
	if req.Operation == admissionv1.Update && len(req.OldObject.Raw) > 0 {
		old := &cuev1alpha1.Package{}
		if err := h.Decoder.DecodeRaw(req.OldObject, old); err == nil && apiequality.Semantic.DeepEqual(old.Spec, obj.Spec) {
			return admission.ValidationResponse(true, "")
		}
	}
	if err := access.Validate(obj.Spec.Restrictions); err != nil {
		return admission.Denied(err.Error())
	}
	return admission.ValidationResponse(true, "")
}

// RegisterValidatingHandler serves the handler on the manager's webhook server.
func RegisterValidatingHandler(mgr manager.Manager) {
	mgr.GetWebhookServer().Register(Path, &webhook.Admission{Handler: &ValidatingHandler{Decoder: admission.NewDecoder(mgr.GetScheme())}})
}
