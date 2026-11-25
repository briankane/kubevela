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
	"fmt"
	"sort"

	monitorContext "github.com/kubevela/pkg/monitor/context"
	"github.com/pkg/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/oam-dev/kubevela/apis/core.oam.dev/common"
	"github.com/oam-dev/kubevela/apis/core.oam.dev/v1beta1"
	"github.com/oam-dev/kubevela/apis/types"
	"github.com/oam-dev/kubevela/pkg/appfile"
	"github.com/oam-dev/kubevela/pkg/resourcekeeper"
	velaerrors "github.com/oam-dev/kubevela/pkg/utils/errors"
)

// componentDelivery is how an Application's components reach their clusters once rendered:
// applied by the hub in-process, or dispatched as signed Components that vela-agent applies.
// The render, the workflow and the status are the same either way; each method is a point
// where the two differ.
type componentDelivery interface {
	// selectTraits leaves on wl the traits rendered with it now, and returns the traits
	// whose component reads are delivered for this render.
	selectTraits(ctx context.Context, comp common.ApplicationComponent, wl *appfile.Component, af *appfile.Appfile) ([]*appfile.Trait, error)
	// readsLiveWorkload reports whether a render sees the live workload as context.output.
	readsLiveWorkload() bool
	// apply delivers one rendered placement and reports its health, or why it is late.
	apply(ctx context.Context, p renderedPlacement) (*unstructured.Unstructured, []*unstructured.Unstructured, bool, string, error)
	// health reports one rendered placement's health without delivering it.
	health(ctx context.Context, p renderedPlacement) (bool, *common.ApplicationComponentStatus, *unstructured.Unstructured, []*unstructured.Unstructured, error)
	// applyPostDispatch applies the PostDispatch traits of every healthy component.
	applyPostDispatch(ctx monitorContext.Context, appParser *appfile.Parser, af *appfile.Appfile) error
	// prunesOnRefresh reports whether a refresh prunes what a component no longer renders.
	prunesOnRefresh() bool
	// policyContextCoversMetadata reports whether a policy's labels and annotations count, as
	// its context does, as a change to re-deliver.
	policyContextCoversMetadata() bool
	// placementObjects reads a component's objects at one placement, for component reads.
	placementObjects(ctx context.Context, component, cluster, ns string, counts placementCounts) ([]*unstructured.Unstructured, []*unstructured.Unstructured, error)
	// resign signs again what carries a signing key other than the active one.
	resign(ctx monitorContext.Context, appParser *appfile.Parser, af *appfile.Appfile) error
	// handOver moves one component's resources to this delivery, from what the other left.
	handOver(ctx monitorContext.Context, component string, inProcess, dispatched []v1beta1.ManagedResource) handoverStep
}

// renderedPlacement is one component rendered for one placement.
type renderedPlacement struct {
	af         *appfile.Appfile
	wl         *appfile.Component
	manifest   *types.ComponentManifest
	workload   *unstructured.Unstructured
	traits     []*unstructured.Unstructured
	cluster    string
	namespace  string
	replicaKey string
}

// handoverStep is where one component's handover stands: what it waits on, whether it is
// still in progress, whether it is to be dispatched again without adoptFrom, and any error.
type handoverStep struct {
	wait       string
	inProgress bool
	again      bool
	err        error
}

// delivery is how this Application's components are delivered.
func (h *AppHandler) delivery() componentDelivery {
	if h.dispatchesComponents() {
		return dispatchedDelivery{h}
	}
	return inProcessDelivery{h}
}

// inProcessDelivery applies a component's resources from the hub, through cluster-gateway.
type inProcessDelivery struct{ h *AppHandler }

// dispatchedDelivery delivers each component as a signed Component for vela-agent to apply.
type dispatchedDelivery struct{ h *AppHandler }

func (d inProcessDelivery) applyPostDispatch(ctx monitorContext.Context, appParser *appfile.Parser, af *appfile.Appfile) error {
	return d.h.applyPostDispatchTraits(ctx, appParser, af)
}

// applyPostDispatch applies nothing: vela-agent renders a dispatched component's PostDispatch
// traits itself.
func (dispatchedDelivery) applyPostDispatch(monitorContext.Context, *appfile.Parser, *appfile.Appfile) error {
	return nil
}

func (inProcessDelivery) prunesOnRefresh() bool { return true }

// prunesOnRefresh is false: the hub tracks a dispatched component's Components, and vela-agent
// collects what a new revision of one no longer carries.
func (dispatchedDelivery) prunesOnRefresh() bool { return false }

func (inProcessDelivery) policyContextCoversMetadata() bool { return false }

// policyContextCoversMetadata is true: a resource vela-agent applied changes only through the
// next signed revision.
func (dispatchedDelivery) policyContextCoversMetadata() bool { return true }

func (d inProcessDelivery) placementObjects(ctx context.Context, component, cluster, ns string, counts placementCounts) ([]*unstructured.Unstructured, []*unstructured.Unstructured, error) {
	return d.h.componentObjects(ctx, component, cluster, ns, counts, controlPlaneOnlyTraits(d.h.currentAppRev))
}

func (d dispatchedDelivery) placementObjects(ctx context.Context, component, cluster, ns string, counts placementCounts) ([]*unstructured.Unstructured, []*unstructured.Unstructured, error) {
	return d.h.observedObjects(ctx, component, cluster, ns, counts)
}

// resign signs nothing: nothing applied in-process is signed.
func (inProcessDelivery) resign(monitorContext.Context, *appfile.Parser, *appfile.Appfile) error {
	return nil
}

func (d dispatchedDelivery) resign(ctx monitorContext.Context, appParser *appfile.Parser, af *appfile.Appfile) error {
	return d.h.resignComponents(ctx, appParser, af)
}

// handOver retires a component's Components once it is healthy everywhere in-process.
func (d inProcessDelivery) handOver(ctx monitorContext.Context, component string, _, dispatched []v1beta1.ManagedResource) handoverStep {
	h := d.h
	if len(dispatched) == 0 {
		return handoverStep{}
	}
	if !h.healthyEverywhere(component) {
		return handoverStep{inProgress: true, wait: fmt.Sprintf("component %s: waiting for it to be healthy before retiring its Components", component)}
	}
	sort.SliceStable(dispatched, func(i, j int) bool { return !isComponentRecord(dispatched[i]) && isComponentRecord(dispatched[j]) })
	var errs []error
	for _, mr := range dispatched {
		// A Component waiting on its tombstone is reported by the tombstone hook.
		err := h.resourceKeeper.Delete(ctx, []*unstructured.Unstructured{mr.ToUnstructured()})
		if err != nil && !errors.Is(err, resourcekeeper.ErrDeletePending) {
			errs = append(errs, errors.WithMessagef(err, "retiring the Components of component %s", component))
		}
	}
	return handoverStep{inProgress: true, err: velaerrors.AggregateErrors(errs)}
}

// handOver moves a component's resources from its Application to its Components.
func (d dispatchedDelivery) handOver(ctx monitorContext.Context, component string, inProcess, dispatched []v1beta1.ManagedResource) handoverStep {
	wait, again, err := d.h.handOver(ctx, component, inProcess, dispatched)
	if err != nil {
		err = errors.WithMessagef(err, "handing component %s to its Components", component)
	}
	return handoverStep{wait: wait, again: again, err: err, inProgress: len(inProcess) > 0 || again}
}
