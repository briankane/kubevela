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

package application

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	authenticationv1 "k8s.io/api/authentication/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/util/validation/field"
	utilfeature "k8s.io/apiserver/pkg/util/feature"
	"k8s.io/klog/v2"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	"github.com/oam-dev/kubevela/apis/core.oam.dev/v1beta1"
	"github.com/oam-dev/kubevela/pkg/definition/access"
	"github.com/oam-dev/kubevela/pkg/definition/nsrestrict"
	"github.com/oam-dev/kubevela/pkg/features"
	"github.com/oam-dev/kubevela/pkg/oam"
	oamutil "github.com/oam-dev/kubevela/pkg/oam/util"
	"github.com/oam-dev/kubevela/pkg/workflow/step"
)

// ValidateDefinitionAccess refuses an Application whose writer may not use a
// definition that requires a role or a grant, or warns where the definition says
// so. On update only the uses whose type or properties changed, and the uses added,
// are checked, so a writer without a role can still change what does not need it.
// A use brought in by another object, a referenced Workflow or Policy, is checked
// on every write, since the object may have changed without the Application.
func (h *ValidatingHandler) ValidateDefinitionAccess(ctx context.Context, app, oldApp *v1beta1.Application, req admission.Request) (field.ErrorList, []string) {
	if !utilfeature.DefaultMutableFeatureGate.Enabled(features.RestrictDefinitionNamespaces) {
		return nil, nil
	}
	uses := definitionUses(ctx, h.Client, app)
	if oldApp != nil {
		uses = changedUses(uses, app, oldApp)
	}
	if len(uses) == 0 {
		return nil, nil
	}
	writer, skip, err := h.applicationWriter(ctx, app, req)
	if err != nil {
		return forbidAll(pathsOf(uses), func(p *field.Path) *field.Error { return field.InternalError(p, err) }), nil
	}
	if skip {
		return nil, nil
	}

	defCtx := oamutil.SetNamespaceInCtx(ctx, app.Namespace)
	var errs field.ErrorList
	var warnings []string
	for _, u := range groupUses(uses) {
		def, err := loadDefinition(defCtx, h.Client, baseDefinitionName(u.defType), u.newDef)
		if errors.IsNotFound(err) {
			continue // ValidateComponents reports it
		}
		if err != nil {
			errs = append(errs, forbidAll(u.paths, func(p *field.Path) *field.Error {
				return field.InternalError(p, fmt.Errorf("cannot read %s %q to check who may use it: %w", nsrestrict.KindOf(def), u.defType, err))
			})...)
			continue
		}
		res, err := access.Check(ctx, h.Client, def, writer)
		switch {
		case err != nil:
			errs = append(errs, forbidAll(u.paths, func(p *field.Path) *field.Error { return field.InternalError(p, err) })...)
		case res.Refusal == "":
		case res.Warn:
			klog.Infof("Would refuse Application %s/%s: %s", app.Namespace, app.Name, res.Refusal)
			warnings = append(warnings, fmt.Sprintf("%s: %s", u.paths[0], res.Refusal))
		default:
			errs = append(errs, forbidAll(u.paths, func(p *field.Path) *field.Error { return field.Forbidden(p, res.Refusal) })...)
		}
	}
	return errs, warnings
}

// applicationWriter is whom roles and grants are checked for: whoever made the
// request, or, when vela-core made it as itself, the Application that dispatched
// this one. skip is true for a dispatcher in the system namespace, an addon, which
// holds vela-core's own roles and grants. Any other dispatcher is checked as its
// author where that author is recorded and trusted, and otherwise as
// application:<namespace>/<name>, which holds none unless someone binds it.
func (h *ValidatingHandler) applicationWriter(ctx context.Context, app *v1beta1.Application, req admission.Request) (authenticationv1.UserInfo, bool, error) {
	if !h.fromController(req) {
		return req.UserInfo, false, nil
	}
	dispatcher := dispatchingNamespace(app)
	if dispatcher == oam.SystemDefinitionNamespace {
		return authenticationv1.UserInfo{}, true, nil
	}
	if dispatcher == "" {
		return authenticationv1.UserInfo{Username: "vela-core"}, false, nil
	}
	author, trusted, err := h.dispatcherAuthor(ctx, app)
	if err != nil || trusted {
		return author, false, err
	}
	return authenticationv1.UserInfo{Username: "application:" + dispatcher + "/" + app.GetLabels()[oam.LabelAppName]}, false, nil
}

// definitionUse is one place an Application uses a definition.
type definitionUse struct {
	defType string
	newDef  func() client.Object
	path    *field.Path
	// external says the use comes from another object the Application references.
	external bool
}

