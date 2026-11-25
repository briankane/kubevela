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
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/validation"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/oam-dev/kubevela/apis/core.oam.dev/common"
	"github.com/oam-dev/kubevela/apis/core.oam.dev/v1alpha1"
	"github.com/oam-dev/kubevela/apis/core.oam.dev/v1beta1"
	"github.com/oam-dev/kubevela/pkg/appfile"
	"github.com/oam-dev/kubevela/pkg/componentdispatch"
	"github.com/oam-dev/kubevela/pkg/cue/definition"
	"github.com/oam-dev/kubevela/pkg/oam"
)

func renderedObject(kind, ns, name string, labels map[string]string) *unstructured.Unstructured {
	u := &unstructured.Unstructured{}
	u.SetAPIVersion("v1")
	u.SetKind(kind)
	u.SetNamespace(ns)
	u.SetName(name)
	u.SetLabels(labels)
	return u
}

func subject(health string) v1alpha1.ComponentHealthSubject {
	return v1alpha1.ComponentHealthSubject{HealthSnippets: v1alpha1.HealthSnippets{HealthPolicy: health}}
}

// shop is the "web" component of Application "shop": a workload with an auxiliary
// resource, a gateway trait with one output, and a control-plane-only trait.
func shop(cluster string) renderedComponent {
	workload := renderedObject("ConfigMap", "prod", "web", nil)
	return renderedComponent{
		target:   componentTarget{app: "shop", appNamespace: "default", component: "web", cluster: cluster, namespace: "prod"},
		workload: workload,
		traits: []*unstructured.Unstructured{
			renderedObject("Service", "prod", "web-svc", map[string]string{oam.TraitTypeLabel: definition.AuxiliaryWorkload, oam.TraitResource: "service"}),
			renderedObject("ConfigMap", "prod", "web-route", map[string]string{oam.TraitTypeLabel: "gateway-v2", oam.TraitResource: "route"}),
			renderedObject("ConfigMap", "default", "web-record", map[string]string{oam.TraitTypeLabel: "hub-record", oam.TraitResource: "record"}),
		},
		componentType:  "webservice",
		context:        map[string]string{"name": "web", "appName": "shop", "appRevision": "shop-v1"},
		workloadHealth: ptrTo(subject("isHealth: true")),
		traitHealth: []renderedTrait{
			{name: "gateway", typeLabel: "gateway-v2", health: subject("isHealth: context.outputs.route != _|_")},
			{name: "hub-record", typeLabel: "hub-record", controlPlaneOnly: true, health: subject("isHealth: true")},
		},
		apply: v1alpha1.ComponentApply{GarbageCollect: &v1alpha1.GarbageCollectPolicySpec{KeepLegacyResource: true}},
	}
}

func ptrTo[T any](v T) *T { return &v }

func TestComponentNames(t *testing.T) {
	target := componentTarget{app: "shop", component: "web", cluster: "prod-use1"}
	require.Regexp(t, `^shop-web-prod-use1-[0-9a-f]{8}$`, target.name())
	target.replicaKey = "beijing"
	require.Regexp(t, `^shop-web-beijing-prod-use1-[0-9a-f]{8}$`, target.name())
	require.Regexp(t, `^shop-web-beijing-prod-use1-prod-[0-9a-f]{8}-cp$`, (componentTarget{app: "shop", component: "web", replicaKey: "beijing",
		cluster: "prod-use1", namespace: "prod"}).controlPlane().name())
}

// Hub control-plane Components all live in the Application's namespace, so each placement's
// name must differ from every other's, including where the dash-joined parts read the same.
func TestControlPlaneNamesAreOnePerPlacement(t *testing.T) {
	cp := func(app, component, replicaKey, cluster, namespace string) string {
		return componentTarget{app: app, appNamespace: "default", component: component, replicaKey: replicaKey,
			cluster: cluster, namespace: namespace}.controlPlane().name()
	}
	require.NotEqual(t, cp("shop", "web", "", "spoke", "prod"), cp("shop", "web", "", "spoke", "staging"), "two namespaces on one cluster")
	require.NotEqual(t, cp("shop", "web", "", "a-b", "c"), cp("shop", "web", "", "a", "b-c"), "dashes in the cluster and namespace")
	require.NotEqual(t, cp("shop", "web", "", "spoke", "prod"), cp("shop-web", "", "", "spoke", "prod"), "dashes in the app and component")
	require.Equal(t, cp("shop", "web", "", "spoke", "prod"), cp("shop", "web", "", "spoke", "prod"))
}

