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
	"slices"
	"strings"
	"time"

	apimeta "k8s.io/apimachinery/pkg/api/meta"

	"github.com/crossplane/crossplane-runtime/v2/pkg/event"
	monitorContext "github.com/kubevela/pkg/monitor/context"
	"github.com/pkg/errors"
	kerrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	utilfeature "k8s.io/apiserver/pkg/util/feature"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/oam-dev/kubevela/apis/core.oam.dev/common"
	"github.com/oam-dev/kubevela/apis/core.oam.dev/v1alpha1"
	"github.com/oam-dev/kubevela/apis/core.oam.dev/v1beta1"
	"github.com/oam-dev/kubevela/apis/types"
	"github.com/oam-dev/kubevela/pkg/appfile"
	"github.com/oam-dev/kubevela/pkg/appkeeper"
	"github.com/oam-dev/kubevela/pkg/componentdispatch"
	"github.com/oam-dev/kubevela/pkg/cue/definition"
	"github.com/oam-dev/kubevela/pkg/cue/definition/health"
	"github.com/oam-dev/kubevela/pkg/features"
	"github.com/oam-dev/kubevela/pkg/multicluster"
	"github.com/oam-dev/kubevela/pkg/oam"
	"github.com/oam-dev/kubevela/pkg/oam/util"
	"github.com/oam-dev/kubevela/pkg/resourcekeeper"
	"github.com/oam-dev/kubevela/pkg/sources"
	"github.com/oam-dev/kubevela/pkg/utils/apply"
	velaerrors "github.com/oam-dev/kubevela/pkg/utils/errors"
	oamprovidertypes "github.com/oam-dev/kubevela/pkg/workflow/providers/types"
)

// ComponentKeys signs the Components the hub dispatches, and opens what vela-agent sealed
// to the hub.
type ComponentKeys interface {
	Ring(ctx context.Context) (*componentdispatch.KeyRing, error)
}

// keyRing is the hub's signing keys, read on first use and kept for the rest of the reconcile.
func (h *AppHandler) keyRing(ctx context.Context) (*componentdispatch.KeyRing, error) {
	h.keyRingMu.Lock()
	defer h.keyRingMu.Unlock()
	if h.ring != nil {
		return h.ring, nil
	}
	ring, err := h.componentKeys.Ring(ctx)
	if err != nil {
		return nil, err
	}
	h.ring = ring
	return ring, nil
}

// openSealed opens a value vela-agent sealed to the hub's key keyID.
func (h *AppHandler) openSealed(ctx context.Context, keyID, sealed string) ([]byte, error) {
	if h.componentKeys == nil {
		return nil, errors.New("no component keys to open a sealed observation with")
	}
	ring, err := h.keyRing(ctx)
	if err != nil {
		return nil, err
	}
	return ring.Open(keyID, sealed)
}

// dispatchesComponents reports whether this Application's components are delivered as
// Components rather than applied in-process.
func (h *AppHandler) dispatchesComponents() bool {
	return utilfeature.DefaultMutableFeatureGate.Enabled(features.EnableComponentDispatch) &&
		h.app.GetAnnotations()[oam.AnnotationDispatch] == oam.DispatchComponent
}

// reader reads past the cache from the cluster a context names, as reads of dispatched
// Components must.
func (h *AppHandler) reader() client.Reader {
	if h.clusterReader != nil {
		return h.clusterReader
	}
	if h.apiReader != nil {
		return h.apiReader
	}
	return h.Client
}

// dispatchComponent packages one rendered component for one placement, delivers it as
// signed Components asked to observe what all reads of it, and reports their health as the
// component's service status.
func (h *AppHandler) dispatchComponent(ctx context.Context, af *appfile.Appfile, wl *appfile.Component, workload *unstructured.Unstructured,
	traits []*unstructured.Unstructured, all []common.ApplicationComponent, cluster, overrideNamespace, replicaKey string) (bool, string, error) {
	rendered, err := h.renderedComponent(af, wl, workload, traits, cluster, overrideNamespace, replicaKey)
	if err != nil {
		return false, "", err
	}
	if rendered.reads, err = h.readsOfProducer(wl.Name, all); err != nil {
		return false, "", err
	}
	packages, err := rendered.packages()
	if err != nil {
		return false, "", err
	}
	delivered := make([]deliveredComponent, 0, len(packages))
	for _, p := range packages {
		d, err := h.deliver(ctx, p)
		if err != nil {
			return false, "", err
		}
		delivered = append(delivered, d)
	}
	status, late := h.reportComponentHealth(wl, cluster, rendered.target, delivered)
	return status.Healthy, late, nil
}

