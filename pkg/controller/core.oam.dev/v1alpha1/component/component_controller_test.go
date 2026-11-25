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

package component

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	kerrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/record"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/config"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	"github.com/oam-dev/kubevela/apis/core.oam.dev/v1alpha1"
	"github.com/oam-dev/kubevela/apis/core.oam.dev/v1beta1"
	"github.com/oam-dev/kubevela/pkg/componentdispatch"
	"github.com/oam-dev/kubevela/pkg/oam"
	"github.com/oam-dev/kubevela/pkg/utils/common"
)

var (
	testClient client.Client
	testConfig *rest.Config
)

func TestMain(m *testing.M) {
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		fmt.Fprintln(os.Stderr, "KUBEBUILDER_ASSETS is not set: run through `make test`, or export the path from `setup-envtest use -p path`")
		os.Exit(1)
	}
	crds := filepath.Join("..", "..", "..", "..", "..", "charts", "vela-core", "crds")
	env := &envtest.Environment{
		CRDInstallOptions: envtest.CRDInstallOptions{Paths: []string{
			filepath.Join(crds, "core.oam.dev_components.yaml"),
			filepath.Join(crds, "core.oam.dev_resourcetrackers.yaml"),
		}},
		ErrorIfCRDPathMissing: true,
	}
	cfg, err := env.Start()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	testConfig = cfg
	testClient, err = client.New(cfg, client.Options{Scheme: common.Scheme})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	code := m.Run()
	_ = env.Stop()
	os.Exit(code)
}

// hub stands in for the application-controller: it packages, signs and writes Components.
type hub struct {
	t       *testing.T
	ns      string
	signer  componentdispatch.Signer
	keys    componentdispatch.TrustedKeys
	reconc  *Reconciler
	events  *record.FakeRecorder
	cluster string
}

func newHub(t *testing.T) *hub {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	ns := strings.ToLower(strings.NewReplacer("/", "-", "_", "-").Replace(t.Name()))
	if len(ns) > 60 {
		ns = ns[:60]
	}
	ns = strings.Trim(ns, "-")
	require.NoError(t, testClient.Create(context.Background(), &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}))
	keys := componentdispatch.TrustedKeys{"hub-1": pub}
	events := record.NewFakeRecorder(100)
	return &hub{
		t: t, ns: ns, cluster: "local",
		signer: componentdispatch.Signer{KeyID: "hub-1", Key: priv},
		keys:   keys,
		events: events,
		reconc: &Reconciler{Client: testClient, Keys: StaticKeys(keys), Cluster: "local", Recorder: events},
	}
}

type rendered struct {
	obj   *unstructured.Unstructured
	role  string
	trait string
}

func configMap(ns, name string, data map[string]interface{}) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "v1", "kind": "ConfigMap",
		"metadata": map[string]interface{}{"name": name, "namespace": ns},
		"data":     data,
	}}
}

// component packages the resources as revision rev, signs it, and returns it unwritten.
func (h *hub) component(name string, rev int64, resources []rendered, mutate func(*v1alpha1.Component)) (*v1alpha1.Component, *corev1.Secret) {
	objs := make([]*unstructured.Unstructured, 0, len(resources))
	index := make([]v1alpha1.ComponentResource, 0, len(resources))
	for _, r := range resources {
		entry, err := componentdispatch.IndexEntry(r.obj)
		require.NoError(h.t, err)
		entry.Role, entry.Definition = r.role, v1alpha1.ComponentResourceDefinition{Kind: v1alpha1.DefinitionComponent, Type: "webservice"}
		if r.trait != "" {
			entry.Definition = v1alpha1.ComponentResourceDefinition{Kind: v1alpha1.DefinitionTrait, Type: r.trait}
		}
		objs = append(objs, r.obj)
		index = append(index, entry)
	}
	data, digest, err := componentdispatch.EncodePayload(objs)
	require.NoError(h.t, err)
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: name + "-payload", Namespace: h.ns},
		Data:       map[string][]byte{componentdispatch.PayloadKey: data},
	}
	c := &v1alpha1.Component{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: h.ns},
		Spec: v1alpha1.ComponentSpec{
			Target:    v1alpha1.ComponentTarget{Cluster: h.cluster},
			Revision:  rev,
			Resources: index,
			Health: v1alpha1.ComponentHealth{
				Context: map[string]string{"name": name},
				Workload: &v1alpha1.ComponentHealthSubject{HealthSnippets: v1alpha1.HealthSnippets{
					HealthPolicy: `isHealth: context.output.data.ready == "yes"`,
					CustomStatus: `message: "ready: \(context.output.data.ready)"`,
				}},
			},
			Observe: []v1alpha1.ComponentObservation{{Resource: "output", Name: "ready", Path: ".data.ready"}},
			Payload: &v1alpha1.ComponentPayload{SecretRef: v1alpha1.ComponentSecretReference{Name: secret.Name}, Format: componentdispatch.PayloadFormat, SHA256: digest},
		},
	}
	if mutate != nil {
		mutate(c)
	}
	require.NoError(h.t, h.signer.Sign(c))
	return c, secret
}

