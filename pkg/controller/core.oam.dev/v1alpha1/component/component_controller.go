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

// Package component is vela-agent's reconciler: it verifies each Component the hub signed,
// applies its rendered resources, tracks and garbage-collects them, and reports health.
package component

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	kerrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/client-go/tools/record"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	ctrlcache "sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"

	"github.com/oam-dev/kubevela/apis/core.oam.dev/common"
	"github.com/oam-dev/kubevela/apis/core.oam.dev/v1alpha1"
	"github.com/oam-dev/kubevela/apis/core.oam.dev/v1beta1"
	"github.com/oam-dev/kubevela/pkg/componentdispatch"
	"github.com/oam-dev/kubevela/pkg/oam"
	"github.com/oam-dev/kubevela/pkg/resourcekeeper"
	"github.com/oam-dev/kubevela/pkg/resourcetracker"
)

// Finalizer holds a Component until its resources are collected or deliberately left.
const Finalizer = "component.oam.dev/resource-tracker"

// Condition types on Component.status: one per stage, in the order a revision passes them.
const (
	ConditionVerified        = "Verified"
	ConditionApplied         = "Applied"
	ConditionWorkloadHealthy = "WorkloadHealthy"
	ConditionPostRendered    = componentdispatch.ConditionPostRendered
	ConditionTraitsHealthy   = "TraitsHealthy"
	ConditionCollected       = "Collected"
)

// Condition types that summarise the stages: Healthy is WorkloadHealthy and TraitsHealthy
// together, and Ready, Reconciling and Stalled are what kstatus reads.
const (
	ConditionHealthy     = "Healthy"
	ConditionReady       = componentdispatch.ConditionReady
	ConditionReconciling = "Reconciling"
	ConditionStalled     = "Stalled"
)

// Reasons on the stage conditions.
const (
	ReasonNotReached        = "NotReached"
	ReasonNoneDeclared      = "NoneDeclared"
	ReasonWorkloadUnhealthy = componentdispatch.ReasonWorkloadUnhealthy
	ReasonTraitUnhealthy    = componentdispatch.ReasonTraitUnhealthy
	ReasonDeleted           = "Deleted"
)

// Reasons for a Verified=False condition, also used as warning event reasons.
const (
	ReasonUnsigned        = "Unsigned"
	ReasonUntrustedKey    = "UntrustedKey"
	ReasonBadSignature    = "BadSignature"
	ReasonWrongCluster    = "WrongCluster"
	ReasonStaleRevision   = "StaleRevision"
	ReasonPayloadMismatch = "PayloadMismatch"
	ReasonPayloadMissing  = "PayloadMissing"
	ReasonStaleTracker    = "StaleTracker"
)

const (
	defaultResync        = 5 * time.Minute
	defaultHealthRecheck = 10 * time.Second
)

// KeySource supplies the hub keys this cluster trusts, and the sealing keys it reports
// sensitive values under.
type KeySource interface {
	TrustedKeys(ctx context.Context) (componentdispatch.TrustedKeys, error)
	SealingKeys(ctx context.Context) (componentdispatch.SealingKeys, error)
}

// StaticKeys is a fixed set of trusted keys, with no sealing keys.
type StaticKeys componentdispatch.TrustedKeys

// TrustedKeys returns the keys.
func (k StaticKeys) TrustedKeys(context.Context) (componentdispatch.TrustedKeys, error) {
	return componentdispatch.TrustedKeys(k), nil
}

// SealingKeys returns none.
func (k StaticKeys) SealingKeys(context.Context) (componentdispatch.SealingKeys, error) {
	return componentdispatch.SealingKeys{}, nil
}

// RingKeys trusts the public half of a hub's key ring, signing and sealing keys both.
type RingKeys struct{ Ring *componentdispatch.KeyRing }

// TrustedKeys returns the ring's signing keys.
func (k RingKeys) TrustedKeys(context.Context) (componentdispatch.TrustedKeys, error) {
	return k.Ring.Trusted(), nil
}

// SealingKeys returns the ring's sealing keys.
func (k RingKeys) SealingKeys(context.Context) (componentdispatch.SealingKeys, error) {
	return k.Ring.Sealing(), nil
}

