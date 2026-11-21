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

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	authenticationv1 "k8s.io/api/authentication/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/oam-dev/kubevela/apis/core.oam.dev/common"
	"github.com/oam-dev/kubevela/apis/core.oam.dev/v1beta1"
	"github.com/oam-dev/kubevela/pkg/definition/access"
	"github.com/oam-dev/kubevela/pkg/oam"
)

// The envtest API server authorizes with RBAC, so roles and grants here are
// decided by real bindings and real SubjectAccessReviews.
var _ = Describe("Definition access against RBAC", Ordered, func() {
	ctx := context.Background()
	sys := oam.SystemDefinitionNamespace
	var created []client.Object
	create := func(objs ...client.Object) {
		for _, o := range objs {
			Expect(k8sClient.Create(ctx, o)).To(Succeed())
			created = append(created, o)
		}
	}
	rule := func(verbs []string, resources []string, names ...string) rbacv1.PolicyRule {
		return rbacv1.PolicyRule{APIGroups: []string{"core.oam.dev"}, Resources: resources, ResourceNames: names, Verbs: verbs}
	}
	bind := func(ns, name, kind, role string, subjects ...rbacv1.Subject) client.Object {
		ref := rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: kind, Name: role}
		if ns == "" {
			return &rbacv1.ClusterRoleBinding{ObjectMeta: metav1.ObjectMeta{Name: name}, RoleRef: ref, Subjects: subjects}
		}
		return &rbacv1.RoleBinding{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns}, RoleRef: ref, Subjects: subjects}
	}
	user := func(n string) rbacv1.Subject { return rbacv1.Subject{Kind: rbacv1.UserKind, Name: n} }
	group := func(n string) rbacv1.Subject { return rbacv1.Subject{Kind: rbacv1.GroupKind, Name: n} }
	restricted := func(name string, r *common.DefinitionRestrictions) *v1beta1.ComponentDefinition {
		return &v1beta1.ComponentDefinition{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: sys},
			Spec: v1beta1.ComponentDefinitionSpec{
				Workload:     common.WorkloadTypeDescriptor{Definition: common.WorkloadGVK{APIVersion: "v1", Kind: "ConfigMap"}},
				Restrictions: r,
			}}
	}
	check := func(defType, who string, groups ...string) []string {
		app := accessApp(comp("x", defType, ""))
		app.Namespace = "access-team-a"
		errs, _ := handler.ValidateDefinitionAccess(ctx, app, nil, requestBy(who, groups...))
		var out []string
		for _, e := range errs {
			out = append(out, e.Detail)
		}
		return out
	}

	BeforeAll(func() {
		create(
			&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "access-team-a"}},
			// A role made only to be required, with no rules.
			&rbacv1.ClusterRole{ObjectMeta: metav1.ObjectMeta{Name: "access-db-owners"}},
			restricted("access-rds", &common.DefinitionRestrictions{
				RequireRoles: []common.RoleRef{{Kind: "ClusterRole", Name: "access-db-owners"}}}),
			restricted("access-bucket", &common.DefinitionRestrictions{RequireGrant: true}),
			bind("", "access-db-owners", "ClusterRole", "access-db-owners", group("payments-leads")),
			bind(sys, "access-db-owners-carol", "ClusterRole", "access-db-owners", user("carol")),
			// dave binds the empty role to himself where his team lives.
			bind("access-team-a", "access-db-owners-dave", "ClusterRole", "access-db-owners", user("dave")),
			// erin may use the bucket definition, where it lives.
			&rbacv1.Role{ObjectMeta: metav1.ObjectMeta{Name: "use-bucket", Namespace: sys},
				Rules: []rbacv1.PolicyRule{rule([]string{"use"}, []string{"componentdefinitions"}, "access-bucket")}},
			bind(sys, "use-bucket-erin", "Role", "use-bucket", user("erin")),
			// frank is a namespace admin with every verb on everything in team-a.
			&rbacv1.Role{ObjectMeta: metav1.ObjectMeta{Name: "ns-admin", Namespace: "access-team-a"},
				Rules: []rbacv1.PolicyRule{{APIGroups: []string{"*"}, Resources: []string{"*"}, Verbs: []string{"*"}}}},
			bind("access-team-a", "ns-admin-frank", "Role", "ns-admin", user("frank")),
		)
	})
	AfterAll(func() {
		for i := len(created) - 1; i >= 0; i-- {
			_ = k8sClient.Delete(ctx, created[i])
		}
	})

	It("admits a member through a group bound by a ClusterRoleBinding", func() {
		Expect(check("access-rds", "alice", "payments-leads")).To(BeEmpty())
	})
	It("admits a member bound where the definition lives", func() {
		Expect(check("access-rds", "carol")).To(BeEmpty())
	})
	It("refuses a binding in the team's own namespace", func() {
		Expect(check("access-rds", "dave")).To(ConsistOf(ContainSubstring("must be a member of ClusterRole/access-db-owners")))
	})
	It("refuses someone with no binding", func() {
		Expect(check("access-rds", "bob")).To(HaveLen(1))
	})
	It("admits a grant of use where the definition lives", func() {
		Expect(check("access-bucket", "erin")).To(BeEmpty())
	})
	It("refuses a namespace admin's wildcard as a grant", func() {
		Expect(check("access-bucket", "frank")).To(ConsistOf(
			`ComponentDefinition "access-bucket" is restricted: the writer frank must hold "use" on componentdefinitions "access-bucket" in namespace "vela-system"`))
	})
	It("leaves a definition with no role or grant alone", func() {
		Expect(check("worker", "bob")).To(BeEmpty())
	})
})

// vela-core reads bindings through its manager's cache, so membership comes from
// informers: a binding made later is seen once the informer has it, with no
// request per check.
var _ = Describe("Definition access membership from informers", func() {
	It("sees a binding made after the first check", func() {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		informers, err := cache.New(cfg, cache.Options{Scheme: testScheme})
		Expect(err).NotTo(HaveOccurred())
		go func() { _ = informers.Start(ctx) }()
		Expect(informers.WaitForCacheSync(ctx)).To(BeTrue())
		cached, err := client.New(cfg, client.Options{Scheme: testScheme, Cache: &client.CacheOptions{Reader: informers}})
		Expect(err).NotTo(HaveOccurred())

		grace := authenticationv1.UserInfo{Username: "grace"}
		roles := []common.RoleRef{{Kind: "ClusterRole", Name: "informer-db-owners"}}
		Expect(access.Member(ctx, cached, grace, roles, oam.SystemDefinitionNamespace)).To(BeFalse())

		b := &rbacv1.ClusterRoleBinding{ObjectMeta: metav1.ObjectMeta{Name: "informer-db-owners-grace"},
			RoleRef:  rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: "informer-db-owners"},
			Subjects: []rbacv1.Subject{{Kind: rbacv1.UserKind, Name: "grace"}}}
		Expect(k8sClient.Create(ctx, b)).To(Succeed())
		defer func() { _ = k8sClient.Delete(context.Background(), b) }()
		Eventually(func() (bool, error) {
			return access.Member(ctx, cached, grace, roles, oam.SystemDefinitionNamespace)
		}).Should(BeTrue())
	})
})