// dispatch writes the payload Secret, then the Component, as the hub does.
func (h *hub) dispatch(c *v1alpha1.Component, secret *corev1.Secret) {
	ctx := context.Background()
	existing := &corev1.Secret{}
	if err := testClient.Get(ctx, client.ObjectKeyFromObject(secret), existing); err == nil {
		existing.Data = secret.Data
		require.NoError(h.t, testClient.Update(ctx, existing))
	} else {
		require.True(h.t, kerrors.IsNotFound(err))
		require.NoError(h.t, testClient.Create(ctx, secret))
	}
	current := &v1alpha1.Component{}
	if err := testClient.Get(ctx, client.ObjectKeyFromObject(c), current); err == nil {
		current.Spec = c.Spec
		require.NoError(h.t, testClient.Update(ctx, current))
		return
	}
	require.NoError(h.t, testClient.Create(ctx, c))
}

func (h *hub) reconcile(name string) ctrl.Result {
	res, err := h.reconc.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Namespace: h.ns, Name: name}})
	require.NoError(h.t, err)
	return res
}

// settle reconciles until nothing is left to do now: requeues of a second or less (garbage
// collection still running) are followed, longer ones are left to time.
// settle reconciles name until vela-agent has nothing left part-way: no collection or
// tombstone still running.
func (h *hub) settle(name string) ctrl.Result {
	for i := 0; i < 10; i++ {
		res := h.reconcile(name)
		if res.Requeue {
			continue
		}
		c := &v1alpha1.Component{}
		if err := testClient.Get(context.Background(), types.NamespacedName{Namespace: h.ns, Name: name}, c); err != nil {
			return res
		}
		if r := meta.FindStatusCondition(c.Status.Conditions, ConditionReady); r == nil || (r.Reason != ReasonCollecting && r.Reason != ReasonDeleting) {
			return res
		}
	}
	h.t.Fatalf("component %s did not settle", name)
	return ctrl.Result{}
}

func (h *hub) get(name string) *v1alpha1.Component {
	c := &v1alpha1.Component{}
	require.NoError(h.t, testClient.Get(context.Background(), types.NamespacedName{Namespace: h.ns, Name: name}, c))
	return c
}

func (h *hub) configMap(name string) *corev1.ConfigMap {
	cm := &corev1.ConfigMap{}
	err := testClient.Get(context.Background(), types.NamespacedName{Namespace: h.ns, Name: name}, cm)
	if kerrors.IsNotFound(err) {
		return nil
	}
	require.NoError(h.t, err)
	return cm
}

func (h *hub) condition(name, condType string) *metav1.Condition {
	return meta.FindStatusCondition(h.get(name).Status.Conditions, condType)
}

func (h *hub) expectEvent(substr string) {
	for {
		select {
		case e := <-h.events.Events:
			if strings.Contains(e, substr) {
				return
			}
		default:
			h.t.Fatalf("no event containing %q", substr)
		}
	}
}

func (h *hub) trackers(name string) []v1beta1.ResourceTracker {
	rts := &v1beta1.ResourceTrackerList{}
	require.NoError(h.t, testClient.List(context.Background(), rts, client.MatchingLabels{
		oam.LabelOwnerKind: v1alpha1.ComponentKind, oam.LabelOwnerNamespace: h.ns, oam.LabelOwnerName: name,
	}))
	return rts.Items
}

func ready(h *hub, value string) []rendered {
	return []rendered{{obj: configMap(h.ns, "settings", map[string]interface{}{"ready": value}), role: "output"}}
}

func TestAppliesAndReportsHealth(t *testing.T) {
	h := newHub(t)
	h.dispatch(h.component("backend", 1, ready(h, "yes"), nil))
	require.Equal(t, h.reconc.resync(), h.settle("backend").RequeueAfter, "a healthy Component waits for the resync")

	cm := h.configMap("settings")
	require.NotNil(t, cm)
	require.Equal(t, "yes", cm.Data["ready"])
	require.Equal(t, v1alpha1.ComponentKind, cm.Labels[oam.LabelOwnerKind])
	require.Equal(t, "backend", cm.Labels[oam.LabelOwnerName])

	c := h.get("backend")
	require.Contains(t, c.Finalizers, Finalizer)
	require.EqualValues(t, 1, c.Status.ObservedRevision)
	require.Equal(t, metav1.ConditionTrue, meta.FindStatusCondition(c.Status.Conditions, ConditionVerified).Status)
	require.Equal(t, metav1.ConditionTrue, meta.FindStatusCondition(c.Status.Conditions, ConditionApplied).Status)
	require.Equal(t, metav1.ConditionTrue, meta.FindStatusCondition(c.Status.Conditions, ConditionHealthy).Status)
	require.NotNil(t, c.Status.Health)
	require.True(t, c.Status.Health.Healthy)
	require.Equal(t, "ready: yes", c.Status.Health.Message)
	require.JSONEq(t, `{"ready":"yes"}`, string(c.Status.Observed["output"].Raw))
}