// componentDispatched reports whether the current revision's tracker already holds the
// Components that carry the component, and their payloads: as for a component applied in
// process, only a new revision of the Application dispatches it again.
func (h *AppHandler) componentDispatched(af *appfile.Appfile, wl *appfile.Component, workload *unstructured.Unstructured,
	traits []*unstructured.Unstructured, all []common.ApplicationComponent, cluster, overrideNamespace, replicaKey string) (bool, error) {
	if h.resourceKeeper == nil {
		return false, nil
	}
	rendered, err := h.renderedComponent(af, wl, workload, traits, cluster, overrideNamespace, replicaKey)
	if err != nil {
		return false, err
	}
	if rendered.reads, err = h.readsOfProducer(wl.Name, all); err != nil {
		return false, err
	}
	packages, err := rendered.packages()
	if err != nil {
		return false, err
	}
	var carried []*unstructured.Unstructured
	for _, p := range packages {
		comp, secret, err := p.build(0)
		if err != nil {
			return false, err
		}
		for _, obj := range []runtime.Object{secret, comp} {
			u, err := toManifest(obj)
			if err != nil {
				return false, err
			}
			carried = append(carried, multicluster.ResourcesWithClusterName(p.target.cluster, u)...)
		}
	}
	return h.resourceKeeper.ContainsResources(carried), nil
}

// componentHealthCheck reads the health of a component's Components without dispatching.
func (h *AppHandler) componentHealthCheck(ctx context.Context, wl *appfile.Component, cluster, overrideNamespace, replicaKey string) (bool, *common.ApplicationComponentStatus, error) {
	target := h.componentTarget(wl, cluster, overrideNamespace, replicaKey)
	targets := []componentTarget{target}
	if target.cluster != multicluster.ClusterLocalName {
		targets = append(targets, target.controlPlane())
	}
	var delivered []deliveredComponent
	for i, t := range targets {
		live, err := h.liveComponent(ctx, t)
		if err != nil {
			return false, nil, err
		}
		if live == nil {
			if i > 0 {
				continue // no control-plane resources
			}
			live = &v1alpha1.Component{}
			live.Name, live.Namespace = t.name(), t.namespace
		}
		delivered = append(delivered, deliveredComponent{target: t, component: live, observed: observedHealth(live), reported: live.Status.Health,
			dispatchedAt: dispatchedAt(live), lastApplied: live.Status.ObservedRevision})
	}
	status, _ := h.reportComponentHealth(wl, cluster, target, delivered)
	return status.Healthy, &status, nil
}

func (h *AppHandler) componentTarget(wl *appfile.Component, cluster, overrideNamespace, replicaKey string) componentTarget {
	if cluster == "" {
		cluster = multicluster.ClusterLocalName
	}
	return componentTarget{
		app: h.app.Name, appNamespace: h.app.Namespace, component: wl.Name, replicaKey: replicaKey, cluster: cluster,
		namespace: util.NewApplicationResourceNamespaceAccessor(h.app.Namespace, overrideNamespace).Namespace(),
	}
}

func (h *AppHandler) renderedComponent(af *appfile.Appfile, wl *appfile.Component, workload *unstructured.Unstructured,
	traits []*unstructured.Unstructured, cluster, overrideNamespace, replicaKey string) (renderedComponent, error) {
	r := renderedComponent{
		target:        h.componentTarget(wl, cluster, overrideNamespace, replicaKey),
		terraform:     wl.CapabilityCategory == types.TerraformCategory,
		componentType: wl.Type,
		context:       definition.GetBaseContextLabels(wl.Ctx),
	}
	policies, err := appkeeper.Policies(h.app)
	if err != nil {
		return r, err
	}
	r.apply = v1alpha1.ComponentApply{
		ApplyOnce: policies.ApplyOnce, GarbageCollect: policies.GarbageCollect, SharedResource: policies.SharedResource,
		TakeOver: policies.TakeOver, ReadOnly: policies.ReadOnly, ResourceUpdate: policies.ResourceUpdate,
	}
	if h.appliedInProcess(wl.Name) {
		r.apply.AdoptFrom = v1beta1.ApplicationKind + "/" + h.app.Namespace + "/" + h.app.Name
	}
	if !wl.SkipApplyWorkload {
		r.workload = workload
		s, err := healthSubject(wl.FullTemplate.AsStatusRequest(wl.Params))
		if err != nil {
			return r, errors.WithMessagef(err, "component %s", wl.Name)
		}
		r.workloadHealth = &s
	}
	r.traits = traits
	if r.postRender, err = h.postRenderSpec(af, wl, r.workload, overrideNamespace); err != nil {
		return r, err
	}
	for _, tr := range append(slices.Clone(wl.Traits), wl.PostDispatchTraits...) {
		s, err := healthSubject(tr.FullTemplate.AsStatusRequest(tr.Params))
		if err != nil {
			return r, errors.WithMessagef(err, "component %s, trait %s", wl.Name, tr.Name)
		}
		r.traitHealth = append(r.traitHealth, renderedTrait{
			name: tr.Name, typeLabel: tr.GetTypeLabel(),
			controlPlaneOnly: tr.FullTemplate.TraitDefinition != nil && tr.FullTemplate.TraitDefinition.Spec.ControlPlaneOnly,
			health:           s,
		})
	}
	return r, nil
}

