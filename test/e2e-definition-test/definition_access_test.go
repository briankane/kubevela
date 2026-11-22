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

package definitions_test

import (
	"context"
	"strings"
	"sync"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/config"

	"github.com/oam-dev/kubevela/apis/core.oam.dev/common"
	"github.com/oam-dev/kubevela/apis/core.oam.dev/v1beta1"
	"github.com/oam-dev/kubevela/pkg/oam"
	"github.com/oam-dev/kubevela/pkg/oam/util"
	"github.com/oam-dev/kubevela/pkg/webhook/core.oam.dev/v1beta1/writer"
)

// warnings collects the warnings the API server returns to one client.
type warnings struct {
	mu   sync.Mutex
	seen []string
}

func (w *warnings) HandleWarningHeader(_ int, _ string, text string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.seen = append(w.seen, text)
}

func (w *warnings) all() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return strings.Join(w.seen, "\n")
}

// Writers are users impersonated through the API server, so the admission
// webhooks see the request's real user and groups, and RBAC decides membership
// and grants.
var _ = Describe("Definition access", Label("core-definitions"), Ordered, func() {
	ctx := context.Background()
	sys := oam.SystemDefinitionNamespace
	var ns string
	var cleanup []client.Object

	// as is a client acting as user, in groups as well as the one every writer here
	// is in, whose RBAC lets it write Applications, definitions and Packages.
	as := func(user string, groups ...string) (client.Client, *warnings) {
		cfg := rest.CopyConfig(config.GetConfigOrDie())
		w := &warnings{}
		cfg.Impersonate = rest.ImpersonationConfig{UserName: user, Groups: append([]string{"access-writers"}, groups...)}
		cfg.WarningHandler = w
		c, err := client.New(cfg, client.Options{Scheme: scheme})
		Expect(err).NotTo(HaveOccurred())
		return c, w
	}
	keep := func(objs ...client.Object) {
		for _, o := range objs {
			Expect(k8sClient.Create(ctx, o)).To(Succeed())
			cleanup = append(cleanup, o)
		}
	}
	binding := func(namespace, name, kind, role string, subject rbacv1.Subject) client.Object {
		ref := rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: kind, Name: role}
		if namespace == "" {
			return &rbacv1.ClusterRoleBinding{ObjectMeta: metav1.ObjectMeta{Name: name}, RoleRef: ref, Subjects: []rbacv1.Subject{subject}}
		}
		return &rbacv1.RoleBinding{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace}, RoleRef: ref, Subjects: []rbacv1.Subject{subject}}
	}
	user := func(n string) rbacv1.Subject {
		return rbacv1.Subject{Kind: rbacv1.UserKind, APIGroup: rbacv1.GroupName, Name: n}
	}
	group := func(n string) rbacv1.Subject {
		return rbacv1.Subject{Kind: rbacv1.GroupKind, APIGroup: rbacv1.GroupName, Name: n}
	}
	definition := func(name string, r *common.DefinitionRestrictions) *v1beta1.ComponentDefinition {
		return &v1beta1.ComponentDefinition{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: sys},
			Spec: v1beta1.ComponentDefinitionSpec{
				Workload:     common.WorkloadTypeDescriptor{Definition: common.WorkloadGVK{APIVersion: "v1", Kind: "ConfigMap"}},
				Restrictions: r,
				Schematic: &common.Schematic{CUE: &common.CUE{Template: `
output: {apiVersion: "v1", kind: "ConfigMap", data: {}}
parameter: note?: string
`}},
			},
		}
	}
	app := func(name string, types ...string) *v1beta1.Application {
		a := &v1beta1.Application{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns}}
		for _, t := range types {
			a.Spec.Components = append(a.Spec.Components, common.ApplicationComponent{Name: name + "-" + t, Type: t})
		}
		return a
	}
	stamp := func(obj client.Object) (string, string) {
		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(obj), obj)).To(Succeed())
		return obj.GetAnnotations()[writer.AnnotationWriter], obj.GetAnnotations()[writer.AnnotationWriterGroups]
	}
	writtenBy := func(obj client.Object) string {
		u, _ := stamp(obj)
		return u
	}

	BeforeAll(func() {
		ns = randomNamespaceName("access")
		keep(
			&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}},
			&rbacv1.ClusterRole{ObjectMeta: metav1.ObjectMeta{Name: "access-e2e-writer"}, Rules: []rbacv1.PolicyRule{{
				APIGroups: []string{"core.oam.dev", "cue.oam.dev"}, Resources: []string{"*"},
				Verbs: []string{"get", "list", "watch", "create", "update", "patch", "delete"}}}},
			binding("", "access-e2e-writer", "ClusterRole", "access-e2e-writer", group("access-writers")),
			// A role made only to be required, with no rules.
			&rbacv1.ClusterRole{ObjectMeta: metav1.ObjectMeta{Name: "access-e2e-db-owners"}},
			binding("", "access-e2e-db-owners", "ClusterRole", "access-e2e-db-owners", group("payments-leads")),
			binding(ns, "access-e2e-db-owners-dave", "ClusterRole", "access-e2e-db-owners", user("dave")),
			&rbacv1.Role{ObjectMeta: metav1.ObjectMeta{Name: "access-e2e-use-bucket", Namespace: sys}, Rules: []rbacv1.PolicyRule{{
				APIGroups: []string{"core.oam.dev"}, Resources: []string{"componentdefinitions"},
				ResourceNames: []string{"access-e2e-bucket"}, Verbs: []string{"use"}}}},
			binding(sys, "access-e2e-use-bucket-erin", "Role", "access-e2e-use-bucket", user("erin")),
			&rbacv1.Role{ObjectMeta: metav1.ObjectMeta{Name: "access-e2e-ns-admin", Namespace: ns}, Rules: []rbacv1.PolicyRule{{
				APIGroups: []string{"*"}, Resources: []string{"*"}, Verbs: []string{"*"}}}},
			binding(ns, "access-e2e-ns-admin-frank", "Role", "access-e2e-ns-admin", user("frank")),
		)
		platform, _ := as("platform", "platform-team")
		roles := []common.RoleRef{{Kind: "ClusterRole", Name: "access-e2e-db-owners"}}
		for _, d := range []*v1beta1.ComponentDefinition{
			definition("access-e2e-rds", &common.DefinitionRestrictions{RequireRoles: roles, Message: "Ask in #db-team."}),
			definition("access-e2e-bucket", &common.DefinitionRestrictions{RequireGrant: true}),
			definition("access-e2e-warn", &common.DefinitionRestrictions{RequireRoles: roles, Mode: common.RestrictionModeWarn}),
			definition("access-e2e-open", nil),
		} {
			Expect(platform.Create(ctx, d)).To(Succeed())
			cleanup = append(cleanup, d)
		}
	})
	AfterAll(func() {
		_ = k8sClient.DeleteAllOf(ctx, &v1beta1.Application{}, client.InNamespace(ns))
		for i := len(cleanup) - 1; i >= 0; i-- {
			_ = k8sClient.Delete(ctx, cleanup[i])
		}
	})

	Context("the writer stamp", func() {
		It("records the user and groups that created a definition", func() {
			u, g := stamp(definition("access-e2e-rds", nil))
			Expect(u).To(Equal("platform"))
			Expect(strings.Split(g, ",")).To(ConsistOf("platform-team", "access-writers", "system:authenticated"))
		})
		It("overwrites a stamp the writer claims", func() {
			mallory, _ := as("mallory")
			d := definition("access-e2e-claimed", nil)
			d.Annotations = map[string]string{writer.AnnotationWriter: "system:admin", writer.AnnotationWriterGroups: "system:masters"}
			Expect(mallory.Create(ctx, d)).To(Succeed())
			cleanup = append(cleanup, d)
			Expect(writtenBy(d)).To(Equal("mallory"))
		})
		It("keeps the stamp through a metadata-only update, which cannot set it", func() {
			bob, _ := as("bob")
			d := definition("access-e2e-rds", nil)
			patch := []byte(`{"metadata":{"annotations":{"note":"x","` + writer.AnnotationWriter + `":"bob"}}}`)
			Expect(bob.Patch(ctx, d, client.RawPatch(types.MergePatchType, patch))).To(Succeed())
			Expect(writtenBy(d)).To(Equal("platform"))
		})
		It("restamps on a spec change, and the new DefinitionRevision carries it", func() {
			bob, _ := as("bob")
			d := definition("access-e2e-open", nil)
			Expect(retry.RetryOnConflict(retry.DefaultRetry, func() error {
				if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(d), d); err != nil {
					return err
				}
				d.Spec.Schematic.CUE.Template += "\n// changed\n"
				return bob.Update(ctx, d)
			})).To(Succeed())
			Expect(writtenBy(d)).To(Equal("bob"))
			Eventually(func(g Gomega) {
				revs := &v1beta1.DefinitionRevisionList{}
				g.Expect(k8sClient.List(ctx, revs, client.InNamespace(sys),
					client.MatchingLabels{oam.LabelComponentDefinitionName: "access-e2e-open"})).To(Succeed())
				var latest *v1beta1.DefinitionRevision
				for i := range revs.Items {
					if latest == nil || revs.Items[i].Spec.Revision > latest.Spec.Revision {
						latest = &revs.Items[i]
					}
				}
				g.Expect(latest).NotTo(BeNil())
				g.Expect(latest.Spec.ComponentDefinition.Annotations).To(HaveKeyWithValue(writer.AnnotationWriter, "bob"))
			}, 30*time.Second, time.Second).Should(Succeed())
		})
		It("stamps a Package", func() {
			platform, _ := as("platform")
			pkg := &unstructured.Unstructured{Object: map[string]any{
				"apiVersion": "cue.oam.dev/v1alpha1", "kind": "Package",
				"metadata": map[string]any{"name": "access-e2e", "namespace": sys},
				"spec": map[string]any{"path": "example.com/access-e2e",
					"templates": map[string]any{"main.cue": "package accesse2e\nx: 1\n"}},
			}}
			Expect(platform.Create(ctx, pkg)).To(Succeed())
			cleanup = append(cleanup, pkg)
			Expect(writtenBy(pkg)).To(Equal("platform"))
		})
		It("records vela-core dispatching a definition as the Application", func() {
			shipped := definition("access-e2e-shipped", nil)
			shipped.TypeMeta = metav1.TypeMeta{APIVersion: "core.oam.dev/v1beta1", Kind: "ComponentDefinition"}
			ships := &v1beta1.Application{
				ObjectMeta: metav1.ObjectMeta{Name: "access-e2e-ships", Namespace: sys},
				Spec: v1beta1.ApplicationSpec{Components: []common.ApplicationComponent{{
					Name: "defs", Type: "k8s-objects",
					Properties: util.Object2RawExtension(map[string]any{"objects": []any{shipped}}),
				}}},
			}
			Expect(k8sClient.Create(ctx, ships)).To(Succeed())
			DeferCleanup(func() { _ = k8sClient.Delete(ctx, ships) })
			Eventually(func(g Gomega) {
				got := definition("access-e2e-shipped", nil)
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(got), got)).To(Succeed())
				g.Expect(got.Annotations).To(HaveKeyWithValue(writer.AnnotationWriter, "application:"+sys+"/access-e2e-ships"))
			}, 60*time.Second, time.Second).Should(Succeed())
		})
	})

	Context("who may use a definition", func() {
		It("admits a member through a group bound cluster-wide, and the Application renders", func() {
			alice, _ := as("alice", "payments-leads")
			a := app("alice", "access-e2e-rds")
			Expect(alice.Create(ctx, a)).To(Succeed())
			Eventually(func(g Gomega) {
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(a), a)).To(Succeed())
				g.Expect(a.Status.Phase).To(Equal(common.ApplicationRunning))
			}, 60*time.Second, time.Second).Should(Succeed())
		})
		It("refuses a binding in the team's own namespace, with the definition's message", func() {
			dave, _ := as("dave")
			err := dave.Create(ctx, app("dave", "access-e2e-rds"))
			Expect(err).To(MatchError(And(
				ContainSubstring("must be a member of ClusterRole/access-e2e-db-owners"),
				ContainSubstring("Ask in #db-team."))))
		})
		It("admits a grant of use where the definition lives", func() {
			erin, _ := as("erin")
			Expect(erin.Create(ctx, app("erin", "access-e2e-bucket"))).To(Succeed())
		})
		It("does not take a namespace admin's wildcard as a grant", func() {
			frank, _ := as("frank")
			err := frank.Create(ctx, app("frank", "access-e2e-bucket"))
			Expect(err).To(MatchError(ContainSubstring(
				`must hold "use" on componentdefinitions "access-e2e-bucket" in namespace "vela-system"`)))
		})
		It("admits in Warn mode with a warning", func() {
			bob, seen := as("bob")
			Expect(bob.Create(ctx, app("bob-warn", "access-e2e-warn"))).To(Succeed())
			Expect(seen.all()).To(ContainSubstring(`ComponentDefinition "access-e2e-warn" is restricted`))
		})
		It("checks only what an update adds or changes", func() {
			alice, _ := as("alice", "payments-leads")
			bob, _ := as("bob")
			Expect(alice.Create(ctx, app("shared", "access-e2e-rds"))).To(Succeed())

			// update gets the latest Application, changes it and writes it as c, again
			// on a conflict with the controller's own writes.
			update := func(c client.Client, change func(*v1beta1.Application)) error {
				return retry.RetryOnConflict(retry.DefaultRetry, func() error {
					a := app("shared")
					if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(a), a); err != nil {
						return err
					}
					change(a)
					return c.Update(ctx, a)
				})
			}
			Expect(update(bob, func(a *v1beta1.Application) {
				a.Spec.Components = append(a.Spec.Components, common.ApplicationComponent{Name: "extra", Type: "access-e2e-open"})
			})).To(Succeed(), "an unrestricted addition beside alice's component")
			Expect(update(bob, func(a *v1beta1.Application) {
				a.Spec.Components = append(a.Spec.Components, common.ApplicationComponent{Name: "bucket", Type: "access-e2e-bucket"})
			})).To(MatchError(ContainSubstring(`ComponentDefinition "access-e2e-bucket" is restricted`)))
			Expect(update(bob, func(a *v1beta1.Application) {
				a.Spec.Components[0].Properties = util.Object2RawExtension(map[string]any{"note": "changed"})
			})).To(MatchError(ContainSubstring(`ComponentDefinition "access-e2e-rds" is restricted`)))
		})
	})
})