func TestReportsUnhealthy(t *testing.T) {
	h := newHub(t)
	h.dispatch(h.component("backend", 1, ready(h, "no"), nil))
	res := h.settle("backend")
	require.Less(t, res.RequeueAfter, h.reconc.resync(), "an unhealthy Component is checked again soon, not at the next resync")

	c := h.get("backend")
	require.False(t, c.Status.Health.Healthy)
	require.Equal(t, metav1.ConditionFalse, meta.FindStatusCondition(c.Status.Conditions, ConditionHealthy).Status)
	require.Equal(t, metav1.ConditionTrue, meta.FindStatusCondition(c.Status.Conditions, ConditionApplied).Status, "applied, just not healthy")
}

func TestRefusesWithoutApplying(t *testing.T) {
	for name, tc := range map[string]struct {
		prepare func(h *hub) (*v1alpha1.Component, *corev1.Secret)
		reason  string
	}{
		"tampered after signing": {
			prepare: func(h *hub) (*v1alpha1.Component, *corev1.Secret) {
				c, s := h.component("backend", 1, ready(h, "yes"), nil)
				c.Spec.Health.Workload.HealthPolicy = "isHealth: true"
				return c, s
			},
			reason: ReasonBadSignature,
		},
		"signed by an untrusted key": {
			prepare: func(h *hub) (*v1alpha1.Component, *corev1.Secret) {
				_, other, err := ed25519.GenerateKey(rand.Reader)
				require.NoError(h.t, err)
				h.signer = componentdispatch.Signer{KeyID: "intruder", Key: other}
				return h.component("backend", 1, ready(h, "yes"), nil)
			},
			reason: ReasonUntrustedKey,
		},
		"addressed to another cluster": {
			prepare: func(h *hub) (*v1alpha1.Component, *corev1.Secret) {
				h.cluster = "prod-use1"
				return h.component("backend", 1, ready(h, "yes"), nil)
			},
			reason: ReasonWrongCluster,
		},
		"payload edited after signing": {
			prepare: func(h *hub) (*v1alpha1.Component, *corev1.Secret) {
				c, s := h.component("backend", 1, ready(h, "yes"), nil)
				_, other := h.component("other", 1, ready(h, "edited"), nil)
				s.Data = other.Data
				return c, s
			},
			reason: ReasonPayloadMismatch,
		},
		"a resource routed to another cluster": {
			prepare: func(h *hub) (*v1alpha1.Component, *corev1.Secret) {
				resources := ready(h, "yes")
				resources[0].obj.SetLabels(map[string]string{oam.LabelAppCluster: "prod-use1"})
				return h.component("backend", 1, resources, nil)
			},
			reason: ReasonPayloadMismatch,
		},
	} {
		t.Run(name, func(t *testing.T) {
			h := newHub(t)
			h.dispatch(tc.prepare(h))
			h.settle("backend")

			require.Nil(t, h.configMap("settings"), "nothing applied")
			cond := h.condition("backend", ConditionVerified)
			require.NotNil(t, cond)
			require.Equal(t, metav1.ConditionFalse, cond.Status)
			require.Equal(t, tc.reason, cond.Reason)
			require.Zero(t, h.get("backend").Status.ObservedRevision)
			h.expectEvent(tc.reason)
		})
	}
}

func TestRefusesAnOlderRevision(t *testing.T) {
	h := newHub(t)
	old, oldSecret := h.component("backend", 1, ready(h, "old"), nil)
	h.dispatch(h.component("backend", 2, ready(h, "new"), nil))
	h.settle("backend")

	// replay revision 1, validly signed, and wipe the status that recorded revision 2
	h.dispatch(old, oldSecret)
	c := h.get("backend")
	c.Status = v1alpha1.ComponentStatus{}
	require.NoError(t, testClient.Status().Update(context.Background(), c))
	h.settle("backend")

	require.Equal(t, "new", h.configMap("settings").Data["ready"], "the replay was not applied")
	cond := h.condition("backend", ConditionVerified)
	require.Equal(t, metav1.ConditionFalse, cond.Status)
	require.Equal(t, ReasonStaleRevision, cond.Reason)
}