// A Component's name is the readable parts, trimmed, and a hash of all of them: never longer
// than a label value, since vela-agent labels what it applies with it, and never the same for
// two placements.
func TestComponentNamesAreValidAndOnePerPlacement(t *testing.T) {
	name := func(appNamespace, app, component, replicaKey, cluster string) string {
		return componentTarget{app: app, appNamespace: appNamespace, component: component, replicaKey: replicaKey,
			cluster: cluster, namespace: "prod"}.name()
	}
	require.Regexp(t, `^shop-web-local-[0-9a-f]{8}$`, name("default", "shop", "web", "", "local"))
	require.NotEqual(t, name("default", "shop", "web", "eu", "c1"), name("default", "shop", "web-eu", "", "c1"), "a replica key and a dash")
	require.NotEqual(t, name("default", "a-b", "c", "", "c1"), name("default", "a", "b-c", "", "c1"), "dashes in the app and component")
	require.NotEqual(t, name("team-a", "shop", "web", "", "c1"), name("team-b", "shop", "web", "", "c1"), "one app name in two namespaces, placed together")

	long := componentTarget{app: "frontend-service", appNamespace: "frontend", component: "web-server", replicaKey: "eu-west",
		cluster: "production-us-east", namespace: "frontend-production"}
	for _, n := range []string{long.name(), long.controlPlane().name()} {
		require.LessOrEqual(t, len(n), validation.LabelValueMaxLength, n)
		require.Empty(t, validation.IsDNS1123Subdomain(n), n)
		require.True(t, strings.HasPrefix(n, "frontend-service-web-server"), n)
	}
	require.True(t, strings.HasSuffix(long.controlPlane().name(), controlPlaneSuffix))
}

func TestPackagesOneComponentOnTheHub(t *testing.T) {
	packages, err := shop("local").packages()
	require.NoError(t, err)
	require.Len(t, packages, 1, "a component on the hub carries its control-plane resources itself")
	p := packages[0]
	require.Regexp(t, `^shop-web-local-[0-9a-f]{8}$`, p.target.name())
	require.Equal(t, []v1alpha1.ComponentResource{
		{APIVersion: "v1", Kind: "ConfigMap", Namespace: "prod", Name: "web", Role: "output", Definition: v1alpha1.ComponentResourceDefinition{Kind: v1alpha1.DefinitionComponent, Type: "webservice"}},
		{APIVersion: "v1", Kind: "Service", Namespace: "prod", Name: "web-svc", Role: "outputs.service", Definition: v1alpha1.ComponentResourceDefinition{Kind: v1alpha1.DefinitionComponent, Type: "webservice"}},
		{APIVersion: "v1", Kind: "ConfigMap", Namespace: "prod", Name: "web-route", Role: "outputs.route", Definition: v1alpha1.ComponentResourceDefinition{Kind: v1alpha1.DefinitionTrait, Type: "gateway"}},
		{APIVersion: "v1", Kind: "ConfigMap", Namespace: "default", Name: "web-record", Role: "outputs.record", Definition: v1alpha1.ComponentResourceDefinition{Kind: v1alpha1.DefinitionTrait, Type: "hub-record"}},
	}, withoutHashes(p.index))
	require.Len(t, p.health.Traits, 2)
	require.Equal(t, "isHealth: true", p.health.Workload.HealthPolicy)
}

