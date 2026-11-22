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
	"testing"

	wfTypesv1alpha1 "github.com/kubevela/pkg/apis/oam/v1alpha1"
	"github.com/stretchr/testify/require"
	admissionv1 "k8s.io/api/admission/v1"
	authenticationv1 "k8s.io/api/authentication/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	"github.com/oam-dev/kubevela/apis/core.oam.dev/common"
	"github.com/oam-dev/kubevela/apis/core.oam.dev/v1alpha1"
	"github.com/oam-dev/kubevela/apis/core.oam.dev/v1beta1"
	"github.com/oam-dev/kubevela/pkg/oam"
)

func accessHandler(t *testing.T, objs ...client.Object) *ValidatingHandler {
	t.Helper()
	s := runtime.NewScheme()
	require.NoError(t, clientgoscheme.AddToScheme(s))
	require.NoError(t, v1beta1.AddToScheme(s))
	require.NoError(t, v1alpha1.AddToScheme(s))
	require.NoError(t, wfTypesv1alpha1.AddToScheme(s))
	sys := oam.SystemDefinitionNamespace
	base := []client.Object{
		&v1beta1.ComponentDefinition{ObjectMeta: metav1.ObjectMeta{Name: "webservice", Namespace: sys}},
		&v1beta1.ComponentDefinition{ObjectMeta: metav1.ObjectMeta{Name: "aws-rds", Namespace: sys},
			Spec: v1beta1.ComponentDefinitionSpec{Restrictions: &common.DefinitionRestrictions{
				RequireRoles: []common.RoleRef{{Kind: "ClusterRole", Name: "db-owners"}},
				Message:      "Ask #platform for db-owners.",
			}}},
		&v1beta1.TraitDefinition{ObjectMeta: metav1.ObjectMeta{Name: "public-ingress", Namespace: sys},
			Spec: v1beta1.TraitDefinitionSpec{Restrictions: &common.DefinitionRestrictions{
				RequireRoles: []common.RoleRef{{Kind: "ClusterRole", Name: "db-owners"}},
				Mode:         common.RestrictionModeWarn,
			}}},
		&rbacv1.ClusterRoleBinding{ObjectMeta: metav1.ObjectMeta{Name: "db-owners"},
			RoleRef:  rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: "db-owners"},
			Subjects: []rbacv1.Subject{{Kind: rbacv1.GroupKind, Name: "payments-leads"}}},
	}
	cli := fake.NewClientBuilder().WithScheme(s).WithObjects(append(base, objs...)...).Build()
	return &ValidatingHandler{Client: cli, controllerUsername: "system:serviceaccount:vela-system:kubevela-vela-core"}
}

func accessApp(components ...common.ApplicationComponent) *v1beta1.Application {
	return &v1beta1.Application{
		ObjectMeta: metav1.ObjectMeta{Name: "shop", Namespace: "team-a"},
		Spec:       v1beta1.ApplicationSpec{Components: components},
	}
}

func comp(name, typ, props string, traits ...common.ApplicationTrait) common.ApplicationComponent {
	c := common.ApplicationComponent{Name: name, Type: typ, Traits: traits}
	if props != "" {
		c.Properties = &runtime.RawExtension{Raw: []byte(props)}
	}
	return c
}

func requestBy(user string, groups ...string) admission.Request {
	return admission.Request{AdmissionRequest: admissionv1.AdmissionRequest{
		UserInfo: authenticationv1.UserInfo{Username: user, Groups: groups},
	}}
}

func TestDefinitionAccessOnCreate(t *testing.T) {
	h := accessHandler(t)
	app := accessApp(comp("web", "webservice", ""), comp("db", "aws-rds", `{"size":"small"}`))

	errs, _ := h.ValidateDefinitionAccess(context.Background(), app, nil, requestBy("bob"))
	require.Len(t, errs, 1)
	require.Equal(t, "spec.components[1].type", errs[0].Field)
	require.Equal(t, `ComponentDefinition "aws-rds" is restricted: the writer bob must be a member of ClusterRole/db-owners. Ask #platform for db-owners.`, errs[0].Detail)

	errs, _ = h.ValidateDefinitionAccess(context.Background(), app, nil, requestBy("alice", "payments-leads"))
	require.Empty(t, errs)

	pinned := accessApp(comp("db", "aws-rds@v1", ""))
	errs, _ = h.ValidateDefinitionAccess(context.Background(), pinned, nil, requestBy("bob"))
	require.Len(t, errs, 1, "a pinned revision is checked as its definition")
}

func TestDefinitionAccessWarns(t *testing.T) {
	h := accessHandler(t)
	app := accessApp(comp("web", "webservice", "", common.ApplicationTrait{Type: "public-ingress"}))
	errs, warnings := h.ValidateDefinitionAccess(context.Background(), app, nil, requestBy("bob"))
	require.Empty(t, errs)
	require.Equal(t, []string{`spec.components[0].traits[0].type: TraitDefinition "public-ingress" is restricted: the writer bob must be a member of ClusterRole/db-owners`}, warnings)
}