func TestCollectsAResourceDroppedBetweenRevisions(t *testing.T) {
	h := newHub(t)
	both := append(ready(h, "yes"), rendered{obj: configMap(h.ns, "extra", nil), role: "outputs.extra"})
	h.dispatch(h.component("backend", 1, both, nil))
	h.settle("backend")
	require.NotNil(t, h.configMap("extra"))

	h.dispatch(h.component("backend", 2, ready(h, "yes"), nil))
	h.settle("backend")
	require.Nil(t, h.configMap("extra"))
	require.NotNil(t, h.configMap("settings"))
	require.EqualValues(t, 2, h.get("backend").Status.ObservedRevision)
}

func TestReportsTheResourcesItApplied(t *testing.T) {
	webservice := v1alpha1.ComponentResourceDefinition{Kind: v1alpha1.DefinitionComponent, Type: "webservice"}
	h := newHub(t)
	both := append(ready(h, "yes"), rendered{obj: configMap(h.ns, "extra", nil), role: "outputs.extra", trait: "gateway"})
	h.dispatch(h.component("backend", 1, both, nil))
	h.settle("backend")
	require.ElementsMatch(t, []v1alpha1.ComponentAppliedResource{
		{APIVersion: "v1", Kind: "ConfigMap", Namespace: h.ns, Name: "settings", Role: "output", Definition: webservice},
		{APIVersion: "v1", Kind: "ConfigMap", Namespace: h.ns, Name: "extra", Role: "outputs.extra", Definition: v1alpha1.ComponentResourceDefinition{Kind: v1alpha1.DefinitionTrait, Type: "gateway"}},
	}, h.get("backend").Status.Resources)

	h.dispatch(h.component("backend", 2, ready(h, "yes"), nil))
	h.settle("backend")
	require.Equal(t, []v1alpha1.ComponentAppliedResource{
		{APIVersion: "v1", Kind: "ConfigMap", Namespace: h.ns, Name: "settings", Role: "output", Definition: webservice},
	}, h.get("backend").Status.Resources, "a resource the new revision dropped is no longer reported")

	h.dispatch(h.component("backend", 3, nil, func(c *v1alpha1.Component) {
		c.Spec.Delete = true
		c.Spec.Payload = nil
		c.Spec.Health = v1alpha1.ComponentHealth{}
		c.Spec.Observe = nil
	}))
	h.settle("backend")
	require.Empty(t, h.get("backend").Status.Resources, "nothing is reported once a tombstone collects it all")
}

func TestCorrectsDrift(t *testing.T) {
	h := newHub(t)
	h.dispatch(h.component("backend", 1, ready(h, "yes"), nil))
	h.settle("backend")

	cm := h.configMap("settings")
	cm.Data["ready"] = "tampered"
	require.NoError(t, testClient.Update(context.Background(), cm))
	h.settle("backend")
	require.Equal(t, "yes", h.configMap("settings").Data["ready"])
}

func TestABareDeleteLeavesResourcesInPlace(t *testing.T) {
	h := newHub(t)
	h.dispatch(h.component("backend", 1, ready(h, "yes"), nil))
	h.settle("backend")
	require.NotEmpty(t, h.trackers("backend"))

	require.NoError(t, testClient.Delete(context.Background(), h.get("backend")))
	h.settle("backend")

	err := testClient.Get(context.Background(), types.NamespacedName{Namespace: h.ns, Name: "backend"}, &v1alpha1.Component{})
	require.True(t, kerrors.IsNotFound(err), "the finalizer was released")
	cm := h.configMap("settings")
	require.NotNil(t, cm, "resources stay without a signed tombstone")
	require.Equal(t, "backend", cm.Labels[oam.LabelOwnerName], "still marked as the Component's")
	require.Empty(t, h.trackers("backend"), "trackers go, so the hub can recreate the Component")
	h.expectEvent("settings")

	// the hub recreates it: the new Component takes the resources back
	h.dispatch(h.component("backend", 2, ready(h, "yes"), nil))
	h.settle("backend")
	require.Equal(t, metav1.ConditionTrue, h.condition("backend", ConditionApplied).Status)
}

func TestATombstoneCollectsEverything(t *testing.T) {
	h := newHub(t)
	h.dispatch(h.component("backend", 1, ready(h, "yes"), nil))
	h.settle("backend")

	h.dispatch(h.component("backend", 2, nil, func(c *v1alpha1.Component) {
		c.Spec.Delete = true
		c.Spec.Payload = nil
		c.Spec.Health = v1alpha1.ComponentHealth{}
		c.Spec.Observe = nil
	}))
	h.settle("backend")
	require.Nil(t, h.configMap("settings"), "collected on the tombstone, before the delete")

	require.NoError(t, testClient.Delete(context.Background(), h.get("backend")))
	h.settle("backend")
	err := testClient.Get(context.Background(), types.NamespacedName{Namespace: h.ns, Name: "backend"}, &v1alpha1.Component{})
	require.True(t, kerrors.IsNotFound(err))
	require.Empty(t, h.trackers("backend"))
}