func healthSubject(req *health.StatusRequest) (v1alpha1.ComponentHealthSubject, error) {
	s := v1alpha1.ComponentHealthSubject{HealthSnippets: v1alpha1.HealthSnippets{
		HealthPolicy: req.Health, CustomStatus: req.Custom, Details: req.Details,
	}}
	for _, a := range req.Ancestors {
		s.Ancestors = append(s.Ancestors, v1alpha1.HealthSnippets{HealthPolicy: a.Health, CustomStatus: a.Custom, Details: a.Details})
	}
	if len(req.Parameter) > 0 {
		raw, err := json.Marshal(req.Parameter)
		if err != nil {
			return s, errors.Wrap(err, "encoding health parameters")
		}
		s.Parameter = &runtime.RawExtension{Raw: raw}
	}
	return s, nil
}

// deliveredComponent is a Component as dispatched, with the health vela-agent reported for
// that revision, nil until it has.
type deliveredComponent struct {
	target    componentTarget
	component *v1alpha1.Component
	// observed is vela-agent's report on the current revision; nil until it makes one.
	observed *common.ApplicationComponentStatus
	// reported is vela-agent's latest report, of whichever revision it last evaluated.
	reported *common.ApplicationComponentStatus
	// dispatchedAt is when the hub wrote the current revision; zero when unknown.
	dispatchedAt time.Time
	// lastApplied is the revision vela-agent last applied; 0 when it has applied none.
	lastApplied int64
	// ready is vela-agent's Ready condition on the current revision; nil until it reports one.
	ready *metav1.Condition
}

// deliver writes the payload Secret, then the signed Component, keeping the live revision
// when nothing changed and moving to the next when anything did.
func (h *AppHandler) deliver(ctx context.Context, p componentPackage) (deliveredComponent, error) {
	live, err := h.liveComponent(ctx, p.target)
	if err != nil {
		return deliveredComponent{}, err
	}
	revision := int64(1)
	if live != nil {
		revision = live.Spec.Revision
	}
	comp, secret, err := p.build(revision)
	if err != nil {
		return deliveredComponent{}, err
	}
	written := time.Now().UTC().Truncate(time.Second)
	if live != nil {
		same, err := componentdispatch.SameSpec(live, comp)
		if err != nil {
			return deliveredComponent{}, err
		}
		if !same {
			comp.Spec.Revision++
		} else if at := dispatchedAt(live); !at.IsZero() {
			written = at
		}
	}
	metav1.SetMetaDataAnnotation(&comp.ObjectMeta, oam.AnnotationDispatchedAt, written.Format(time.RFC3339))
	if h.componentKeys == nil {
		return deliveredComponent{}, errors.New("component dispatch is on, but the controller has no component signing keys")
	}
	ring, err := h.keyRing(ctx)
	if err != nil {
		return deliveredComponent{}, errors.WithMessage(err, "loading the component signing key")
	}
	if err := ring.Signer().Sign(comp); err != nil {
		return deliveredComponent{}, err
	}
	// Separate dispatches, so the payload is in place before the Component that names it.
	for _, obj := range []runtime.Object{secret, comp} {
		u, err := toManifest(obj)
		if err != nil {
			return deliveredComponent{}, err
		}
		if err := h.Dispatch(ctx, h.Client, p.target.cluster, common.WorkflowResourceCreator, u); err != nil {
			return deliveredComponent{}, errors.WithMessagef(err, "dispatching component %s/%s to cluster %s", comp.Namespace, comp.Name, p.target.cluster)
		}
	}
	remembered := comp.DeepCopy()
	if live != nil {
		remembered.Status = live.Status
	}
	h.rememberComponent(p.target, remembered)
	d := deliveredComponent{target: p.target, component: comp, dispatchedAt: written}
	if live != nil {
		d.reported = live.Status.Health
		d.lastApplied = live.Status.ObservedRevision
	}
	if live != nil && live.Spec.Revision == comp.Spec.Revision {
		d.observed = observedHealth(live)
		if ready := apimeta.FindStatusCondition(live.Status.Conditions, componentdispatch.ConditionReady); ready != nil && ready.ObservedGeneration == live.Generation {
			d.ready = ready
		}
	}
	return d, nil
}

// liveComponent is the Component for target on its cluster, nil if there is none. It is read
// once per reconcile; what the reconcile writes since replaces it (rememberComponent). One that
// names a different Application, component or cluster is a name collision.
func (h *AppHandler) liveComponent(ctx context.Context, target componentTarget) (*v1alpha1.Component, error) {
	key := client.ObjectKey{Namespace: target.namespace, Name: target.name()}
	h.liveMu.Lock()
	seen, ok := h.live[target.cluster+"/"+key.String()]
	h.liveMu.Unlock()
	if ok {
		if seen == nil {
			return nil, nil
		}
		return seen.DeepCopy(), nil
	}
	live := &v1alpha1.Component{}
	err := h.reader().Get(multicluster.ContextWithClusterName(ctx, target.cluster), key, live)
	if kerrors.IsNotFound(err) {
		h.rememberComponent(target, nil)
		return nil, nil
	}
	if err != nil {
		return nil, errors.WithMessagef(err, "reading component %s on cluster %s", key, target.cluster)
	}
	for k, v := range target.labels() {
		if live.Labels[k] != v {
			return nil, fmt.Errorf("component %s on cluster %s belongs to something else (%s=%q, not %q)", key, target.cluster, k, live.Labels[k], v)
		}
	}
	h.rememberComponent(target, live)
	return live.DeepCopy(), nil
}

