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
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	kerrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	utilfeature "k8s.io/apiserver/pkg/util/feature"
	"k8s.io/client-go/tools/record"
	featuregatetesting "k8s.io/component-base/featuregate/testing"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/yaml"

	wfTypesv1alpha1 "github.com/kubevela/pkg/apis/oam/v1alpha1"
	monitorContext "github.com/kubevela/pkg/monitor/context"

	"github.com/oam-dev/kubevela/apis/core.oam.dev/common"
	"github.com/oam-dev/kubevela/apis/core.oam.dev/v1alpha1"
	"github.com/oam-dev/kubevela/apis/core.oam.dev/v1beta1"
	"github.com/oam-dev/kubevela/pkg/appfile"
	"github.com/oam-dev/kubevela/pkg/appkeeper"
	"github.com/oam-dev/kubevela/pkg/componentdispatch"
	"github.com/oam-dev/kubevela/pkg/controller/core.oam.dev/v1alpha1/component"
	"github.com/oam-dev/kubevela/pkg/features"
	"github.com/oam-dev/kubevela/pkg/oam"
	"github.com/oam-dev/kubevela/pkg/oam/testutil"
	"github.com/oam-dev/kubevela/pkg/oam/util"
	"github.com/oam-dev/kubevela/pkg/resourcekeeper"
	"github.com/oam-dev/kubevela/pkg/utils/apply"
)

// ringKeys signs with, and opens with, a fixed key ring.
type ringKeys struct{ ring *componentdispatch.KeyRing }

func (k ringKeys) Ring(context.Context) (*componentdispatch.KeyRing, error) {
	return k.ring, nil
}

func newRingKeys(priv ed25519.PrivateKey) ringKeys {
	return ringKeys{ring: &componentdispatch.KeyRing{Active: "hub-1", Keys: map[string]ed25519.PrivateKey{"hub-1": priv}}}
}

// dispatchDeployStepDefinition is the deploy step, without its approval.
const dispatchDeployStepDefinition = `
apiVersion: core.oam.dev/v1beta1
kind: WorkflowStepDefinition
metadata:
  name: deploy
  namespace: vela-system
spec:
  schematic:
    cue:
      template: |
        import "vela/multicluster"

        deploy: multicluster.#Deploy & {
          $params: {
            policies:                 parameter.policies
            parallelism:              parameter.parallelism
            ignoreTerraformComponent: parameter.ignoreTerraformComponent
          }
        }
        parameter: {
          policies:                 *[] | [...string]
          parallelism:              *5 | int
          ignoreTerraformComponent: *true | bool
        }
`

const dispatchConfigDefinition = `
apiVersion: core.oam.dev/v1beta1
kind: ComponentDefinition
metadata:
  name: dispatch-config
  namespace: vela-system
spec:
  workload:
    definition:
      apiVersion: v1
      kind: ConfigMap
  schematic:
    cue:
      template: |
        output: {
          apiVersion: "v1"
          kind:       "ConfigMap"
          metadata: name: context.name
          data: ready: parameter.ready
          if parameter.note != _|_ {
            data: note: parameter.note
          }
        }
        parameter: {
          ready: string
          note?: string
        }
  status:
    healthPolicy: |
      isHealth: context.output.data.ready == "yes"
    customStatus: |
      message: "ready: \(context.output.data.ready)"
`

// dispatchSecretDefinition renders a Secret, whose reads vela-agent seals.
const dispatchSecretDefinition = `
apiVersion: core.oam.dev/v1beta1
kind: ComponentDefinition
metadata:
  name: dispatch-secret
  namespace: vela-system
spec:
  workload:
    definition:
      apiVersion: v1
      kind: Secret
  schematic:
    cue:
      template: |
        output: {
          apiVersion: "v1"
          kind:       "Secret"
          metadata: name: context.name
          stringData: password: parameter.password
        }
        parameter: password: string
  status:
    healthPolicy: |
      isHealth: true
`

// parityDefinitions read every part of the health context: output, outputs, a trait's
// outputs, parameter, details and the base context.
const parityDefinitions = `
apiVersion: core.oam.dev/v1beta1
kind: ComponentDefinition
metadata:
  name: parity-config
  namespace: vela-system
spec:
  workload:
    definition:
      apiVersion: v1
      kind: ConfigMap
  schematic:
    cue:
      template: |
        output: {
          apiVersion: "v1"
          kind:       "ConfigMap"
          metadata: name: context.name
          data: ready: parameter.ready
        }
        outputs: extra: {
          apiVersion: "v1"
          kind:       "ConfigMap"
          metadata: name: context.name + "-extra"
          data: owner: context.appName
        }
        parameter: ready: string
  status:
    healthPolicy: |
      isHealth: context.output.data.ready == "yes"
    customStatus: |
      message: "ready: \(context.output.data.ready) (\(context.name) in \(context.appName)@\(context.appRevision))"
    details: |
      owner: context.outputs.extra.data.owner
      want: parameter.ready
---
apiVersion: core.oam.dev/v1beta1
kind: TraitDefinition
metadata:
  name: parity-note
  namespace: vela-system
spec:
  schematic:
    cue:
      template: |
        outputs: note: {
          apiVersion: "v1"
          kind:       "ConfigMap"
          metadata: name: context.name + "-note"
          data: text: parameter.text
        }
        parameter: text: string
  status:
    healthPolicy: |
      isHealth: context.outputs.note.data.text == "ok"
    customStatus: |
      message: "note: \(context.outputs.note.data.text)"
`

// dispatchPostDefinition is a PostDispatch trait, rendered once its workload is healthy,
// from what its workload holds.
const dispatchPostDefinition = `
apiVersion: core.oam.dev/v1beta1
kind: TraitDefinition
metadata:
  name: dispatch-post
  namespace: vela-system
spec:
  stage: PostDispatch
  schematic:
    cue:
      template: |
        outputs: post: {
          apiVersion: "v1"
          kind:       "ConfigMap"
          metadata: name: context.name + "-post"
          data: {
            ready: context.output.data.ready
            text:  parameter.text
          }
        }
        parameter: text: string
`

// dispatchParityDefinition is a PostDispatch trait whose outputs exercise every rule that
// finishes a trait's output: a named and an unnamed output, annotations the output sets
// itself, one of them filtered by the Application.
const dispatchParityDefinition = `
apiVersion: core.oam.dev/v1beta1
kind: TraitDefinition
metadata:
  name: dispatch-parity
  namespace: vela-system
spec:
  stage: PostDispatch
  schematic:
    cue:
      template: |
        outputs: named: {
          apiVersion: "v1"
          kind:       "ConfigMap"
          metadata: {
            name: context.name + "-named"
            annotations: {
              "example.com/own":    "kept"
              "example.com/hidden": "dropped"
            }
          }
          data: ready: context.output.data.ready
        }
        outputs: unnamed: {
          apiVersion: "v1"
          kind:       "ConfigMap"
          data: text: parameter.text
        }
        parameter: text: string
`