// The hub deletes a Component once its tombstone is observed, so a tombstone is observed only
// when everything has been collected.
func TestATombstoneIsObservedOnceCollected(t *testing.T) {
	ctx := context.Background()
	h := newHub(t)
	h.dispatch(h.component("backend", 1, ready(h, "yes"), nil))
	h.settle("backend")
	cm := h.configMap("settings")
	cm.Finalizers = append(cm.Finalizers, "example.com/hold")
	require.NoError(t, testClient.Update(ctx, cm))

	h.dispatch(h.component("backend", 2, nil, func(c *v1alpha1.Component) {
		c.Spec.Delete = true
		c.Spec.Payload = nil
		c.Spec.Health = v1alpha1.ComponentHealth{}
		c.Spec.Observe = nil
	}))
	for i := 0; i < 3; i++ {
		h.reconcile("backend")
	}
	require.EqualValues(t, 1, h.get("backend").Status.ObservedRevision, "not observed while a resource is still being deleted")
	require.Equal(t, "Deleting", h.condition("backend", ConditionApplied).Reason)

	cm = h.configMap("settings")
	cm.Finalizers = nil
	require.NoError(t, testClient.Update(ctx, cm))
	h.settle("backend")
	require.EqualValues(t, 2, h.get("backend").Status.ObservedRevision)
	require.Equal(t, "Deleted", h.condition("backend", ConditionApplied).Reason)
}

func TestObservesNestedFields(t *testing.T) {
	h := newHub(t)
	h.dispatch(h.component("backend", 1, ready(h, "yes"), func(c *v1alpha1.Component) {
		c.Spec.Observe = append(c.Spec.Observe,
			v1alpha1.ComponentObservation{Resource: "output", Name: "name", Path: ".metadata.name"},
			v1alpha1.ComponentObservation{Resource: "output", Name: "missing", Path: ".status.nothing"},
		)
	}))
	h.settle("backend")
	var got map[string]interface{}
	require.NoError(t, json.Unmarshal(h.get("backend").Status.Observed["output"].Raw, &got))
	require.Equal(t, map[string]interface{}{"ready": "yes", "name": "settings"}, got, "a missing field is left out")
}

func TestObservesAWholeResource(t *testing.T) {
	h := newHub(t)
	h.dispatch(h.component("backend", 1, ready(h, "yes"), func(c *v1alpha1.Component) {
		c.Spec.Observe = []v1alpha1.ComponentObservation{{Resource: "output", Name: "all", Path: ""}}
	}))
	h.settle("backend")
	var got map[string]map[string]interface{}
	require.NoError(t, json.Unmarshal(h.get("backend").Status.Observed["output"].Raw, &got))
	require.Equal(t, map[string]interface{}{"ready": "yes"}, got["all"]["data"])
	require.NotContains(t, got["all"]["metadata"], "managedFields")
}

// creds is a Secret output whose password is observed as sensitive.
func creds(h *hub) func(*v1alpha1.Component) {
	return func(c *v1alpha1.Component) {
		c.Spec.Observe = []v1alpha1.ComponentObservation{
			{Resource: "output", Name: "ready", Path: ".data.ready"},
			{Resource: "outputs.creds", Name: "password", Path: ".data.password", Sensitive: true},
		}
	}
}

func withCreds(h *hub) []rendered {
	secret := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "v1", "kind": "Secret",
		"metadata": map[string]interface{}{"name": "creds", "namespace": h.ns},
		"data":     map[string]interface{}{"password": "czNjcmV0"},
	}}
	return append(ready(h, "yes"), rendered{obj: secret, role: "outputs.creds"})
}

func TestSealsSensitiveObservations(t *testing.T) {
	h := newHub(t)
	ring := &componentdispatch.KeyRing{Active: h.signer.KeyID, Keys: map[string]ed25519.PrivateKey{h.signer.KeyID: h.signer.Key}}
	h.reconc.Keys = RingKeys{Ring: ring}
	h.dispatch(h.component("backend", 1, withCreds(h), creds(h)))
	h.settle("backend")

	status := h.get("backend").Status
	require.JSONEq(t, `{"ready":"yes"}`, string(status.Observed["output"].Raw))
	require.NotContains(t, status.Observed, "outputs.creds", "a sensitive value is never in plain text")
	require.Equal(t, h.signer.KeyID, status.SealedWith)
	sealed := status.SealedObserved["outputs.creds"]["password"]
	require.NotEmpty(t, sealed)
	require.NotContains(t, sealed, "czNjcmV0")
	opened, err := ring.Open(status.SealedWith, sealed)
	require.NoError(t, err)
	require.Equal(t, `"czNjcmV0"`, string(opened))
}