// rememberComponent records c as target's Component for the rest of the reconcile, nil for none.
func (h *AppHandler) rememberComponent(target componentTarget, c *v1alpha1.Component) {
	h.liveMu.Lock()
	defer h.liveMu.Unlock()
	if h.live == nil {
		h.live = map[string]*v1alpha1.Component{}
	}
	if c != nil {
		c = c.DeepCopy()
	}
	h.live[target.cluster+"/"+target.namespace+"/"+target.name()] = c
}

func observedHealth(c *v1alpha1.Component) *common.ApplicationComponentStatus {
	if c.Spec.Revision == 0 || c.Status.ObservedRevision != c.Spec.Revision {
		return nil
	}
	return c.Status.Health
}

// dispatchedAt is when the hub wrote c's current revision, zero when it did not say.
func dispatchedAt(c *v1alpha1.Component) time.Time {
	at, err := time.Parse(time.RFC3339, c.GetAnnotations()[oam.AnnotationDispatchedAt])
	if err != nil {
		return time.Time{}
	}
	return at
}

// overdue says what is wrong when vela-agent has not applied d's revision within timeout
// of the hub writing it, and is empty otherwise.
func overdue(d deliveredComponent, now time.Time, timeout time.Duration) string {
	c := d.component
	if d.dispatchedAt.IsZero() || d.lastApplied == c.Spec.Revision {
		return ""
	}
	if now.Sub(d.dispatchedAt) <= timeout {
		return ""
	}
	// Named by when the revision was written, so the report stays the same while nothing
	// changes: its events aggregate and the status is not rewritten on every reconcile.
	since := d.dispatchedAt.UTC().Format(time.RFC3339)
	if d.lastApplied == 0 {
		return fmt.Sprintf("waiting since %s for anything on cluster %s to reconcile Component %s/%s: is vela-agent running there?",
			since, d.target.cluster, c.Namespace, c.Name)
	}
	return fmt.Sprintf("waiting since %s for vela-agent on cluster %s to apply revision %d of Component %s/%s (last applied %d)",
		since, d.target.cluster, c.Spec.Revision, c.Namespace, c.Name, d.lastApplied)
}

// reportComponentHealth records and returns the component's service status, from what
// vela-agent reported for each of its Components. The status names the cluster as the
// placement did, empty for the hub by default, as the in-process path does. It also returns
// what is wrong when a Component has waited on vela-agent past the timeout, which the
// status message then says too.
func (h *AppHandler) reportComponentHealth(wl *appfile.Component, cluster string, target componentTarget, delivered []deliveredComponent) (common.ApplicationComponentStatus, string) {
	timeout := h.componentReconcilerTimeout
	if timeout <= 0 {
		timeout = defaultComponentReconcilerTimeout
	}
	late := ""
	status := h.getServiceStatus(common.ApplicationComponentStatus{
		Name:               wl.Name,
		WorkloadDefinition: wl.FullTemplate.Reference.Definition,
		Namespace:          target.namespace,
		Cluster:            cluster,
	})
	status.Healthy, status.WorkloadHealthy, status.Message, status.Details, status.Traits = true, false, "", nil, nil
	for _, d := range delivered {
		if d.observed == nil {
			status.Healthy, status.Traits = false, nil
			if d.target.controlPlaneFor == "" {
				// The workload's last report stands until vela-agent reports on this revision, so
				// a render meanwhile keeps the PostDispatch traits a healthy workload was given.
				status.WorkloadHealthy = d.reported != nil && d.reported.WorkloadHealthy
			}
			status.Message = fmt.Sprintf("waiting for vela-agent on cluster %s to apply revision %d of Component %s/%s",
				d.target.cluster, d.component.Spec.Revision, d.component.Namespace, d.component.Name)
			if report := agentReport(d); report != "" {
				status.Message = report
				break
			}
			if d.component.Spec.Revision == 0 {
				status.Message = fmt.Sprintf("Component %s/%s has not been dispatched to cluster %s", d.component.Namespace, d.component.Name, d.target.cluster)
			}
			if late = overdue(d, time.Now(), timeout); late != "" {
				status.Message = late
				if h.recorder != nil {
					h.recorder.Event(h.app, event.Warning(reasonComponentNotApplied, errors.New(late)))
				}
			}
			break
		}
		o := d.observed
		if d.target.controlPlaneFor == "" {
			status.WorkloadHealthy = o.WorkloadHealthy
			status.Details = o.Details
		}
		status.Healthy = status.Healthy && o.Healthy
		if report := agentReport(d); report != "" {
			status.Healthy, status.Message = false, report
		} else if status.Message == "" {
			status.Message = o.Message
		}
		status.Traits = append(status.Traits, o.Traits...)
	}
	if !status.Healthy {
		h.waitingOnAgent.Store(true)
		h.liveMu.Lock()
		for _, d := range delivered {
			key := client.ObjectKey{Namespace: d.target.namespace, Name: d.target.name()}
			if seen := h.live[d.target.cluster+"/"+key.String()]; seen != nil {
				h.waitedOn = append(h.waitedOn, waitedComponent{
					cluster: d.target.cluster, key: key, revision: seen.Spec.Revision, status: *seen.Status.DeepCopy(),
				})
			}
		}
		h.liveMu.Unlock()
	}
	h.recordComponentSourceReads(wl, &status)
	h.addServiceStatus(true, status)
	return status, late
}