var _ = Describe("Component dispatch", func() {
	ctx := context.Background()

	It("delivers an annotated Application's component as a signed Component, and reports its health", func() {
		featuregatetesting.SetFeatureGateDuringTest(GinkgoT(), utilfeature.DefaultFeatureGate, features.EnableComponentDispatch, true)
		pub, priv, err := ed25519.GenerateKey(rand.Reader)
		Expect(err).NotTo(HaveOccurred())
		keys := componentdispatch.TrustedKeys{"hub-1": pub}
		reconciler.ComponentKeys = newRingKeys(priv)
		DeferCleanup(func() { reconciler.ComponentKeys = nil })
		agent := &component.Reconciler{Client: k8sClient, Keys: component.StaticKeys(keys), Cluster: "local", Recorder: record.NewFakeRecorder(100)}

		ns := "component-dispatch"
		Expect(k8sClient.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}})).Should(SatisfyAny(BeNil(), &util.AlreadyExistMatcher{}))
		def := &v1beta1.ComponentDefinition{}
		defJSON, err := yaml.YAMLToJSON([]byte(dispatchConfigDefinition))
		Expect(err).NotTo(HaveOccurred())
		Expect(json.Unmarshal(defJSON, def)).To(Succeed())
		Expect(k8sClient.Create(ctx, def)).Should(SatisfyAny(BeNil(), &util.AlreadyExistMatcher{}))

		app := &v1beta1.Application{
			ObjectMeta: metav1.ObjectMeta{Name: "shop", Namespace: ns, Annotations: map[string]string{oam.AnnotationDispatch: oam.DispatchComponent}},
			Spec: v1beta1.ApplicationSpec{Components: []common.ApplicationComponent{{
				Name: "web", Type: "dispatch-config", Properties: &runtime.RawExtension{Raw: []byte(`{"ready":"no"}`)},
			}}},
		}
		Expect(k8sClient.Create(ctx, app)).To(Succeed())
		appKey := client.ObjectKeyFromObject(app)
		compKey := types.NamespacedName{Namespace: ns, Name: componentName(ns, "shop", "web")}

		reconcileApp := func() *v1beta1.Application {
			_, err := testutil.ReconcileOnceAfterFinalizer(reconciler, reconcile.Request{NamespacedName: appKey})
			Expect(err).NotTo(HaveOccurred())
			current := &v1beta1.Application{}
			Expect(k8sClient.Get(ctx, appKey, current)).To(Succeed())
			return current
		}
		runAgent := func() {
			for i := 0; i < 3; i++ {
				_, err := agent.Reconcile(ctx, ctrl.Request{NamespacedName: compKey})
				Expect(err).NotTo(HaveOccurred())
			}
		}
		getComponent := func() *v1alpha1.Component {
			c := &v1alpha1.Component{}
			Expect(k8sClient.Get(ctx, compKey, c)).To(Succeed())
			return c
		}

		By("the hub dispatches a signed Component and its payload, and applies nothing itself")
		Eventually(func() error {
			reconcileApp()
			return k8sClient.Get(ctx, compKey, &v1alpha1.Component{})
		}, "10s", "100ms").Should(Succeed())
		comp := getComponent()
		Expect(comp.Spec.Revision).To(Equal(int64(1)))
		Expect(keys.Verify(comp)).To(Succeed())
		Expect(comp.Labels).To(HaveKeyWithValue(oam.LabelAppComponent, "web"))
		Expect(comp.Labels).To(HaveKeyWithValue(oam.LabelComponentCluster, "local"))
		Expect(comp.Spec.Resources).To(HaveLen(1))
		Expect(comp.Spec.Resources[0].Kind).To(Equal("ConfigMap"))
		Expect(comp.Spec.Resources[0].Role).To(Equal("output"))
		Expect(comp.Spec.Health.Workload.HealthPolicy).To(ContainSubstring(`context.output.data.ready == "yes"`))
		Expect(comp.Spec.Health.Context).To(HaveKeyWithValue("name", "web"))
		Expect(comp.Spec.Health.Context).To(HaveKeyWithValue("appName", "shop"))
		Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: ns, Name: componentName(ns, "shop", "web") + payloadSuffix}, &corev1.Secret{})).To(Succeed())
		Expect(kerrors.IsNotFound(k8sClient.Get(ctx, types.NamespacedName{Namespace: ns, Name: "web"}, &corev1.ConfigMap{}))).To(BeTrue(),
			"the workload is vela-agent's to apply")

		current := reconcileApp()
		Expect(current.Status.Services).To(HaveLen(1))
		Expect(current.Status.Services[0].Healthy).To(BeFalse())
		Expect(current.Status.Services[0].Message).To(ContainSubstring("waiting for vela-agent on cluster local to apply revision 1"))
		var kinds []string
		for _, r := range current.Status.AppliedResources {
			kinds = append(kinds, r.Kind)
		}
		Expect(kinds).To(ConsistOf("Secret", "Component"))

		By("vela-agent applies it, and the hub reports the health it evaluated")
		runAgent()
		cm := &corev1.ConfigMap{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: ns, Name: "web"}, cm)).To(Succeed())
		Expect(cm.Data).To(HaveKeyWithValue("ready", "no"))
		Expect(cm.Labels).NotTo(HaveKey(oam.LabelAppCluster))
		current = reconcileApp()
		Expect(current.Status.Services[0].Healthy).To(BeFalse())
		Expect(current.Status.Services[0].Message).To(Equal("ready: no"))

		By("an unchanged render keeps the revision")
		reconcileApp()
		Expect(getComponent().Spec.Revision).To(Equal(int64(1)))

		By("a changed render is the next revision, and becomes healthy once applied")
		Expect(k8sClient.Get(ctx, appKey, current)).To(Succeed())
		current.Spec.Components[0].Properties = &runtime.RawExtension{Raw: []byte(`{"ready":"yes"}`)}
		Expect(k8sClient.Update(ctx, current)).To(Succeed())
		Eventually(func() int64 {
			reconcileApp()
			return getComponent().Spec.Revision
		}, "10s", "100ms").Should(Equal(int64(2)))
		comp = getComponent()
		Expect(keys.Verify(comp)).To(Succeed())
		runAgent()
		Eventually(func() common.ApplicationPhase {
			current = reconcileApp()
			return current.Status.Phase
		}, "10s", "100ms").Should(Equal(common.ApplicationRunning))
		Expect(current.Status.Services[0].Healthy).To(BeTrue())
		Expect(current.Status.Services[0].Message).To(Equal("ready: yes"))
		Expect(getComponent().Spec.Revision).To(Equal(int64(2)))
	})

	It("reports the same health as the in-process path", func() {
		featuregatetesting.SetFeatureGateDuringTest(GinkgoT(), utilfeature.DefaultFeatureGate, features.EnableComponentDispatch, true)
		pub, priv, err := ed25519.GenerateKey(rand.Reader)
		Expect(err).NotTo(HaveOccurred())
		reconciler.ComponentKeys = newRingKeys(priv)
		DeferCleanup(func() { reconciler.ComponentKeys = nil })
		agent := &component.Reconciler{Client: k8sClient, Keys: component.StaticKeys{"hub-1": pub}, Cluster: "local", Recorder: record.NewFakeRecorder(100)}

		for _, doc := range strings.Split(parityDefinitions, "\n---\n") {
			obj := &unstructured.Unstructured{}
			raw, err := yaml.YAMLToJSON([]byte(doc))
			Expect(err).NotTo(HaveOccurred())
			Expect(obj.UnmarshalJSON(raw)).To(Succeed())
			Expect(k8sClient.Create(ctx, obj)).Should(SatisfyAny(BeNil(), &util.AlreadyExistMatcher{}))
		}

		// The same Application, by name, in two namespaces: the context is the same in both.
		services := func(ns string, dispatch bool, properties, note string) common.ApplicationComponentStatus {
			Expect(k8sClient.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}})).Should(SatisfyAny(BeNil(), &util.AlreadyExistMatcher{}))
			app := &v1beta1.Application{
				ObjectMeta: metav1.ObjectMeta{Name: "parity", Namespace: ns},
				Spec: v1beta1.ApplicationSpec{Components: []common.ApplicationComponent{{
					Name: "web", Type: "parity-config", Properties: &runtime.RawExtension{Raw: []byte(properties)},
					Traits: []common.ApplicationTrait{{Type: "parity-note", Properties: &runtime.RawExtension{Raw: []byte(note)}}},
				}}},
			}
			if dispatch {
				app.Annotations = map[string]string{oam.AnnotationDispatch: oam.DispatchComponent}
			}
			Expect(k8sClient.Create(ctx, app)).To(Succeed())
			key := client.ObjectKeyFromObject(app)
			current := &v1beta1.Application{}
			Eventually(func() []common.ApplicationComponentStatus {
				_, err := testutil.ReconcileOnceAfterFinalizer(reconciler, reconcile.Request{NamespacedName: key})
				Expect(err).NotTo(HaveOccurred())
				if dispatch {
					_, err := agent.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Namespace: ns, Name: componentName(ns, "parity", "web")}})
					Expect(err).NotTo(HaveOccurred())
				}
				Expect(k8sClient.Get(ctx, key, current)).To(Succeed())
				if len(current.Status.Services) == 1 && strings.HasPrefix(current.Status.Services[0].Message, "waiting for") {
					return nil
				}
				return current.Status.Services
			}, "15s", "200ms").Should(HaveLen(1))
			return current.Status.Services[0]
		}
		comparable := func(s common.ApplicationComponentStatus) common.ApplicationComponentStatus {
			s.Namespace = ""
			sort.Slice(s.Traits, func(i, j int) bool { return s.Traits[i].Type < s.Traits[j].Type })
			return s
		}

		for i, c := range []struct{ properties, note string }{
			{`{"ready":"no"}`, `{"text":"pending"}`},
			{`{"ready":"yes"}`, `{"text":"ok"}`},
		} {
			inProcess := services(fmt.Sprintf("parity-inproc-%d", i), false, c.properties, c.note)
			dispatched := services(fmt.Sprintf("parity-dispatch-%d", i), true, c.properties, c.note)
			Expect(dispatched.Message).To(ContainSubstring("(web in parity@parity-v1)"))
			Expect(comparable(dispatched)).To(Equal(comparable(inProcess)))
		}
	})

	It("answers a read of a dispatched component from what vela-agent observed, opening what it sealed", func() {
		featuregatetesting.SetFeatureGateDuringTest(GinkgoT(), utilfeature.DefaultFeatureGate, features.EnableComponentDispatch, true)
		featuregatetesting.SetFeatureGateDuringTest(GinkgoT(), utilfeature.DefaultFeatureGate, features.EnableCelExpressions, true)
		_, priv, err := ed25519.GenerateKey(rand.Reader)
		Expect(err).NotTo(HaveOccurred())
		hubKeys := newRingKeys(priv)
		reconciler.ComponentKeys = hubKeys
		DeferCleanup(func() { reconciler.ComponentKeys = nil })
		agent := &component.Reconciler{Client: k8sClient, Keys: component.RingKeys{Ring: hubKeys.ring}, Cluster: "local", Recorder: record.NewFakeRecorder(100)}

		ns := "component-reads"
		Expect(k8sClient.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}})).Should(SatisfyAny(BeNil(), &util.AlreadyExistMatcher{}))
		for _, doc := range []string{dispatchConfigDefinition, dispatchSecretDefinition} {
			def := &v1beta1.ComponentDefinition{}
			defJSON, err := yaml.YAMLToJSON([]byte(doc))
			Expect(err).NotTo(HaveOccurred())
			Expect(json.Unmarshal(defJSON, def)).To(Succeed())
			Expect(k8sClient.Create(ctx, def)).Should(SatisfyAny(BeNil(), &util.AlreadyExistMatcher{}))
		}

		props := func(raw string) *runtime.RawExtension { return &runtime.RawExtension{Raw: []byte(raw)} }
		app := &v1beta1.Application{
			ObjectMeta: metav1.ObjectMeta{Name: "shop", Namespace: ns, Annotations: map[string]string{
				oam.AnnotationDispatch: oam.DispatchComponent, oam.AnnotationCelExpressions: "true"}},
			Spec: v1beta1.ApplicationSpec{Components: []common.ApplicationComponent{
				{Name: "web", Type: "dispatch-config", Properties: props(`{"ready":"yes"}`)},
				{Name: "creds", Type: "dispatch-secret", Properties: props(`{"password":"hunter2"}`)},
				{Name: "api", Type: "dispatch-config", Properties: props(
					`{"ready":"$(component.web.output.data.ready)","note":"$(component.creds.output.data.password)"}`)},
			}},
		}
		Expect(k8sClient.Create(ctx, app)).To(Succeed())
		appKey := client.ObjectKeyFromObject(app)
		compKey := func(name string) types.NamespacedName {
			return types.NamespacedName{Namespace: ns, Name: componentName(ns, "shop", name)}
		}
		Eventually(func() common.ApplicationPhase {
			_, err := testutil.ReconcileOnceAfterFinalizer(reconciler, reconcile.Request{NamespacedName: appKey})
			Expect(err).NotTo(HaveOccurred())
			for _, c := range []string{"web", "creds", "api"} {
				_, err := agent.Reconcile(ctx, ctrl.Request{NamespacedName: compKey(c)})
				Expect(err).NotTo(HaveOccurred())
			}
			current := &v1beta1.Application{}
			Expect(k8sClient.Get(ctx, appKey, current)).To(Succeed())
			return current.Status.Phase
		}, "20s", "100ms").Should(Equal(common.ApplicationRunning))

		apiConfig := &corev1.ConfigMap{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: ns, Name: "api"}, apiConfig)).To(Succeed())
		Expect(apiConfig.Data).To(Equal(map[string]string{"ready": "yes", "note": "aHVudGVyMg=="}),
			"api read what vela-agent observed of web and creds")

		By("the producers were asked to observe what api reads, a Secret's sealed")
		web := &v1alpha1.Component{}
		Expect(k8sClient.Get(ctx, compKey("web"), web)).To(Succeed())
		Expect(web.Spec.Observe).To(ContainElement(v1alpha1.ComponentObservation{Resource: "output", Name: `["data","ready"]`, Path: ".data.ready"}))
		Expect(string(web.Status.Observed["output"].Raw)).To(ContainSubstring(`"[\"data\",\"ready\"]":"yes"`))
		creds := &v1alpha1.Component{}
		Expect(k8sClient.Get(ctx, compKey("creds"), creds)).To(Succeed())
		Expect(creds.Spec.Observe).To(ContainElement(v1alpha1.ComponentObservation{
			Resource: "output", Name: `["data","password"]`, Path: ".data.password", Sensitive: true}))
		Expect(creds.Status.SealedWith).To(Equal("hub-1"))
		Expect(creds.Status.SealedObserved["output"]).To(HaveKey(`["data","password"]`))
		raw, err := json.Marshal(creds.Status)
		Expect(err).NotTo(HaveOccurred())
		Expect(string(raw)).NotTo(ContainSubstring("aHVudGVyMg=="), "the password is never in the clear on the Component")
	})

	It("leaves a dispatched component's Components to vela-agent when components are re-applied outside the workflow", func() {
		featuregatetesting.SetFeatureGateDuringTest(GinkgoT(), utilfeature.DefaultFeatureGate, features.EnableComponentDispatch, true)
		pub, priv, err := ed25519.GenerateKey(rand.Reader)
		Expect(err).NotTo(HaveOccurred())
		reconciler.ComponentKeys = newRingKeys(priv)
		DeferCleanup(func() { reconciler.ComponentKeys = nil })
		agent := &component.Reconciler{Client: k8sClient, Keys: component.StaticKeys{"hub-1": pub}, Cluster: "local", Recorder: record.NewFakeRecorder(100)}

		ns := "component-reapply"
		Expect(k8sClient.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}})).Should(SatisfyAny(BeNil(), &util.AlreadyExistMatcher{}))
		def := &v1beta1.ComponentDefinition{}
		defJSON, err := yaml.YAMLToJSON([]byte(dispatchConfigDefinition))
		Expect(err).NotTo(HaveOccurred())
		Expect(json.Unmarshal(defJSON, def)).To(Succeed())
		Expect(k8sClient.Create(ctx, def)).Should(SatisfyAny(BeNil(), &util.AlreadyExistMatcher{}))

		web := common.ApplicationComponent{Name: "web", Type: "dispatch-config", Properties: &runtime.RawExtension{Raw: []byte(`{"ready":"yes"}`)}}
		app := &v1beta1.Application{
			ObjectMeta: metav1.ObjectMeta{Name: "shop", Namespace: ns, Annotations: map[string]string{oam.AnnotationDispatch: oam.DispatchComponent}},
			Spec:       v1beta1.ApplicationSpec{Components: []common.ApplicationComponent{web}},
		}
		Expect(k8sClient.Create(ctx, app)).To(Succeed())
		appKey := client.ObjectKeyFromObject(app)
		compKey := types.NamespacedName{Namespace: ns, Name: componentName(ns, "shop", "web")}
		Eventually(func() common.ApplicationPhase {
			_, err := testutil.ReconcileOnceAfterFinalizer(reconciler, reconcile.Request{NamespacedName: appKey})
			Expect(err).NotTo(HaveOccurred())
			_, err = agent.Reconcile(ctx, ctrl.Request{NamespacedName: compKey})
			Expect(err).NotTo(HaveOccurred())
			Expect(k8sClient.Get(ctx, appKey, app)).To(Succeed())
			return app.Status.Phase
		}, "15s", "100ms").Should(Equal(common.ApplicationRunning))

		logCtx := monitorContext.NewTraceContext(ctx, "")
		parser := appfile.NewApplicationParser(reconciler.Client)
		handler, err := NewAppHandler(logCtx, reconciler, app)
		Expect(err).NotTo(HaveOccurred())
		af, err := reconciler.generateAppFile(logCtx, parser, app, handler)
		Expect(err).NotTo(HaveOccurred())
		Expect(handler.PrepareCurrentAppRevision(logCtx, af)).To(Succeed())
		complete := reconciler.reapplyComponents(logCtx, handler, parser, af, app, map[string]common.ApplicationComponent{"web": web})

		Expect(k8sClient.Get(ctx, compKey, &v1alpha1.Component{})).To(Succeed(), "the Component is what delivers web")
		Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: ns, Name: componentName(ns, "shop", "web") + payloadSuffix}, &corev1.Secret{})).To(Succeed())
		Expect(complete).To(BeTrue())
	})

	It("re-signs a settled Component with the active key after a rotation, at the same revision", func() {
		featuregatetesting.SetFeatureGateDuringTest(GinkgoT(), utilfeature.DefaultFeatureGate, features.EnableComponentDispatch, true)
		pub1, priv1, err := ed25519.GenerateKey(rand.Reader)
		Expect(err).NotTo(HaveOccurred())
		pub2, priv2, err := ed25519.GenerateKey(rand.Reader)
		Expect(err).NotTo(HaveOccurred())
		reconciler.ComponentKeys = newRingKeys(priv1)
		DeferCleanup(func() { reconciler.ComponentKeys = nil })
		agent := &component.Reconciler{Client: k8sClient, Keys: component.StaticKeys{"hub-1": pub1, "hub-2": pub2}, Cluster: "local", Recorder: record.NewFakeRecorder(100)}

		ns := "component-rotation"
		Expect(k8sClient.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}})).Should(SatisfyAny(BeNil(), &util.AlreadyExistMatcher{}))
		def := &v1beta1.ComponentDefinition{}
		defJSON, err := yaml.YAMLToJSON([]byte(dispatchConfigDefinition))
		Expect(err).NotTo(HaveOccurred())
		Expect(json.Unmarshal(defJSON, def)).To(Succeed())
		Expect(k8sClient.Create(ctx, def)).Should(SatisfyAny(BeNil(), &util.AlreadyExistMatcher{}))

		app := &v1beta1.Application{
			ObjectMeta: metav1.ObjectMeta{Name: "shop", Namespace: ns, Annotations: map[string]string{oam.AnnotationDispatch: oam.DispatchComponent}},
			Spec: v1beta1.ApplicationSpec{Components: []common.ApplicationComponent{
				{Name: "web", Type: "dispatch-config", Properties: &runtime.RawExtension{Raw: []byte(`{"ready":"yes"}`)}}}},
		}
		Expect(k8sClient.Create(ctx, app)).To(Succeed())
		appKey := client.ObjectKeyFromObject(app)
		compKey := types.NamespacedName{Namespace: ns, Name: componentName(ns, "shop", "web")}
		settle := func() {
			_, err := testutil.ReconcileOnceAfterFinalizer(reconciler, reconcile.Request{NamespacedName: appKey})
			Expect(err).NotTo(HaveOccurred())
			_, err = agent.Reconcile(ctx, ctrl.Request{NamespacedName: compKey})
			Expect(err).NotTo(HaveOccurred())
		}
		live := func() *v1alpha1.Component {
			c := &v1alpha1.Component{}
			Expect(k8sClient.Get(ctx, compKey, c)).To(Succeed())
			return c
		}
		Eventually(func() common.ApplicationPhase {
			settle()
			Expect(k8sClient.Get(ctx, appKey, app)).To(Succeed())
			return app.Status.Phase
		}, "15s", "100ms").Should(Equal(common.ApplicationRunning))
		before := live()
		Expect(before.Spec.Signature.KeyID).To(Equal("hub-1"))

		By("hub-2 becomes the active key, hub-1 still trusted")
		reconciler.ComponentKeys = ringKeys{ring: &componentdispatch.KeyRing{Active: "hub-2",
			Keys: map[string]ed25519.PrivateKey{"hub-1": priv1, "hub-2": priv2}}}
		Eventually(func() string {
			settle()
			return live().Spec.Signature.KeyID
		}, "15s", "100ms").Should(Equal("hub-2"))
		after := live()
		Expect(after.Spec.Revision).To(Equal(before.Spec.Revision), "the spec is unchanged, so the revision is too")
		Expect(meta.IsStatusConditionTrue(after.Status.Conditions, component.ConditionVerified)).To(BeTrue())
	})

	It("has vela-agent render a PostDispatch trait once the workload is healthy, in the same revision", func() {
		featuregatetesting.SetFeatureGateDuringTest(GinkgoT(), utilfeature.DefaultFeatureGate, features.EnableComponentDispatch, true)
		pub, priv, err := ed25519.GenerateKey(rand.Reader)
		Expect(err).NotTo(HaveOccurred())
		reconciler.ComponentKeys = newRingKeys(priv)
		DeferCleanup(func() { reconciler.ComponentKeys = nil })
		agent := &component.Reconciler{Client: k8sClient, Keys: component.StaticKeys{"hub-1": pub}, Cluster: "local", Recorder: record.NewFakeRecorder(100)}

		ns := "component-post-dispatch"
		Expect(k8sClient.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}})).Should(SatisfyAny(BeNil(), &util.AlreadyExistMatcher{}))
		for _, doc := range []string{dispatchConfigDefinition, dispatchPostDefinition} {
			obj := &unstructured.Unstructured{}
			raw, err := yaml.YAMLToJSON([]byte(doc))
			Expect(err).NotTo(HaveOccurred())
			Expect(obj.UnmarshalJSON(raw)).To(Succeed())
			Expect(k8sClient.Create(ctx, obj)).Should(SatisfyAny(BeNil(), &util.AlreadyExistMatcher{}))
		}

		web := func(text string) common.ApplicationComponent {
			return common.ApplicationComponent{Name: "web", Type: "dispatch-config", Properties: &runtime.RawExtension{Raw: []byte(`{"ready":"yes"}`)},
				Traits: []common.ApplicationTrait{{Type: "dispatch-post", Properties: &runtime.RawExtension{Raw: []byte(`{"text":"` + text + `"}`)}}}}
		}
		app := &v1beta1.Application{
			ObjectMeta: metav1.ObjectMeta{Name: "shop", Namespace: ns, Annotations: map[string]string{oam.AnnotationDispatch: oam.DispatchComponent}},
			Spec:       v1beta1.ApplicationSpec{Components: []common.ApplicationComponent{web("one")}},
		}
		Expect(k8sClient.Create(ctx, app)).To(Succeed())
		appKey := client.ObjectKeyFromObject(app)
		compKey := types.NamespacedName{Namespace: ns, Name: componentName(ns, "shop", "web")}
		hub := func() {
			_, err := testutil.ReconcileOnceAfterFinalizer(reconciler, reconcile.Request{NamespacedName: appKey})
			Expect(err).NotTo(HaveOccurred())
		}
		runAgent := func() {
			_, err := agent.Reconcile(ctx, ctrl.Request{NamespacedName: compKey})
			Expect(err).NotTo(HaveOccurred())
		}
		roles := func(c *v1alpha1.Component) []string {
			var out []string
			for _, r := range c.Spec.Resources {
				out = append(out, r.Role)
			}
			return out
		}
		live := func() *v1alpha1.Component {
			c := &v1alpha1.Component{}
			Expect(k8sClient.Get(ctx, compKey, c)).To(Succeed())
			return c
		}

		By("one revision carries the workload, and the trait for vela-agent to render")
		Eventually(func() error {
			hub()
			return k8sClient.Get(ctx, compKey, &v1alpha1.Component{})
		}, "15s", "100ms").Should(Succeed())
		Expect(live().Spec.Revision).To(Equal(int64(1)))
		Expect(roles(live())).To(Equal([]string{"output"}))
		Expect(live().Spec.PostRender).NotTo(BeNil())
		Expect(live().Spec.PostRender.Traits).To(HaveLen(1))

		By("vela-agent renders the trait once the workload is healthy, with no new revision")
		Eventually(func() common.ApplicationPhase {
			runAgent()
			hub()
			Expect(k8sClient.Get(ctx, appKey, app)).To(Succeed())
			return app.Status.Phase
		}, "15s", "100ms").Should(Equal(common.ApplicationRunning))
		post := &corev1.ConfigMap{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: ns, Name: "web-post"}, post)).To(Succeed())
		Expect(post.Data).To(Equal(map[string]string{"ready": "yes", "text": "one"}))
		Expect(post.Labels).To(HaveKeyWithValue(oam.LabelAppName, "shop"))
		Expect(post.Labels).To(HaveKeyWithValue(oam.TraitTypeLabel, "dispatch-post"))
		var statusRoles []string
		for _, r := range live().Status.Resources {
			statusRoles = append(statusRoles, r.Role)
		}
		Expect(statusRoles).To(ConsistOf("output", "outputs.post"))
		Expect(meta.IsStatusConditionTrue(live().Status.Conditions, component.ConditionReady)).To(BeTrue(), "every stage passed, the post render included")
		Expect(meta.IsStatusConditionTrue(live().Status.Conditions, component.ConditionPostRendered)).To(BeTrue())
		for range 3 {
			runAgent()
			hub()
		}
		Expect(live().Spec.Revision).To(Equal(int64(1)), "a settled component is not re-dispatched")

		By("annotating the Application cuts no revision, so dispatches nothing")
		Eventually(func() error {
			Expect(k8sClient.Get(ctx, appKey, app)).To(Succeed())
			metav1.SetMetaDataAnnotation(&app.ObjectMeta, "example.com/touched", "yes")
			return k8sClient.Update(ctx, app)
		}, "5s", "100ms").Should(Succeed())
		for range 3 {
			runAgent()
			hub()
		}
		Expect(live().Spec.Revision).To(Equal(int64(1)), "an annotation alone is not a new render")

		By("an update is a new revision, and vela-agent renders the trait from it")
		Expect(k8sClient.Get(ctx, appKey, app)).To(Succeed())
		app.Spec.Components = []common.ApplicationComponent{web("two")}
		Expect(k8sClient.Update(ctx, app)).To(Succeed())
		Eventually(func() int64 {
			hub()
			return live().Spec.Revision
		}, "15s", "100ms").Should(Equal(int64(2)))
		Eventually(func() map[string]string {
			runAgent()
			hub()
			Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: ns, Name: "web-post"}, post)).To(Succeed())
			return post.Data
		}, "15s", "100ms").Should(Equal(map[string]string{"ready": "yes", "text": "two"}))
	})

	It("renders a PostDispatch trait's outputs on vela-agent exactly as the hub renders them in-process", func() {
		featuregatetesting.SetFeatureGateDuringTest(GinkgoT(), utilfeature.DefaultFeatureGate, features.EnableComponentDispatch, true)
		pub, priv, err := ed25519.GenerateKey(rand.Reader)
		Expect(err).NotTo(HaveOccurred())
		reconciler.ComponentKeys = newRingKeys(priv)
		DeferCleanup(func() { reconciler.ComponentKeys = nil })
		agent := &component.Reconciler{Client: k8sClient, Keys: component.StaticKeys{"hub-1": pub}, Cluster: "local", Recorder: record.NewFakeRecorder(100)}
		for _, doc := range []string{dispatchConfigDefinition, dispatchParityDefinition} {
			obj := &unstructured.Unstructured{}
			raw, err := yaml.YAMLToJSON([]byte(doc))
			Expect(err).NotTo(HaveOccurred())
			Expect(obj.UnmarshalJSON(raw)).To(Succeed())
			Expect(k8sClient.Create(ctx, obj)).Should(SatisfyAny(BeNil(), &util.AlreadyExistMatcher{}))
		}

		// rendered is each output of the trait in ns, as applied, keyed by name, less what
		// differs by design:
		//   - the namespace, and the dispatch annotation the Application passes down;
		//   - the app-revision hash, which covers the dispatch mode;
		//   - the cluster label, which no dispatched resource carries: it is in its own cluster;
		//   - the owner labels, the render hash over them, and the last-applied record: the
		//     Component owns what vela-agent applies, the Application what the hub applies.
		rendered := func(ns string) map[string]corev1.ConfigMap {
			list := &corev1.ConfigMapList{}
			Expect(k8sClient.List(ctx, list, client.InNamespace(ns), client.MatchingLabels{oam.TraitTypeLabel: "dispatch-parity"})).To(Succeed())
			out := map[string]corev1.ConfigMap{}
			for _, cm := range list.Items {
				if cm.Labels[oam.LabelAppNamespace] == ns {
					cm.Labels[oam.LabelAppNamespace] = "<namespace>"
				}
				for _, k := range []string{oam.LabelAppRevisionHash, oam.LabelAppCluster, oam.LabelOwnerKind, oam.LabelOwnerName,
					oam.LabelOwnerNamespace, apply.LabelRenderHash} {
					delete(cm.Labels, k)
				}
				delete(cm.Annotations, oam.AnnotationDispatch)
				delete(cm.Annotations, oam.AnnotationLastAppliedConfig)
				out[cm.Name] = corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: cm.Name, Labels: cm.Labels, Annotations: cm.Annotations}, Data: cm.Data}
			}
			return out
		}
		run := func(ns string, dispatched bool) map[string]corev1.ConfigMap {
			Expect(k8sClient.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}})).Should(SatisfyAny(BeNil(), &util.AlreadyExistMatcher{}))
			annotations := map[string]string{
				"example.com/team":                 "a",
				"example.com/hidden":               "from-the-app",
				oam.AnnotationFilterAnnotationKeys: "example.com/hidden",
			}
			if dispatched {
				annotations[oam.AnnotationDispatch] = oam.DispatchComponent
			}
			app := &v1beta1.Application{
				ObjectMeta: metav1.ObjectMeta{Name: "shop", Namespace: ns, Annotations: annotations},
				Spec: v1beta1.ApplicationSpec{Components: []common.ApplicationComponent{{
					Name: "web", Type: "dispatch-config", Properties: &runtime.RawExtension{Raw: []byte(`{"ready":"yes"}`)},
					Traits: []common.ApplicationTrait{{Type: "dispatch-parity", Properties: &runtime.RawExtension{Raw: []byte(`{"text":"hello"}`)}}},
				}}},
			}
			Expect(k8sClient.Create(ctx, app)).To(Succeed())
			compKey := types.NamespacedName{Namespace: ns, Name: componentName(ns, "shop", "web")}
			Eventually(func() int {
				_, err := testutil.ReconcileOnceAfterFinalizer(reconciler, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(app)})
				Expect(err).NotTo(HaveOccurred())
				if dispatched {
					if err := k8sClient.Get(ctx, compKey, &v1alpha1.Component{}); err == nil {
						_, err := agent.Reconcile(ctx, ctrl.Request{NamespacedName: compKey})
						Expect(err).NotTo(HaveOccurred())
					}
				}
				return len(rendered(ns))
			}, "20s", "100ms").Should(Equal(2))
			return rendered(ns)
		}

		inProcess := run("component-parity-in-process", false)
		dispatched := run("component-parity-dispatched", true)
		Expect(dispatched).To(Equal(inProcess))
		Expect(inProcess["web-named"].Annotations).To(HaveKeyWithValue("example.com/own", "kept"))
		Expect(inProcess["web-named"].Annotations).NotTo(HaveKey("example.com/hidden"))
	})

	It("has vela-agent render a replicated component's PostDispatch trait in each replica's Component", func() {
		featuregatetesting.SetFeatureGateDuringTest(GinkgoT(), utilfeature.DefaultFeatureGate, features.EnableComponentDispatch, true)
		pub, priv, err := ed25519.GenerateKey(rand.Reader)
		Expect(err).NotTo(HaveOccurred())
		reconciler.ComponentKeys = newRingKeys(priv)
		DeferCleanup(func() { reconciler.ComponentKeys = nil })
		agent := &component.Reconciler{Client: k8sClient, Keys: component.StaticKeys{"hub-1": pub}, Cluster: "local", Recorder: record.NewFakeRecorder(100)}

		ns := "component-replicated-post"
		Expect(k8sClient.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}})).Should(SatisfyAny(BeNil(), &util.AlreadyExistMatcher{}))
		replicated := func(doc string) string {
			doc = strings.ReplaceAll(doc, "name: dispatch-config", "name: dispatch-replica-config")
			doc = strings.ReplaceAll(doc, "name: dispatch-post", "name: dispatch-replica-post")
			return strings.ReplaceAll(doc, "metadata: name: context.name", `metadata: name: context.name + "-" + context.replicaKey`)
		}
		for _, doc := range []string{replicated(dispatchConfigDefinition), replicated(dispatchPostDefinition), dispatchDeployStepDefinition} {
			obj := &unstructured.Unstructured{}
			raw, err := yaml.YAMLToJSON([]byte(doc))
			Expect(err).NotTo(HaveOccurred())
			Expect(obj.UnmarshalJSON(raw)).To(Succeed())
			Expect(k8sClient.Create(ctx, obj)).Should(SatisfyAny(BeNil(), &util.AlreadyExistMatcher{}))
		}

		app := &v1beta1.Application{
			ObjectMeta: metav1.ObjectMeta{Name: "shop", Namespace: ns, Annotations: map[string]string{oam.AnnotationDispatch: oam.DispatchComponent}},
			Spec: v1beta1.ApplicationSpec{
				Components: []common.ApplicationComponent{{Name: "web", Type: "dispatch-replica-config",
					Properties: &runtime.RawExtension{Raw: []byte(`{"ready":"yes"}`)},
					Traits:     []common.ApplicationTrait{{Type: "dispatch-replica-post", Properties: &runtime.RawExtension{Raw: []byte(`{"text":"one"}`)}}}}},
				Policies: []v1beta1.AppPolicy{{Name: "replicas", Type: "replication",
					Properties: &runtime.RawExtension{Raw: []byte(`{"keys":["eu","us"],"selector":["web"]}`)}}},
				Workflow: &v1beta1.Workflow{Steps: []wfTypesv1alpha1.WorkflowStep{{WorkflowStepBase: wfTypesv1alpha1.WorkflowStepBase{
					Name: "deploy", Type: "deploy", Properties: &runtime.RawExtension{Raw: []byte(`{"policies":["replicas"]}`)}}}}},
			},
		}
		Expect(k8sClient.Create(ctx, app)).To(Succeed())
		appKey := client.ObjectKeyFromObject(app)
		components := func() []v1alpha1.Component {
			list := &v1alpha1.ComponentList{}
			Expect(k8sClient.List(ctx, list, client.InNamespace(ns))).To(Succeed())
			return list.Items
		}
		// rolesByReplica is each Component's resource roles, by its replica key.
		rolesByReplica := func() map[string][]string {
			_, err := testutil.ReconcileOnceAfterFinalizer(reconciler, reconcile.Request{NamespacedName: appKey})
			Expect(err).NotTo(HaveOccurred())
			out := map[string][]string{}
			for _, c := range components() {
				_, err := agent.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(&c)})
				Expect(err).NotTo(HaveOccurred())
				var roles []string
				for _, r := range c.Status.Resources {
					roles = append(roles, r.Role)
				}
				sort.Strings(roles)
				out[c.Labels[oam.LabelReplicaKey]] = roles
			}
			return out
		}

		Eventually(rolesByReplica, "20s", "100ms").Should(Equal(map[string][]string{
			"eu": {"output", "outputs.post"},
			"us": {"output", "outputs.post"},
		}), "each replica's Component renders the trait, and no Component is left without a replica key")
		for _, key := range []string{"eu", "us"} {
			Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: ns, Name: "web-" + key + "-post"}, &corev1.ConfigMap{})).To(Succeed())
		}
	})

	It("hands an in-process component over to vela-agent and back, keeping every resource", func() {
		featuregatetesting.SetFeatureGateDuringTest(GinkgoT(), utilfeature.DefaultFeatureGate, features.EnableComponentDispatch, true)
		pub, priv, err := ed25519.GenerateKey(rand.Reader)
		Expect(err).NotTo(HaveOccurred())
		reconciler.ComponentKeys = newRingKeys(priv)
		DeferCleanup(func() { reconciler.ComponentKeys = nil })
		agent := &component.Reconciler{Client: k8sClient, Keys: component.StaticKeys{"hub-1": pub}, Cluster: "local", Recorder: record.NewFakeRecorder(100)}
		DeferCleanup(func(p float64) { resourcekeeper.MarkWithProbability = p }, resourcekeeper.MarkWithProbability)
		resourcekeeper.MarkWithProbability = 1.0

		ns := "component-handover"
		Expect(k8sClient.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}})).Should(SatisfyAny(BeNil(), &util.AlreadyExistMatcher{}))
		for _, doc := range []string{dispatchConfigDefinition, dispatchPostDefinition} {
			def := &unstructured.Unstructured{}
			defJSON, err := yaml.YAMLToJSON([]byte(doc))
			Expect(err).NotTo(HaveOccurred())
			Expect(json.Unmarshal(defJSON, &def.Object)).To(Succeed())
			Expect(k8sClient.Create(ctx, def)).Should(SatisfyAny(BeNil(), &util.AlreadyExistMatcher{}))
		}

		app := &v1beta1.Application{
			ObjectMeta: metav1.ObjectMeta{Name: "shop", Namespace: ns},
			Spec: v1beta1.ApplicationSpec{Components: []common.ApplicationComponent{
				{Name: "web", Type: "dispatch-config", Properties: &runtime.RawExtension{Raw: []byte(`{"ready":"yes"}`)},
					Traits: []common.ApplicationTrait{{Type: "dispatch-post", Properties: &runtime.RawExtension{Raw: []byte(`{"text":"one"}`)}}}}}},
		}
		Expect(k8sClient.Create(ctx, app)).To(Succeed())
		appKey := client.ObjectKeyFromObject(app)
		compKey := types.NamespacedName{Namespace: ns, Name: componentName(ns, "shop", "web")}
		cmKey := types.NamespacedName{Namespace: ns, Name: "web"}
		reconcileAll := func() {
			_, err := testutil.ReconcileOnceAfterFinalizer(reconciler, reconcile.Request{NamespacedName: appKey})
			Expect(err).NotTo(HaveOccurred())
			_, err = agent.Reconcile(ctx, ctrl.Request{NamespacedName: compKey})
			Expect(err).NotTo(HaveOccurred())
		}
		running := func() bool {
			Expect(k8sClient.Get(ctx, appKey, app)).To(Succeed())
			return app.Status.Phase == common.ApplicationRunning
		}
		configMap := func() *corev1.ConfigMap {
			cm := &corev1.ConfigMap{}
			Expect(k8sClient.Get(ctx, cmKey, cm)).To(Succeed())
			return cm
		}
		postKey := types.NamespacedName{Namespace: ns, Name: "web-post"}
		post := func() *corev1.ConfigMap {
			cm := &corev1.ConfigMap{}
			Expect(k8sClient.Get(ctx, postKey, cm)).To(Succeed())
			return cm
		}
		setDispatch := func(on bool) {
			Eventually(func() error {
				Expect(k8sClient.Get(ctx, appKey, app)).To(Succeed())
				if on {
					metav1.SetMetaDataAnnotation(&app.ObjectMeta, oam.AnnotationDispatch, oam.DispatchComponent)
				} else {
					delete(app.Annotations, oam.AnnotationDispatch)
				}
				return k8sClient.Update(ctx, app)
			}, "5s", "100ms").Should(Succeed())
		}

		Eventually(func() bool { reconcileAll(); return running() }, "15s", "100ms").Should(BeTrue())
		uid := configMap().UID
		Eventually(func() error { reconcileAll(); return k8sClient.Get(ctx, postKey, &corev1.ConfigMap{}) }, "15s", "100ms").Should(Succeed())
		postUID := post().UID
		inProcessPost := post()
		Expect(configMap().Labels).To(HaveKeyWithValue(oam.LabelOwnerKind, v1beta1.ApplicationKind))
		Expect(k8sClient.Get(ctx, appKey, app)).To(Succeed())
		for _, c := range app.Status.Conditions {
			Expect(c.Type).NotTo(Equal(handoverCondition), "an Application that has not changed mode reports no handover")
		}
		// web-extra stands for a resource the in-process render had and the dispatched one has not.
		extra := &unstructured.Unstructured{}
		extra.SetAPIVersion("v1")
		extra.SetKind("ConfigMap")
		extra.SetNamespace(ns)
		extra.SetName("web-extra")
		extra.SetLabels(map[string]string{oam.LabelAppName: "shop", oam.LabelAppNamespace: ns, oam.LabelAppComponent: "web"})
		inProcess, err := appkeeper.New(ctx, k8sClient, app)
		Expect(err).NotTo(HaveOccurred())
		Expect(inProcess.Dispatch(ctx, []*unstructured.Unstructured{extra}, nil)).To(Succeed())

		By("dispatching it hands the ConfigMap to its Component in place")
		setDispatch(true)
		Eventually(func() string {
			reconcileAll()
			return configMap().Labels[oam.LabelOwnerKind]
		}, "15s", "100ms").Should(Equal(v1alpha1.ComponentKind))
		comp := &v1alpha1.Component{}
		Expect(k8sClient.Get(ctx, compKey, comp)).To(Succeed())
		Expect(comp.Spec.Apply.AdoptFrom).To(Equal("Application/"+ns+"/shop"), "adopts during the handover")
		Expect(configMap().UID).To(Equal(uid))
		Eventually(func() string {
			reconcileAll()
			Expect(k8sClient.Get(ctx, compKey, comp)).To(Succeed())
			return comp.Spec.Apply.AdoptFrom
		}, "15s", "100ms").Should(BeEmpty(), "and not after the Application has forgotten what it handed over")
		Eventually(func() bool { reconcileAll(); return running() }, "15s", "100ms").Should(BeTrue())
		Expect(configMap().UID).To(Equal(uid))
		Expect(configMap().Labels).To(HaveKeyWithValue(oam.LabelOwnerKind, v1alpha1.ComponentKind))
		Expect(post().UID).To(Equal(postUID), "a PostDispatch trait's resource is handed over too")
		By("vela-agent renders the trait as the hub rendered it in process")
		// Owner, revision and render hash follow the handover; the rest is the same render.
		moved := map[string]bool{oam.LabelOwnerKind: true, oam.LabelOwnerName: true, oam.LabelOwnerNamespace: true,
			oam.LabelAppRevision: true, oam.LabelAppRevisionHash: true, apply.LabelRenderHash: true}
		for k, v := range inProcessPost.Labels {
			if !moved[k] {
				Expect(post().Labels).To(HaveKeyWithValue(k, v))
			}
		}
		for k, v := range inProcessPost.Annotations {
			Expect(post().Annotations).To(HaveKeyWithValue(k, v))
		}
		Expect(post().Data).To(Equal(inProcessPost.Data))
		Expect(post().Labels).To(HaveKeyWithValue(oam.LabelOwnerKind, v1alpha1.ComponentKind))
		Expect(kerrors.IsNotFound(k8sClient.Get(ctx, types.NamespacedName{Namespace: ns, Name: "web-extra"}, &corev1.ConfigMap{}))).
			To(BeTrue(), "what no Component carries is collected, as a resource no longer rendered")
		Expect(app.Status.GetCondition(handoverCondition).Status).To(Equal(corev1.ConditionTrue))

		By("dropping the annotation hands it back to the Application in place, and retires the Component")
		setDispatch(false)
		Eventually(func() string {
			_, err := testutil.ReconcileOnceAfterFinalizer(reconciler, reconcile.Request{NamespacedName: appKey})
			Expect(err).NotTo(HaveOccurred())
			Expect(k8sClient.Get(ctx, appKey, app)).To(Succeed())
			return app.Status.GetCondition(handoverCondition).Message
		}, "15s", "100ms").Should(ContainSubstring("vela-agent on cluster local to collect the resources of Component "+ns+"/"+componentName(ns, "shop", "web")),
			"the Application says what the hand-back waits on")
		Eventually(func() bool {
			reconcileAll()
			return kerrors.IsNotFound(k8sClient.Get(ctx, compKey, &v1alpha1.Component{}))
		}, "20s", "100ms").Should(BeTrue())
		Eventually(func() bool { reconcileAll(); return running() }, "15s", "100ms").Should(BeTrue())
		Expect(configMap().UID).To(Equal(uid))
		Expect(configMap().Labels).To(HaveKeyWithValue(oam.LabelOwnerKind, v1beta1.ApplicationKind))
		Expect(configMap().Data).To(HaveKeyWithValue("ready", "yes"))
		Eventually(func() string { reconcileAll(); return post().Labels[oam.LabelOwnerKind] }, "15s", "100ms").
			Should(Equal(v1beta1.ApplicationKind), "a PostDispatch trait's resource comes back too")
		Expect(post().UID).To(Equal(postUID))
		Expect(app.Status.GetCondition(handoverCondition).Status).To(Equal(corev1.ConditionTrue))
	})

	It("finishes handing a component over while its workflow is suspended", func() {
		featuregatetesting.SetFeatureGateDuringTest(GinkgoT(), utilfeature.DefaultFeatureGate, features.EnableComponentDispatch, true)
		pub, priv, err := ed25519.GenerateKey(rand.Reader)
		Expect(err).NotTo(HaveOccurred())
		reconciler.ComponentKeys = newRingKeys(priv)
		DeferCleanup(func() { reconciler.ComponentKeys = nil })
		agent := &component.Reconciler{Client: k8sClient, Keys: component.StaticKeys{"hub-1": pub}, Cluster: "local", Recorder: record.NewFakeRecorder(100)}

		ns := "component-handover-suspended"
		Expect(k8sClient.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}})).Should(SatisfyAny(BeNil(), &util.AlreadyExistMatcher{}))
		def := &v1beta1.ComponentDefinition{}
		defJSON, err := yaml.YAMLToJSON([]byte(dispatchConfigDefinition))
		Expect(err).NotTo(HaveOccurred())
		Expect(json.Unmarshal(defJSON, def)).To(Succeed())
		Expect(k8sClient.Create(ctx, def)).Should(SatisfyAny(BeNil(), &util.AlreadyExistMatcher{}))

		app := &v1beta1.Application{
			ObjectMeta: metav1.ObjectMeta{Name: "shop", Namespace: ns},
			Spec: v1beta1.ApplicationSpec{
				Components: []common.ApplicationComponent{
					{Name: "web", Type: "dispatch-config", Properties: &runtime.RawExtension{Raw: []byte(`{"ready":"yes"}`)}}},
				Workflow: &v1beta1.Workflow{Steps: []wfTypesv1alpha1.WorkflowStep{
					{WorkflowStepBase: wfTypesv1alpha1.WorkflowStepBase{Name: "deploy", Type: "apply-component",
						Properties: &runtime.RawExtension{Raw: []byte(`{"component":"web"}`)}}},
					{WorkflowStepBase: wfTypesv1alpha1.WorkflowStepBase{Name: "gate", Type: "suspend"}},
				}},
			},
		}
		Expect(k8sClient.Create(ctx, app)).To(Succeed())
		appKey := client.ObjectKeyFromObject(app)
		compKey := types.NamespacedName{Namespace: ns, Name: componentName(ns, "shop", "web")}
		reconcileAll := func() {
			_, err := testutil.ReconcileOnceAfterFinalizer(reconciler, reconcile.Request{NamespacedName: appKey})
			Expect(err).NotTo(HaveOccurred())
			_, err = agent.Reconcile(ctx, ctrl.Request{NamespacedName: compKey})
			Expect(err).NotTo(HaveOccurred())
		}
		suspended := func() bool {
			Expect(k8sClient.Get(ctx, appKey, app)).To(Succeed())
			return app.Status.Workflow != nil && app.Status.Workflow.Suspend
		}
		Eventually(func() bool { reconcileAll(); return suspended() }, "15s", "100ms").Should(BeTrue())

		Eventually(func() error {
			Expect(k8sClient.Get(ctx, appKey, app)).To(Succeed())
			metav1.SetMetaDataAnnotation(&app.ObjectMeta, oam.AnnotationDispatch, oam.DispatchComponent)
			return k8sClient.Update(ctx, app)
		}, "5s", "100ms").Should(Succeed())
		comp := &v1alpha1.Component{}
		Eventually(func() string {
			reconcileAll()
			if err := k8sClient.Get(ctx, compKey, comp); err != nil {
				return "no Component yet"
			}
			if comp.Spec.Apply.AdoptFrom != "" {
				return "adopting"
			}
			return "handed over"
		}, "20s", "100ms").Should(Equal("handed over"), "and dispatched again without adoptFrom")
		Expect(suspended()).To(BeTrue(), "while the workflow waits at its gate")
		cm := &corev1.ConfigMap{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: ns, Name: "web"}, cm)).To(Succeed())
		Expect(cm.Labels).To(HaveKeyWithValue(oam.LabelOwnerKind, v1alpha1.ComponentKind))
	})

	It("collects a removed component, and a deleted Application, through signed tombstones", func() {
		featuregatetesting.SetFeatureGateDuringTest(GinkgoT(), utilfeature.DefaultFeatureGate, features.EnableComponentDispatch, true)
		pub, priv, err := ed25519.GenerateKey(rand.Reader)
		Expect(err).NotTo(HaveOccurred())
		keys := componentdispatch.TrustedKeys{"hub-1": pub}
		reconciler.ComponentKeys = newRingKeys(priv)
		DeferCleanup(func() { reconciler.ComponentKeys = nil })
		agent := &component.Reconciler{Client: k8sClient, Keys: component.StaticKeys(keys), Cluster: "local", Recorder: record.NewFakeRecorder(100)}
		DeferCleanup(func(p float64) { resourcekeeper.MarkWithProbability = p }, resourcekeeper.MarkWithProbability)
		resourcekeeper.MarkWithProbability = 1.0

		ns := "component-tombstones"
		Expect(k8sClient.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}})).Should(SatisfyAny(BeNil(), &util.AlreadyExistMatcher{}))
		def := &v1beta1.ComponentDefinition{}
		defJSON, err := yaml.YAMLToJSON([]byte(dispatchConfigDefinition))
		Expect(err).NotTo(HaveOccurred())
		Expect(json.Unmarshal(defJSON, def)).To(Succeed())
		Expect(k8sClient.Create(ctx, def)).Should(SatisfyAny(BeNil(), &util.AlreadyExistMatcher{}))

		appComponent := func(name string) common.ApplicationComponent {
			return common.ApplicationComponent{Name: name, Type: "dispatch-config", Properties: &runtime.RawExtension{Raw: []byte(`{"ready":"yes"}`)}}
		}
		app := &v1beta1.Application{
			ObjectMeta: metav1.ObjectMeta{Name: "shop", Namespace: ns, Annotations: map[string]string{oam.AnnotationDispatch: oam.DispatchComponent}},
			Spec:       v1beta1.ApplicationSpec{Components: []common.ApplicationComponent{appComponent("web"), appComponent("api")}},
		}
		Expect(k8sClient.Create(ctx, app)).To(Succeed())
		appKey := client.ObjectKeyFromObject(app)
		compKey := func(name string) types.NamespacedName {
			return types.NamespacedName{Namespace: ns, Name: componentName(ns, "shop", name)}
		}
		configMapExists := func(name string) bool {
			err := k8sClient.Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, &corev1.ConfigMap{})
			if kerrors.IsNotFound(err) {
				return false
			}
			Expect(err).NotTo(HaveOccurred())
			return true
		}
		// reconcileAll runs the hub, then vela-agent on each named Component.
		reconcileAll := func(components ...string) {
			_, err := testutil.ReconcileOnceAfterFinalizer(reconciler, reconcile.Request{NamespacedName: appKey})
			if err != nil && !kerrors.IsNotFound(err) {
				Expect(err).NotTo(HaveOccurred())
			}
			for _, c := range components {
				_, err := agent.Reconcile(ctx, ctrl.Request{NamespacedName: compKey(c)})
				Expect(err).NotTo(HaveOccurred())
			}
		}
		Eventually(func() common.ApplicationPhase {
			reconcileAll("web", "api")
			current := &v1beta1.Application{}
			Expect(k8sClient.Get(ctx, appKey, current)).To(Succeed())
			return current.Status.Phase
		}, "15s", "100ms").Should(Equal(common.ApplicationRunning))
		Expect(configMapExists("api")).To(BeTrue())

		By("removing a component tombstones its Component, which waits for vela-agent")
		current := &v1beta1.Application{}
		Expect(k8sClient.Get(ctx, appKey, current)).To(Succeed())
		current.Spec.Components = []common.ApplicationComponent{appComponent("web")}
		Expect(k8sClient.Update(ctx, current)).To(Succeed())
		tombstone := &v1alpha1.Component{}
		Eventually(func() bool {
			reconcileAll("web")
			Expect(k8sClient.Get(ctx, compKey("api"), tombstone)).To(Succeed())
			return tombstone.Spec.Delete
		}, "15s", "100ms").Should(BeTrue())
		Expect(keys.Verify(tombstone)).To(Succeed())
		Expect(tombstone.Spec.Revision).To(Equal(int64(2)))
		Expect(tombstone.Spec.Payload).To(BeNil())
		reconcileAll("web")
		Expect(k8sClient.Get(ctx, compKey("api"), tombstone)).To(Succeed(), "not deleted before vela-agent collects")
		Expect(tombstone.DeletionTimestamp).To(BeNil())
		Expect(configMapExists("api")).To(BeTrue())
		Expect(k8sClient.Get(ctx, appKey, current)).To(Succeed())
		var messages []string
		for _, c := range current.Status.Conditions {
			messages = append(messages, c.Message)
		}
		Expect(messages).To(ContainElement(ContainSubstring("Waiting for vela-agent on cluster local to collect the resources of Component " + ns + "/" + componentName(ns, "shop", "api"))))

		By("once vela-agent has collected, the hub deletes the Component")
		Eventually(func() bool {
			reconcileAll("web", "api")
			return kerrors.IsNotFound(k8sClient.Get(ctx, compKey("api"), &v1alpha1.Component{}))
		}, "15s", "100ms").Should(BeTrue())
		Expect(configMapExists("api")).To(BeFalse())
		Expect(configMapExists("web")).To(BeTrue())

		By("deleting the Application collects the rest the same way")
		Expect(k8sClient.Delete(ctx, current)).To(Succeed())
		Eventually(func() bool {
			reconcileAll("web")
			return kerrors.IsNotFound(k8sClient.Get(ctx, appKey, &v1beta1.Application{}))
		}, "20s", "100ms").Should(BeTrue())
		Expect(kerrors.IsNotFound(k8sClient.Get(ctx, compKey("web"), &v1alpha1.Component{}))).To(BeTrue())
		Expect(configMapExists("web")).To(BeFalse())
	})

	It("says when nothing on a cluster applies a Component in time, and carries on when vela-agent does", func() {
		featuregatetesting.SetFeatureGateDuringTest(GinkgoT(), utilfeature.DefaultFeatureGate, features.EnableComponentDispatch, true)
		pub, priv, err := ed25519.GenerateKey(rand.Reader)
		Expect(err).NotTo(HaveOccurred())
		reconciler.ComponentKeys = newRingKeys(priv)
		reconciler.ComponentReconcilerTimeout = time.Nanosecond
		DeferCleanup(func() { reconciler.ComponentKeys, reconciler.ComponentReconcilerTimeout = nil, 0 })
		agent := &component.Reconciler{Client: k8sClient, Keys: component.StaticKeys{"hub-1": pub}, Cluster: "local", Recorder: record.NewFakeRecorder(100)}

		ns := "component-not-applied"
		Expect(k8sClient.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}})).Should(SatisfyAny(BeNil(), &util.AlreadyExistMatcher{}))
		def := &v1beta1.ComponentDefinition{}
		defJSON, err := yaml.YAMLToJSON([]byte(dispatchConfigDefinition))
		Expect(err).NotTo(HaveOccurred())
		Expect(json.Unmarshal(defJSON, def)).To(Succeed())
		Expect(k8sClient.Create(ctx, def)).Should(SatisfyAny(BeNil(), &util.AlreadyExistMatcher{}))

		app := &v1beta1.Application{
			ObjectMeta: metav1.ObjectMeta{Name: "shop", Namespace: ns,
				Annotations: map[string]string{oam.AnnotationDispatch: oam.DispatchComponent}},
			Spec: v1beta1.ApplicationSpec{Components: []common.ApplicationComponent{
				{Name: "web", Type: "dispatch-config", Properties: &runtime.RawExtension{Raw: []byte(`{"ready":"yes"}`)}}}},
		}
		Expect(k8sClient.Create(ctx, app)).To(Succeed())
		appKey := client.ObjectKeyFromObject(app)
		compKey := types.NamespacedName{Namespace: ns, Name: componentName(ns, "shop", "web")}
		reconcileHub := func() {
			_, err := testutil.ReconcileOnceAfterFinalizer(reconciler, reconcile.Request{NamespacedName: appKey})
			Expect(err).NotTo(HaveOccurred())
		}
		// stepMessage is the deploy step's message, after a reconcile of the hub alone.
		stepMessage := func() string {
			reconcileHub()
			Expect(k8sClient.Get(ctx, appKey, app)).To(Succeed())
			if app.Status.Workflow == nil || len(app.Status.Workflow.Steps) == 0 {
				return ""
			}
			return app.Status.Workflow.Steps[0].Message
		}
		serviceMessage := func() string {
			for _, svc := range app.Status.Services {
				if svc.Name == "web" {
					return svc.Message
				}
			}
			return ""
		}

		By("with no vela-agent, the Application says nothing on the cluster has reconciled the Component")
		Eventually(stepMessage, "15s", "200ms").Should(MatchRegexp(
			`^waiting since \S+ for anything on cluster local to reconcile Component ` + ns + "/" + componentName(ns, "shop", "web") + `: is vela-agent running there\?$`))
		Expect(serviceMessage()).To(ContainSubstring("is vela-agent running there?"))
		comp := &v1alpha1.Component{}
		Expect(k8sClient.Get(ctx, compKey, comp)).To(Succeed())
		stamp := comp.Annotations[oam.AnnotationDispatchedAt]
		Expect(stamp).NotTo(BeEmpty())
		time.Sleep(1100 * time.Millisecond) // stamps are to the second
		reconcileHub()
		Expect(k8sClient.Get(ctx, compKey, comp)).To(Succeed())
		Expect(comp.Annotations[oam.AnnotationDispatchedAt]).To(Equal(stamp), "an unchanged revision keeps when it was written")

		By("once vela-agent applies it, the Application carries on by itself")
		Eventually(func() common.ApplicationPhase {
			reconcileHub()
			_, err := agent.Reconcile(ctx, ctrl.Request{NamespacedName: compKey})
			Expect(err).NotTo(HaveOccurred())
			Expect(k8sClient.Get(ctx, appKey, app)).To(Succeed())
			return app.Status.Phase
		}, "15s", "100ms").Should(Equal(common.ApplicationRunning))

		By("a revision vela-agent does not apply is reported with the one it last applied")
		Eventually(func() error {
			Expect(k8sClient.Get(ctx, appKey, app)).To(Succeed())
			app.Spec.Components[0].Properties = &runtime.RawExtension{Raw: []byte(`{"ready":"no"}`)}
			return k8sClient.Update(ctx, app)
		}, "5s", "100ms").Should(Succeed())
		Eventually(stepMessage, "15s", "200ms").Should(MatchRegexp(
			`^waiting since \S+ for vela-agent on cluster local to apply revision 2 of Component ` + ns + "/" + componentName(ns, "shop", "web") + ` \(last applied 1\)$`))
		Expect(serviceMessage()).To(ContainSubstring("(last applied 1)"))
	})

	It("dispatches a changed spec from a deploy step, which then waits on vela-agent", func() {
		featuregatetesting.SetFeatureGateDuringTest(GinkgoT(), utilfeature.DefaultFeatureGate, features.EnableComponentDispatch, true)
		pub, priv, err := ed25519.GenerateKey(rand.Reader)
		Expect(err).NotTo(HaveOccurred())
		reconciler.ComponentKeys = newRingKeys(priv)
		DeferCleanup(func() { reconciler.ComponentKeys = nil })
		agent := &component.Reconciler{Client: k8sClient, Keys: component.StaticKeys{"hub-1": pub}, Cluster: "local", Recorder: record.NewFakeRecorder(100)}

		ns := "component-revert"
		Expect(k8sClient.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}})).Should(SatisfyAny(BeNil(), &util.AlreadyExistMatcher{}))
		def := &v1beta1.ComponentDefinition{}
		defJSON, err := yaml.YAMLToJSON([]byte(dispatchConfigDefinition))
		Expect(err).NotTo(HaveOccurred())
		Expect(json.Unmarshal(defJSON, def)).To(Succeed())
		Expect(k8sClient.Create(ctx, def)).Should(SatisfyAny(BeNil(), &util.AlreadyExistMatcher{}))
		step := &v1beta1.WorkflowStepDefinition{}
		stepJSON, err := yaml.YAMLToJSON([]byte(dispatchDeployStepDefinition))
		Expect(err).NotTo(HaveOccurred())
		Expect(json.Unmarshal(stepJSON, step)).To(Succeed())
		Expect(k8sClient.Create(ctx, step)).Should(SatisfyAny(BeNil(), &util.AlreadyExistMatcher{}))

		app := &v1beta1.Application{
			ObjectMeta: metav1.ObjectMeta{Name: "shop", Namespace: ns,
				Annotations: map[string]string{oam.AnnotationDispatch: oam.DispatchComponent}},
			Spec: v1beta1.ApplicationSpec{Components: []common.ApplicationComponent{
				{Name: "web", Type: "dispatch-config", Properties: &runtime.RawExtension{Raw: []byte(`{"ready":"yes","note":"a"}`)}}},
				Workflow: &v1beta1.Workflow{Steps: []wfTypesv1alpha1.WorkflowStep{
					{WorkflowStepBase: wfTypesv1alpha1.WorkflowStepBase{Name: "deploy", Type: "deploy"}}}},
			},
		}
		Expect(k8sClient.Create(ctx, app)).To(Succeed())
		appKey := client.ObjectKeyFromObject(app)
		compKey := types.NamespacedName{Namespace: ns, Name: componentName(ns, "shop", "web")}
		reconcileHub := func() {
			_, err := testutil.ReconcileOnceAfterFinalizer(reconciler, reconcile.Request{NamespacedName: appKey})
			Expect(err).NotTo(HaveOccurred())
		}
		runningWithAgent := func() {
			Eventually(func() string {
				reconcileHub()
				_, err := agent.Reconcile(ctx, ctrl.Request{NamespacedName: compKey})
				Expect(err).NotTo(HaveOccurred())
				Expect(k8sClient.Get(ctx, appKey, app)).To(Succeed())
				msg := ""
				if app.Status.Workflow != nil && len(app.Status.Workflow.Steps) > 0 {
					msg = string(app.Status.Workflow.Steps[0].Phase) + " " + app.Status.Workflow.Steps[0].Message
				}
				for _, svc := range app.Status.Services {
					msg += " | " + svc.Message
				}
				return string(app.Status.Phase) + ": " + msg
			}, "15s", "100ms").Should(HavePrefix(string(common.ApplicationRunning) + ":"))
		}
		setNote := func(v string) {
			Eventually(func() error {
				Expect(k8sClient.Get(ctx, appKey, app)).To(Succeed())
				app.Spec.Components[0].Properties = &runtime.RawExtension{Raw: []byte(`{"ready":"yes","note":"` + v + `"}`)}
				return k8sClient.Update(ctx, app)
			}, "5s", "100ms").Should(Succeed())
		}
		runningWithAgent()
		applied := &v1alpha1.Component{}
		Expect(k8sClient.Get(ctx, compKey, applied)).To(Succeed())

		By("changing the spec, with no vela-agent to apply it")
		setNote("b")
		for range 10 {
			reconcileHub()
		}
		comp := &v1alpha1.Component{}
		Expect(k8sClient.Get(ctx, compKey, comp)).To(Succeed())
		Expect(comp.Spec.Revision).To(BeNumerically(">", applied.Spec.Revision), "the deploy step dispatches the new spec")
		Expect(k8sClient.Get(ctx, appKey, app)).To(Succeed())
		Expect(app.Status.Phase).NotTo(Equal(common.ApplicationRunning), "nothing has applied it yet")
		Expect(app.Status.Workflow.Steps[0].Phase).NotTo(BeEquivalentTo("succeeded"),
			"the deploy step waits on vela-agent: %s", app.Status.Workflow.Steps[0].Message)
	})
})
