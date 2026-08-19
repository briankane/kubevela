/*
Copyright 2021 The KubeVela Authors.

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
	"maps"
	"slices"
	"sort"
	"strings"
	"sync"

	"github.com/pkg/errors"
	corev1 "k8s.io/api/core/v1"
	kerrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	utilfeature "k8s.io/apiserver/pkg/util/feature"
	"sigs.k8s.io/controller-runtime/pkg/client"

	monitorContext "github.com/kubevela/pkg/monitor/context"
	pkgmulticluster "github.com/kubevela/pkg/multicluster"
	terraformtypes "github.com/oam-dev/terraform-controller/api/types"
	terraforv1beta1 "github.com/oam-dev/terraform-controller/api/v1beta1"
	terraforv1beta2 "github.com/oam-dev/terraform-controller/api/v1beta2"

	"github.com/oam-dev/kubevela/apis/core.oam.dev/common"
	"github.com/oam-dev/kubevela/apis/core.oam.dev/v1beta1"
	"github.com/oam-dev/kubevela/apis/types"
	"github.com/oam-dev/kubevela/pkg/appfile"
	cuedefinition "github.com/oam-dev/kubevela/pkg/cue/definition"
	velaprocess "github.com/oam-dev/kubevela/pkg/cue/process"
	"github.com/oam-dev/kubevela/pkg/features"
	"github.com/oam-dev/kubevela/pkg/monitor/metrics"
	"github.com/oam-dev/kubevela/pkg/multicluster"
	"github.com/oam-dev/kubevela/pkg/oam"
	"github.com/oam-dev/kubevela/pkg/oam/util"
	"github.com/oam-dev/kubevela/pkg/resourcekeeper"
)

// AppHandler handles application reconcile
type AppHandler struct {
	client.Client

	app            *v1beta1.Application
	currentAppRev  *v1beta1.ApplicationRevision
	latestAppRev   *v1beta1.ApplicationRevision
	resourceKeeper resourcekeeper.ResourceKeeper

	isNewRevision  bool
	currentRevHash string

	services         []common.ApplicationComponentStatus
	appliedResources []common.ClusterObjectReference
	deletedResources []common.ClusterObjectReference

	// sourceStatuses accumulates one entry per spec.sources[] binding across every
	// render in this reconcile. Keyed by binding name, because a binding is
	// declared once for the whole Application even though many surfaces read it.
	sourceStatuses map[string]*common.ApplicationSourceStatus

	// Application-scoped PolicyDefinitions that were resolved and applied
	// These need to be stored in the ApplicationRevision for version pinning
	applicationScopedPolicyDefs map[string]*v1beta1.PolicyDefinition

	// policyVersions stores version metadata for each policy (parallel to applicationScopedPolicyDefs)
	policyVersions map[string]v1beta1.PolicyVersionMetadata

	mu sync.Mutex
}

// NewAppHandler create new app handler
func NewAppHandler(ctx context.Context, r *Reconciler, app *v1beta1.Application) (*AppHandler, error) {
	if ctx, ok := ctx.(monitorContext.Context); ok {
		subCtx := ctx.Fork("create-app-handler", monitorContext.DurationMetric(func(v float64) {
			metrics.AppReconcileStageDurationHistogram.WithLabelValues("create-app-handler").Observe(v)
		}))
		defer subCtx.Commit("finish create appHandler")
	}
	resourceHandler, err := resourcekeeper.NewResourceKeeper(ctx, r.Client, app)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to create resourceKeeper")
	}
	return &AppHandler{
		Client:                      r.Client,
		app:                         app,
		resourceKeeper:              resourceHandler,
		applicationScopedPolicyDefs: make(map[string]*v1beta1.PolicyDefinition),
	}, nil
}

// Dispatch apply manifests into k8s.
func (h *AppHandler) Dispatch(ctx context.Context, _ client.Client, cluster string, owner string, manifests ...*unstructured.Unstructured) error {
	manifests = multicluster.ResourcesWithClusterName(cluster, manifests...)
	if err := h.resourceKeeper.Dispatch(ctx, manifests, nil); err != nil {
		return err
	}
	for _, mf := range manifests {
		if mf == nil {
			continue
		}
		if oam.GetCluster(mf) != "" {
			cluster = oam.GetCluster(mf)
		}
		ref := common.ClusterObjectReference{
			Cluster: cluster,
			Creator: owner,
			ObjectReference: corev1.ObjectReference{
				Name:       mf.GetName(),
				Namespace:  mf.GetNamespace(),
				Kind:       mf.GetKind(),
				APIVersion: mf.GetAPIVersion(),
			},
		}
		h.addAppliedResource(false, ref)
	}
	return nil
}

// Delete delete manifests from k8s.
func (h *AppHandler) Delete(ctx context.Context, _ client.Client, cluster string, owner string, manifest *unstructured.Unstructured) error {
	manifests := multicluster.ResourcesWithClusterName(cluster, manifest)
	if err := h.resourceKeeper.Delete(ctx, manifests); err != nil {
		return err
	}
	ref := common.ClusterObjectReference{
		Cluster: cluster,
		Creator: owner,
		ObjectReference: corev1.ObjectReference{
			Name:       manifest.GetName(),
			Namespace:  manifest.GetNamespace(),
			Kind:       manifest.GetKind(),
			APIVersion: manifest.GetAPIVersion(),
		},
	}
	h.deleteAppliedResource(ref)
	return nil
}

// addAppliedResource recorde applied resource.
// reconcile run at single threaded. So there is no need to consider to use locker.
func (h *AppHandler) addAppliedResource(previous bool, refs ...common.ClusterObjectReference) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, ref := range refs {
		if previous {
			for i, deleted := range h.deletedResources {
				if deleted.Equal(ref) {
					h.deletedResources = removeResources(h.deletedResources, i)
					return
				}
			}
		}

		found := false
		for _, current := range h.appliedResources {
			if current.Equal(ref) {
				found = true
				break
			}
		}
		if !found {
			h.appliedResources = append(h.appliedResources, ref)
		}
	}
}

func (h *AppHandler) deleteAppliedResource(ref common.ClusterObjectReference) {
	delIndex := -1
	for i, current := range h.appliedResources {
		if current.Equal(ref) {
			delIndex = i
		}
	}
	if delIndex < 0 {
		isDeleted := false
		for _, deleted := range h.deletedResources {
			if deleted.Equal(ref) {
				isDeleted = true
				break
			}
		}
		if !isDeleted {
			h.deletedResources = append(h.deletedResources, ref)
		}
	} else {
		h.appliedResources = removeResources(h.appliedResources, delIndex)
	}

}

func removeResources(elements []common.ClusterObjectReference, index int) []common.ClusterObjectReference {
	elements[index] = elements[len(elements)-1]
	return elements[:len(elements)-1]
}

// getServiceStatus get specified component status
func (h *AppHandler) getServiceStatus(svc common.ApplicationComponentStatus) common.ApplicationComponentStatus {
	for i := range h.services {
		current := h.services[i]
		if current.Equal(svc) {
			return current
		}
	}
	return svc
}

// addServiceStatus recorde the whole component status.
// reconcile run at single threaded. So there is no need to consider to use locker.
func (h *AppHandler) addServiceStatus(cover bool, svcs ...common.ApplicationComponentStatus) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, svc := range svcs {
		found := false
		for i := range h.services {
			current := h.services[i]
			if current.Equal(svc) {
				if cover {
					h.services[i] = svc
				}
				found = true
				break
			}
		}
		if !found {
			h.services = append(h.services, svc)
		}
	}
}

// collectTraitHealthStatus collect trait health status
func (h *AppHandler) collectTraitHealthStatus(comp *appfile.Component, tr *appfile.Trait, overrideNamespace string) (common.ApplicationTraitStatus, []*unstructured.Unstructured, error) {
	defer func(clusterName string) {
		comp.Ctx.SetCtx(pkgmulticluster.WithCluster(comp.Ctx.GetCtx(), clusterName))
	}(multicluster.ClusterNameInContext(comp.Ctx.GetCtx()))
	appRev := h.currentAppRev
	var (
		pCtx        = comp.Ctx
		appName     = appRev.Spec.Application.Name
		traitStatus = common.ApplicationTraitStatus{
			Type:    tr.Name,
			Healthy: true,
		}
		traitOverrideNamespace = overrideNamespace
		err                    error
	)
	if tr.FullTemplate.TraitDefinition.Spec.ControlPlaneOnly {
		traitOverrideNamespace = appRev.GetNamespace()
		pCtx.SetCtx(pkgmulticluster.WithCluster(pCtx.GetCtx(), pkgmulticluster.Local))
	}
	_accessor := util.NewApplicationResourceNamespaceAccessor(h.app.Namespace, traitOverrideNamespace)
	templateContext, err := tr.GetTemplateContext(pCtx, h.Client, _accessor)
	if err != nil {
		return common.ApplicationTraitStatus{}, nil, errors.WithMessagef(err, "app=%s, comp=%s, trait=%s, get template context error", appName, comp.Name, tr.Name)
	}
	if err != nil {
		return common.ApplicationTraitStatus{}, nil, errors.WithMessagef(err, "app=%s, comp=%s, trait=%s, evaluate status message error", appName, comp.Name, tr.Name)
	}
	statusResult, err := tr.EvalStatus(templateContext)
	if err == nil && statusResult != nil {
		traitStatus.Healthy = statusResult.Healthy
		traitStatus.Message = statusResult.Message
		traitStatus.Details = statusResult.Details
	}
	return traitStatus, extractOutputs(templateContext), err
}

// collectWorkloadHealthStatus collect workload health status
func (h *AppHandler) collectWorkloadHealthStatus(ctx context.Context, comp *appfile.Component, status *common.ApplicationComponentStatus, accessor util.NamespaceAccessor) (bool, *unstructured.Unstructured, []*unstructured.Unstructured, error) {
	var output *unstructured.Unstructured
	var outputs []*unstructured.Unstructured
	var (
		appRev  = h.currentAppRev
		appName = appRev.Spec.Application.Name
	)
	if comp.CapabilityCategory == types.TerraformCategory {
		var configuration terraforv1beta2.Configuration
		if err := h.Client.Get(ctx, client.ObjectKey{Name: comp.Name, Namespace: accessor.Namespace()}, &configuration); err != nil {
			if kerrors.IsNotFound(err) {
				var legacyConfiguration terraforv1beta1.Configuration
				if err := h.Client.Get(ctx, client.ObjectKey{Name: comp.Name, Namespace: accessor.Namespace()}, &legacyConfiguration); err != nil {
					return false, nil, nil, errors.WithMessagef(err, "app=%s, comp=%s, check health error", appName, comp.Name)
				}
				setStatus(status, legacyConfiguration.Status.ObservedGeneration, legacyConfiguration.Generation,
					legacyConfiguration.GetLabels(), appRev.Name, legacyConfiguration.Status.Apply.State, legacyConfiguration.Status.Apply.Message)
			} else {
				return false, nil, nil, errors.WithMessagef(err, "app=%s, comp=%s, check health error", appName, comp.Name)
			}
		} else {
			setStatus(status, configuration.Status.ObservedGeneration, configuration.Generation, configuration.GetLabels(),
				appRev.Name, configuration.Status.Apply.State, configuration.Status.Apply.Message)
		}
	} else {
		templateContext, err := comp.GetTemplateContext(comp.Ctx, h.Client, accessor)
		if err != nil {
			return false, nil, nil, errors.WithMessagef(err, "app=%s, comp=%s, get template context error", appName, comp.Name)
		}
		statusResult, err := comp.EvalStatus(templateContext)
		if err != nil {
			return false, nil, nil, errors.WithMessagef(err, "app=%s, comp=%s, evaluate workload status message error", appName, comp.Name)
		}
		if statusResult != nil {
			status.Healthy = statusResult.Healthy
			if statusResult.Message != "" {
				status.Message = statusResult.Message
			}
			if statusResult.Details != nil {
				status.Details = statusResult.Details
			}
		} else {
			status.Healthy = false
		}
		output, outputs = extractOutputAndOutputs(templateContext)
	}
	return status.Healthy, output, outputs, nil
}

// nolint
// collectHealthStatus will collect health status of component, including component itself and traits.
func (h *AppHandler) collectHealthStatus(ctx context.Context, comp *appfile.Component, overrideNamespace string, skipWorkload bool, traitFilters ...TraitFilter) (*common.ApplicationComponentStatus, *unstructured.Unstructured, []*unstructured.Unstructured, bool, error) {
	output := new(unstructured.Unstructured)
	outputs := make([]*unstructured.Unstructured, 0)
	accessor := util.NewApplicationResourceNamespaceAccessor(h.app.Namespace, overrideNamespace)
	var (
		status = common.ApplicationComponentStatus{
			Name:               comp.Name,
			WorkloadDefinition: comp.FullTemplate.Reference.Definition,
			Healthy:            true,
			Details:            make(map[string]string),
			Namespace:          accessor.Namespace(),
			Cluster:            multicluster.ClusterNameInContext(ctx),
		}
		isHealth = true
		err      error
	)

	status = h.getServiceStatus(status)
	if !skipWorkload {
		isHealth, output, outputs, err = h.collectWorkloadHealthStatus(ctx, comp, &status, accessor)
		if err != nil {
			return nil, nil, nil, false, err
		}
		status.WorkloadHealthy = isHealth
	}

	multiStagingEnabled := utilfeature.DefaultMutableFeatureGate.Enabled(features.MultiStageComponentApply)
	type traitKey struct {
		Type  string
		Index int
	}
	traitStatusByKey := make(map[traitKey]common.ApplicationTraitStatus, len(status.Traits))
	traitIndexByType := make(map[string]int)
	for _, ts := range status.Traits {
		key := traitKey{Type: ts.Type, Index: traitIndexByType[ts.Type]}
		traitIndexByType[ts.Type]++
		if _, exists := traitStatusByKey[key]; exists {
			continue
		}
		traitStatusByKey[key] = ts
	}
	addTraitStatus := func(key traitKey, ts common.ApplicationTraitStatus) {
		traitStatusByKey[key] = ts
	}
	traitIndexByType = make(map[string]int)
collectNext:
	for _, tr := range comp.Traits {
		key := traitKey{Type: tr.Name, Index: traitIndexByType[tr.Name]}
		traitIndexByType[tr.Name]++
		for _, filter := range traitFilters {
			// If filtered out by one of the filters
			if filter(*tr) {
				continue collectNext
			}
		}

		traitStatus, _outputs, err := h.collectTraitHealthStatus(comp, tr, overrideNamespace)
		if err != nil {
			return nil, nil, nil, false, err
		}
		outputs = append(outputs, _outputs...)

		isHealth = isHealth && traitStatus.Healthy
		if status.Message == "" && traitStatus.Message != "" {
			status.Message = traitStatus.Message
		}
		addTraitStatus(key, traitStatus)
	}
	if multiStagingEnabled && !status.WorkloadHealthy {
		for _, component := range h.currentAppRev.Spec.Application.Spec.Components {
			if component.Name != comp.Name {
				continue
			}
			traitIndexByType = make(map[string]int)
			for _, trait := range component.Traits {
				key := traitKey{Type: trait.Type, Index: traitIndexByType[trait.Type]}
				traitIndexByType[trait.Type]++
				if _, ok := traitStatusByKey[key]; ok {
					continue
				}
				traitStage, err := getTraitDispatchStage(h.Client, trait.Type, h.currentAppRev, h.app.Annotations)
				isPostDispatch := err == nil && traitStage == PostDispatch
				if isPostDispatch {
					addTraitStatus(
						key,
						common.ApplicationTraitStatus{
							Type:    trait.Type,
							Healthy: false,
							Pending: true,
							Message: "\u23f3 Waiting for component to be healthy",
						},
					)
				}
			}
			break
		}
	}
	traitHealthy := true
	for _, ts := range traitStatusByKey {
		if ts.Pending {
			continue
		}
		if !ts.Healthy {
			traitHealthy = false
			break
		}
	}
	if !skipWorkload {
		status.Healthy = status.WorkloadHealthy && traitHealthy
	} else if !traitHealthy {
		status.Healthy = false
		if status.Message == "" {
			status.Message = "traits are not healthy"
		}
	}
	h.mergeSourceResolutionStatus(comp, &status)
	status.Traits = slices.Collect(maps.Values(traitStatusByKey))
	h.addServiceStatus(true, status)
	return &status, output, outputs, isHealth, nil
}

// consumedReads renders the individual reads one consumer made, each carrying
// the source field, the property it landed in, and the value.
//
// readerKind filters: a chained source's reads are recorded against the source
// that made them, so a component's entry must not claim them.
func consumedReads(src v1beta1.ApplicationSource, rs cuedefinition.SourceResolutionStatus,
	readerKind, readerName string) []common.SourceRead {
	if src.StatusPolicy != nil && !src.StatusPolicy.ExposeConsumedValues && !src.StatusPolicy.ExposeResolvedFields {
		return nil
	}
	maskSet := sourceMaskSet(src, rs)
	reads := make([]common.SourceRead, 0, len(rs.Reads))
	for _, rd := range rs.Reads {
		if rd.ReaderKind != readerKind || rd.ReaderName != readerName {
			continue
		}
		val := redactValue(rd.Field, rd.Value, maskSet)
		out := common.SourceRead{Field: rd.Field, Property: rd.Property}
		if raw, err := mapToRawExtension(map[string]interface{}{"v": val}); err == nil && raw != nil {
			// Unwrap the single-key envelope mapToRawExtension needs.
			out.Value = unwrapValue(raw)
		}
		reads = append(reads, out)
	}
	sort.Slice(reads, func(i, j int) bool {
		if reads[i].Property != reads[j].Property {
			return reads[i].Property < reads[j].Property
		}
		return reads[i].Field < reads[j].Field
	})
	if len(reads) == 0 {
		return nil
	}
	return reads
}

func sourceMaskSet(src v1beta1.ApplicationSource, rs cuedefinition.SourceResolutionStatus) map[string]struct{} {
	maskPaths := append([]string{}, rs.SensitivePaths...)
	if src.StatusPolicy != nil {
		maskPaths = append(maskPaths, src.StatusPolicy.MaskPaths...)
	}
	maskSet := make(map[string]struct{}, len(maskPaths))
	for _, p := range maskPaths {
		if p != "" {
			maskSet[p] = struct{}{}
		}
	}
	return maskSet
}

// consumedValues renders what one reader took from a source, with sensitive and
// masked paths redacted. Nil when the binding's statusPolicy withholds values or
// nothing was consumed.
func consumedValues(src v1beta1.ApplicationSource, rs cuedefinition.SourceResolutionStatus) *runtime.RawExtension {
	if src.StatusPolicy != nil && !src.StatusPolicy.ExposeConsumedValues && !src.StatusPolicy.ExposeResolvedFields {
		return nil
	}
	if len(rs.ConsumedFields) == 0 {
		return nil
	}
	maskPaths := append([]string{}, rs.SensitivePaths...)
	if src.StatusPolicy != nil {
		maskPaths = append(maskPaths, src.StatusPolicy.MaskPaths...)
	}
	maskSet := make(map[string]struct{}, len(maskPaths))
	for _, p := range maskPaths {
		if p != "" {
			maskSet[p] = struct{}{}
		}
	}
	paths := make([]string, 0, len(rs.ConsumedFields))
	for p := range rs.ConsumedFields {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	props := make(map[string]interface{}, len(paths))
	for _, p := range paths {
		props[p] = redactValue(p, rs.ConsumedFields[p], maskSet)
	}
	raw, err := mapToRawExtension(props)
	if err != nil {
		return nil
	}
	return raw
}

// recordSourceResolution folds one render's source resolution into the
// Application-level view, and notes who read what.
//
// Called for every surface that resolves sources, not just components: a
// workflow step has nowhere of its own to report, since its status type belongs
// to the workflow repo and that engine is deliberately unaware sources exist.
func (h *AppHandler) recordSourceResolution(kind, name, readerType, cluster string,
	resolved map[string]cuedefinition.SourceResolutionStatus) {
	if len(resolved) == 0 {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.sourceStatuses == nil {
		h.sourceStatuses = map[string]*common.ApplicationSourceStatus{}
	}
	for _, src := range h.app.Spec.Sources {
		rs, ok := resolved[src.Name]
		if !ok {
			continue
		}
		entry := h.sourceStatuses[src.Name]
		if entry == nil {
			entry = &common.ApplicationSourceStatus{Name: src.Name, Type: src.Type}
			h.sourceStatuses[src.Name] = entry
		}
		if rs.Type != "" {
			entry.Type = rs.Type
		}
		// Later renders overwrite: within a reconcile they resolve the same
		// binding, and a failure seen by any reader is the one worth surfacing.
		if rs.Phase != "" && (entry.Phase == "" || rs.Phase == sourcePhaseFailed) {
			entry.Phase = rs.Phase
		}
		if rs.Message != "" {
			entry.Message = rs.Message
		}
		if rs.Config != "" {
			entry.Config = rs.Config
		}
		if rs.ExpiresAt != "" {
			entry.ExpiresAt = rs.ExpiresAt
		}
		if len(rs.ConsumedFields) == 0 {
			continue
		}
		entry.ConsumedBy = append(entry.ConsumedBy, common.SourceConsumer{
			DefinitionKind: kind,
			Name:           name,
			Type:           readerType,
			Cluster:        cluster,
			Reads:          consumedReads(src, rs, "", ""),
		})
	}
}

// sourceStatusList renders the accumulated view in spec order, so the report
// reads in the order the bindings were declared rather than in map order.
//
// A binding nothing read still appears, as Unused. Silence would be ambiguous
// with a binding that failed.
func (h *AppHandler) sourceStatusList() []common.ApplicationSourceStatus {
	if len(h.app.Spec.Sources) == 0 {
		return nil
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]common.ApplicationSourceStatus, 0, len(h.app.Spec.Sources))
	autoUpdateDefault := sourceAutoUpdateDefault()
	_, pinned := h.app.GetAnnotations()[oam.AnnotationPublishVersion]
	for _, src := range h.app.Spec.Sources {
		entry := common.ApplicationSourceStatus{Name: src.Name, Type: src.Type, Phase: sourcePhaseUnused}
		if got := h.sourceStatuses[src.Name]; got != nil {
			entry = *got
			if entry.Phase == "" {
				entry.Phase = sourcePhaseResolved
			}
		}
		wanted := sourceAutoUpdateEnabled(src, autoUpdateDefault)
		effective := wanted && !pinned
		entry.AutoUpdate = &effective
		// A bool cannot say why it is false, so where the binding asked for
		// auto-update and did not get it, the message carries the reason. Without
		// this the three ways to end up false - gate off, opted out, pinned - are
		// indistinguishable, which is exactly the diagnosis that is expensive.
		if wanted && pinned && entry.Message == "" {
			entry.Message = "autoUpdate suppressed: the Application is pinned by app.oam.dev/publishVersion"
		}
		out = append(out, entry)
	}
	return out
}

// mergeSourceResolutionStatus records what this component's render consumed, and
// nothing else.
//
// Whether a binding resolved, what backs it and when that expires belong to the
// binding rather than to each component reading it, and are reported once on
// AppStatus.Sources. Repeating them per component made a source a component did
// not read look identical to one that failed, and multiplied the same facts by
// the number of components.
func (h *AppHandler) mergeSourceResolutionStatus(comp *appfile.Component, status *common.ApplicationComponentStatus) {
	if len(h.app.Spec.Sources) == 0 || comp == nil || comp.Ctx == nil {
		return
	}
	resolvedStatuses, _ := comp.Ctx.GetData(cuedefinition.SourceResolutionStatusKey).(map[string]cuedefinition.SourceResolutionStatus)
	h.recordSourceResolution(sourceKindComponent, status.Name, comp.Type, status.Cluster, resolvedStatuses)
	consumed := make([]common.ComponentSourceStatus, 0, len(h.app.Spec.Sources))
	for _, src := range h.app.Spec.Sources {
		rs, ok := resolvedStatuses[src.Name]
		if !ok || len(rs.ConsumedFields) == 0 {
			// Not read by this component. Saying nothing is the honest report;
			// an empty entry reads as a failure.
			continue
		}
		consumed = append(consumed, common.ComponentSourceStatus{
			Name:       src.Name,
			Properties: consumedValues(src, rs),
		})
	}
	status.Sources = consumed
}

// maskedPath reports whether a consumed field is covered by a mask, either
// exactly or by sitting underneath one.
//
// The descent matters. A marker can only be written where the schema declares a
// field, so a source exposing an open struct - `properties: _`, whose shape is
// whatever template produced it - has nowhere to put a marker except on the
// struct itself. Matching exactly would mask a read of `properties` and publish
// `properties.token` beside it, which is the one case the marker exists for.
// redactValue blanks anything marked sensitive inside a read value.
//
// maskedPath alone is not enough. It answers "is this path at or below a mark",
// which covers reading db.password directly, but an expression may substitute a
// whole collection - "$(source.creds.db)" - and then the read path is db while
// the mark is db.password, one level below. Nothing matched and the secret went
// into status verbatim.
//
// Marks are schema paths and carry no list indices, since collectSensitivePaths
// descends only into struct literals. So elements of a list share their parent's
// path: a mark of "members.token" applies to the token of every member.
func redactValue(path string, v interface{}, masks map[string]struct{}) interface{} {
	if len(masks) == 0 {
		return v
	}
	if maskedPath(path, masks) {
		return "***"
	}
	switch val := v.(type) {
	case map[string]interface{}:
		out := make(map[string]interface{}, len(val))
		for k, child := range val {
			out[k] = redactValue(joinPath(path, k), child, masks)
		}
		return out
	case []interface{}:
		out := make([]interface{}, 0, len(val))
		for _, child := range val {
			out = append(out, redactValue(path, child, masks))
		}
		return out
	default:
		return v
	}
}

func joinPath(prefix, key string) string {
	if prefix == "" {
		return key
	}
	return prefix + "." + key
}

func maskedPath(path string, masks map[string]struct{}) bool {
	if _, ok := masks[path]; ok {
		return true
	}
	for mask := range masks {
		if strings.HasPrefix(path, mask+".") {
			return true
		}
	}
	return false
}

func mapToRawExtension(v map[string]interface{}) (*runtime.RawExtension, error) {
	bt, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return &runtime.RawExtension{Raw: bt}, nil
}

func setStatus(status *common.ApplicationComponentStatus, observedGeneration, generation int64, labels map[string]string,
	appRevName string, state terraformtypes.ConfigurationState, message string) {
	isLatest := func() bool {
		if observedGeneration != 0 && observedGeneration != generation {
			return false
		}
		// Use AppRevision to avoid getting the configuration before the patch.
		if v, ok := labels[oam.LabelAppRevision]; ok {
			if v != appRevName {
				return false
			}
		}
		return true
	}
	status.Message = message
	if !isLatest() || state != terraformtypes.Available {
		status.Healthy = false
		return
	}
	status.Healthy = true
}

// ApplyPolicies will render policies into manifests from appfile and dispatch them
// Note the builtin policy like apply-once, shared-resource, etc. is not handled here.
func (h *AppHandler) ApplyPolicies(ctx context.Context, af *appfile.Appfile) error {
	if ctx, ok := ctx.(monitorContext.Context); ok {
		subCtx := ctx.Fork("apply-policies", monitorContext.DurationMetric(func(v float64) {
			metrics.AppReconcileStageDurationHistogram.WithLabelValues("apply-policies").Observe(v)
		}))
		defer subCtx.Commit("finish apply policies")
	}
	policyManifests, err := af.GeneratePolicyManifests(ctx, h.Client)
	if err != nil {
		return errors.Wrapf(err, "failed to render policy manifests")
	}
	if len(policyManifests) > 0 {
		for _, policyManifest := range policyManifests {
			util.AddLabels(policyManifest, map[string]string{
				oam.LabelAppName:      h.app.GetName(),
				oam.LabelAppNamespace: h.app.GetNamespace(),
			})
		}
		if err = h.Dispatch(ctx, h.Client, "", common.PolicyResourceCreator, policyManifests...); err != nil {
			return errors.Wrapf(err, "failed to dispatch policy manifests")
		}
	}
	return nil
}

func extractOutputAndOutputs(templateContext map[string]interface{}) (*unstructured.Unstructured, []*unstructured.Unstructured) {
	output := new(unstructured.Unstructured)
	if templateContext["output"] != nil {
		output = &unstructured.Unstructured{Object: templateContext["output"].(map[string]interface{})}
	}
	outputs := extractOutputs(templateContext)
	return output, outputs
}

func extractOutputs(templateContext map[string]interface{}) []*unstructured.Unstructured {
	outputs := make([]*unstructured.Unstructured, 0)
	if templateContext["outputs"] != nil {
		for k, v := range templateContext["outputs"].(map[string]interface{}) {
			obj := &unstructured.Unstructured{Object: v.(map[string]interface{})}
			labels := obj.GetLabels()
			if labels == nil {
				labels = map[string]string{}
			}
			if labels[oam.TraitResource] == "" && k != "" {
				labels[oam.TraitResource] = k
			}
			obj.SetLabels(labels)
			outputs = append(outputs, obj)
		}
	}
	return outputs
}

// applyPostDispatchTraits applies PostDispatch stage traits for healthy components.
// This is called after the workflow succeeds and component health is confirmed.
func (h *AppHandler) applyPostDispatchTraits(ctx monitorContext.Context, appParser *appfile.Parser, af *appfile.Appfile) error {
	for _, svc := range h.services {
		workloadHealthy := svc.WorkloadHealthy
		if !workloadHealthy && svc.Healthy {
			workloadHealthy = true
		}
		if !workloadHealthy {
			continue
		}

		// Find the component spec
		var comp common.ApplicationComponent
		found := false
		for _, c := range h.app.Spec.Components {
			if c.Name == svc.Name {
				comp = c
				found = true
				break
			}
		}
		if !found {
			continue
		}

		// Parse the component to get all traits
		wl, err := appParser.ParseComponentFromRevisionAndClient(ctx.GetContext(), comp, h.currentAppRev)
		if err != nil {
			return errors.WithMessagef(err, "failed to parse component %s for PostDispatch traits", comp.Name)
		}

		// Filter to keep ONLY PostDispatch traits
		var postDispatchTraits []*appfile.Trait
		for _, trait := range wl.Traits {
			if trait.FullTemplate.TraitDefinition.Spec.Stage == v1beta1.PostDispatch {
				postDispatchTraits = append(postDispatchTraits, trait)
			}
		}

		if len(postDispatchTraits) == 0 {
			continue
		}

		wl.Traits = postDispatchTraits

		// Generate manifest with context that includes live workload status
		manifest, err := af.GenerateComponentManifest(wl, func(ctxData *velaprocess.ContextData) {
			if svc.Namespace != "" {
				ctxData.Namespace = svc.Namespace
			}
			if svc.Cluster != "" {
				ctxData.Cluster = svc.Cluster
			} else {
				ctxData.Cluster = pkgmulticluster.Local
			}
			ctxData.ClusterVersion = multicluster.GetVersionInfoFromObject(
				pkgmulticluster.WithCluster(ctx.GetContext(), types.ClusterLocalName),
				h.Client,
				ctxData.Cluster,
			)

			// Fetch live workload status for PostDispatch traits to use if it's created on the cluster
			tempCtx := appfile.NewBasicContext(*ctxData, wl.Params)
			if err := wl.EvalContext(tempCtx); err != nil {
				ctx.Error(err, "failed to evaluate context for workload %s", wl.Name)
				return
			}
			base, _ := tempCtx.Output()
			componentWorkload, err := base.Unstructured()
			if err != nil {
				ctx.Error(err, "failed to unstructure base component generated using workload %s", wl.Name)
				return
			}
			if componentWorkload.GetName() == "" {
				componentWorkload.SetName(ctxData.CompName)
			}
			_ctx := util.WithCluster(tempCtx.GetCtx(), componentWorkload)
			object, err := util.GetResourceFromObj(_ctx, tempCtx, componentWorkload, h.Client, ctxData.Namespace, map[string]string{
				oam.LabelOAMResourceType: oam.ResourceTypeWorkload,
				oam.LabelAppComponent:    ctxData.CompName,
				oam.LabelAppName:         ctxData.AppName,
			}, "")
			if err != nil {
				ctx.Error(err, "failed to fetch workload output resource %s from the cluster", componentWorkload.GetName())
				return
			}
			ctxData.Output = object
		})
		if err != nil {
			return errors.WithMessagef(err, "failed to generate manifest for PostDispatch traits of component %s", comp.Name)
		}

		// Render traits
		_, readyTraits, err := renderComponentsAndTraits(manifest, h.currentAppRev, svc.Cluster, svc.Namespace)
		if err != nil {
			return errors.WithMessagef(err, "failed to render PostDispatch traits for component %s", comp.Name)
		}

		// Add app ownership labels
		for _, trait := range readyTraits {
			util.AddLabels(trait, map[string]string{
				oam.LabelAppName:      h.app.GetName(),
				oam.LabelAppNamespace: h.app.GetNamespace(),
			})
		}

		// Dispatch the traits
		dispatchCtx := multicluster.ContextWithClusterName(ctx.GetContext(), svc.Cluster)
		if err := h.Dispatch(dispatchCtx, h.Client, svc.Cluster, common.WorkflowResourceCreator, readyTraits...); err != nil {
			return errors.WithMessagef(err, "failed to dispatch PostDispatch traits for component %s", comp.Name)
		}
		// Restore all traits and collect health status to update the application status.
		//
		// Why this is necessary:
		// When the workflow is in "executing" state (e.g., one component is unhealthy),
		// the reconcile loop returns early after applyPostDispatchTraits() and does NOT
		// call evalStatus(). This means collectHealthStatus() would never be called for
		// the healthy component's traits.
		//
		// During the initial workflow apply, prepareWorkloadAndManifests() filters out
		// PostDispatch traits when serviceHealthy=false, so the status only contains
		// non-PostDispatch traits (like "scaler"). Without this explicit call here,
		// PostDispatch traits would be dispatched to the cluster but never reflected
		// in the application status.
		//
		healthCtx := multicluster.ContextWithClusterName(ctx.GetContext(), svc.Cluster)
		if _, _, _, _, err := h.collectHealthStatus(healthCtx, wl, svc.Namespace, false); err != nil {
			ctx.Error(err, "failed to refresh PostDispatch trait status", "component", comp.Name)
		}
	}
	return nil
}

// Phases reported on AppStatus.Sources.
const (
	sourcePhaseResolved = "Resolved"
	sourcePhaseStale    = "Stale"
	sourcePhaseFailed   = "Failed"
	sourcePhaseUnused   = "Unused"
)

// Definition kinds reported on SourceConsumer.
const (
	sourceKindComponent    = "component"
	sourceKindTrait        = "trait"
	sourceKindWorkflowStep = "workflowstep"
	sourceKindPolicy       = "policy"
)

// unwrapValue pulls the single value back out of the envelope mapToRawExtension
// requires, so a read reports its value directly rather than nested under a key.
func unwrapValue(raw *runtime.RawExtension) *runtime.RawExtension {
	var wrapper map[string]json.RawMessage
	if err := json.Unmarshal(raw.Raw, &wrapper); err != nil {
		return nil
	}
	inner, ok := wrapper["v"]
	if !ok {
		return nil
	}
	return &runtime.RawExtension{Raw: inner}
}