// Reconciler reconciles Components addressed to Cluster.
type Reconciler struct {
	client.Client
	// Reader reads payload Secrets past the cache, so this cluster caches no Secrets and a
	// revision is checked against the payload written just before it. Unset, Client reads them.
	Reader client.Reader
	Keys   KeySource
	// TrustNamespace holds the trust Secret. When set, a change to it re-checks every Component
	// this cluster has not verified, so one refused for an unknown key goes ahead once the hub's
	// push lands.
	TrustNamespace string
	// Cluster is this cluster's name, as the hub addresses it.
	Cluster  string
	Recorder record.EventRecorder
	// Resync is how often a healthy Component is re-applied and its health re-evaluated.
	Resync time.Duration
	// HealthRecheck is the longest a Component that is not ready waits to be evaluated again.
	// Nothing watches the resources it applied, so this is how it notices them becoming ready.
	HealthRecheck time.Duration
	// unready is when each Component not ready was first found so at its revision.
	unready sync.Map
}

// refusal is a Component vela-agent will not act on, with the reason it reports.
type refusal struct {
	reason string
	err    error
}

func (r *refusal) Error() string { return r.err.Error() }

func refuse(reason string, err error) error { return &refusal{reason: reason, err: err} }

// Reconcile verifies, applies, collects and reports on one Component.
func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	comp := &v1alpha1.Component{}
	if err := r.Get(ctx, req.NamespacedName, comp); err != nil {
		if kerrors.IsNotFound(err) {
			r.unready.Delete(req.NamespacedName)
		}
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	trackers, err := r.trackers(ctx, comp)
	var other *resourcetracker.OtherOwnerError
	if errors.As(err, &other) {
		return r.staleTracker(ctx, comp, other)
	}
	if err != nil {
		return ctrl.Result{}, err
	}
	verifyErr := r.verify(ctx, comp, trackers)
	if comp.DeletionTimestamp != nil {
		// Only a refusal is a bare delete: an error reading the keys is retried, since a
		// verified tombstone collects what a bare delete leaves.
		var ref *refusal
		if verifyErr != nil && !errors.As(verifyErr, &ref) {
			return ctrl.Result{}, verifyErr
		}
		return r.finalize(ctx, comp, verifyErr == nil && comp.Spec.Delete)
	}
	if verifyErr != nil {
		return r.refused(ctx, comp, verifyErr)
	}
	if controllerutil.AddFinalizer(comp, Finalizer) {
		if err := r.Update(ctx, comp); err != nil {
			return ctrl.Result{}, err
		}
	}
	if comp.Spec.Delete {
		return r.collectAll(ctx, comp)
	}
	return r.apply(ctx, comp)
}

// trackers loads the Component's trackers. A tracker of an earlier Component of the same
// name (another UID) is an error the hub must resolve.
func (r *Reconciler) trackers(ctx context.Context, comp *v1alpha1.Component) (resourcetracker.Trackers, error) {
	return tracked(comp, false).LoadTrackers(ctx, r.Client)
}

// verify checks the signature, the target and the revision, then nothing else: the payload is
// checked when it is read.
func (r *Reconciler) verify(ctx context.Context, comp *v1alpha1.Component, trackers resourcetracker.Trackers) error {
	keys, err := r.Keys.TrustedKeys(ctx)
	if err != nil {
		return err
	}
	if err := keys.Verify(comp); err != nil {
		switch {
		case errors.Is(err, componentdispatch.ErrUnsigned):
			return refuse(ReasonUnsigned, err)
		case errors.Is(err, componentdispatch.ErrUntrustedKey):
			return refuse(ReasonUntrustedKey, err)
		default:
			return refuse(ReasonBadSignature, err)
		}
	}
	if comp.Spec.Target.Cluster != r.Cluster {
		return refuse(ReasonWrongCluster, fmt.Errorf("component is addressed to cluster %q, this is %q", comp.Spec.Target.Cluster, r.Cluster))
	}
	if last := lastRevision(comp, trackers); comp.Spec.Revision < last {
		return refuse(ReasonStaleRevision, fmt.Errorf("revision %d is older than revision %d, already applied", comp.Spec.Revision, last))
	}
	return nil
}