// workflowRequeue is when to render again after backoff: no sooner than the render interval once
// this reconcile has found vela-agent yet to report a Component healthy, since vela-agent reports
// in its own time and every render costs the whole Application.
func (h *AppHandler) workflowRequeue(backoff time.Duration) time.Duration {
	if !h.waitingOnAgent.Load() {
		return backoff
	}
	interval := h.agentRenderInterval
	if interval <= 0 {
		interval = defaultComponentAgentRenderInterval
	}
	return max(backoff, interval)
}

// agentReport is what vela-agent says stops d's revision, when its health report does not
// already say it: a refusal, a render it cannot do, collection still running.
func agentReport(d deliveredComponent) string {
	r := d.ready
	if r == nil || r.Status == metav1.ConditionTrue ||
		r.Reason == componentdispatch.ReasonWorkloadUnhealthy || r.Reason == componentdispatch.ReasonTraitUnhealthy {
		return ""
	}
	report := fmt.Sprintf("vela-agent on cluster %s: %s", d.target.cluster, r.Reason)
	if r.Message != "" {
		report += ": " + r.Message
	}
	return report
}

// defaultComponentReconcilerTimeout is how long a Component waits on vela-agent before it is
// reported, when the controller sets none.
const defaultComponentReconcilerTimeout = 2 * time.Minute

// defaultComponentAgentRenderInterval and defaultComponentAgentHealthInterval pace an Application
// waiting on vela-agent when the controller sets neither.
const (
	defaultComponentAgentRenderInterval = 5 * time.Second
	defaultComponentAgentHealthInterval = time.Second
)

// reasonComponentNotApplied is the event reason for a Component vela-agent has not applied in time.
const reasonComponentNotApplied = "ComponentNotApplied"

// toManifest is obj as the keeper applies it: no status, no server-set metadata.
func toManifest(obj runtime.Object) (*unstructured.Unstructured, error) {
	m, err := runtime.DefaultUnstructuredConverter.ToUnstructured(obj)
	if err != nil {
		return nil, err
	}
	u := &unstructured.Unstructured{Object: m}
	delete(u.Object, "status")
	unstructured.RemoveNestedField(u.Object, "metadata", "creationTimestamp")
	return u, nil
}

// keeperOptions holds the delete of a dispatched Component until vela-agent has collected what
// it applied. Without signing keys there is nothing to sign a tombstone with, and a delete goes
// straight through, which vela-agent treats as leaving the resources in place.
func (h *AppHandler) keeperOptions(opts *resourcekeeper.Options) {
	if h.componentKeys != nil {
		opts.BeforeDelete = h.tombstoneFirst
	}
}

// tombstoneFirst lets a Component's delete through once vela-agent has observed its signed
// tombstone, which it does only after collecting everything the Component applied. Until then
// it writes the tombstone if it is missing, and records what the delete waits on. It waits for
// as long as it takes: a cluster that never answers keeps the Component, and its resources.
func (h *AppHandler) tombstoneFirst(ctx context.Context, obj *unstructured.Unstructured) (bool, error) {
	if obj.GroupVersionKind() != v1alpha1.ComponentGroupVersionKind {
		return true, nil
	}
	live := &v1alpha1.Component{}
	if err := h.reader().Get(ctx, client.ObjectKeyFromObject(obj), live); err != nil {
		if kerrors.IsNotFound(err) {
			return true, nil
		}
		return false, err
	}
	if live.Spec.Delete && live.Status.ObservedRevision >= live.Spec.Revision {
		return true, nil
	}
	if !live.Spec.Delete {
		live.Spec = v1alpha1.ComponentSpec{
			Target:   live.Spec.Target,
			Revision: live.Spec.Revision + 1,
			Delete:   true,
			Apply:    live.Spec.Apply,
		}
		ring, err := h.keyRing(ctx)
		if err != nil {
			return false, errors.WithMessage(err, "loading the component signing key")
		}
		if err := ring.Signer().Sign(live); err != nil {
			return false, err
		}
		if err := h.Client.Update(ctx, live); err != nil {
			return false, errors.WithMessagef(err, "writing the tombstone for component %s/%s", live.Namespace, live.Name)
		}
	}
	h.tombstoneWaits = append(h.tombstoneWaits, fmt.Sprintf(
		"Waiting for vela-agent on cluster %s to collect the resources of Component %s/%s (tombstone, revision %d)",
		live.Spec.Target.Cluster, live.Namespace, live.Name, live.Spec.Revision))
	return false, nil
}