func TestSplitsControlPlaneResourcesOffASpoke(t *testing.T) {
	packages, err := shop("prod-use1").packages()
	require.NoError(t, err)
	require.Len(t, packages, 2)
	spoke, hub := packages[0], packages[1]

	require.Equal(t, componentTarget{app: "shop", appNamespace: "default", component: "web", cluster: "prod-use1", namespace: "prod"}, spoke.target)
	require.Len(t, spoke.index, 3)
	require.Equal(t, []string{"gateway"}, traitTypes(spoke.health))
	require.NotNil(t, spoke.health.Workload)

	require.Regexp(t, `^shop-web-prod-use1-prod-[0-9a-f]{8}-cp$`, hub.target.name())
	require.Equal(t, "local", hub.target.cluster)
	require.Equal(t, "default", hub.target.namespace, "the hub Component lives with the Application")
	require.Equal(t, "prod-use1", hub.target.controlPlaneFor)
	require.Equal(t, []v1alpha1.ComponentResource{
		{APIVersion: "v1", Kind: "ConfigMap", Namespace: "default", Name: "web-record", Role: "outputs.record", Definition: v1alpha1.ComponentResourceDefinition{Kind: v1alpha1.DefinitionTrait, Type: "hub-record"}},
	}, withoutHashes(hub.index))
	require.Nil(t, hub.health.Workload, "the workload's health is the spoke's to report")
	require.Equal(t, []string{"hub-record"}, traitTypes(hub.health))
	require.Equal(t, spoke.health.Context, hub.health.Context)
}

func TestBuildsASignedComponentAndItsPayload(t *testing.T) {
	packages, err := shop("prod-use1").packages()
	require.NoError(t, err)
	comp, secret, err := packages[1].build(3)
	require.NoError(t, err)

	require.Regexp(t, `^shop-web-prod-use1-prod-[0-9a-f]{8}-cp$`, comp.Name)
	require.Equal(t, "default", comp.Namespace)
	require.Equal(t, v1alpha1.ComponentGroupVersionKind, comp.GroupVersionKind())
	require.Equal(t, map[string]string{
		oam.LabelAppName: "shop", oam.LabelAppNamespace: "default", oam.LabelAppComponent: "web",
		oam.LabelComponentCluster: "local", oam.LabelControlPlaneFor: "prod-use1",
	}, comp.Labels)
	require.Equal(t, int64(3), comp.Spec.Revision)
	require.Equal(t, "local", comp.Spec.Target.Cluster)
	require.True(t, comp.Spec.Apply.GarbageCollect.KeepLegacyResource)

	require.Equal(t, comp.Name+"-payload", secret.Name)
	require.Equal(t, comp.Namespace, secret.Namespace)
	require.Equal(t, secret.Name, comp.Spec.Payload.SecretRef.Name)
	objs, err := componentdispatch.DecodePayload(secret.Data[componentdispatch.PayloadKey], comp.Spec.Payload.SHA256)
	require.NoError(t, err)
	require.NoError(t, componentdispatch.CheckIndex(objs, comp.Spec.Resources))

	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	require.NoError(t, componentdispatch.Signer{KeyID: "hub-1", Key: priv}.Sign(comp))
	require.NoError(t, componentdispatch.TrustedKeys{"hub-1": pub}.Verify(comp))
}

func TestPayloadResourcesCarryNoClusterLabel(t *testing.T) {
	r := shop("prod-use1")
	for _, obj := range append([]*unstructured.Unstructured{r.workload}, r.traits...) {
		oam.SetCluster(obj, "prod-use1")
	}
	r.traits[2].SetLabels(map[string]string{oam.TraitTypeLabel: "hub-record", oam.TraitResource: "record", oam.LabelAppCluster: "local"})
	packages, err := r.packages()
	require.NoError(t, err)
	for _, p := range packages {
		for _, obj := range p.objects {
			require.NotContains(t, obj.GetLabels(), oam.LabelAppCluster, "%s/%s", obj.GetKind(), obj.GetName())
		}
	}
	require.Equal(t, "prod-use1", oam.GetCluster(r.workload), "the rendered objects are not changed")
}

func observationsOf(p componentPackage) map[string]v1alpha1.ComponentObservation {
	out := map[string]v1alpha1.ComponentObservation{}
	for _, o := range p.observe {
		out[o.Resource+" "+o.Name] = o
	}
	return out
}