// lastRevision is the highest revision applied: from status, and from the trackers, which
// survive a status that is wiped or rewritten.
func lastRevision(comp *v1alpha1.Component, trackers resourcetracker.Trackers) int64 {
	last := comp.Status.ObservedRevision
	for _, rt := range append([]*v1beta1.ResourceTracker{trackers.Current}, trackers.History...) {
		if rt != nil && rt.Spec.ApplicationGeneration > last {
			last = rt.Spec.ApplicationGeneration
		}
	}
	return last
}

func (r *Reconciler) refused(ctx context.Context, comp *v1alpha1.Component, err error) (ctrl.Result, error) {
	var ref *refusal
	if !errors.As(err, &ref) {
		return ctrl.Result{}, err
	}
	r.Recorder.Event(comp, corev1.EventTypeWarning, ref.reason, ref.Error())
	setCondition(comp, ConditionVerified, metav1.ConditionFalse, ref.reason, ref.Error())
	after := r.resync()
	if ref.reason == ReasonPayloadMismatch || ref.reason == ReasonPayloadMissing {
		// The hub writes the payload, then the Component, and rewrites both until they agree.
		after = r.healthRecheck()
	}
	return ctrl.Result{RequeueAfter: after}, r.writeStatus(ctx, comp)
}

// staleTracker reports a tracker under comp's name that an earlier Component of that name left,
// for whoever deletes it to decide what becomes of the resources it records. A deleted comp
// is let go, leaving that tracker as it is.
func (r *Reconciler) staleTracker(ctx context.Context, comp *v1alpha1.Component, other *resourcetracker.OtherOwnerError) (ctrl.Result, error) {
	message := fmt.Sprintf("ResourceTracker %s belongs to an earlier Component %s (uid %s): delete it to let this one apply", other.Tracker, comp.Name, other.UID)
	r.Recorder.Event(comp, corev1.EventTypeWarning, ReasonStaleTracker, message)
	if comp.DeletionTimestamp != nil {
		if !controllerutil.RemoveFinalizer(comp, Finalizer) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, r.Update(ctx, comp)
	}
	// Signature and target are checked without the trackers, so a stale tracker is reported
	// only for a Component this cluster would otherwise apply.
	if err := r.verify(ctx, comp, resourcetracker.Trackers{}); err != nil {
		return r.refused(ctx, comp, err)
	}
	setCondition(comp, ConditionVerified, metav1.ConditionTrue, ConditionVerified, "")
	setCondition(comp, ConditionApplied, metav1.ConditionFalse, ReasonStaleTracker, message)
	return ctrl.Result{RequeueAfter: r.resync()}, r.writeStatus(ctx, comp)
}

func (r *Reconciler) apply(ctx context.Context, comp *v1alpha1.Component) (ctrl.Result, error) {
	setCondition(comp, ConditionVerified, metav1.ConditionTrue, ConditionVerified, "")
	objs, err := r.payload(ctx, comp)
	if err != nil {
		return r.refused(ctx, comp, err)
	}
	keeper, err := resourcekeeper.New(ctx, r.Client, tracked(comp, false), policies(comp), resourcekeeper.Options{})
	if err != nil {
		return ctrl.Result{}, err
	}
	// A new revision is dispatched; between revisions StateKeep re-applies what is tracked,
	// which corrects drift that dispatch skips (it skips a resource whose render hash matches).
	apply := keeper.StateKeep
	if comp.Spec.Revision > comp.Status.ObservedRevision {
		apply = func(ctx context.Context) error { return keeper.Dispatch(ctx, objs, nil) }
	}
	if err := apply(ctx); err != nil {
		setCondition(comp, ConditionApplied, metav1.ConditionFalse, applyFailure(err), err.Error())
		return ctrl.Result{}, errors.Join(err, r.writeStatus(ctx, comp))
	}
	comp.Status.ObservedRevision = comp.Spec.Revision
	setCondition(comp, ConditionApplied, metav1.ConditionTrue, ConditionApplied, "")
	index := comp.Spec.Resources
	r.reportHealth(ctx, comp, index)
	if comp.Spec.PostRender != nil {
		if index, err = r.postRender(ctx, comp, keeper, objs); err != nil {
			return ctrl.Result{}, errors.Join(err, r.writeStatus(ctx, comp))
		}
		r.reportHealth(ctx, comp, index)
	} else {
		setCondition(comp, ConditionPostRendered, metav1.ConditionTrue, ReasonNoneDeclared, "")
	}
	// Collection waits for the PostDispatch traits to render on this revision: until then,
	// what they rendered on the last one is tracked only there.
	finished := false
	if meta.IsStatusConditionTrue(comp.Status.Conditions, ConditionPostRendered) {
		if finished, _, err = keeper.GarbageCollect(ctx); err != nil {
			return ctrl.Result{}, err
		}
	}
	if finished {
		setCondition(comp, ConditionCollected, metav1.ConditionTrue, ConditionCollected, "")
	} else {
		setCondition(comp, ConditionCollected, metav1.ConditionFalse, ReasonCollecting, "collecting what earlier revisions applied")
	}
	comp.Status.Resources = appliedResources(index, keeper.GetAppliedResources())
	if err := r.writeStatus(ctx, comp); err != nil {
		return ctrl.Result{}, err
	}
	if !finished && meta.IsStatusConditionTrue(comp.Status.Conditions, ConditionPostRendered) {
		return ctrl.Result{RequeueAfter: time.Second}, nil
	}
	if !meta.IsStatusConditionTrue(comp.Status.Conditions, ConditionReady) {
		return ctrl.Result{RequeueAfter: r.recheckAfter(comp, time.Now())}, nil
	}
	r.ready(comp)
	return ctrl.Result{RequeueAfter: r.resync()}, nil
}