// observedObjects is a dispatched component's resources at one placement, rebuilt from what
// vela-agent observed of them. Its control-plane Component on the hub counts as this
// placement's only when no other placement in the cluster could own it.
func (h *AppHandler) observedObjects(ctx context.Context, component, cluster, ns string,
	counts placementCounts) ([]*unstructured.Unstructured, []*unstructured.Unstructured, error) {
	cluster = normaliseCluster(cluster)
	labels := func(extra string, value string) client.MatchingLabels {
		return client.MatchingLabels{oam.LabelAppName: h.app.Name, oam.LabelAppNamespace: h.app.Namespace,
			oam.LabelAppComponent: component, extra: value}
	}
	var comps []v1alpha1.Component
	list := func(onCluster, namespace string, match client.MatchingLabels, controlPlane bool) error {
		var l v1alpha1.ComponentList
		if err := h.reader().List(multicluster.ContextWithClusterName(ctx, onCluster), &l, client.InNamespace(namespace), match); err != nil {
			return errors.WithMessagef(err, "listing the Components of %q on cluster %s", component, onCluster)
		}
		for _, c := range l.Items {
			if (c.Labels[oam.LabelControlPlaneFor] != "") == controlPlane {
				comps = append(comps, c)
			}
		}
		return nil
	}
	if err := list(cluster, ns, labels(oam.LabelComponentCluster, cluster), false); err != nil {
		return nil, nil, err
	}
	if cluster != multicluster.ClusterLocalName && counts.inCluster == 1 {
		if err := list(multicluster.ClusterLocalName, h.app.Namespace, labels(oam.LabelControlPlaneFor, cluster), true); err != nil {
			return nil, nil, err
		}
	}

	var workloads, outputs []*unstructured.Unstructured
	for i := range comps {
		c := &comps[i]
		if c.Spec.Revision == 0 || c.Status.ObservedRevision != c.Spec.Revision {
			return nil, nil, sources.ComponentReadNotReady{Reason: fmt.Sprintf(
				"waiting for vela-agent on cluster %s to observe revision %d of Component %s/%s",
				c.Spec.Target.Cluster, c.Spec.Revision, c.Namespace, c.Name)}
		}
		w, o, err := observedResources(c, func(sealed string) ([]byte, error) {
			return h.openSealed(ctx, c.Status.SealedWith, sealed)
		})
		if err != nil {
			return nil, nil, err
		}
		workloads, outputs = append(workloads, w...), append(outputs, o...)
	}
	return workloads, outputs, nil
}

// readsOfProducer is every read the Application's components make of producer, below it:
// what its Components are asked to observe.
func (h *AppHandler) readsOfProducer(producer string, all []common.ApplicationComponent) ([][]string, error) {
	d := h.componentReads()
	if !d.enabled {
		return nil, nil
	}
	var out [][]string
	for _, c := range all {
		reads, err := d.readsOf(c)
		if err != nil {
			return nil, err
		}
		for _, r := range reads {
			if r.Producer == producer {
				out = append(out, r.Path)
			}
		}
	}
	return out, nil
}

// componentPlacement is where one of a component's Components delivers it.
type componentPlacement struct {
	cluster, namespace, replicaKey string
}

// apply applies comp at the placement, as the replica it delivers.
func (p componentPlacement) apply(ctx context.Context, apply oamprovidertypes.ComponentApply, comp common.ApplicationComponent) error {
	comp.ReplicaKey = p.replicaKey
	_, _, _, _, err := apply(ctx, comp, nil, p.cluster, p.namespace)
	return err
}

// dispatchedPlacements is every placement of component that the Application's Components
// deliver, replica keys included, read from those Components. A hub control-plane Component
// follows the placement it serves and is left out.
func (h *AppHandler) dispatchedPlacements(ctx context.Context, component string) ([]componentPlacement, error) {
	_, dispatched := splitTracked(h.resourceKeeper.TrackedComponentResources(component))
	comps, err := h.liveComponents(ctx, dispatched)
	if err != nil {
		return nil, err
	}
	return placementsOf(comps), nil
}

// placementsOf is where comps deliver their component, leaving out hub control-plane Components.
func placementsOf(comps []*v1alpha1.Component) []componentPlacement {
	var out []componentPlacement
	for _, c := range comps {
		if _, cp := c.Labels[oam.LabelControlPlaneFor]; cp || c.Spec.Delete {
			continue
		}
		out = append(out, componentPlacement{cluster: c.Labels[oam.LabelComponentCluster], namespace: c.Namespace, replicaKey: c.Labels[oam.LabelReplicaKey]})
	}
	return out
}