func TestObservesNothingUnread(t *testing.T) {
	packages, err := shop("local").packages()
	require.NoError(t, err)
	require.Empty(t, packages[0].observe)
}

func TestObservesWhatOtherComponentsRead(t *testing.T) {
	r := shop("prod-use1")
	r.traits = append(r.traits, renderedObject("Secret", "prod", "web-creds",
		map[string]string{oam.TraitTypeLabel: definition.AuxiliaryWorkload, oam.TraitResource: "creds"}))
	r.reads = [][]string{
		{"output", "status", "endpoint"},
		{"output", "status", "conditions", "0", "type"},
		{"output"},
		{"outputs", "service", "spec", "clusterIP"},
		{"outputs", "route", "metadata", "labels", "example.com/tier"},
		{"outputs", "route", "data", "odd[key]", "x"},
		{"outputs", "creds", "data", "password"},
		{"outputs", "record", "data", "note"},
		{"outputs", "absent", "spec"},
	}
	packages, err := r.packages()
	require.NoError(t, err)
	require.Len(t, packages, 2)

	require.Equal(t, map[string]v1alpha1.ComponentObservation{
		`output ["status","endpoint"]`:         {Resource: "output", Name: `["status","endpoint"]`, Path: ".status.endpoint"},
		`output ["status","conditions"]`:       {Resource: "output", Name: `["status","conditions"]`, Path: ".status.conditions"},
		`output []`:                            {Resource: "output", Name: `[]`, Path: ""},
		`outputs.service ["spec","clusterIP"]`: {Resource: "outputs.service", Name: `["spec","clusterIP"]`, Path: ".spec.clusterIP"},
		`outputs.route ["metadata","labels","example.com/tier"]`: {Resource: "outputs.route",
			Name: `["metadata","labels","example.com/tier"]`, Path: `.metadata.labels.example\.com/tier`},
		`outputs.route ["data"]`:            {Resource: "outputs.route", Name: `["data"]`, Path: ".data"},
		`outputs.creds ["data","password"]`: {Resource: "outputs.creds", Name: `["data","password"]`, Path: ".data.password", Sensitive: true},
	}, observationsOf(packages[0]), "a read of a resource elsewhere is left to its Component; a read of no resource, to the reader's error")
	require.Equal(t, map[string]v1alpha1.ComponentObservation{
		`outputs.record ["data","note"]`: {Resource: "outputs.record", Name: `["data","note"]`, Path: ".data.note"},
	}, observationsOf(packages[1]))

	comp, _, err := packages[0].build(1)
	require.NoError(t, err)
	require.Len(t, comp.Spec.Observe, 7)
	require.Equal(t, `output ["status","conditions"]`, comp.Spec.Observe[0].Resource+" "+comp.Spec.Observe[0].Name, "in a stable order")
}

