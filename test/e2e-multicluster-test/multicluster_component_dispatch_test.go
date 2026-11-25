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

package e2e_multicluster_test

import (
	"context"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	kerrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/yaml"

	"github.com/oam-dev/kubevela/apis/core.oam.dev/common"
	"github.com/oam-dev/kubevela/apis/core.oam.dev/v1alpha1"
	"github.com/oam-dev/kubevela/apis/core.oam.dev/v1beta1"
	velatypes "github.com/oam-dev/kubevela/apis/types"
	"github.com/oam-dev/kubevela/pkg/componentdispatch"
	"github.com/oam-dev/kubevela/pkg/oam"
)

// Component dispatch: Applications annotated to deliver each component as a signed
// Component, which vela-agent applies on its cluster. They need the
// EnableComponentDispatch gate on the hub and vela-agent on the worker, named as the
// suite joins it, so they carry their own label (make e2e-component-dispatch-test).
var _ = Describe("Component dispatch across clusters", Label("component-dispatch"), func() {
	var namespace string
	var hubCtx context.Context
	var workerCtx context.Context

	BeforeEach(func() {
		hubCtx, workerCtx, namespace = initializeContextAndNamespace()
	})

	AfterEach(func() {
		cleanUpNamespace(hubCtx, workerCtx, namespace)
	})

	// create makes the Application in doc in the test namespace, annotated for Component
	// dispatch when dispatch is true, and deletes it, waiting for it to go, after the spec.
	create := func(doc string, dispatch bool) *v1beta1.Application {
		GinkgoHelper()
		app := &v1beta1.Application{}
		Expect(yaml.Unmarshal([]byte(strings.NewReplacer("NAMESPACE", namespace, "WORKER", WorkerClusterName).Replace(doc)), app)).To(Succeed())
		app.SetNamespace(namespace)
		annotations := map[string]string{oam.AnnotationCelExpressions: "true"}
		if dispatch {
			annotations[oam.AnnotationDispatch] = oam.DispatchComponent
		}
		app.SetAnnotations(annotations)
		Eventually(func(g Gomega) {
			g.Expect(k8sClient.Create(hubCtx, app)).Should(Succeed())
		}).WithTimeout(10 * time.Second).WithPolling(2 * time.Second).Should(Succeed())
		DeferCleanup(func() {
			key := client.ObjectKeyFromObject(app)
			Expect(client.IgnoreNotFound(k8sClient.Delete(hubCtx, &v1beta1.Application{ObjectMeta: metav1.ObjectMeta{Name: key.Name, Namespace: key.Namespace}}))).To(Succeed())
			Eventually(func() bool {
				return kerrors.IsNotFound(k8sClient.Get(hubCtx, key, &v1beta1.Application{}))
			}).WithTimeout(2 * time.Minute).WithPolling(2 * time.Second).Should(BeTrue())
		})
		return app
	}

	running := func(app *v1beta1.Application) func(g Gomega) {
		return func(g Gomega) {
			live := &v1beta1.Application{}
			g.Expect(k8sClient.Get(hubCtx, client.ObjectKeyFromObject(app), live)).To(Succeed())
			g.Expect(live.Status.ObservedGeneration).To(Equal(live.Generation))
			g.Expect(live.Status.Phase).To(Equal(common.ApplicationRunning))
		}
	}

	get := func(ctx context.Context, name string, obj client.Object) error {
		return k8sClient.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, obj)
	}

	// componentFor is the name of the Component delivering component of app to cluster, found
	// by its labels; "" while there is none.
	componentFor := func(g Gomega, ctx context.Context, app, component, cluster string) string {
		list := &v1alpha1.ComponentList{}
		g.Expect(k8sClient.List(ctx, list, client.InNamespace(namespace), client.MatchingLabels{
			oam.LabelAppName: app, oam.LabelAppComponent: component, oam.LabelComponentCluster: cluster})).To(Succeed())
		var names []string
		for _, c := range list.Items {
			if _, cp := c.Labels[oam.LabelControlPlaneFor]; !cp {
				names = append(names, c.Name)
			}
		}
		g.Expect(len(names)).To(BeNumerically("<=", 1), "%v", names)
		if len(names) == 0 {
			return ""
		}
		return names[0]
	}

	// appliedNamed is the Component's revision once vela-agent has applied it and found it healthy.
	appliedNamed := func(g Gomega, ctx context.Context, name string) int64 {
		c := &v1alpha1.Component{}
		g.Expect(get(ctx, name, c)).To(Succeed())
		g.Expect(c.Status.ObservedRevision).To(Equal(c.Spec.Revision))
		g.Expect(c.Status.Health).NotTo(BeNil())
		g.Expect(c.Status.Health.Healthy).To(BeTrue())
		return c.Spec.Revision
	}

	// applied is appliedNamed for the Component delivering component of app to cluster.
	applied := func(ctx context.Context, app, component, cluster string) func(g Gomega) int64 {
		return func(g Gomega) int64 {
			name := componentFor(g, ctx, app, component, cluster)
			g.Expect(name).NotTo(BeEmpty())
			return appliedNamed(g, ctx, name)
		}
	}

	// controlPlaneApplied is applied for the hub Component holding the control-plane resources of
	// component placed on the worker, found by its labels.
	controlPlaneApplied := func(app, component string) func(g Gomega) int64 {
		return func(g Gomega) int64 {
			list := &v1alpha1.ComponentList{}
			g.Expect(k8sClient.List(hubCtx, list, client.InNamespace(namespace), client.MatchingLabels{
				oam.LabelAppName: app, oam.LabelAppComponent: component, oam.LabelControlPlaneFor: WorkerClusterName})).To(Succeed())
			g.Expect(list.Items).To(HaveLen(1))
			return appliedNamed(g, hubCtx, list.Items[0].Name)
		}
	}

	ownerKind := func(ctx context.Context, name string, obj client.Object) func() (string, error) {
		return func() (string, error) {
			if err := get(ctx, name, obj); err != nil {
				return "", err
			}
			return obj.GetLabels()[oam.LabelOwnerKind], nil
		}
	}

	It("delivers each placement as a Component, has vela-agent render its PostDispatch traits, and collects them on delete", func() {
		app := create(`
metadata: {name: dispatch-web}
spec:
  components:
    - name: web
      type: webservice
      properties:
        image: nginx:1.27-alpine
      traits:
        - {type: scaler, properties: {replicas: 2}}
        - {type: expose, properties: {port: [80]}}
  policies:
    - {name: both, type: topology, properties: {clusters: ["local", "WORKER"], namespace: NAMESPACE}}
`, true)
		Eventually(running(app)).WithTimeout(3 * time.Minute).WithPolling(3 * time.Second).Should(Succeed())
		for cluster, ctx := range map[string]context.Context{"local": hubCtx, WorkerClusterName: workerCtx} {
			Eventually(applied(ctx, "dispatch-web", "web", cluster)).WithTimeout(time.Minute).WithPolling(2 * time.Second).Should(Equal(int64(1)))
			Eventually(func(g Gomega) {
				d := &appsv1.Deployment{}
				g.Expect(get(ctx, "web", d)).To(Succeed())
				g.Expect(d.Status.ReadyReplicas).To(Equal(int32(2)))
			}).WithTimeout(time.Minute).WithPolling(2 * time.Second).Should(Succeed())
			Eventually(ownerKind(ctx, "web", &corev1.Service{})).WithTimeout(time.Minute).WithPolling(2*time.Second).
				Should(Equal(v1alpha1.ComponentKind), "vela-agent renders the expose Service on %s once the workload is healthy", cluster)
			Consistently(applied(ctx, "dispatch-web", "web", cluster)).WithTimeout(10*time.Second).WithPolling(2*time.Second).
				Should(Equal(int64(1)), "in the same revision, which nothing re-dispatches on %s", cluster)
		}
		live := &v1beta1.Application{}
		Expect(k8sClient.Get(hubCtx, client.ObjectKeyFromObject(app), live)).To(Succeed())
		for _, svc := range live.Status.Services {
			Expect(svc.Healthy).To(BeTrue(), "%s on %q: %s", svc.Name, svc.Cluster, svc.Message)
		}

		By("vela-agent reports what it applied, what rendered each one, and that it is Ready, in the Component's status")
		Eventually(func(g Gomega) {
			comp := &v1alpha1.Component{}
			g.Expect(k8sClient.Get(workerCtx, client.ObjectKey{Namespace: namespace,
				Name: componentFor(g, workerCtx, "dispatch-web", "web", WorkerClusterName)}, comp)).To(Succeed())
			defs := map[string]v1alpha1.ComponentResourceDefinition{}
			for _, res := range comp.Status.Resources {
				defs[res.Kind+"/"+res.Name] = res.Definition
			}
			g.Expect(defs).To(HaveKeyWithValue("Deployment/web", v1alpha1.ComponentResourceDefinition{Kind: v1alpha1.DefinitionComponent, Type: "webservice"}))
			g.Expect(defs).To(HaveKeyWithValue("Service/web", v1alpha1.ComponentResourceDefinition{Kind: v1alpha1.DefinitionTrait, Type: "expose"}))
			ready := meta.FindStatusCondition(comp.Status.Conditions, "Ready")
			g.Expect(ready).NotTo(BeNil())
			g.Expect(ready.Status).To(Equal(metav1.ConditionTrue), "%s: %s", ready.Reason, ready.Message)
			g.Expect(comp.Status.ObservedGeneration).To(Equal(comp.Generation))
		}).WithTimeout(time.Minute).WithPolling(2 * time.Second).Should(Succeed())

		By("vela status --tree lists what vela-agent applied on each cluster, not the Components")
		Eventually(func(g Gomega) {
			out, err := execCommand("status", app.Name, "-n", namespace, "--tree")
			g.Expect(err).To(Succeed())
			g.Expect(out).To(SatisfyAll(
				ContainSubstring("Deployment/web"), ContainSubstring("Service/web"),
				ContainSubstring(WorkerClusterName),
				Not(ContainSubstring("Component/")), Not(ContainSubstring("-payload")),
			), out)
		}).WithTimeout(time.Minute).WithPolling(2 * time.Second).Should(Succeed())

		By("deleting the Application collects every Component and what it applied")
		Expect(k8sClient.Delete(hubCtx, live)).To(Succeed())
		for cluster, ctx := range map[string]context.Context{"local": hubCtx, WorkerClusterName: workerCtx} {
			Eventually(func(g Gomega) string { return componentFor(g, ctx, "dispatch-web", "web", cluster) }).
				WithTimeout(2*time.Minute).WithPolling(2*time.Second).Should(BeEmpty(), "the Component on %s", cluster)
			Eventually(func() bool { return kerrors.IsNotFound(get(ctx, "web", &appsv1.Deployment{})) }).
				WithTimeout(2*time.Minute).WithPolling(2*time.Second).Should(BeTrue(), "the Deployment on %s", cluster)
		}
	})

	It("reads a dispatched producer on another cluster", func() {
		create(`
metadata: {name: dispatch-reads}
spec:
  components:
    - name: db
      type: k8s-objects
      properties:
        objects:
          - {apiVersion: v1, kind: ConfigMap, metadata: {name: dispatch-db}, data: {host: 'db.$(context.cluster)'}}
    - name: reader
      type: k8s-objects
      properties:
        objects:
          - apiVersion: v1
            kind: ConfigMap
            metadata: {name: dispatch-reader}
            data:
              remote: '$(component.db.cluster("WORKER").output.data.host)'
              beside: '$(component.db.output.data.host)'
  policies:
    - {name: both, type: topology, properties: {clusters: ["local", "WORKER"], namespace: NAMESPACE}}
`, true)
		data := func(ctx context.Context) func() (map[string]string, error) {
			return func() (map[string]string, error) {
				cm := &corev1.ConfigMap{}
				err := get(ctx, "dispatch-reader", cm)
				return cm.Data, err
			}
		}
		Eventually(data(hubCtx)).WithTimeout(3 * time.Minute).WithPolling(3 * time.Second).Should(Equal(map[string]string{
			"remote": "db." + WorkerClusterName,
			"beside": "db.local",
		}))
		Eventually(data(workerCtx)).WithTimeout(3*time.Minute).WithPolling(3*time.Second).Should(Equal(map[string]string{
			"remote": "db." + WorkerClusterName,
			"beside": "db." + WorkerClusterName,
		}), "answered from what each db Component's vela-agent observed")
	})

	It("keeps a control-plane-only trait's resources on the hub, in a Component of their own", func() {
		def := &v1beta1.TraitDefinition{}
		Expect(yaml.Unmarshal([]byte(`
metadata: {name: dispatch-hub-record, namespace: vela-system}
spec:
  controlPlaneOnly: true
  schematic:
    cue:
      template: |
        outputs: record: {
          apiVersion: "v1"
          kind:       "ConfigMap"
          metadata: name: context.name + "-record"
          data: cluster: context.cluster
        }
        parameter: {}
`), def)).To(Succeed())
		Expect(client.IgnoreAlreadyExists(k8sClient.Create(hubCtx, def))).To(Succeed())
		DeferCleanup(func() { Expect(client.IgnoreNotFound(k8sClient.Delete(hubCtx, def))).To(Succeed()) })

		app := create(`
metadata: {name: dispatch-cp}
spec:
  components:
    - name: api
      type: webservice
      properties: {image: nginx:1.27-alpine}
      traits:
        - {type: dispatch-hub-record}
  policies:
    - {name: worker, type: topology, properties: {clusters: ["WORKER"], namespace: NAMESPACE}}
`, true)
		Eventually(running(app)).WithTimeout(3 * time.Minute).WithPolling(3 * time.Second).Should(Succeed())
		Eventually(applied(workerCtx, "dispatch-cp", "api", WorkerClusterName)).WithTimeout(time.Minute).Should(BeNumerically(">=", 1))
		Eventually(controlPlaneApplied("dispatch-cp", "api")).WithTimeout(time.Minute).Should(BeNumerically(">=", 1))
		Expect(get(workerCtx, "api", &appsv1.Deployment{})).To(Succeed())
		Expect(ownerKind(hubCtx, "api-record", &corev1.ConfigMap{})()).To(Equal(v1alpha1.ComponentKind))
		Expect(kerrors.IsNotFound(get(workerCtx, "api-record", &corev1.ConfigMap{}))).To(BeTrue(), "the record is the hub's alone")
	})

	It("hands an in-process component to vela-agent and back, keeping every resource", func() {
		app := create(`
metadata: {name: dispatch-handover}
spec:
  components:
    - name: cart
      type: webservice
      properties: {image: nginx:1.27-alpine}
      traits:
        - {type: expose, properties: {port: [80]}}
  policies:
    - {name: worker, type: topology, properties: {clusters: ["WORKER"], namespace: NAMESPACE}}
`, false)
		Eventually(running(app)).WithTimeout(3 * time.Minute).WithPolling(3 * time.Second).Should(Succeed())
		Eventually(func() error { return get(workerCtx, "cart", &corev1.Service{}) }).WithTimeout(time.Minute).Should(Succeed())
		uids := func() map[string]types.UID {
			GinkgoHelper()
			d, s := &appsv1.Deployment{}, &corev1.Service{}
			Expect(get(workerCtx, "cart", d)).To(Succeed())
			Expect(get(workerCtx, "cart", s)).To(Succeed())
			return map[string]types.UID{"Deployment": d.UID, "Service": s.UID}
		}
		before := uids()
		setDispatch := func(on bool) {
			Eventually(func(g Gomega) {
				live := &v1beta1.Application{}
				g.Expect(k8sClient.Get(hubCtx, client.ObjectKeyFromObject(app), live)).To(Succeed())
				if on {
					metav1.SetMetaDataAnnotation(&live.ObjectMeta, oam.AnnotationDispatch, oam.DispatchComponent)
				} else {
					delete(live.Annotations, oam.AnnotationDispatch)
				}
				g.Expect(k8sClient.Update(hubCtx, live)).To(Succeed())
			}).WithTimeout(10 * time.Second).Should(Succeed())
		}
		handover := func(g Gomega) {
			live := &v1beta1.Application{}
			g.Expect(k8sClient.Get(hubCtx, client.ObjectKeyFromObject(app), live)).To(Succeed())
			g.Expect(live.Status.GetCondition("Handover").Status).To(Equal(corev1.ConditionTrue), live.Status.GetCondition("Handover").Message)
		}

		By("dispatching it hands the Deployment and Service to its Component in place")
		setDispatch(true)
		for _, obj := range []client.Object{&appsv1.Deployment{}, &corev1.Service{}} {
			Eventually(ownerKind(workerCtx, "cart", obj)).WithTimeout(2*time.Minute).WithPolling(2*time.Second).Should(Equal(v1alpha1.ComponentKind), "%T", obj)
		}
		Eventually(func(g Gomega) {
			c := &v1alpha1.Component{}
			name := componentFor(g, workerCtx, "dispatch-handover", "cart", WorkerClusterName)
			g.Expect(name).NotTo(BeEmpty())
			g.Expect(get(workerCtx, name, c)).To(Succeed())
			g.Expect(c.Spec.Apply.AdoptFrom).To(BeEmpty())
			handover(g)
		}).WithTimeout(2 * time.Minute).WithPolling(2 * time.Second).Should(Succeed())
		Expect(uids()).To(Equal(before))

		By("dropping the annotation hands them back to the Application in place, and retires the Component")
		setDispatch(false)
		for _, obj := range []client.Object{&appsv1.Deployment{}, &corev1.Service{}} {
			Eventually(ownerKind(workerCtx, "cart", obj)).WithTimeout(2*time.Minute).WithPolling(2*time.Second).Should(Equal(v1beta1.ApplicationKind), "%T", obj)
		}
		Eventually(func(g Gomega) {
			g.Expect(componentFor(g, workerCtx, "dispatch-handover", "cart", WorkerClusterName)).To(BeEmpty())
			handover(g)
		}).WithTimeout(2 * time.Minute).WithPolling(2 * time.Second).Should(Succeed())
		Expect(uids()).To(Equal(before))
	})

	It("signs every Component with a new key on request, and retires the old one once none carries it", func() {
		app := create(`
metadata: {name: dispatch-rotate}
spec:
  components:
    - name: cfg
      type: k8s-objects
      properties:
        objects: [{apiVersion: v1, kind: ConfigMap, metadata: {name: rotate-cfg}, data: {k: v}}]
  policies:
    - {name: worker, type: topology, properties: {clusters: ["WORKER"], namespace: NAMESPACE}}
`, true)
		Eventually(running(app)).WithTimeout(2 * time.Minute).WithPolling(2 * time.Second).Should(Succeed())
		var revision int64
		Eventually(func(g Gomega) { revision = applied(workerCtx, app.Name, "cfg", WorkerClusterName)(g) }).
			WithTimeout(2 * time.Minute).WithPolling(2 * time.Second).Should(Succeed())

		keysKey := client.ObjectKey{Namespace: velatypes.DefaultKubeVelaNS, Name: componentdispatch.SigningKeysSecret}
		active := func() string {
			keys := &corev1.Secret{}
			Expect(k8sClient.Get(hubCtx, keysKey, keys)).To(Succeed())
			return componentdispatch.ActiveKeyID(keys)
		}
		signedWith := func(g Gomega) string {
			name := componentFor(g, workerCtx, app.Name, "cfg", WorkerClusterName)
			g.Expect(name).NotTo(BeEmpty())
			c := &v1alpha1.Component{}
			g.Expect(get(workerCtx, name, c)).To(Succeed())
			return c.Spec.Signature.KeyID
		}
		old := active()
		Eventually(signedWith).WithTimeout(time.Minute).WithPolling(2 * time.Second).Should(Equal(old))

		By("asking for a rotation")
		Eventually(func(g Gomega) {
			keys := &corev1.Secret{}
			g.Expect(k8sClient.Get(hubCtx, keysKey, keys)).To(Succeed())
			metav1.SetMetaDataAnnotation(&keys.ObjectMeta, componentdispatch.AnnotationRotateSigningKey, "e2e")
			g.Expect(k8sClient.Update(hubCtx, keys)).To(Succeed())
		}).WithTimeout(30 * time.Second).WithPolling(2 * time.Second).Should(Succeed())
		Eventually(active).WithTimeout(2 * time.Minute).WithPolling(2 * time.Second).ShouldNot(Equal(old))
		current := active()

		By("re-signing straight away, well inside the Application's resync, at the same revision")
		Eventually(signedWith).WithTimeout(time.Minute).WithPolling(2 * time.Second).Should(Equal(current))
		Eventually(applied(workerCtx, app.Name, "cfg", WorkerClusterName)).
			WithTimeout(time.Minute).WithPolling(2 * time.Second).Should(Equal(revision))

		By("retiring the old key on the hub and every cluster")
		Eventually(func(g Gomega) {
			keys, trust := &corev1.Secret{}, &corev1.Secret{}
			g.Expect(k8sClient.Get(hubCtx, keysKey, keys)).To(Succeed())
			g.Expect(k8sClient.Get(workerCtx, client.ObjectKey{Namespace: velatypes.DefaultKubeVelaNS, Name: componentdispatch.TrustSecret}, trust)).To(Succeed())
			g.Expect(keys.Data).NotTo(HaveKey(old))
			g.Expect(trust.Data).NotTo(HaveKey(old))
		}).WithTimeout(4 * time.Minute).WithPolling(5 * time.Second).Should(Succeed())
	})

	It("stops trusting a revoked key on every cluster at once, and signs its Components again", func() {
		app := create(`
metadata: {name: dispatch-revoke}
spec:
  components:
    - name: cfg
      type: k8s-objects
      properties:
        objects: [{apiVersion: v1, kind: ConfigMap, metadata: {name: revoke-cfg}, data: {k: v}}]
  policies:
    - {name: worker, type: topology, properties: {clusters: ["WORKER"], namespace: NAMESPACE}}
`, true)
		Eventually(running(app)).WithTimeout(2 * time.Minute).WithPolling(2 * time.Second).Should(Succeed())
		var revision int64
		Eventually(func(g Gomega) { revision = applied(workerCtx, app.Name, "cfg", WorkerClusterName)(g) }).
			WithTimeout(2 * time.Minute).WithPolling(2 * time.Second).Should(Succeed())

		keysKey := client.ObjectKey{Namespace: velatypes.DefaultKubeVelaNS, Name: componentdispatch.SigningKeysSecret}
		keys := &corev1.Secret{}
		Expect(k8sClient.Get(hubCtx, keysKey, keys)).To(Succeed())
		compromised := componentdispatch.ActiveKeyID(keys)

		By("revoking the active key")
		Eventually(func(g Gomega) {
			keys := &corev1.Secret{}
			g.Expect(k8sClient.Get(hubCtx, keysKey, keys)).To(Succeed())
			metav1.SetMetaDataAnnotation(&keys.ObjectMeta, componentdispatch.AnnotationRevokeSigningKey, compromised)
			g.Expect(k8sClient.Update(hubCtx, keys)).To(Succeed())
		}).WithTimeout(30 * time.Second).WithPolling(2 * time.Second).Should(Succeed())
		Eventually(func(g Gomega) {
			trust := &corev1.Secret{}
			g.Expect(k8sClient.Get(workerCtx, client.ObjectKey{Namespace: velatypes.DefaultKubeVelaNS, Name: componentdispatch.TrustSecret}, trust)).To(Succeed())
			g.Expect(trust.Data).NotTo(HaveKey(compromised))
		}).WithTimeout(2 * time.Minute).WithPolling(2 * time.Second).Should(Succeed())

		By("signing the Component again with the new key, at the same revision, its resources untouched")
		Eventually(func(g Gomega) {
			name := componentFor(g, workerCtx, app.Name, "cfg", WorkerClusterName)
			g.Expect(name).NotTo(BeEmpty())
			c := &v1alpha1.Component{}
			g.Expect(get(workerCtx, name, c)).To(Succeed())
			g.Expect(c.Spec.Signature.KeyID).NotTo(Equal(compromised))
		}).WithTimeout(time.Minute).WithPolling(2 * time.Second).Should(Succeed())
		Eventually(applied(workerCtx, app.Name, "cfg", WorkerClusterName)).
			WithTimeout(time.Minute).WithPolling(2 * time.Second).Should(Equal(revision))
		Expect(get(workerCtx, "revoke-cfg", &corev1.ConfigMap{})).To(Succeed())
	})
})