// appliedResources names what the current revision's tracker holds, with each resource's
// role and definition from the index (the signed one, plus what vela-agent post-rendered), in a stable order so an unchanged list writes no
// new status.
func appliedResources(resources []v1alpha1.ComponentResource, refs []common.ClusterObjectReference) []v1alpha1.ComponentAppliedResource {
	type key struct{ apiVersion, kind, namespace, name string }
	index := make(map[key]v1alpha1.ComponentResource, len(resources))
	for _, res := range resources {
		index[key{res.APIVersion, res.Kind, res.Namespace, res.Name}] = res
	}
	out := make([]v1alpha1.ComponentAppliedResource, 0, len(refs))
	for _, ref := range refs {
		res := v1alpha1.ComponentAppliedResource{APIVersion: ref.APIVersion, Kind: ref.Kind, Namespace: ref.Namespace, Name: ref.Name}
		if entry, ok := index[key{ref.APIVersion, ref.Kind, ref.Namespace, ref.Name}]; ok {
			res.Role, res.Definition = entry.Role, entry.Definition
		}
		out = append(out, res)
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Kind != b.Kind {
			return a.Kind < b.Kind
		}
		if a.Namespace != b.Namespace {
			return a.Namespace < b.Namespace
		}
		return a.Name < b.Name
	})
	return out
}

// payload reads the payload Secret and checks it against the signed digest and index.
func (r *Reconciler) payload(ctx context.Context, comp *v1alpha1.Component) ([]*unstructured.Unstructured, error) {
	if comp.Spec.Payload == nil {
		return nil, refuse(ReasonPayloadMissing, errors.New("component has no payload"))
	}
	var reader client.Reader = r.Client
	if r.Reader != nil {
		reader = r.Reader
	}
	secret := &corev1.Secret{}
	if err := reader.Get(ctx, client.ObjectKey{Namespace: comp.Namespace, Name: comp.Spec.Payload.SecretRef.Name}, secret); err != nil {
		return nil, refuse(ReasonPayloadMissing, fmt.Errorf("reading payload secret %s: %w", comp.Spec.Payload.SecretRef.Name, err))
	}
	if comp.Spec.Payload.Format != componentdispatch.PayloadFormat {
		return nil, refuse(ReasonPayloadMismatch, fmt.Errorf("payload format %q is not %q", comp.Spec.Payload.Format, componentdispatch.PayloadFormat))
	}
	objs, err := componentdispatch.DecodePayload(secret.Data[componentdispatch.PayloadKey], comp.Spec.Payload.SHA256)
	if err != nil {
		return nil, refuse(ReasonPayloadMismatch, err)
	}
	if err := componentdispatch.CheckIndex(objs, comp.Spec.Resources); err != nil {
		return nil, refuse(ReasonPayloadMismatch, err)
	}
	// The keeper routes a resource by its cluster label; a Component's resources are applied
	// only in the cluster it is delivered to.
	for _, obj := range objs {
		if cluster := oam.GetCluster(obj); cluster != "" {
			return nil, refuse(ReasonPayloadMismatch, fmt.Errorf("%s %s/%s names cluster %q: a Component's resources apply only where it is delivered",
				obj.GetKind(), obj.GetNamespace(), obj.GetName(), cluster))
		}
	}
	return objs, nil
}

