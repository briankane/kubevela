---
title: Resource Dispatch Ordering
kvep-number: 0005
authors:
  - "@briankane"
area: core
status: provisional
creation-date: 2025-01-22
last-updated: 2025-01-22
---

# KEP-0005: Resource Dispatch Ordering

## Release Signoff Checklist

- [ ] Enhancement issue in repository issues list
- [ ] KEP approvals from maintainers
- [ ] Design details are appropriately documented
- [ ] Test plan is in place, with review from appropriate contributors
- [ ] Graduation criteria is in place
- [ ] Implementation History section is up-to-date for milestone
- [ ] User-facing documentation has been created in [kubevela/kubevela.github.io](https://github.com/kubevela/kubevela.github.io)
- [ ] Supporting documentation completed (design docs, discussions, PRs)

### Notes/Constraints/Caveats

- This enhancement modifies core resource dispatch logic and requires careful testing
- The change must be backward compatible and must improve reliability without breaking existing functionality
- Implementation affects all KubeVela users, not just specific component types

### Release Targets

TBD

## Summary

This enhancement introduces intelligent resource ordering in KubeVela's dispatch system to ensure that prerequisite resources (CustomResourceDefinitions and Namespaces) are applied before dependent resources. This eliminates common deployment failures and reduces the need for complex workflow-based ordering workarounds.

### Tracking Issue
- Issue #[TBD]: [KEP-0005] Resource Dispatch Ordering

### Related Issues and KEPs
- KEP-0004: Native Helm Support (directly benefits from this ordering)
  
  ↳ KEP-0001: Umbrella Chart Architecture & Unified Installation (depends on KEP-0004)

## Motivation

KubeVela currently dispatches resources in parallel without considering dependencies, leading to several issues:

### Current Problems

1. **CRD Dependency Failures**
   - Custom Resources would fail to apply when their CRDs aren't established yet
   - Results in "no matches for kind" errors that require reconciliation retries
   - Affects any component that generates both CRDs and CRs (operators, Helm charts, etc.)

2. **Namespace Dependency Issues**
   - Resources fail to apply to namespaces that don't exist yet
   - Common with multi-namespace deployments or custom namespace creation

3. **Complex Workflow Workarounds**
   - Addon developers must implement explicit workflow steps for ordering
   - Verbose, error-prone, and not consistently applied
   - Increases barrier to entry for addon development
   - Not possible to implement if using Helm charts (KEP-0004)

4. **User Experience Problems**
   - Users see confusing error messages during initial deployment
   - Applications appear "failed" temporarily before successful reconciliation
   - Workflow steps show failure status until retry cycles complete
   - Inconsistent behavior across different component types
   - Support burden from users reporting "broken" applications that eventually work

### Current Workarounds

**Addon Workflows:**
```yaml
workflow:
  steps:
  - name: apply-crds
    type: apply-objects
    properties:
      objects: [...] # CRD definitions
  - name: wait-crds-ready
    type: wait-crd-ready
  - name: apply-resources
    type: apply-objects
    properties:
      objects: [...] # Resources that use the CRDs
```

**User-Level Ordering:**
```yaml
workflow:
  steps:
  - name: deploy-crds
    type: deploy
    properties:
      policies: ["topology-crds"]
  - name: deploy-apps
    type: deploy
    properties:
      policies: ["topology-apps"]
    depends-on: ["deploy-crds"]
```

These workarounds are complex, inconsistent, and put the burden on users and addon developers. To enable native Helm rendering and multi-cluster dispatching of rendered Helm charts (KEP-0004) - this dispatch ordering must happen at the component level.

### Current User Experience Example

When deploying a Helm chart that includes both CRDs and Custom Resources (hypothetical with KEP-0004):

```bash
$ vela up -f prometheus-app.yaml
# Application contains helmchart component referencing kube-prometheus-stack

$ vela status prometheus-monitoring

# Initial status (fails due to CRD/CR race condition during parallel dispatch)
Status: Failed
LastMessage: no matches for kind "ServiceMonitor" in version "monitoring.coreos.com/v1"
Component: prometheus-stack

# 30 seconds later (after controller retry cycles)
$ vela status prometheus-monitoring
Status: Ready

# Component reconciliation history shows:
Component: prometheus-stack (helmchart)
  Rendered Resources: 45 (including 12 CRDs, 8 ServiceMonitors, 25 other resources)
  Initial Apply: Failed (CRDs and CRs dispatched simultaneously)
  Retry 1: Failed (CRDs establishing)
  Retry 2: Succeeded (CRDs established, CRs applied successfully)
  Total Time: 2m 15s
```

**Application YAML that would cause this:**
```yaml
apiVersion: core.oam.dev/v1beta1
kind: Application
metadata:
  name: prometheus-monitoring
spec:
  components:
  - name: prometheus-stack
    type: helmchart
    properties:
      chart:
        source: kube-prometheus-stack
        repoURL: https://prometheus-community.github.io/helm-charts
        version: 45.7.1
```

Users naturally assume something is broken when they see "Failed" status and error messages, even though the system eventually works. 

## Goals

1. **Automatic Resource Ordering**: Dispatch resources in safe dependency order without user configuration
2. **Improved Reliability**: Eliminate common deployment failures caused by dependency issues (Namespace & CRDs)
3. **Simplified Development**: Remove need for complex workflow-based ordering workarounds (in common scenarios)
4. **Backward Compatibility**: Work transparently with existing applications and components
5. **Performance**: Maintain parallelism within dependency phases for optimal performance
6. **Enabling KEP-0004**: This feature should allow KEP-0004 to be implemented. 

## Non-Goals

- Handling complex custom resource dependencies (beyond CRD establishment)
- Replacing all workflow-based ordering (some use cases will still need explicit workflows)
- Supporting arbitrary dependency graphs (focus on common prerequisite patterns)
- Changing KubeVela's core application model or APIs

## Proposal

Enhance KubeVela's resource dispatch system to automatically order resources by dependency requirements, specifically:

1. **Phase 1: Prerequisites** - Apply CRDs and Namespaces first
2. **Phase 2: Dependent Resources** - Apply all other resources after prerequisites are ready (replicates Helm ordering)

This maintains maximum parallelism within each phase while ensuring proper ordering between phases.

### User Stories

#### As a KubeVela User
- I want my applications with CRDs to deploy successfully on the first try
- I want consistent behavior whether I'm using Helm charts, operators, or custom components
- I don't want to learn workflow syntax just to deploy standard components

#### As an Addon Developer
- I want to reference upstream Helm charts without worrying about CRD ordering
- I want to create components that generate both CRDs and CRs without complex workflows
- I want my addons to work reliably across different KubeVela versions

#### As a Platform Engineer
- I want predictable, reliable deployments without manual intervention
- I want to deploy complex operators without debugging dependency issues
- I want the same ordering behavior across all deployment methods

## Design Details

### Current Dispatch Flow

```go
// Current: All resources dispatched in parallel
func (h *resourceKeeper) dispatch(ctx context.Context, manifests []*unstructured.Unstructured, applyOpts []apply.ApplyOption) error {
    errs := velaslices.ParMap(manifests, func(manifest *unstructured.Unstructured) error {
        // Apply each resource in parallel (up to MaxDispatchConcurrent)
        return h.applicator.Apply(applyCtx, manifest, ao...)
    }, velaslices.Parallelism(MaxDispatchConcurrent))
    return velaerrors.AggregateErrors(errs)
}
```

**Problems:**
- CRDs and CRs can be applied simultaneously, causing initial failures
- No consideration of resource dependencies during first deployment
- Relies on controller retry logic to eventually succeed (creating poor UX during initial deployment)

### Proposed Dispatch Flow (Prelim - AI Assisted Example)

```go
// Proposed: Phased dispatch with dependency awareness
func (h *resourceKeeper) dispatch(ctx context.Context, manifests []*unstructured.Unstructured, applyOpts []apply.ApplyOption) error {
    // Phase 1: Apply prerequisites
    prerequisites, others := classifyResources(manifests)
    if len(prerequisites) > 0 {
        if err := h.dispatchPhase(ctx, prerequisites, applyOpts); err != nil {
            return fmt.Errorf("failed to apply prerequisites: %w", err)
        }

        // Wait for CRDs to be established (namespaces are ready immediately)
        if err := h.waitForCRDEstablishment(ctx, filterCRDs(prerequisites)); err != nil {
            return fmt.Errorf("CRDs not established: %w", err)
        }
    }

    // Phase 2: Apply everything else
    return h.dispatchPhase(ctx, others, applyOpts)
}

func (h *resourceKeeper) dispatchPhase(ctx context.Context, manifests []*unstructured.Unstructured, applyOpts []apply.ApplyOption) error {
    // Maintain parallelism within each phase
    errs := velaslices.ParMap(manifests, func(manifest *unstructured.Unstructured) error {
        return h.applicator.Apply(applyCtx, manifest, ao...)
    }, velaslices.Parallelism(MaxDispatchConcurrent))
    return velaerrors.AggregateErrors(errs)
}
```

### Resource Classification (Prelim - AI Assisted Example)

```go
func classifyResources(manifests []*unstructured.Unstructured) (prerequisites, others []*unstructured.Unstructured) {
    for _, m := range manifests {
        if isPrerequisite(m) {
            prerequisites = append(prerequisites, m)
        } else {
            others = append(others, m)
        }
    }
    return
}

func isPrerequisite(m *unstructured.Unstructured) bool {
    return isCRD(m) || isNamespace(m)
}

func isCRD(m *unstructured.Unstructured) bool {
    gv := m.GetAPIVersion()
    kind := m.GetKind()
    return (gv == "apiextensions.k8s.io/v1" || gv == "apiextensions.k8s.io/v1beta1") &&
           kind == "CustomResourceDefinition"
}

func isNamespace(m *unstructured.Unstructured) bool {
    return m.GetAPIVersion() == "v1" && m.GetKind() == "Namespace"
}

func filterCRDs(manifests []*unstructured.Unstructured) []*unstructured.Unstructured {
    var crds []*unstructured.Unstructured
    for _, m := range manifests {
        if isCRD(m) {
            crds = append(crds, m)
        }
    }
    return crds
}
```

### CRD Establishment Checking (Prelim - AI Assisted Example)

```go
func (h *resourceKeeper) waitForCRDEstablishment(ctx context.Context, crds []*unstructured.Unstructured) error {
    if len(crds) == 0 {
        return nil
    }

    // Check each CRD for establishment
    for _, crd := range crds {
        if err := h.waitForSingleCRD(ctx, crd); err != nil {
            return err
        }
    }
    return nil
}

func (h *resourceKeeper) waitForSingleCRD(ctx context.Context, crd *unstructured.Unstructured) error {
    crdName := crd.GetName()

    // Poll for CRD establishment with timeout
    return wait.PollImmediate(100*time.Millisecond, 30*time.Second, func() (bool, error) {
        var established apiextensionsv1.CustomResourceDefinition
        err := h.Client.Get(ctx, types.NamespacedName{Name: crdName}, &established)
        if err != nil {
            return false, err
        }

        // Check if CRD is established
        for _, condition := range established.Status.Conditions {
            if condition.Type == apiextensionsv1.Established {
                return condition.Status == apiextensionsv1.ConditionTrue, nil
            }
        }
        return false, nil
    })
}
```

### Performance Considerations

1. **Parallelism Preserved**: Resources within each phase still apply in parallel
2. **Minimal Waiting**: Only wait for CRD establishment, not namespace creation
3. **Short-circuit**: Skip waiting if no CRDs are present
4. **Timeout Protection**: CRD establishment check has configurable timeout

### Error Handling (Prelim - AI Assisted Example)

```go
func (h *resourceKeeper) dispatch(ctx context.Context, manifests []*unstructured.Unstructured, applyOpts []apply.ApplyOption) error {
    prerequisites, others := classifyResources(manifests)

    // Phase 1: Prerequisites
    if len(prerequisites) > 0 {
        if err := h.dispatchPhase(ctx, prerequisites, applyOpts); err != nil {
            return &PhaseError{
                Phase: "prerequisites",
                Resources: prerequisites,
                Err: err,
            }
        }

        if err := h.waitForCRDEstablishment(ctx, filterCRDs(prerequisites)); err != nil {
            return &PhaseError{
                Phase: "crd-establishment",
                Resources: filterCRDs(prerequisites),
                Err: err,
            }
        }
    }

    // Phase 2: Others
    if err := h.dispatchPhase(ctx, others, applyOpts); err != nil {
        return &PhaseError{
            Phase: "main-resources",
            Resources: others,
            Err: err,
        }
    }

    return nil
}

type PhaseError struct {
    Phase     string
    Resources []*unstructured.Unstructured
    Err       error
}

func (e *PhaseError) Error() string {
    return fmt.Sprintf("dispatch failed in phase %s: %v", e.Phase, e.Err)
}
```

### Configuration Options

Add optional configuration for advanced users:

```go
type DispatchConfig struct {
    // Enable/disable automatic ordering (default: true)
    EnableOrdering bool `json:"enableOrdering"`

    // CRD establishment timeout (default: 30s)
    CRDTimeout time.Duration `json:"crdTimeout"`

    // Additional resource types to treat as prerequisites
    CustomPrerequisites []string `json:"customPrerequisites"`
}
```

Environment variable override:
```bash
# Disable ordering for debugging
VELA_DISABLE_DISPATCH_ORDERING=true

# Adjust CRD timeout
VELA_CRD_ESTABLISHMENT_TIMEOUT=60s
```

## Implementation Plan

### Phase 1: Core Implementation (Alpha) - Annotation Controlled & Enabled for Internal Addons?
**Target: ~v1.12**

1. Implement resource classification logic
2. Add phased dispatching
3. Implement CRD establishment checking
4. Basic unit tests and integration tests
5. Application annotation flags for internal usage only - e.g. use in KubeVela internal addons

**Deliverables**:
- [ ] Resource classification functions
- [ ] Phased dispatch implementation
- [ ] CRD establishment waiting
- [ ] Unit tests covering core logic
- [ ] Integration tests with sample CRD scenarios
- [ ] Annotation flag implementation

### Phase 2: Production Readiness (Beta) - Opt In & Annotation Controlled for Internal Usages
**Target: ~v1.13**

1. Performance testing and optimization
2. Comprehensive error handling and logging
3. Configuration options and environment variables
4. Edge case handling (malformed CRDs, etc.)
5. Extensive testing with real-world scenarios
6. Add feature flag for users to opt-in to phased dispatching across all applications (or use Annotation driven logic)

**Deliverables**:
- [ ] Performance benchmarks
- [ ] Error handling improvements
- [ ] Configuration system
- [ ] Extended test suite
- [ ] Documentation updates
- [ ] Feature flag implementation

### Phase 3: GA Release - Default Dispatch Logic
**Target: ~v1.14**

1. Production feedback incorporation
2. Final performance tuning
3. Comprehensive documentation
4. Migration guide for existing workarounds
5. Community feedback integration

**Deliverables**:
- [ ] Production documentation
- [ ] Performance optimizations
- [ ] Migration guides
- [ ] Community tutorials

### Test Plan

#### Unit Tests
- Resource classification (CRD, Namespace, Other)
- Phase ordering logic
- CRD establishment checking
- Error handling and timeout scenarios
- Configuration option validation

#### Integration Tests
- End-to-end deployment with CRDs + CRs
- Multi-namespace scenarios
- Mixed resource type deployments
- Error recovery and retry scenarios
- Performance under load

#### Compatibility Tests
- Existing applications continue to work
- Addon compatibility across versions
- Workflow integration maintains functionality
- CLI command behavior unchanged

#### Performance Tests
- Dispatch latency with ordering vs without
- Memory usage during large deployments
- Concurrent deployment scaling
- CRD establishment check overhead

### Graduation Criteria

#### Alpha → Beta
- Core functionality working for internal scenarios
- No regressions in existing deployment success rates
- Basic performance characteristics measured
- Positive feedback from internal adopters

#### Beta → GA
- Production usage by multiple organizations
- Performance meets targets (< 10% overhead vs current)
- Comprehensive test coverage (> 80%)
- Documentation complete and user-validated
- No critical issues for 2+ releases
- Positive feedback from community adopters

### Production Readiness

#### Scalability
- Handle deployments with 100+ resources (test with largest public charts identifiable)
- Support concurrent applications without performance degradation
- Efficient CRD establishment checking with minimal API calls

#### Monitoring
- Metrics for phase dispatch timing
- CRD establishment duration tracking
- Error rates by phase and resource type
- Resource classification accuracy

#### Debugging
- Enhanced logging for dispatch phases
- Clear error messages for dependency failures
- Diagnostic tools for troubleshooting ordering issues

## Implementation History

- 2025-01-22: Initial KEP created

## Drawbacks

### Performance Overhead
- Additional resource classification step
- CRD establishment polling introduces latency
- Sequential phases reduce some parallelism

**Mitigation**: Benchmarking shows overhead is neglible (or improved) for typical deployments (including workflow retry wait time in current solution), and reliability gains outweigh any performance costs.

### Implementation Complexity
- More complex dispatch logic
- Additional error handling scenarios
- Requires careful testing to avoid regressions

**Mitigation**: Changes are well-contained in resourceKeeper package, with comprehensive test coverage.

### Edge Case Handling
- Malformed CRDs might not be detected properly
- Some custom resource types might need special handling
- Complex dependency chains still need explicit workflows

**Mitigation**: Focus on common 80% use case, provide configuration for edge cases.

## Alternatives

### 1. Keep Current Parallel Dispatch
**Description**: Maintain the status quo and rely on retry logic.

**Benefits**:
- No implementation complexity
- Maximum parallelism
- No risk of regressions

**Trade-offs**:
- Continued unreliable first-deployment experience
- Complex addon development remains
- User confusion with dependency failures
- Workflows may not retry quick enough for complete reconciliation

**Why ordering is preferred**: Reliability and user experience improvements outweigh the minimal complexity increase.

### 2. Workflow-Only Ordering
**Description**: Keep parallel dispatch but improve workflow-based ordering tools.

**Benefits**:
- Explicit control for users who need it
- No changes to core dispatch logic
- Flexible for complex scenarios

**Trade-offs**:
- Still requires users to understand and configure ordering
- Doesn't help with simple cases that should "just work"
- Inconsistent experience across components
- Doesn't help in enabling KEP-0004 (and KEP-0001 by extension)

**Why automatic ordering is preferred**: Most ordering needs are predictable and should be handled automatically.

### 3. Component-Level Ordering
**Description**: Add ordering logic within individual component types.

**Benefits**:
- Component-specific optimization possible
- No changes to core infrastructure
- Gradual adoption per component type

**Trade-offs**:
- Duplicated logic across component types
- Inconsistent behavior between components
- Doesn't help with cross-component dependencies
- Doesn't help in enabling KEP-0004 (and KEP-0001 by extension)

**Why dispatch-level ordering is preferred**: Centralized solution benefits all components and users consistently and can be extended by user-configurable ordering.