---
title: Native Helm Support in KubeVela
kvep-number: 0004
authors:
  - "@briankane"
area: core
status: provisional
creation-date: 2025-01-22
last-updated: 2025-01-22
---

# KEP-0004: Native Helm Support in KubeVela

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

- This KEP introduces a new component type that directly renders Helm charts without requiring FluxCD (relieving dependency issues blocking KEP-0001)
- The implementation leverages the existing CueX provider system for clean integration
- The FluxCD addon is still maintained for users with preference for Flux usage
- The feature is designed to work seamlessly with KubeVela's multi-cluster capabilities

### Release Targets

- KubeVela v1.12: Alpha implementation (internal KubeVela use only - no documentation provided)
- KubeVela v1.14: Beta implementation & bug fixes (consumable by early access users)
- KubeVela v1.13: GA

## Summary

This enhancement proposes adding native Helm chart support to KubeVela through a new `helmchart` component type. This eliminates the dependency on FluxCD for Helm deployments while providing a more integrated experience with KubeVela's core features like multi-cluster deployment, revision management, and unified workflows.

This ensures that KubeVela's internal extensions (i.e. workflow, kube-trigger, prism, et al.) have a consistent means on installation via Helm that does not involve duplication of resources and which has no dependencies to external products (Flux) and post-installation addons (`fluxcd`). See KEP-0001 for details of the proposed Helm Umbrella charting this would help to enable. 

### Tracking Issue
- Issue #[TBD]: [KEP-0004] Native Helm Support in KubeVela

### Related Issues and KEPs
- KEP-0001: Umbrella Chart Architecture (provides installation framework)
- KEP-0002: Definition Catalog (enables definition distribution)
- Issue #[TBD]: Remove FluxCD dependency for Helm charts
- Issue #[TBD]: Simplify Helm chart deployment in multi-cluster scenarios

## Motivation

KubeVela currently supports Helm charts through the FluxCD addon, which introduces several challenges:

### Current Problems

