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

// Package writer stamps definitions and CueX Packages with who last changed their
// spec, so the functions their CUE calls can be checked against that writer's
// roles and grants when it renders.
package writer

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	admissionv1 "k8s.io/api/admission/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	"github.com/oam-dev/kubevela/pkg/oam"
	"github.com/oam-dev/kubevela/pkg/utils"
)

const (
	// AnnotationWriter is the user who last changed the object's spec.
	AnnotationWriter = "cue.oam.dev/writer"
	// AnnotationWriterGroups is that user's groups at the time, comma-separated.
	AnnotationWriterGroups = "cue.oam.dev/writer-groups"

	// Path is where the handler is served.
	Path = "/mutating-core-oam-dev-v1beta1-writers"
	// WebhookName is the webhook's name in the chart's configuration. Its policy
	// is always Ignore: a definition written while no vela-core serves it has no
	// writer, and its restricted calls are refused at render.
	WebhookName = "mutating.core.oam.dev.v1beta1.writers"
)

// Handler stamps the writer on create and on any update that changes the spec,
// overwriting whatever the object carries. An update that leaves the spec alone
// keeps the stamp it had, so a label or annotation change, or the controller's own
// metadata writes, never become the writer.
type Handler struct {
	// ControllerUsername is vela-core's own identity. A write made with it is
	// stamped as the Application that dispatched the object, or as vela-core.
	ControllerUsername string
}

var _ admission.Handler = &Handler{}

// Handle implements admission.Handler.
func (h *Handler) Handle(_ context.Context, req admission.Request) admission.Response {
	obj := &unstructured.Unstructured{}
	if err := json.Unmarshal(req.Object.Raw, &obj.Object); err != nil {
		return admission.Errored(http.StatusBadRequest, err)
	}
	user, groups := h.writerOf(req, obj)
	if req.Operation == admissionv1.Update {
		old := &unstructured.Unstructured{}
		if err := json.Unmarshal(req.OldObject.Raw, &old.Object); err != nil {
			return admission.Errored(http.StatusBadRequest, err)
		}
		if equality.Semantic.DeepEqual(obj.Object["spec"], old.Object["spec"]) {
			user, groups = old.GetAnnotations()[AnnotationWriter], old.GetAnnotations()[AnnotationWriterGroups]
		}
	}
	annotations := obj.GetAnnotations()
	if annotations == nil {
		annotations = map[string]string{}
	}
	set(annotations, AnnotationWriter, user)
	set(annotations, AnnotationWriterGroups, groups)
	if len(annotations) == 0 {
		annotations = nil
	}
	obj.SetAnnotations(annotations)
	raw, err := json.Marshal(obj.Object)
	if err != nil {
		return admission.Errored(http.StatusInternalServerError, err)
	}
	return admission.PatchResponseFromRaw(req.Object.Raw, raw)
}

// writerOf is who the request is from. vela-core writing as itself is recorded as
// application:<namespace>/<name> from the labels it sets on what it dispatches, or
// as vela-core, with no groups: an Application holds roles and grants only as
// decided where the stamp is checked.
func (h *Handler) writerOf(req admission.Request, obj *unstructured.Unstructured) (string, string) {
	if h.ControllerUsername == "" || req.UserInfo.Username != h.ControllerUsername {
		return req.UserInfo.Username, strings.Join(req.UserInfo.Groups, ",")
	}
	labels := obj.GetLabels()
	if name, ns := labels[oam.LabelAppName], labels[oam.LabelAppNamespace]; name != "" && ns != "" {
		return "application:" + ns + "/" + name, ""
	}
	return "vela-core", ""
}

func set(m map[string]string, k, v string) {
	if v == "" {
		delete(m, k)
		return
	}
	m[k] = v
}

// RegisterHandler serves the handler for every definition kind and for Packages,
// as the chart's webhook configuration routes them.
func RegisterHandler(mgr manager.Manager) {
	mgr.GetWebhookServer().Register(Path, &webhook.Admission{Handler: &Handler{ControllerUsername: controllerUsername(mgr.GetConfig())}})
}

func controllerUsername(cfg *rest.Config) string {
	if u := utils.GetUserInfoFromConfig(cfg); u != nil {
		return u.Username
	}
	return ""
}