func TestRebuildsResourcesFromObservations(t *testing.T) {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	ring := &componentdispatch.KeyRing{Active: "k1", Keys: map[string]ed25519.PrivateKey{"k1": priv}}
	sealed, err := ring.Sealing().Seal("k1", []byte(`"aHVudGVyMg=="`))
	require.NoError(t, err)

	c := &v1alpha1.Component{}
	c.Namespace, c.Name = "prod", "shop-web-r1-prod-use1"
	c.Labels = map[string]string{oam.LabelReplicaKey: "r1"}
	c.Spec.Resources = []v1alpha1.ComponentResource{
		{APIVersion: "apps/v1", Kind: "Deployment", Namespace: "prod", Name: "web", Role: "output"},
		{APIVersion: "v1", Kind: "Service", Namespace: "prod", Name: "web", Role: "outputs.service", Definition: v1alpha1.ComponentResourceDefinition{Kind: v1alpha1.DefinitionTrait, Type: "expose"}},
		{APIVersion: "v1", Kind: "Secret", Namespace: "prod", Name: "web-creds", Role: "outputs.creds", Definition: v1alpha1.ComponentResourceDefinition{Kind: v1alpha1.DefinitionTrait, Type: "creds"}},
		{APIVersion: "v1", Kind: "ConfigMap", Namespace: "prod", Name: "web-extra", Definition: v1alpha1.ComponentResourceDefinition{Kind: v1alpha1.DefinitionTrait, Type: "extra"}},
	}
	c.Status.Observed = map[string]runtime.RawExtension{
		"output":          {Raw: []byte(`{"[\"status\"]":{"readyReplicas":2,"endpoint":"stale"},"[\"status\",\"endpoint\"]":"web.prod:80"}`)},
		"outputs.service": {Raw: []byte(`{"[]":{"apiVersion":"v1","kind":"Service","metadata":{"name":"web","namespace":"prod"},"spec":{"clusterIP":"10.0.0.7"}}}`)},
	}
	c.Status.SealedObserved = map[string]map[string]string{"outputs.creds": {`["data","password"]`: sealed}}
	c.Status.SealedWith = "k1"

	workloads, outputs, err := observedResources(c, func(box string) ([]byte, error) { return ring.Open(c.Status.SealedWith, box) })
	require.NoError(t, err)

	require.Len(t, workloads, 1)
	require.Equal(t, map[string]interface{}{
		"apiVersion": "apps/v1", "kind": "Deployment",
		"metadata": map[string]interface{}{"name": "web", "namespace": "prod", "labels": map[string]interface{}{oam.LabelReplicaKey: "r1"}},
		"status":   map[string]interface{}{"readyReplicas": json.Number("2"), "endpoint": "web.prod:80"},
	}, workloads[0].Object, "a field read on its own wins over its copy in the parent; the replica key picks the workload")

	require.Len(t, outputs, 2, "a resource no read names is not an output")
	require.Equal(t, "10.0.0.7", outputs[0].Object["spec"].(map[string]interface{})["clusterIP"])
	require.Equal(t, "service", outputs[0].GetLabels()[oam.TraitResource])
	require.Equal(t, "aHVudGVyMg==", outputs[1].Object["data"].(map[string]interface{})["password"], "a sealed value is opened")
	require.Equal(t, "creds", outputs[1].GetLabels()[oam.TraitResource])
}

func TestRefusesAnObservationThatDoesNotOpen(t *testing.T) {
	c := &v1alpha1.Component{}
	c.Namespace, c.Name = "prod", "shop-web-prod-use1"
	c.Spec.Resources = []v1alpha1.ComponentResource{{APIVersion: "v1", Kind: "Secret", Name: "web-creds", Role: "outputs.creds"}}
	c.Status.SealedObserved = map[string]map[string]string{"outputs.creds": {`["data"]`: "bm9wZQ=="}}
	_, _, err := observedResources(c, func(string) ([]byte, error) { return nil, componentdispatch.ErrCannotOpen })
	require.ErrorIs(t, err, componentdispatch.ErrCannotOpen)
	require.ErrorContains(t, err, `component prod/shop-web-prod-use1 observed outputs.creds ["data"]`)
}

func TestADispatchModeIsANewRevision(t *testing.T) {
	inProcess, err := withDispatchMode("acdeb07100e572c", "")
	require.NoError(t, err)
	require.Equal(t, "acdeb07100e572c", inProcess, "an in-process Application's revisions keep their hashes")
	dispatched, err := withDispatchMode("acdeb07100e572c", oam.DispatchComponent)
	require.NoError(t, err)
	require.NotEqual(t, inProcess, dispatched)
}

func TestRefusesATerraformComponent(t *testing.T) {
	r := shop("local")
	r.terraform = true
	_, err := r.packages()
	require.ErrorContains(t, err, `component "web" is a Terraform component`)
}

func withoutHashes(index []v1alpha1.ComponentResource) []v1alpha1.ComponentResource {
	out := make([]v1alpha1.ComponentResource, len(index))
	for i, r := range index {
		r.SHA256 = ""
		out[i] = r
	}
	return out
}

func traitTypes(h v1alpha1.ComponentHealth) []string {
	var out []string
	for _, t := range h.Traits {
		out = append(out, t.Type)
	}
	return out
}