1. **Addon Dependency Complexity**
   - Users must install and manage the FluxCD addon to deploy Helm charts
   - Many users already have Flux installed separately, creating conflicts
   - The fluxcd addon cannot be used when Flux is pre-installed (todo: verify this - see https://github.com/kubevela/catalog/issues/747)

2. **Resource Duplication**
   - KubeVela's own addons duplicate Helm chart resources instead of using charts directly
   - In multi-cluster setups, a Helm install to the Hub installs **only** to the Hub; mandating addon usage for spoke (managed cluster) installation
   - Maintaining duplicate YAML resources increases maintenance burden, risk of human error and resource drifts

3. **Multi-cluster Limitations**
   - For multi-cluster setups, rolling a Helm Chart out to all managed clusters relies entirely on FluxCD
   - This is acceptable for users already using, or wanting to use FluxCD - but forcing the dependency to fulfil KubeVela's internal requirements is a bad practice

4. **Installation Bootstrap Problem**
   - KubeVela cannot rely on its own Helm charts for core extensions
   - The Hub cannot install extensions via Helm without FluxCD pre-installed
   - Circular dependency: need FluxCD to install Helm charts, but FluxCD itself needs installation

5. **User Experience Fragmentation**
   - Different deployment models for Helm vs native KubeVela components
   - Helm releases managed separately from Application revisions
   - No unified status tracking and health checking

### Why Native Support

Native Helm support would:
- Enable direct Helm chart consumption without external dependencies
- Unify the deployment experience across all component types
- Leverage KubeVela's existing multi-cluster and revision capabilities
- Simplify the overall architecture and reduce operational complexity

## Goals

1. **Native Helm Rendering**: Add a `helmchart` component type that renders Helm charts directly
2. **Multi-source Support**: Support charts from HTTP(S) repositories, OCI registries, and direct URLs
3. **Comprehensive Value Management**: Enable value merging from multiple sources (inline KubeVela params, ConfigMap, Secret)
4. **Multi-cluster Native**: Seamless integration with KubeVela's placement policies
5. **Performance**: Implement caching and optimization for production workloads
6. **Migration Path**: Provide clear migration from FluxCD-based deployments
7. **Simplified Addons**: Addons can simply wrap the existing Helm charts of external solutions (scope for generation in the future)

## Non-Goals

- Replacing Helm as a package manager (we use Helm libraries, not recreate them)
- Supporting Helm < v3 charts
- Implementing a Helm repository server (we consume from existing repos)
- Breaking compatibility with existing FluxCD addon users
- Supporting imperative Helm commands (helm upgrade, helm rollback, etc.)
- Competing directly with FluxCD

## Proposal

Add a native `helmchart` component type to KubeVela that:

1. **Renders Helm charts directly** using the Helm Go SDK
2. **Integrates via CueX** as a provider function
3. **Outputs Kubernetes resources** that flow through KubeVela's standard pipeline
4. **Maintains stateless operation** with no server-side release storage
5. **Provides familiar Helm UX** while leveraging KubeVela's features

### User Stories

#### As a KubeVela User
- I want to deploy Helm charts directly without needing to understand or manage FluxCD
- I want a simpler KubeVela installation without mandatory addon dependencies
- I want the flexibility to choose between native Helm support and GitOps workflows
- I want to use existing Helm charts from any registry without modification
- I want a consistent experience whether deploying Helm charts or native components

#### As a Platform Engineer
- I want to deploy Helm charts without installing FluxCD
- I want to use the same Application spec for all deployments
- I want Helm deployments to work with multi-cluster placement
- I want unified revision tracking across all component types

#### As an Addon Developer
- I want to reference upstream Helm charts instead of copying resources
- I want to maintain a single source of truth for my addon
- I want to leverage Helm's templating without duplicating it
- I want my addon to work regardless of FluxCD presence

#### As a KubeVela Developer
- I want to prevent resource drift between our Helm charts and addon definitions
- I want to reduce maintenance overhead by eliminating duplicate resource definitions
- I want a unified installation process for all KubeVela components and extensions
- I want to use our own Helm charts for internal extensions without circular dependencies
- I want to ensure consistency across different installation methods (standalone vs. addon)

## Design Details

### Component Definition Structure (Prelim)

The `helmchart` component would be defined as a built-in ComponentDefinition:

```cue
import "vela/helm"

"helmchart": {
    type: "component"
    annotations: {
        "definition.oam.dev/description": "Helm chart component for deploying Helm packages"
    }
    attributes: {
        workload: type: "autodetects.core.oam.dev"
        status: {
            customStatus: {
                ... // TODO how to implement this in some configurable fashion?
            }
            healthPolicy: {
                ... // TODO how to implement this in some configurable fashion?
            }
        }
    }
    template: {
        parameter: {...} # see below

        renderedChart: helm.#Render & {
            ... # inputs from parameters
        }

        outputs: renderedChart.$returns # supply the rendered chart to the outputs - let KubeVela handle multi-cluster deployment
    }
}
```

### Parameter Schema (Prelim)

```cue
parameter: {
    // Chart source configuration
    chart: {
        // Chart location - automatically detected based on format:
        // - OCI: "oci://ghcr.io/org/charts/app"
        // - Direct URL: "https://example.com/charts/app-1.0.0.tgz"
        // - Repo chart: "postgresql" (requires repoURL to be set)
        source: string

        // Repository URL for repository-based charts
        repoURL?: string     // Repository URL (e.g., "https://charts.bitnami.com/bitnami")

        // Version/tag for repository and OCI charts (ignored for direct URLs)
        version?: string     // Chart version or OCI tag (e.g., "12.1.0", "latest")

        // Authentication (optional)
        auth?: {
            // Reference to Secret
            secretRef?: {
                name: string
                namespace?: string
            }
        }
    }

    // Release configuration (optional - uses context defaults)
    release: {
        name: *context.name | string            // Release name (defaults to component name)
        namespace: *context.namespace | string  // Target namespace (defaults to Application namespace)
    }

    // Value sources (merged in order)
    values?: [...]           // Inline values (highest priority)
    valuesFrom?: [...{
        kind: "Secret" | "ConfigMap" | "OCIRepository"
        name: string
        namespace?: string
        key?: string         // Specific key in ConfigMap/Secret
        url?: string         // For OCIRepository
        tag?: string         // For OCIRepository
        optional?: bool      // Don't fail if source doesn't exist
    }]

    // Rendering options
    options?: {
        includeCRDs?: bool       // Install CRDs from chart (default: true)
        skipTests?: bool         // Skip test resources (default: true)
        skipHooks?: bool         // Skip hook resources (default: false)
        timeout?: string         // Rendering timeout (default: "5m")
        maxHistory?: int         // Revisions to keep (default: 10)
        atomic?: bool            // Rollback on failure (default: false)
        wait?: bool              // Wait for resources (default: false)
        waitTimeout?: string     // Wait timeout (default: "10m")
        force?: bool             // Force resource updates (default: false)
        recreatePods?: bool      // Recreate pods on upgrade (default: false)
        cleanupOnFail?: bool     // Cleanup on failure (default: false)

        // Post-rendering
        postRender?: {
            // Option 1: Kustomize patches
            kustomize?: {
                patches?: [...]
                patchesJson6902?: [...]
                patchesStrategicMerge?: [...]
                images?: [...]
                replicas?: [...]
            }
            // Option 2: External binary
            exec?: {
                command: string
                args?: [...]
                env?: [...]
            }
        }
    }
}
```

### CueX Provider Implementation

The Helm provider would be implemented as a CueX provider at `pkg/cue/cuex/providers/helm/`:

```go
// Provider interface implementation
type Provider struct {
    cache  ChartCache
    client HelmClient
}

// Render is the main provider function
func (p *Provider) Render(ctx context.Context, params *RenderParams) (*RenderReturns, error) {
    // 1. Fetch chart (with caching)
    // 2. Merge values from all sources
    // 3. Render templates
    // 4. Post-process if configured
    // 5. Parse to unstructured objects
    // 6. Order resources (CRDs first, then namespaces, then other resources)
    //    This ensures CRDs are installed before any Custom Resources that depend on them
    //    Without this ordering, CR creation would fail with "no matches for kind" errors
    //    TODO - investigate KubeVela's ability to respect the ordering at deploy time
    // 7. Return to component to handle deployment
    return &RenderReturns{
        ...
    }, nil
}
```

### Chart Source Detection Examples

The simplified API automatically detects the chart source type:

```yaml
# Example 1: Minimal configuration (uses all defaults)
chart:
  source: postgresql
  repoURL: https://charts.bitnami.com/bitnami
  version: 12.1.0

# Example 2: Repository chart with explicit release config
chart:
  source: postgresql
  repoURL: https://charts.bitnami.com/bitnami
  version: 12.1.0
release:
  name: my-postgres  # Override default name
  namespace: database  # Override default namespace

# Example 3: OCI registry chart (detected by oci:// prefix)
chart:
  source: oci://ghcr.io/stefanprodan/charts/podinfo
  version: 6.5.0  # OCI tag

# Example 4: Direct URL chart (detected by .tgz suffix)
chart:
  source: https://github.com/nginx/nginx-helm/releases/download/nginx-1.1.0/nginx-1.1.0.tgz

# Example 5: OCI with authentication and specific version
chart:
  source: oci://myregistry.io/private/chart
  version: 2.1.0  # Specific OCI tag
  auth:
    secretRef:
      name: registry-creds
```

### Integration with Application Controller

The helmchart component integrates with the existing application controller flow:

```
User creates Application
    ↓
Controller processes component
    ↓
CUE template evaluation
    ↓
Helm provider renders chart ← [New]
    ↓
Resources added to workload
    ↓
Standard KubeVela apply flow
    ↓
Multi-cluster dispatch
    ↓
Status aggregation
```

### Caching Strategy

Chart caching is critical for performance:

1. **Cache Key**: `<source_type>/<source_url>/<chart_name>/<version>/<digest>`
2. **Cache Location**: Controller pod's ephemeral storage with configurable size
3. **Cache Eviction**: LRU with configurable TTL (default: 24h)
4. **Cache Validation**: Digest verification on each use
5. **Cache Sharing**: Shared across all helmchart components in the controller

### Multi-cluster Deployment

The helmchart component naturally supports multi-cluster as the chart is rendered to standard KubeVela component outputs:

```yaml
apiVersion: core.oam.dev/v1beta1
kind: Application
metadata:
  name: multi-cluster-postgresql
spec:
  components:
  - name: database
    type: helmchart
    properties:
      chart:
        source: postgresql
        repoURL: https://charts.bitnami.com/bitnami
        version: 12.1.0
      # Using defaults: release.name = "database", release.namespace = Application namespace
      values:
        - global:
            postgresql:
              auth:
                database: myapp
  policies:
  - name: placement
    type: topology
    properties:
      clusters: ["us-west-1", "us-east-1", "eu-central-1"]
```

### Resource Ordering and CRD Handling

Many Helm charts include both Custom Resource Definitions (CRDs) and instances of those custom resources. For example, a Prometheus Operator chart might include:
1. The ServiceMonitor CRD definition
2. ServiceMonitor custom resource instances

Without proper ordering, applying these resources would fail because Kubernetes cannot create a custom resource before its CRD exists. The provider must:

1. **Identify CRDs** - Detect resources with `apiVersion: apiextensions.k8s.io/v1` and `kind: CustomResourceDefinition`
2. **Order resources** - Apply in this sequence:
   - CRDs first
   - Namespaces second (if any resources depend on them)
   - All other resources last
3. **Handle CRD establishment** - Wait for CRDs to be established before applying CRs
4. **Support the `includeCRDs` option** - Allow users to skip CRD installation if they manage CRDs separately

This matches Helm's own behavior where it installs CRDs in a separate phase before other resources.

#### Integration with KubeVela's Apply Logic

**Unknown**: It's currently unclear whether KubeVela's deployment logic respects resource ordering. We have two potential approaches:

**Option 1: Leverage Existing Mechanisms (If Available)**
If investigation reveals that KubeVela has ordering capabilities, the `helmchart` component should:
- Use the `outputs` field for regular resources
- Use special output keys or annotations that KubeVela might recognize for ordering
- Rely on any existing logic to apply outputs in dependency order

**Option 2: Enhance KubeVela's Core (If Needed)**
If KubeVela doesn't currently handle resource ordering, we'll need to enhance the core application controller to:
- Recognize resource types that need ordering (CRDs, Namespaces)
- Apply resources in dependency order
- Wait for CRD establishment before applying CRs
- This would be a beneficial change regardless as it reduces the onus on end-users to do workflow based ordering

**Implementation Note**: During the alpha phase, we'll validate which approach works best with KubeVela's existing architecture. If changes to core are needed, they'll be minimal and backward-compatible.

### Error Handling and Rollback

1. **Rendering Errors**: Caught during render phase, before any resources are applied
2. **Validation Errors**: Chart schema validation before rendering
3. **Apply Errors**: Standard KubeVela error handling with automatic rollback
4. **Rollback**: Uses ApplicationRevision, not Helm's release mechanism

### Migration from FluxCD

For users currently using the FluxCD addon:

**Before (FluxCD)**:
```yaml
apiVersion: core.oam.dev/v1beta1
kind: Application
metadata:
  name: postgresql-fluxcd
spec:
  components:
  - name: database
    type: helm
    properties:
      repoType: helm
      url: https://charts.bitnami.com/bitnami
      chart: postgresql
      version: 12.1.0
      targetNamespace: default
      releaseName: postgres
      values:
        auth:
          database: myapp
```

**After (Native)**:
```yaml
apiVersion: core.oam.dev/v1beta1
kind: Application
metadata:
  name: postgresql-native
spec:
  components:
  - name: database
    type: helmchart
    properties:
      chart:
        source: postgresql
        repoURL: https://charts.bitnami.com/bitnami
        version: 12.1.0
      # release.name defaults to component name "database"
      # release.namespace defaults to Application namespace
      values:
        auth:
          database: myapp
```

## Implementation Plan

### Pre-Implementation Investigation

Before implementation can begin, the following critical items must be confirmed:

1. **Resource Ordering in KubeVela**
   - [ ] Verify if KubeVela's application controller respects output ordering
   - [ ] Test if CRDs can be applied before CRs in the same reconciliation
   - [ ] Determine if core changes are needed for resource dependencies
   - [ ] Document findings and required changes

2. **CueX Provider Integration**
   - [ ] Confirm CueX can handle large rendered outputs (some charts produce 100+ resources)
   - [ ] Confirm ApplicationRevisions can handle large rendered outputs

3. **Multi-cluster Deployment**
   - [ ] Validate that rendered resources can be dispatched to multiple clusters
   - [ ] Confirm per-cluster value overrides work with the proposed design
   - [ ] Test if cluster-specific failures are handled gracefully

4. **Component Definition Capabilities**
   - [ ] Identify plan for how health status can be aggregated from the resources

**Decision Gate**: If any of these investigations reveal blockers, the KEP must be updated with the findings and alternative approaches before proceeding to Phase 1.

### Phase 0: POC (Pre-Alpha)
**Target: ASAP**
1. **Investigate and implement CRD ordering solution**  // critical!
2. Implement basic Helm provider with repository support
3. Create helmchart component definition
4. Basic caching implementation
5. Internal KubeVela addon ("extension") deployable to multi-cluster setup via Helm

**Deliverables**:
- [ ] Demo
- [ ] POC write up
- [ ] KEP updated to reflect issues / findings

### Phase 1: Core Implementation (Alpha)
**Target: v1.10**

1. Refine Helm provider and add OCI support
2. Finalise helmchart component definition
3. Support inline values only
4. Caching implementation tested and finalised
5. Implement CRD ordering solution if needed (based on POC findings)
6. Unit tests for provider

**Deliverables**:
- [ ] Helm provider in cuex/providers/helm
- [ ] Component definition with working CRD ordering
- [ ] Basic examples including charts with CRDs
- [ ] Alpha documentation (not provided externally)
- [ ] Decision on ordering approach (document findings)
- [ ] Internal "Extensions" (KubeVela internal addons) ported to chart components

### Phase 2: Full Features (Beta)
**Target: v1.11**

1. Implement valuesFrom with ConfigMap/Secret support
2. Add post-rendering capabilities
3. Implement more comprehensive caching
4. Integration tests with real charts

**Deliverables**:
- [ ] OCI support
- [ ] ValuesFrom implementation
- [ ] Post-render hooks
- [ ] Performance optimizations
- [ ] Beta documentation

### Phase 3: Production Ready (GA)
**Target: v1.12**

1. Production-grade caching with monitoring
2. Comprehensive test coverage
3. Performance benchmarks
4. Production quality documentation
5. FluxCD addon migration guide

**Deliverables**:
- [ ] Migration documentation
- [ ] Performance benchmarks
- [ ] Production guide

### Test Plan

#### Unit Tests
- Chart fetching from different sources
- Value merging logic
- Template rendering
- Post-processing
- Cache operations

#### Integration Tests
- Deploy popular charts (PostgreSQL, Redis, nginx)
- Multi-cluster deployments
- Value override scenarios
- Upgrade and rollback operations
- CRD installation

#### E2E Tests
- Complete application lifecycle with helmchart
- Migration from FluxCD addon
- Multi-cluster scenarios
- Performance under load

#### Performance Tests
- Rendering time for large charts
- Cache hit ratios
- Memory usage with many charts
- Concurrent rendering performance

### Graduation Criteria

#### Alpha → Beta
- Basic functionality working for common charts
- No critical bugs in core rendering
- Documentation for basic usage
- Positive internal feedback

#### Beta → GA
- Production usage by at least 3 organizations
- Performance meets targets (< 5s for average "fresh" chart - caching should be sub-1s)
- Migration path validated
- Comprehensive documentation
- Positive community feedback
- No critical bugs for 2 releases

### Production Readiness

#### Scalability
- Cache size limits and eviction policies
- Concurrent rendering limits
- Resource limits for provider container

#### Monitoring
- Metrics for cache hit/miss ratios
- Rendering duration histograms
- Error rates by chart source
- Resource usage metrics

#### Security
- Secure credential handling
- Chart signature verification (optional)
- Network policies for chart fetching
- RBAC for accessing secrets/configmaps

## Implementation History

- 2025-01-22: Initial KEP created

## Drawbacks

### Complexity
- Adds another component type to maintain
- Increases codebase size with Helm dependencies
- More complex than simple YAML templates

### Helm Version Coupling
- Need to track Helm library updates (addon developer responsibility)
- Potential compatibility issues with charts
- Dependency on Helm's template engine behavior

### Performance Considerations
- Chart fetching adds latency
- Large charts consume memory during rendering
- Cache management adds operational complexity

## Alternatives

### 1. Continue with FluxCD Addon Only
**Description**: Keep the existing FluxCD-based approach as the only way to deploy Helm charts.

**Benefits**:
- No new code to maintain
- GitOps workflow for those who want it
- Already works and is documented

**Trade-offs**:
- Continues all current pain points
- No solution for bootstrap problem
- Duplication issue persists and blocks KEP-0001
- Requires external dependency for basic Helm requirements

**Why native support is preferred**: Native support eliminates external dependencies, eases addon development and provides better integration with KubeVela's features.

### 2. Shell Out to Helm CLI
**Description**: Use exec to call helm template command instead of using Helm SDK.

**Benefits**:
- Simpler implementation
- Always matches CLI behavior
- Smaller binary size

**Trade-offs**:
- Requires Helm CLI in controller image
- Harder to control and error handling
- Performance overhead of process creation
- Security concerns with shell execution

**Why SDK is preferred**: The SDK provides better performance, security, and integration capabilities.

### 3. Create a Separate Helm Controller
**Description**: Build a dedicated controller for Helm charts, similar to Flux Helm Controller.

**Benefits**:
- Separation of concerns
- Could be used standalone
- Specialized for Helm operations

**Trade-offs**:
- Another controller to deploy and manage
- Complex integration with KubeVela
- Duplicates work already done by Flux
- Doesn't solve the bootstrap problem

**Why provider approach is preferred**: Integrating as a provider keeps the architecture simple and leverages existing KubeVela features.

## References

- [Helm Go SDK Documentation](https://helm.sh/docs/topics/advanced/#using-helm-as-a-library)
- [Flux Helm Controller](https://github.com/fluxcd/helm-controller)
- [KubeVela CueX Documentation](https://kubevela.io/docs/platform-engineers/cue/cuex/)
- [OCI Registry As Storage](https://helm.sh/docs/topics/registries/)
- [Helm Post Rendering](https://helm.sh/docs/topics/advanced/#post-rendering)