// resignComponents dispatches a component again, at each of its placements, when any of its
// Components is signed with a key other than the active one: after a rotation the old key is
// retired only once no Component carries it. The spec is unchanged, so the revision is too.
func (h *AppHandler) resignComponents(ctx monitorContext.Context, appParser *appfile.Parser, af *appfile.Appfile) error {
	ring, err := h.keyRing(ctx.GetContext())
	if err != nil {
		return errors.WithMessage(err, "loading the component signing key")
	}
	apply := h.applyComponentFunc(appParser, af)
	var errs []error
	for _, comp := range h.app.Spec.Components {
		_, dispatched := splitTracked(h.resourceKeeper.TrackedComponentResources(comp.Name))
		comps, err := h.liveComponents(ctx.GetContext(), dispatched)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		stale := false
		for _, c := range comps {
			stale = stale || (!c.Spec.Delete && c.Spec.Signature.KeyID != ring.Active)
		}
		if !stale {
			continue
		}
		for _, p := range placementsOf(comps) {
			if err := p.apply(ctx.GetContext(), apply, comp); err != nil {
				errs = append(errs, errors.WithMessagef(err, "signing the Components of component %s with key %s", comp.Name, ring.Active))
			}
		}
	}
	return velaerrors.AggregateErrors(errs)
}

// appliedInProcess reports whether the Application's trackers still record resources of
// component that it applied itself, which its Components then adopt in place.
func (h *AppHandler) appliedInProcess(component string) bool {
	inProcess, _ := splitTracked(h.resourceKeeper.TrackedComponentResources(component))
	return len(inProcess) > 0
}

// splitTracked separates the records of resources an Application applied itself from those
// of the Components, and their payload Secrets, it dispatched.
func splitTracked(tracked []v1beta1.ManagedResource) (inProcess, dispatched []v1beta1.ManagedResource) {
	payloads := map[string]bool{}
	for _, mr := range tracked {
		if isComponentRecord(mr) {
			payloads[mr.Cluster+"/"+mr.Namespace+"/"+mr.Name+payloadSuffix] = true
		}
	}
	for _, mr := range tracked {
		if isComponentRecord(mr) || (mr.Kind == "Secret" && payloads[mr.Cluster+"/"+mr.Namespace+"/"+mr.Name]) {
			dispatched = append(dispatched, mr)
			continue
		}
		inProcess = append(inProcess, mr)
	}
	return inProcess, dispatched
}

// completeHandover finishes a change of dispatch mode, before each state keep. It reports
// whether one is in progress, and what it waits on. A dispatched component's Application forgets the resources its
// Components have adopted, collects what none of them carries, and once nothing is left
// dispatches the component again without adoptFrom. A healthy in-process component retires
// the Components it was dispatched as: payloads first, then each Component through its
// tombstone, which vela-agent collects without touching the resources the Application has
// taken back.
func (h *AppHandler) completeHandover(ctx monitorContext.Context, appParser *appfile.Parser, af *appfile.Appfile) (bool, []string, error) {
	var errs []error
	var waits []string
	inProgress := false
	redispatch := map[string]common.ApplicationComponent{}
	delivery := h.delivery()
	for _, comp := range h.app.Spec.Components {
		inProcess, dispatched := splitTracked(h.resourceKeeper.TrackedComponentResources(comp.Name))
		step := delivery.handOver(ctx, comp.Name, inProcess, dispatched)
		inProgress = inProgress || step.inProgress
		switch {
		case step.err != nil:
			errs = append(errs, step.err)
		case step.wait != "":
			waits = append(waits, step.wait)
		case step.again:
			redispatch[comp.Name] = comp
		}
	}
	if len(redispatch) > 0 {
		apply := h.applyComponentFunc(appParser, af)
		for _, comp := range redispatch {
			placements, err := h.dispatchedPlacements(ctx.GetContext(), comp.Name)
			if err != nil {
				errs = append(errs, err)
				continue
			}
			for _, p := range placements {
				if err := p.apply(ctx.GetContext(), apply, comp); err != nil {
					errs = append(errs, errors.WithMessagef(err, "dispatching component %s without adoptFrom", comp.Name))
				}
			}
		}
	}
	return inProgress, waits, velaerrors.AggregateErrors(errs)
}

// handOver moves one component's resources from its Application to its Components. It says
// what it waits on, or whether the component is to be dispatched again without adoptFrom.
func (h *AppHandler) handOver(ctx context.Context, component string, inProcess, dispatched []v1beta1.ManagedResource) (string, bool, error) {
	comps, err := h.liveComponents(ctx, dispatched)
	if err != nil {
		return "", false, err
	}
	if len(inProcess) > 0 {
		forgotten, err := h.resourceKeeper.ForgetTakenOver(ctx, inProcess)
		if err != nil {
			return "", false, err
		}
		gone := map[string]bool{}
		for _, mr := range forgotten {
			gone[mr.ResourceKey()] = true
		}
		var remaining []v1beta1.ManagedResource
		for _, mr := range inProcess {
			if !gone[mr.ResourceKey()] {
				remaining = append(remaining, mr)
			}
		}
		if len(remaining) > 0 {
			if !adoptionApplied(comps) {
				return fmt.Sprintf("component %s: waiting for vela-agent to adopt its resources", component), false, nil
			}
			var carried []string
			for _, mr := range remaining {
				if c := carrier(mr, comps); c != nil {
					carried = append(carried, fmt.Sprintf("%s %s/%s (Component %s/%s)", mr.Kind, mr.Namespace, mr.Name, c.Namespace, c.Name))
					continue
				}
				// No Component carries it: a resource the component no longer renders.
				if err := h.resourceKeeper.Delete(ctx, []*unstructured.Unstructured{mr.ToUnstructured()}); err != nil {
					return "", false, err
				}
			}
			if len(carried) > 0 {
				return fmt.Sprintf("component %s: waiting for vela-agent to adopt %s", component, strings.Join(carried, ", ")), false, nil
			}
		}
	}
	for _, c := range comps {
		if c.Spec.Apply.AdoptFrom != "" {
			return "", true, nil
		}
	}
	return "", false, nil
}