func TestReportsAComponentNothingHasAppliedInTime(t *testing.T) {
	written := time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)
	d := func(revision, lastApplied int64) deliveredComponent {
		c := &v1alpha1.Component{}
		c.Namespace, c.Name, c.Spec.Revision = "prod", "shop-web-spoke", revision
		return deliveredComponent{target: componentTarget{cluster: "spoke"}, component: c, dispatchedAt: written, lastApplied: lastApplied}
	}
	require.Empty(t, overdue(d(1, 0), written.Add(time.Minute), 2*time.Minute), "within the timeout")
	require.Equal(t, "waiting since 2026-11-22T12:00:00Z for anything on cluster spoke to reconcile Component prod/shop-web-spoke: is vela-agent running there?",
		overdue(d(1, 0), written.Add(3*time.Minute), 2*time.Minute))
	require.Equal(t, "waiting since 2026-11-22T12:00:00Z for vela-agent on cluster spoke to apply revision 4 of Component prod/shop-web-spoke (last applied 3)",
		overdue(d(4, 3), written.Add(3*time.Minute), 2*time.Minute))
	require.Equal(t, overdue(d(4, 3), written.Add(3*time.Minute), 2*time.Minute), overdue(d(4, 3), written.Add(9*time.Minute), 2*time.Minute),
		"the report stays the same while nothing changes, so its events aggregate and the status is not rewritten")
	require.Empty(t, overdue(d(4, 4), written.Add(3*time.Minute), 2*time.Minute), "applied")
	unstamped := d(1, 0)
	unstamped.dispatchedAt = time.Time{}
	require.Empty(t, overdue(unstamped, written.Add(time.Hour), 2*time.Minute), "no time to measure from")
}

type countingKeys struct {
	calls atomic.Int32
	fail  bool
}

func (k *countingKeys) Ring(context.Context) (*componentdispatch.KeyRing, error) {
	k.calls.Add(1)
	if k.fail {
		return nil, errors.New("no keys")
	}
	return &componentdispatch.KeyRing{Active: "hub-1"}, nil
}

// One reconcile reads the hub's keys once, however many Components it signs or opens, the deploy
// step's parallel applies included.
func TestReadsTheKeysOncePerReconcile(t *testing.T) {
	keys := &countingKeys{}
	h := &AppHandler{componentKeys: keys}
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := h.keyRing(context.Background())
			require.NoError(t, err)
		}()
	}
	wg.Wait()
	require.EqualValues(t, 1, keys.calls.Load())

	failing := &countingKeys{fail: true}
	h = &AppHandler{componentKeys: failing}
	_, err := h.keyRing(context.Background())
	require.Error(t, err)
	_, err = h.keyRing(context.Background())
	require.Error(t, err)
	require.EqualValues(t, 2, failing.calls.Load(), "a failed read is tried again")
}

type componentGetCounter struct {
	client.Reader
	gets atomic.Int32
}

func (r *componentGetCounter) Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	r.gets.Add(1)
	return r.Reader.Get(ctx, key, obj, opts...)
}

// One reconcile reads each Component from its cluster once, and sees what it wrote itself since.
func TestReadsEachComponentOncePerReconcile(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, v1alpha1.AddToScheme(scheme))
	target := componentTarget{app: "shop", appNamespace: "default", component: "web", cluster: "local", namespace: "prod"}
	existing := &v1alpha1.Component{
		ObjectMeta: metav1.ObjectMeta{Name: target.name(), Namespace: "prod", Labels: target.labels()},
		Spec:       v1alpha1.ComponentSpec{Revision: 3},
	}
	cli := fake.NewClientBuilder().WithScheme(scheme).WithObjects(existing).Build()
	reader := &componentGetCounter{Reader: cli}
	h := &AppHandler{Client: cli, clusterReader: reader}
	ctx := context.Background()

	for range 3 {
		live, err := h.liveComponent(ctx, target)
		require.NoError(t, err)
		require.EqualValues(t, 3, live.Spec.Revision)
	}
	require.EqualValues(t, 1, reader.gets.Load())

	written := existing.DeepCopy()
	written.Spec.Revision = 4
	h.rememberComponent(target, written)
	live, err := h.liveComponent(ctx, target)
	require.NoError(t, err)
	require.EqualValues(t, 4, live.Spec.Revision, "a later dispatch builds on the revision written")
	require.EqualValues(t, 1, reader.gets.Load())

	missing := target
	missing.component = "api"
	for range 2 {
		live, err := h.liveComponent(ctx, missing)
		require.NoError(t, err)
		require.Nil(t, live)
	}
	require.EqualValues(t, 2, reader.gets.Load(), "a Component that is not there is remembered too")
}

