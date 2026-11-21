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

package access

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	authenticationv1 "k8s.io/api/authentication/v1"
	authorizationv1 "k8s.io/api/authorization/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"github.com/oam-dev/kubevela/apis/core.oam.dev/common"
	"github.com/oam-dev/kubevela/apis/core.oam.dev/v1beta1"
	"github.com/oam-dev/kubevela/pkg/oam"
)

var dbOwners = common.RoleRef{Kind: "ClusterRole", Name: "db-owners"}

func binding(ns, name string, role rbacv1.RoleRef, subjects ...rbacv1.Subject) client.Object {
	if ns == "" {
		return &rbacv1.ClusterRoleBinding{ObjectMeta: metav1.ObjectMeta{Name: name}, RoleRef: role, Subjects: subjects}
	}
	return &rbacv1.RoleBinding{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns}, RoleRef: role, Subjects: subjects}
}

func clusterRole(name string) rbacv1.RoleRef {
	return rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: name}
}

func def(r *common.DefinitionRestrictions, annotations map[string]string) *v1beta1.ComponentDefinition {
	return &v1beta1.ComponentDefinition{
		ObjectMeta: metav1.ObjectMeta{Name: "aws-rds", Namespace: "vela-system", Annotations: annotations},
		Spec:       v1beta1.ComponentDefinitionSpec{Restrictions: r},
	}
}

// grants answers SubjectAccessReviews from a table of user|verb|resource|name|namespace.
func newClient(t *testing.T, grants map[string]bool, objs ...client.Object) (client.Client, *[]authorizationv1.ResourceAttributes) {
	t.Helper()
	s := runtime.NewScheme()
	require.NoError(t, clientgoscheme.AddToScheme(s))
	var asked []authorizationv1.ResourceAttributes
	cli := fake.NewClientBuilder().WithScheme(s).WithObjects(objs...).WithInterceptorFuncs(interceptor.Funcs{
		Create: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
			sar, ok := obj.(*authorizationv1.SubjectAccessReview)
			if !ok {
				return c.Create(ctx, obj, opts...)
			}
			a := *sar.Spec.ResourceAttributes
			asked = append(asked, a)
			key := sar.Spec.User + "|" + a.Verb + "|" + a.Resource + "|" + a.Name + "|" + a.Namespace
			sar.Status.Allowed = grants[key]
			for _, g := range sar.Spec.Groups {
				sar.Status.Allowed = sar.Status.Allowed || grants["group:"+g+"|"+a.Verb+"|"+a.Resource+"|"+a.Name+"|"+a.Namespace]
			}
			return nil
		},
	}).Build()
	return cli, &asked
}

func TestMember(t *testing.T) {
	alice := authenticationv1.UserInfo{Username: "alice", Groups: []string{"payments-leads"}}
	argo := authenticationv1.UserInfo{Username: "system:serviceaccount:argocd:argocd-payments"}
	bob := authenticationv1.UserInfo{Username: "bob", Groups: []string{"team-a-admins"}}

	cases := map[string]struct {
		writer   authenticationv1.UserInfo
		bindings []client.Object
		want     bool
	}{
		"user through a ClusterRoleBinding": {alice, []client.Object{
			binding("", "b", clusterRole("db-owners"), rbacv1.Subject{Kind: rbacv1.UserKind, Name: "alice"})}, true},
		"group through a ClusterRoleBinding": {alice, []client.Object{
			binding("", "b", clusterRole("db-owners"), rbacv1.Subject{Kind: rbacv1.GroupKind, Name: "payments-leads"})}, true},
		"service account through a ClusterRoleBinding": {argo, []client.Object{
			binding("", "b", clusterRole("db-owners"), rbacv1.Subject{Kind: rbacv1.ServiceAccountKind, Namespace: "argocd", Name: "argocd-payments"})}, true},
		"RoleBinding where the definition lives": {alice, []client.Object{
			binding("vela-system", "b", clusterRole("db-owners"), rbacv1.Subject{Kind: rbacv1.UserKind, Name: "alice"})}, true},
		"RoleBinding in a team's namespace does not count": {bob, []client.Object{
			binding("team-a", "b", clusterRole("db-owners"), rbacv1.Subject{Kind: rbacv1.UserKind, Name: "bob"})}, false},
		"a wider role is not membership": {bob, []client.Object{
			binding("", "b", clusterRole("cluster-admin"), rbacv1.Subject{Kind: rbacv1.UserKind, Name: "bob"})}, false},
		"another subject's binding": {bob, []client.Object{
			binding("", "b", clusterRole("db-owners"), rbacv1.Subject{Kind: rbacv1.UserKind, Name: "alice"})}, false},
		"a service account of the same name in another namespace": {argo, []client.Object{
			binding("", "b", clusterRole("db-owners"), rbacv1.Subject{Kind: rbacv1.ServiceAccountKind, Namespace: "team-a", Name: "argocd-payments"})}, false},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			cli, _ := newClient(t, nil, tc.bindings...)
			got, err := Member(context.Background(), cli, tc.writer, []common.RoleRef{dbOwners}, "vela-system")
			require.NoError(t, err)
			require.Equal(t, tc.want, got)
		})
	}
}