// A writer without the role may still change what does not need it.
func TestDefinitionAccessOnUpdate(t *testing.T) {
	h := accessHandler(t)
	old := accessApp(comp("web", "webservice", `{"image":"a"}`), comp("db", "aws-rds", `{"size":"small"}`))
	bob := requestBy("bob")

	cases := map[string]struct {
		app     *v1beta1.Application
		refused []string
	}{
		"another component changed":      {accessApp(comp("web", "webservice", `{"image":"b"}`), comp("db", "aws-rds", `{"size":"small"}`)), nil},
		"the restricted component moved": {accessApp(comp("db", "aws-rds", `{"size":"small"}`), comp("web", "webservice", `{"image":"b"}`)), nil},
		"a trait added to the restricted component": {accessApp(comp("web", "webservice", `{"image":"a"}`),
			comp("db", "aws-rds", `{"size":"small"}`, common.ApplicationTrait{Type: "scaler"})), nil},
		"its properties changed": {accessApp(comp("web", "webservice", `{"image":"a"}`), comp("db", "aws-rds", `{"size":"large"}`)),
			[]string{"spec.components[1].type"}},
		"renamed, so a new use": {accessApp(comp("web", "webservice", `{"image":"a"}`), comp("db2", "aws-rds", `{"size":"small"}`)),
			[]string{"spec.components[1].type"}},
		"a second one added": {accessApp(comp("web", "webservice", `{"image":"a"}`), comp("db", "aws-rds", `{"size":"small"}`), comp("cache", "aws-rds", "")),
			[]string{"spec.components[2].type"}},
		"a component turned into one": {accessApp(comp("web", "aws-rds", `{"image":"a"}`), comp("db", "aws-rds", `{"size":"small"}`)),
			[]string{"spec.components[0].type"}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			errs, _ := h.ValidateDefinitionAccess(context.Background(), tc.app, old, bob)
			var fields []string
			for _, e := range errs {
				fields = append(fields, e.Field)
			}
			require.Equal(t, tc.refused, fields)
		})
	}
}

// vela-core writing as itself is checked as the Application that dispatched the write.
func TestDefinitionAccessChildApplications(t *testing.T) {
	h := accessHandler(t)
	child := func(parentNS string) *v1beta1.Application {
		app := accessApp(comp("db", "aws-rds", ""))
		app.Labels = map[string]string{oam.LabelAppName: "parent", oam.LabelAppNamespace: parentNS}
		return app
	}
	core := requestBy(h.controllerUsername, "system:serviceaccounts")

	errs, _ := h.ValidateDefinitionAccess(context.Background(), child(oam.SystemDefinitionNamespace), nil, core)
	require.Empty(t, errs, "an addon holds vela-core's roles")

	errs, _ = h.ValidateDefinitionAccess(context.Background(), child("team-a"), nil, core)
	require.Len(t, errs, 1, "a team's Application holds none")
	require.Contains(t, errs[0].Detail, "the writer application:team-a/parent must be a member")

	errs, _ = h.ValidateDefinitionAccess(context.Background(), accessApp(comp("db", "aws-rds", "")), nil, core)
	require.Len(t, errs, 1, "vela-core with no dispatcher holds none")
}

// A Workflow the Application references may change without the Application, so
// its uses are checked on every write.
func TestDefinitionAccessExternalUses(t *testing.T) {
	restrictedStep := &v1beta1.WorkflowStepDefinition{ObjectMeta: metav1.ObjectMeta{Name: "provision-db", Namespace: oam.SystemDefinitionNamespace},
		Spec: v1beta1.WorkflowStepDefinitionSpec{Restrictions: &common.DefinitionRestrictions{
			RequireRoles: []common.RoleRef{{Kind: "ClusterRole", Name: "db-owners"}},
		}}}
	wf := &wfTypesv1alpha1.Workflow{ObjectMeta: metav1.ObjectMeta{Name: "release", Namespace: "team-a"},
		Steps: []wfTypesv1alpha1.WorkflowStep{{WorkflowStepBase: wfTypesv1alpha1.WorkflowStepBase{Name: "provision", Type: "provision-db"}}}}
	h := accessHandler(t, restrictedStep, wf)
	app := accessApp(comp("web", "webservice", ""))
	app.Spec.Workflow = &v1beta1.Workflow{Ref: "release"}

	errs, _ := h.ValidateDefinitionAccess(context.Background(), app, app.DeepCopy(), requestBy("bob"))
	require.Len(t, errs, 1)
	require.Equal(t, "spec.workflow.ref", errs[0].Field)
}