func TestLeavesOutASensitiveValueItCannotSeal(t *testing.T) {
	h := newHub(t)
	h.dispatch(h.component("backend", 1, withCreds(h), creds(h)))
	h.settle("backend")

	status := h.get("backend").Status
	require.JSONEq(t, `{"ready":"yes"}`, string(status.Observed["output"].Raw))
	require.Empty(t, status.SealedObserved)
	require.Empty(t, status.SealedWith)
	h.expectEvent("CannotSeal")
}

// Handover: the first Component of an Application names it in adoptFrom, inside the signature.
func TestAdoptsResourcesFromTheNamedOwner(t *testing.T) {
	h := newHub(t)
	existing := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "settings", Namespace: h.ns, Labels: map[string]string{
		oam.LabelOwnerKind: "Application", oam.LabelOwnerNamespace: h.ns, oam.LabelOwnerName: "shop",
	}}, Data: map[string]string{"ready": "yes"}}
	require.NoError(t, testClient.Create(context.Background(), existing))

	h.dispatch(h.component("backend", 1, ready(h, "yes"), nil))
	_, err := h.reconc.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Namespace: h.ns, Name: "backend"}})
	require.ErrorContains(t, err, "Application/"+h.ns+"/shop", "not adopted without adoptFrom")
	require.Equal(t, metav1.ConditionFalse, h.condition("backend", ConditionApplied).Status)

	h.dispatch(h.component("backend", 2, ready(h, "yes"), func(c *v1alpha1.Component) {
		c.Spec.Apply.AdoptFrom = "Application/" + h.ns + "/shop"
	}))
	h.settle("backend")
	require.Equal(t, metav1.ConditionTrue, h.condition("backend", ConditionApplied).Status)
	cm := h.configMap("settings")
	require.Equal(t, existing.UID, cm.UID, "adopted in place")
	require.Equal(t, v1alpha1.ComponentKind, cm.Labels[oam.LabelOwnerKind])
}

// A Component refused because the hub's key is not trusted yet is applied once the trust Secret
// arrives, not at the next resync: a cluster just joined, or a rotation the push has not reached.
func TestAppliesOnceTheHubsKeyIsTrusted(t *testing.T) {
	h := newHub(t)
	mgr, err := ctrl.NewManager(testConfig, ctrl.Options{
		Scheme:                 common.Scheme,
		Metrics:                metricsserver.Options{BindAddress: "0"},
		HealthProbeBindAddress: "0",
		Controller:             config.Controller{SkipNameValidation: ptr.To(true)},
	})
	require.NoError(t, err)
	r := &Reconciler{Client: mgr.GetClient(), Keys: SecretKeySource{Reader: mgr.GetAPIReader(), Namespace: h.ns},
		TrustNamespace: h.ns, Cluster: "local", Recorder: h.events, Resync: time.Hour}
	require.NoError(t, r.SetupWithManager(mgr))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = mgr.Start(ctx) }()

	h.dispatch(h.component("backend", 1, ready(h, "yes"), nil))
	require.Eventually(t, func() bool {
		cond := h.condition("backend", ConditionVerified)
		return cond != nil && cond.Reason == ReasonUntrustedKey
	}, 10*time.Second, 100*time.Millisecond, "refused while the hub's key is unknown here")

	ring := &componentdispatch.KeyRing{Active: h.signer.KeyID, Keys: map[string]ed25519.PrivateKey{h.signer.KeyID: h.signer.Key}}
	require.NoError(t, testClient.Create(ctx, &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Namespace: h.ns, Name: componentdispatch.TrustSecret},
		Data:       componentdispatch.TrustSecretData(ring),
	}))
	require.Eventually(t, func() bool {
		cond := h.condition("backend", ConditionVerified)
		return cond != nil && cond.Status == metav1.ConditionTrue
	}, 10*time.Second, 100*time.Millisecond, "applied once the key is trusted")
	require.Equal(t, "yes", h.configMap("settings").Data["ready"])
}

// unreadableKeys is a trust Secret the API server cannot return just now.
type unreadableKeys struct{}

func (unreadableKeys) TrustedKeys(context.Context) (componentdispatch.TrustedKeys, error) {
	return nil, errors.New("the server was unable to return a response in the time allotted")
}

func (unreadableKeys) SealingKeys(context.Context) (componentdispatch.SealingKeys, error) {
	return componentdispatch.SealingKeys{}, nil
}