func TestCheck(t *testing.T) {
	alice := authenticationv1.UserInfo{Username: "alice", Groups: []string{"payments-leads"}}
	bob := authenticationv1.UserInfo{Username: "bob"}
	aliceIsOwner := binding("", "b", clusterRole("db-owners"), rbacv1.Subject{Kind: rbacv1.GroupKind, Name: "payments-leads"})

	t.Run("no role or grant asks nothing", func(t *testing.T) {
		cli, asked := newClient(t, nil)
		res, err := Check(context.Background(), cli, def(&common.DefinitionRestrictions{Namespaces: []string{"team-*"}}, nil), bob)
		require.NoError(t, err)
		require.Empty(t, res.Refusal)
		require.Empty(t, *asked)
	})
	t.Run("role refused names the roles and the message", func(t *testing.T) {
		cli, _ := newClient(t, nil, aliceIsOwner)
		d := def(&common.DefinitionRestrictions{RequireRoles: []common.RoleRef{dbOwners, {Kind: "ClusterRole", Name: "platform-ops"}},
			Message: "Ask #platform for db-owners"}, nil)
		res, err := Check(context.Background(), cli, d, bob)
		require.NoError(t, err)
		require.Equal(t, `ComponentDefinition "aws-rds" is restricted: the writer bob must be a member of ClusterRole/db-owners or ClusterRole/platform-ops. Ask #platform for db-owners`, res.Refusal)
		require.False(t, res.Warn)
		res, err = Check(context.Background(), cli, d, alice)
		require.NoError(t, err)
		require.Empty(t, res.Refusal)
	})
	t.Run("role from the annotation", func(t *testing.T) {
		cli, _ := newClient(t, nil, aliceIsOwner)
		d := def(nil, map[string]string{oam.AnnotationRequireRoles: "ClusterRole/db-owners"})
		res, err := Check(context.Background(), cli, d, bob)
		require.NoError(t, err)
		require.Contains(t, res.Refusal, "must be a member of ClusterRole/db-owners")
		res, err = Check(context.Background(), cli, d, alice)
		require.NoError(t, err)
		require.Empty(t, res.Refusal)
	})
	t.Run("spec roles win over the annotation", func(t *testing.T) {
		cli, _ := newClient(t, nil, aliceIsOwner)
		d := def(&common.DefinitionRestrictions{RequireRoles: []common.RoleRef{{Kind: "ClusterRole", Name: "platform-ops"}}},
			map[string]string{oam.AnnotationRequireRoles: "ClusterRole/db-owners"})
		res, err := Check(context.Background(), cli, d, alice)
		require.NoError(t, err)
		require.Contains(t, res.Refusal, "ClusterRole/platform-ops")
	})
	t.Run("grant asked in the definition's namespace", func(t *testing.T) {
		cli, asked := newClient(t, map[string]bool{"group:payments-leads|use|componentdefinitions|aws-rds|vela-system": true})
		d := def(&common.DefinitionRestrictions{RequireGrant: true}, nil)
		res, err := Check(context.Background(), cli, d, alice)
		require.NoError(t, err)
		require.Empty(t, res.Refusal)
		require.Equal(t, "vela-system", (*asked)[0].Namespace)
		require.Equal(t, "core.oam.dev", (*asked)[0].Group)
		res, err = Check(context.Background(), cli, d, bob)
		require.NoError(t, err)
		require.Equal(t, `ComponentDefinition "aws-rds" is restricted: the writer bob must hold "use" on componentdefinitions "aws-rds" in namespace "vela-system"`, res.Refusal)
	})
	t.Run("both required, both missing", func(t *testing.T) {
		cli, _ := newClient(t, nil)
		d := def(&common.DefinitionRestrictions{RequireRoles: []common.RoleRef{dbOwners}, RequireGrant: true}, nil)
		res, err := Check(context.Background(), cli, d, bob)
		require.NoError(t, err)
		require.Contains(t, res.Refusal, "must be a member of ClusterRole/db-owners and hold \"use\"")
	})
	t.Run("warn mode", func(t *testing.T) {
		cli, _ := newClient(t, nil)
		d := def(&common.DefinitionRestrictions{RequireRoles: []common.RoleRef{dbOwners}, Mode: common.RestrictionModeWarn}, nil)
		res, err := Check(context.Background(), cli, d, bob)
		require.NoError(t, err)
		require.NotEmpty(t, res.Refusal)
		require.True(t, res.Warn)
	})
}