// collectAll garbage-collects everything the Component tracks, on a verified tombstone.
func (r *Reconciler) collectAll(ctx context.Context, comp *v1alpha1.Component) (ctrl.Result, error) {
	keeper, err := resourcekeeper.New(ctx, r.Client, tracked(comp, true), policies(comp), resourcekeeper.Options{})
	if err != nil {
		return ctrl.Result{}, err
	}
	finished, _, err := keeper.GarbageCollect(ctx)
	if err != nil {
		return ctrl.Result{}, err
	}
	setCondition(comp, ConditionVerified, metav1.ConditionTrue, ConditionVerified, "")
	comp.Status.Health, comp.Status.Observed, comp.Status.SealedObserved, comp.Status.SealedWith = nil, nil, nil, ""
	comp.Status.Resources = nil
	// The tombstone is observed only once everything is collected: the hub deletes the
	// Component on seeing it observed.
	if finished {
		setCondition(comp, ConditionApplied, metav1.ConditionFalse, ReasonDeleted, "tombstone: resources collected")
		comp.Status.ObservedRevision = comp.Spec.Revision
	} else {
		setCondition(comp, ConditionApplied, metav1.ConditionFalse, ReasonDeleting, "tombstone: collecting resources")
	}
	if err := r.writeStatus(ctx, comp); err != nil {
		return ctrl.Result{}, err
	}
	if !finished {
		return ctrl.Result{RequeueAfter: time.Second}, nil
	}
	return ctrl.Result{}, nil
}

// finalize releases a deleted Component. Its resources are collected only under a verified
// tombstone; otherwise they stay where they are, still marked as the Component's, and only
// its trackers go, so the hub can recreate it and take them back.
func (r *Reconciler) finalize(ctx context.Context, comp *v1alpha1.Component, tombstone bool) (ctrl.Result, error) {
	if !controllerutil.ContainsFinalizer(comp, Finalizer) {
		return ctrl.Result{}, nil
	}
	if tombstone {
		res, err := r.collectAll(ctx, comp)
		if err != nil || res.RequeueAfter > 0 {
			return res, err
		}
	} else {
		left, err := r.dropTrackers(ctx, comp)
		if err != nil {
			return ctrl.Result{}, err
		}
		r.Recorder.Eventf(comp, corev1.EventTypeWarning, "DeletedWithoutTombstone",
			"deleted without a signed tombstone; left in place: %v", left)
	}
	controllerutil.RemoveFinalizer(comp, Finalizer)
	return ctrl.Result{}, r.Update(ctx, comp)
}

// dropTrackers deletes the Component's trackers without touching what they record, and
// names what was recorded.
func (r *Reconciler) dropTrackers(ctx context.Context, comp *v1alpha1.Component) ([]string, error) {
	trackers, err := tracked(comp, true).LoadTrackers(ctx, r.Client)
	if err != nil {
		return nil, err
	}
	var left []string
	if trackers.Current != nil {
		for _, mr := range trackers.Current.Spec.ManagedResources {
			left = append(left, fmt.Sprintf("%s %s/%s", mr.Kind, mr.Namespace, mr.Name))
		}
	}
	for _, rt := range append([]*v1beta1.ResourceTracker{trackers.Root, trackers.Current}, trackers.History...) {
		if rt == nil {
			continue
		}
		if controllerutil.RemoveFinalizer(rt, resourcetracker.Finalizer) {
			if err := r.Update(ctx, rt); err != nil {
				return nil, client.IgnoreNotFound(err)
			}
		}
		if err := r.Delete(ctx, rt); client.IgnoreNotFound(err) != nil {
			return nil, err
		}
	}
	return left, nil
}

func (r *Reconciler) resync() time.Duration {
	if r.Resync > 0 {
		return r.Resync
	}
	return defaultResync
}

// unreadyAt is when a Component was first found not ready at revision.
type unreadyAt struct {
	revision int64
	since    time.Time
}