// A tombstoned Component deleted while the trusted keys cannot be read waits for them: it is
// not a bare delete, which would leave its resources behind with nothing to collect them.
func TestATombstoneWaitsForTheKeysWhenDeleted(t *testing.T) {
	h := newHub(t)
	h.dispatch(h.component("backend", 1, ready(h, "yes"), nil))
	h.settle("backend")
	h.dispatch(h.component("backend", 2, nil, func(c *v1alpha1.Component) {
		c.Spec.Delete = true
		c.Spec.Payload = nil
		c.Spec.Health = v1alpha1.ComponentHealth{}
		c.Spec.Observe = nil
	}))
	require.NoError(t, testClient.Delete(context.Background(), h.get("backend")))

	h.reconc.Keys = unreadableKeys{}
	_, err := h.reconc.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Namespace: h.ns, Name: "backend"}})
	require.Error(t, err, "retried, not treated as unsigned")
	require.NotEmpty(t, h.trackers("backend"), "nothing is forgotten while the keys are unknown")
	require.Contains(t, h.get("backend").Finalizers, Finalizer)

	h.reconc.Keys = StaticKeys(h.keys)
	h.settle("backend")
	require.Nil(t, h.configMap("settings"), "collected through the tombstone once the keys are read")
}

// A tracker left by an earlier Component of the same name is reported on the Component, and
// never holds up its deletion.
func TestReportsATrackerOfAnEarlierComponent(t *testing.T) {
	h := newHub(t)
	h.dispatch(h.component("backend", 1, ready(h, "yes"), nil))
	h.settle("backend")
	stale := h.trackers("backend")
	require.NotEmpty(t, stale)

	// The finalizer is taken off by hand, and the hub creates the Component again.
	c := h.get("backend")
	c.Finalizers = nil
	require.NoError(t, testClient.Update(context.Background(), c))
	require.NoError(t, testClient.Delete(context.Background(), c))
	h.dispatch(h.component("backend", 2, ready(h, "yes"), nil))

	h.settle("backend")
	applied := h.condition("backend", ConditionApplied)
	require.NotNil(t, applied)
	require.Equal(t, ReasonStaleTracker, applied.Reason)
	require.Contains(t, applied.Message, stale[0].Name)

	require.NoError(t, testClient.Delete(context.Background(), h.get("backend")))
	h.settle("backend")
	err := testClient.Get(context.Background(), types.NamespacedName{Namespace: h.ns, Name: "backend"}, &v1alpha1.Component{})
	require.True(t, kerrors.IsNotFound(err), "deleted despite the stale tracker")
	require.NotEmpty(t, h.trackers("backend"), "the stale tracker is not this Component's to drop")
}

// staleSecrets is a cache that has not seen the latest write to any Secret yet.
type staleSecrets struct {
	client.Client
	seen map[types.NamespacedName][]byte
}

func (s staleSecrets) Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	if err := s.Client.Get(ctx, key, obj, opts...); err != nil {
		return err
	}
	if secret, ok := obj.(*corev1.Secret); ok {
		if old, ok := s.seen[key]; ok {
			secret.Data[componentdispatch.PayloadKey] = old
		}
	}
	return nil
}

// A new revision is applied from the payload written just before it, however far behind the
// cache is, and a payload that does not match yet is looked at again soon.
func TestReadsThePayloadPastTheCache(t *testing.T) {
	h := newHub(t)
	h.dispatch(h.component("backend", 1, ready(h, "yes"), nil))
	h.settle("backend")
	first := &corev1.Secret{}
	require.NoError(t, testClient.Get(context.Background(), types.NamespacedName{Namespace: h.ns, Name: "backend-payload"}, first))

	h.reconc.Client = staleSecrets{Client: testClient, seen: map[types.NamespacedName][]byte{
		{Namespace: h.ns, Name: "backend-payload"}: first.Data[componentdispatch.PayloadKey],
	}}
	h.reconc.Reader = testClient
	h.dispatch(h.component("backend", 2, ready(h, "no"), nil))
	h.settle("backend")
	require.Equal(t, "no", h.configMap("settings").Data["ready"])

	// A Component whose payload write has not landed is refused, and checked again well
	// before the resync.
	c, s := h.component("backend", 3, ready(h, "later"), nil)
	s.Data = first.Data
	h.dispatch(c, s)
	res := h.reconcile("backend")
	require.Equal(t, ReasonPayloadMismatch, h.condition("backend", ConditionVerified).Reason)
	require.Less(t, res.RequeueAfter, h.reconc.resync())
}

// announce has vela-agent render a PostDispatch trait: a ConfigMap reading the workload.
func announce(h *hub, template string) func(*v1alpha1.Component) {
	return func(c *v1alpha1.Component) {
		ctxData, err := json.Marshal(map[string]interface{}{"name": "settings", "namespace": h.ns, "appName": "shop", "appRevision": "shop-v1"})
		require.NoError(h.t, err)
		c.Spec.PostRender = &v1alpha1.ComponentPostRender{
			Context: &runtime.RawExtension{Raw: ctxData},
			Traits: []v1alpha1.ComponentPostRenderTrait{{Type: "announce",
				Templates: []v1alpha1.ComponentTemplate{{Name: "announce", Template: template}},
				Labels:    map[string]string{"example.com/contract": "yes"}}},
		}
		c.Spec.Health.Traits = []v1alpha1.ComponentTraitHealth{{Type: "announce", ComponentHealthSubject: v1alpha1.ComponentHealthSubject{
			HealthSnippets: v1alpha1.HealthSnippets{HealthPolicy: `isHealth: context.outputs.notice.data.seen == "yes"`}}}}
	}
}