// componentName is the Component that delivers an Application's component on the hub's own cluster.
func componentName(appNamespace, app, component string) string {
	return componentTarget{app: app, appNamespace: appNamespace, component: component, cluster: "local", namespace: appNamespace}.name()
}

func TestReportsWhatVelaAgentSaysStopsAComponent(t *testing.T) {
	d := func(ready *metav1.Condition) deliveredComponent {
		return deliveredComponent{target: componentTarget{cluster: "spoke"}, ready: ready}
	}
	cond := func(status metav1.ConditionStatus, reason, message string) *metav1.Condition {
		return &metav1.Condition{Type: componentdispatch.ConditionReady, Status: status, Reason: reason, Message: message}
	}
	require.Empty(t, agentReport(d(nil)), "nothing reported on this revision")
	require.Empty(t, agentReport(d(cond(metav1.ConditionTrue, "Ready", ""))))
	require.Empty(t, agentReport(d(cond(metav1.ConditionFalse, componentdispatch.ReasonWorkloadUnhealthy, "ready: 0/2"))),
		"health speaks for itself, in the definition's own words")
	require.Empty(t, agentReport(d(cond(metav1.ConditionFalse, componentdispatch.ReasonTraitUnhealthy, "expose: no endpoints"))))
	require.Equal(t, `vela-agent on cluster spoke: UntrustedKey: signed with key "hub-3", which this cluster does not trust`,
		agentReport(d(cond(metav1.ConditionFalse, "UntrustedKey", `signed with key "hub-3", which this cluster does not trust`))))
	require.Equal(t, "vela-agent on cluster spoke: Collecting", agentReport(d(cond(metav1.ConditionFalse, "Collecting", ""))))
}

func TestRendersAtMostEveryRenderIntervalWhileWaitingOnVelaAgent(t *testing.T) {
	app := &v1beta1.Application{}
	wl := &appfile.Component{Name: "web", FullTemplate: &appfile.Template{}}
	target := componentTarget{cluster: "spoke", namespace: "default"}
	waiting := deliveredComponent{target: target, component: &v1alpha1.Component{Spec: v1alpha1.ComponentSpec{Revision: 1}}}
	healthy := waiting
	healthy.observed = &common.ApplicationComponentStatus{Healthy: true, WorkloadHealthy: true}

	h := &AppHandler{app: app, agentRenderInterval: 5 * time.Second}
	require.Equal(t, 800*time.Millisecond, h.workflowRequeue(800*time.Millisecond), "nothing waits on vela-agent")

	h.reportComponentHealth(wl, "spoke", target, []deliveredComponent{healthy})
	require.Equal(t, 800*time.Millisecond, h.workflowRequeue(800*time.Millisecond), "vela-agent reported healthy")

	h.reportComponentHealth(wl, "spoke", target, []deliveredComponent{waiting})
	require.Equal(t, 5*time.Second, h.workflowRequeue(800*time.Millisecond), "waiting on vela-agent")
	require.Equal(t, 40*time.Second, h.workflowRequeue(40*time.Second), "the backoff still applies above the interval")

	h = &AppHandler{app: app}
	h.reportComponentHealth(wl, "spoke", target, []deliveredComponent{waiting})
	require.Equal(t, defaultComponentAgentRenderInterval, h.workflowRequeue(time.Second), "the default when unset")
}
