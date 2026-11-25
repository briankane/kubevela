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

// Package component admits writes to Components from their producers only.
package component

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strings"

	admissionv1 "k8s.io/api/admission/v1"
	authenticationv1 "k8s.io/api/authentication/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	"github.com/oam-dev/kubevela/apis/core.oam.dev/v1alpha1"
)

// Path is where the handler is served.
const Path = "/validating-core-oam-dev-v1alpha1-components"

const groupPrefix = "group:"

// Producers are the identities allowed to write a Component's spec: usernames, and groups
// written as group:<name>.
type Producers struct {
	users, groups map[string]bool
}

// ParseProducers reads producer entries, ignoring blanks.
func ParseProducers(entries []string) (Producers, error) {
	p := Producers{users: map[string]bool{}, groups: map[string]bool{}}
	for _, e := range entries {
		e = strings.TrimSpace(e)
		switch {
		case e == "":
		case strings.HasPrefix(e, groupPrefix):
			g := strings.TrimPrefix(e, groupPrefix)
			if g == "" {
				return p, fmt.Errorf("component producer %q names no group", e)
			}
			p.groups[g] = true
		default:
			p.users[e] = true
		}
	}
	return p, nil
}

// Allows reports whether user is a producer.
func (p Producers) Allows(user authenticationv1.UserInfo) bool {
	if p.users[user.Username] {
		return true
	}
	for _, g := range user.Groups {
		if p.groups[g] {
			return true
		}
	}
	return false
}

func (p Producers) empty() bool { return len(p.users) == 0 && len(p.groups) == 0 }

func (p Producers) String() string {
	var out []string
	for u := range p.users {
		out = append(out, u)
	}
	for g := range p.groups {
		out = append(out, groupPrefix+g)
	}
	sort.Strings(out)
	return strings.Join(out, ", ")
}

// ValidatingHandler refuses anyone but a producer creating a Component or changing its spec.
// Others may change metadata, which the signature does not cover: vela-agent manages its
// finalizer that way. Anyone may delete one, as far as RBAC allows: without a signed tombstone
// vela-agent leaves its resources in place.
type ValidatingHandler struct {
	Decoder   admission.Decoder
	Producers Producers
}

// Handle admits or refuses one request.
func (h *ValidatingHandler) Handle(_ context.Context, req admission.Request) admission.Response {
	if h.Producers.Allows(req.UserInfo) {
		return admission.Allowed("")
	}
	if req.Operation == admissionv1.Update {
		oldComp, newComp := &v1alpha1.Component{}, &v1alpha1.Component{}
		if err := h.Decoder.DecodeRaw(req.OldObject, oldComp); err != nil {
			return admission.Errored(http.StatusBadRequest, err)
		}
		if err := h.Decoder.DecodeRaw(req.Object, newComp); err != nil {
			return admission.Errored(http.StatusBadRequest, err)
		}
		if equality.Semantic.DeepEqual(oldComp.Spec, newComp.Spec) {
			return admission.Allowed("")
		}
	}
	if req.Operation == admissionv1.Connect || req.Operation == admissionv1.Delete {
		return admission.Allowed("")
	}
	if h.Producers.empty() {
		return admission.Denied(fmt.Sprintf("%q may not %s a Component: no producers are configured (--component-producers)",
			req.UserInfo.Username, verb(req.Operation)))
	}
	return admission.Denied(fmt.Sprintf("%q may not %s a Component: only its producers may (%s)",
		req.UserInfo.Username, verb(req.Operation), h.Producers))
}

func verb(op admissionv1.Operation) string {
	switch op {
	case admissionv1.Update:
		return "change the spec of"
	default:
		return strings.ToLower(string(op))
	}
}

// RegisterValidatingHandler serves the handler on the manager's webhook server.
func RegisterValidatingHandler(mgr manager.Manager, producers Producers) {
	mgr.GetWebhookServer().Register(Path, &webhook.Admission{Handler: &ValidatingHandler{
		Decoder:   admission.NewDecoder(mgr.GetScheme()),
		Producers: producers,
	}})
}