// recheckAfter is how soon to evaluate again comp, not ready at now: as long again as it has
// been not ready at this revision, from a second up to the health recheck, so a workload that
// turns ready a moment after it is applied is seen a moment later.
func (r *Reconciler) recheckAfter(comp *v1alpha1.Component, now time.Time) time.Duration {
	key := client.ObjectKeyFromObject(comp)
	v, ok := r.unready.Load(key)
	u, _ := v.(unreadyAt)
	if !ok || u.revision != comp.Spec.Revision {
		u = unreadyAt{revision: comp.Spec.Revision, since: now}
		r.unready.Store(key, u)
	}
	return min(max(now.Sub(u.since), time.Second), r.healthRecheck())
}

// ready forgets when comp was found not ready.
func (r *Reconciler) ready(comp *v1alpha1.Component) {
	r.unready.Delete(client.ObjectKeyFromObject(comp))
}

func (r *Reconciler) healthRecheck() time.Duration {
	if r.HealthRecheck > 0 {
		return r.HealthRecheck
	}
	return defaultHealthRecheck
}

// tracked is the Component as a ResourceTracker owner. Its revision is the tracker
// generation, so each revision applied gets a versioned tracker and the older ones are
// garbage-collected against it.
func tracked(comp *v1alpha1.Component, deleting bool) resourcetracker.Tracked {
	return resourcetracker.NewBase(resourcetracker.Owner{
		Kind: v1alpha1.ComponentKind, Namespace: comp.Namespace, Name: comp.Name, UID: comp.UID,
		Generation: comp.Spec.Revision, Deleting: deleting,
	})
}

func policies(comp *v1alpha1.Component) resourcekeeper.Policies {
	a := comp.Spec.Apply
	return resourcekeeper.Policies{
		ApplyOnce: a.ApplyOnce, GarbageCollect: a.GarbageCollect, SharedResource: a.SharedResource,
		TakeOver: a.TakeOver, ReadOnly: a.ReadOnly, ResourceUpdate: a.ResourceUpdate,
		AdoptFrom: a.AdoptFrom,
	}
}

func setCondition(comp *v1alpha1.Component, condType string, status metav1.ConditionStatus, reason, message string) {
	meta.SetStatusCondition(&comp.Status.Conditions, metav1.Condition{
		Type: condType, Status: status, Reason: reason, Message: message, ObservedGeneration: comp.Generation,
	})
}

// SetupWithManager registers the reconciler.
func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
	if r.Reader == nil {
		r.Reader = mgr.GetAPIReader()
	}
	if r.Recorder == nil {
		r.Recorder = mgr.GetEventRecorderFor("vela-agent") //nolint:staticcheck // the application controller records with the same API
	}
	klog.InfoS("Setting up the Component controller", "cluster", r.Cluster)
	b := ctrl.NewControllerManagedBy(mgr).
		Named("component").
		For(&v1alpha1.Component{})
	if r.TrustNamespace != "" {
		// A cache of the trust Secret alone, so this cluster caches no other Secret.
		trust, err := ctrlcache.New(mgr.GetConfig(), ctrlcache.Options{
			Scheme: mgr.GetScheme(), Mapper: mgr.GetRESTMapper(), HTTPClient: mgr.GetHTTPClient(),
			ByObject: map[client.Object]ctrlcache.ByObject{&corev1.Secret{}: {
				Namespaces: map[string]ctrlcache.Config{r.TrustNamespace: {}},
				Field:      fields.OneTermEqualSelector("metadata.name", componentdispatch.TrustSecret),
			}},
		})
		if err != nil {
			return err
		}
		if err := mgr.Add(trust); err != nil {
			return err
		}
		b = b.WatchesRawSource(source.Kind(trust, &corev1.Secret{}, handler.TypedEnqueueRequestsFromMapFunc(r.unverified)))
	}
	return b.Complete(r)
}

// unverified is every Component on this cluster that is not verified, to check again when the
// trust Secret changes.
func (r *Reconciler) unverified(ctx context.Context, _ *corev1.Secret) []reconcile.Request {
	list := &v1alpha1.ComponentList{}
	if err := r.List(ctx, list); err != nil {
		klog.ErrorS(err, "Listing Components to check again against the trusted keys")
		return nil
	}
	var requests []reconcile.Request
	for i := range list.Items {
		if meta.IsStatusConditionTrue(list.Items[i].Status.Conditions, ConditionVerified) {
			continue
		}
		requests = append(requests, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&list.Items[i])})
	}
	return requests
}