// liveComponents reads the recorded Components that still exist.
func (h *AppHandler) liveComponents(ctx context.Context, dispatched []v1beta1.ManagedResource) ([]*v1alpha1.Component, error) {
	var out []*v1alpha1.Component
	for _, mr := range dispatched {
		if !isComponentRecord(mr) {
			continue
		}
		live := &v1alpha1.Component{}
		err := h.reader().Get(multicluster.ContextWithClusterName(ctx, mr.Cluster), mr.NamespacedName(), live)
		if kerrors.IsNotFound(err) {
			continue
		}
		if err != nil {
			return nil, errors.WithMessagef(err, "reading component %s on cluster %s", mr.NamespacedName(), mr.Cluster)
		}
		out = append(out, live)
	}
	return out, nil
}

// adoptionApplied reports whether vela-agent has applied, on every Component, a revision
// that adopts from the Application.
func adoptionApplied(comps []*v1alpha1.Component) bool {
	for _, c := range comps {
		if c.Spec.Apply.AdoptFrom == "" || c.Spec.Revision == 0 || c.Status.ObservedRevision != c.Spec.Revision {
			return false
		}
	}
	return len(comps) > 0
}

// carrier is the Component whose resources include mr's, if any. Until vela-agent has
// rendered a Component's PostDispatch traits on its current revision, it carries every
// resource of their types; after, the ones it reports.
func carrier(mr v1beta1.ManagedResource, comps []*v1alpha1.Component) *v1alpha1.Component {
	for _, c := range comps {
		if normaliseCluster(c.Spec.Target.Cluster) != normaliseCluster(mr.Cluster) {
			continue
		}
		for _, r := range c.Spec.Resources {
			if r.APIVersion == mr.APIVersion && r.Kind == mr.Kind && r.Namespace == mr.Namespace && r.Name == mr.Name {
				return c
			}
		}
		if mr.Trait == "" || c.Spec.PostRender == nil {
			continue
		}
		if c.Status.ObservedRevision == c.Spec.Revision && apimeta.IsStatusConditionTrue(c.Status.Conditions, componentdispatch.ConditionPostRendered) {
			for _, r := range c.Status.Resources {
				if r.APIVersion == mr.APIVersion && r.Kind == mr.Kind && r.Namespace == mr.Namespace && r.Name == mr.Name {
					return c
				}
			}
			continue
		}
		for _, t := range c.Spec.PostRender.Traits {
			if t.Type == mr.Trait {
				return c
			}
		}
	}
	return nil
}

// healthyEverywhere reports whether component is placed, and healthy wherever it is.
func (h *AppHandler) healthyEverywhere(component string) bool {
	placed := false
	for _, svc := range h.services {
		if svc.Name == component {
			if !svc.Healthy {
				return false
			}
			placed = true
		}
	}
	return placed
}

// dispatchedFrom is the owner key of every Component the Application's trackers record for
// component: what an in-process apply adopts from when the component is handed back.
func (h *AppHandler) dispatchedFrom(component string) []string {
	var keys []string
	for _, mr := range h.resourceKeeper.TrackedComponentResources(component) {
		if isComponentRecord(mr) {
			keys = append(keys, v1alpha1.ComponentKind+"/"+mr.Namespace+"/"+mr.Name)
		}
	}
	return keys
}

func isComponentRecord(mr v1beta1.ManagedResource) bool {
	return mr.Kind == v1alpha1.ComponentKind && mr.APIVersion == v1alpha1.SchemeGroupVersion.String()
}

type adoptFromKey struct{}

// contextWithAdoptFrom has the applies made with ctx adopt resources the owners with keys
// control.
func contextWithAdoptFrom(ctx context.Context, keys []string) context.Context {
	if len(keys) == 0 {
		return ctx
	}
	return context.WithValue(ctx, adoptFromKey{}, keys)
}

// adoptFromContext is the apply option contextWithAdoptFrom asked for, if any.
func adoptFromContext(ctx context.Context) []apply.ApplyOption {
	keys, _ := ctx.Value(adoptFromKey{}).([]string)
	if len(keys) == 0 {
		return nil
	}
	return []apply.ApplyOption{apply.AdoptFrom(keys...)}
}