// definitionUses lists every use the namespace restrictions check, at its path.
func definitionUses(ctx context.Context, cli client.Client, app *v1beta1.Application) []definitionUse {
	usage := collectDefinitionUsage(app)
	var uses []definitionUse
	add := func(defType string, newDef func() client.Object, paths []*field.Path) {
		for _, p := range paths {
			uses = append(uses, definitionUse{defType: defType, newDef: newDef, path: p})
		}
	}
	for t, i := range usage.componentTypes {
		add(t, newComponentDefinition, componentTypePaths(i))
	}
	for t, l := range usage.traitTypes {
		add(t, newTraitDefinition, traitTypePaths(l))
	}
	for t, i := range usage.policyTypes {
		add(t, newPolicyDefinition, policyTypePaths(i))
	}
	for t, l := range usage.workflowStepTypes {
		if !step.IsBuiltinWorkflowStepType(t) {
			add(t, newWorkflowStepDefinition, workflowStepTypePaths(l))
		}
	}
	for t, i := range usage.sourceTypes {
		add(t, func() client.Object { return &v1beta1.SourceDefinition{} }, sourceTypePaths(i))
	}
	for _, u := range indirectReach(ctx, cli, app).uses {
		// An inline override names its type at a .type field; anything else is
		// reported at the reference that loads another object.
		external := !strings.HasSuffix(u.path.String(), ".type")
		uses = append(uses, definitionUse{defType: u.defType, newDef: u.newDef, path: u.path, external: external})
	}
	return uses
}

// changedUses keeps the uses that are new, changed, or external.
func changedUses(uses []definitionUse, app, oldApp *v1beta1.Application) []definitionUse {
	now, before := asJSON(app.Spec), asJSON(oldApp.Spec)
	var out []definitionUse
	for _, u := range uses {
		if u.external || !sameUse(u.path, now, before) {
			out = append(out, u)
		}
	}
	return out
}

// sameUse reports whether the use at path is in both specs with the same type and
// properties. A list element is matched in the old spec by its name, or by its
// type where it has none (a trait), so moving a component does not change it.
func sameUse(path *field.Path, now, before any) bool {
	segs := pathSegments(path)
	if len(segs) == 0 || segs[0] != "spec" {
		return false
	}
	segs = segs[1:]
	if len(segs) > 0 && segs[len(segs)-1] == "type" {
		segs = segs[:len(segs)-1]
	}
	for _, s := range segs {
		if i, err := strconv.Atoi(s); err == nil {
			list, ok := now.([]any)
			oldList, ok2 := before.([]any)
			if !ok || !ok2 || i >= len(list) {
				return false
			}
			now = list[i]
			if before = matching(oldList, now); before == nil {
				return false
			}
			continue
		}
		m, ok := now.(map[string]any)
		om, ok2 := before.(map[string]any)
		if !ok || !ok2 {
			return false
		}
		now, before = m[s], om[s]
	}
	m, ok := now.(map[string]any)
	om, ok2 := before.(map[string]any)
	if !ok || !ok2 {
		return equality.Semantic.DeepEqual(now, before)
	}
	return equality.Semantic.DeepEqual(m["type"], om["type"]) && equality.Semantic.DeepEqual(m["properties"], om["properties"])
}

// matching finds elem's counterpart in list: the one element with its name, or
// with its type where it has no name.
func matching(list []any, elem any) any {
	key, val := identity(elem)
	if key == "" {
		return nil
	}
	var found any
	for _, o := range list {
		if k, v := identity(o); k == key && v == val {
			if found != nil {
				return nil
			}
			found = o
		}
	}
	return found
}

func identity(elem any) (string, string) {
	m, ok := elem.(map[string]any)
	if !ok {
		return "", ""
	}
	if n, ok := m["name"].(string); ok && n != "" {
		return "name", n
	}
	if t, ok := m["type"].(string); ok && t != "" {
		return "type", t
	}
	return "", ""
}

var indexSegment = regexp.MustCompile(`\[(\d+)\]`)

// pathSegments splits spec.components[2].type into spec, components, 2, type.
func pathSegments(p *field.Path) []string {
	s := indexSegment.ReplaceAllString(p.String(), ".$1")
	return strings.Split(s, ".")
}

func asJSON(v any) any {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	var out any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil
	}
	return out
}

// groupedUse is one definition with every path it is used at.
type groupedUse struct {
	defType string
	newDef  func() client.Object
	paths   []*field.Path
}

// groupUses checks each definition once: webservice and webservice@v1 are one.
func groupUses(uses []definitionUse) []groupedUse {
	var out []groupedUse
	index := map[string]int{}
	for _, u := range uses {
		key := fmt.Sprintf("%T/%s", u.newDef(), baseDefinitionName(u.defType))
		i, ok := index[key]
		if !ok {
			i = len(out)
			index[key] = i
			out = append(out, groupedUse{defType: u.defType, newDef: u.newDef})
		}
		out[i].paths = append(out[i].paths, u.path)
	}
	return out
}

func pathsOf(uses []definitionUse) []*field.Path {
	paths := make([]*field.Path, len(uses))
	for i, u := range uses {
		paths[i] = u.path
	}
	return paths
}