const announceTemplate = `
outputs: notice: {
	apiVersion: "v1"
	kind:       "ConfigMap"
	metadata: name: "notice"
	data: seen: context.output.data.ready
}
`

func TestPostRendersOnceTheWorkloadIsHealthy(t *testing.T) {
	h := newHub(t)
	h.dispatch(h.component("backend", 1, ready(h, "no"), announce(h, announceTemplate)))
	h.settle("backend")
	require.Nil(t, h.configMap("notice"), "nothing is rendered against an unhealthy workload")
	require.False(t, h.get("backend").Status.Health.Healthy)

	h.dispatch(h.component("backend", 2, ready(h, "yes"), announce(h, announceTemplate)))
	h.settle("backend")
	notice := h.configMap("notice")
	require.NotNil(t, notice)
	require.Equal(t, map[string]string{"seen": "yes"}, notice.Data)
	require.Equal(t, "announce", notice.Labels[oam.TraitTypeLabel])
	require.Equal(t, "yes", notice.Labels["example.com/contract"])

	comp := h.get("backend")
	require.Contains(t, comp.Status.Resources, v1alpha1.ComponentAppliedResource{APIVersion: "v1", Kind: "ConfigMap", Namespace: h.ns, Name: "notice",
		Role: "outputs.notice", Definition: v1alpha1.ComponentResourceDefinition{Kind: v1alpha1.DefinitionTrait, Type: "announce"}})
	require.True(t, comp.Status.Health.Healthy, "the trait's health reads what vela-agent rendered: %s", comp.Status.Health.Message)
	require.Equal(t, metav1.ConditionTrue, h.condition("backend", ConditionPostRendered).Status)

	cm := h.configMap("notice")
	cm.Data["seen"] = "tampered"
	require.NoError(t, testClient.Update(context.Background(), cm))
	h.settle("backend")
	require.Equal(t, "yes", h.configMap("notice").Data["seen"], "drift on a rendered trait is corrected like the payload's")
}

func TestRefusesAPostRenderItCannotRender(t *testing.T) {
	h := newHub(t)
	h.dispatch(h.component("backend", 1, ready(h, "yes"), announce(h, `
import "vela/kube"

outputs: notice: {}
`)))
	h.settle("backend")
	require.NotNil(t, h.configMap("settings"), "the workload is applied regardless")
	cond := h.condition("backend", ConditionPostRendered)
	require.Equal(t, metav1.ConditionFalse, cond.Status)
	require.Contains(t, cond.Message, `imports "vela/kube"`)
	require.False(t, h.get("backend").Status.Health.Healthy)
	h.expectEvent("vela/kube")
}

func TestRenderedWorkloadCarriesTheLiveStatus(t *testing.T) {
	rendered := &unstructured.Unstructured{Object: map[string]interface{}{"apiVersion": "apps/v1", "kind": "Deployment",
		"metadata": map[string]interface{}{"name": "web"}, "spec": map[string]interface{}{"replicas": int64(2)}}}
	live := rendered.DeepCopy()
	live.Object["spec"] = map[string]interface{}{"replicas": int64(5)}
	live.Object["status"] = map[string]interface{}{"readyReplicas": int64(2)}
	got := withLiveStatus(rendered, live)
	require.Equal(t, map[string]interface{}{"replicas": int64(2)}, got.Object["spec"], "the spec stays as rendered")
	require.Equal(t, live.Object["status"], got.Object["status"])
	require.Nil(t, rendered.Object["status"], "the rendered workload is not changed")
}

func TestKeepsAPostRenderedResourceAcrossRevisions(t *testing.T) {
	h := newHub(t)
	h.dispatch(h.component("backend", 1, ready(h, "yes"), announce(h, announceTemplate)))
	h.settle("backend")
	uid := h.configMap("notice").UID

	h.dispatch(h.component("backend", 2, ready(h, "yes"), announce(h, announceTemplate)))
	h.settle("backend")
	require.Equal(t, uid, h.configMap("notice").UID, "rendered again on the new revision, never collected in between")

	h.dispatch(h.component("backend", 3, ready(h, "no"), announce(h, announceTemplate)))
	h.settle("backend")
	require.NotNil(t, h.configMap("notice"), "kept while the new revision waits on the workload")
	require.Equal(t, uid, h.configMap("notice").UID)
}
