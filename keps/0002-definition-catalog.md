---
title: Definition Catalog and Independent Versioning
kvep-number: 0003
authors:
  - "@briankane"
area: definitions
status: provisional
creation-date: 2025-09-19
last-updated: 2025-09-19
---

# KEP-0002: Definition Catalog and Independent Versioning

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

- Migration must maintain backward compatibility
- Phased rollout required across multiple repositories
- Community coordination needed for timeline
- Documentation updates across multiple sites/repos required

### Release Targets

- KubeVela v1.13: Direct to stable (no graduation stages)

## Introduction

This enhancement proposes a definition catalog reorganization that centralises shared X-Definitions, reduces incompatibilities between products and enables independent versioning of X-Definitions separate from core KubeVela releases.

### Tracking Issue
- Issue #[TBD]: [KVEP-0002] Definition Catalog and Independent Versioning

### Related Issues and KVEPs
- KVEP-0002: Umbrella Chart Architecture and Unified Installation (enables this KVEP)
- Issue #[TBD]: Definition versioning independent of KubeVela core releases
- Issue #[TBD]: Definition testing framework and quality improvements

## Motivation

Current issues identified in the [Resource Duplication Audit](../docs/resource-duplication-audit-report.md):

- **Definitions too tightly coupled to product releases:** Minor CUE fixes in definitions require full KubeVela releases, forcing users to upgrade their entire installation for simple bug fixes
  - **No consistency enforcement:** Different products (kubevela vs workflow) can ship conflicting versions of the same definitions
  - **Addon installation drift:** Users can install extensions with definitions incompatible with their KubeVela version (partly resolvable in related KEP-0001)

- **No independent definition updates** - tied to core releases
  - **Cross-version fixes impossible:** Cannot backport definition fixes to older KubeVela versions
  - **User impact:** Critical bug in definition requires waiting for next KubeVela release
  - **Version coupling:** Definition at v1.0.1 unnecessarily requires KubeVela upgrade to v1.10.4

- **Addon system duplication** - maintaining copies instead of referencing sources

## Goals

1. **Independent definition versioning** - Definitions can update without core KubeVela releases
2. **Definition quality framework** - Enable steps toward comprehensive testing for all definitions
   - Tests colocated with definitions
   - Each definition tested in isolation with its own fixtures
   - Version matrix testing across KubeVela versions & products
3. **Catalog organization** - Clear staging (stable/beta/alpha) and discovery
4. **Cross-product consistency** - Same definition versions across kubevela, workflow, and extensions
5. **Fast iteration** - Definition bug fixes and improvements ship independently of major releases

## Non-Goals

- Changing the OAM specification or underlying X-Definition structures
- Breaking backward compatibility for existing users
- Forcing all components into KubeVela core (maintaining standalone capabilities)

## Proposal

This enhancement proposes restructuring KubeVela's resource management through:

1. **Centralized Shared Resources** - Move X-Definitions to the existing `catalog` repository for centralised management independent of Kubevela codebase/s
2. **Master Chart Architecture** - Integrate with the KubeVela umbrella chart architecture outlined in KVEP-0002
3. **Definition Catalog Restructuring** - Organize X-Definitions in the catalog with staging support (stable/beta/alpha)
4. **Enable Comprehensive Testing Framework** - Implement steps toward isolated testing for definitions with version matrix support
5. **Enhanced Installation Experience** - Single declarative installation via Helm with post-install management capabilities (for hotfixing etc.)

This approach maintains backward compatibility while eliminating duplication and enabling independent evolution of components.

### User Stories

#### As a KubeVela User
- I want to upgrade individual definitions without upgrading KubeVela core so that I can get bug fixes quickly
- I want to receive hotfixes for definition issues with a clear, simple installation method so that I can resolve problems immediately without waiting for major releases
- I want confidence that definitions are stable and well-tested across KubeVela releases so that my applications won't break due to definition quality issues
- I want to be able to test upcoming features (alpha/beta definitions) in a suitable environment ahead of general release so that I can provide feedback and prepare for adoption
- I want clear documentation on what each component does so that I can choose the right configuration

#### As a KubeVela Definition Contributor
- I want to release my X-Definitions independently so that users can get updates without waiting for KubeVela mainline releases
- I want my definition to be guaranteed to work both standalone and as part of KubeVela so that I can serve different user needs
- I want an ability to test my definitions across products (where applicable) and co-located with my definitions so that I can validate compatibility
- I want issues in my components to be identified before merging to the codebase so that users don't encounter broken functionality
- I want to test my definitions in isolation from Kubevela so that I can iterate quickly
- I want to publish alpha/beta versions so that users can opt into experimental features
- I want my X-Definitions versioned independently so that fixes can be backported
- I want automated publishing so that releases are consistent and reliable

#### As a KubeVela Core Developer
- I want to focus on the KubeVela platform without being responsible for individual definition maintenance so that I can concentrate on core functionality
- I want to see the impact of my platform changes on existing definitions immediately so that I can catch breaking changes before they affect users
- I want definition contributors to own their testing and releases so that core development isn't blocked by definition issues

## Design Details

### CRD Centralization Strategy

#### Fork pkg to kubevela-common
```bash
# Create new repository with better name
github.com/kubevela/pkg → github.com/kubevela/kubevela-common (forked)
github.com/kubevela/pkg → archived (read-only for compatibility)
```

#### Shared CRD Structure
```
kubevela-common/
├── charts/
│   ├── vela-workflow-crds/       # Shared by kubevela + workflow
│   │   └── crds/
│   │       ├── core.oam.dev_workflows.yaml
│   │       ├── core.oam.dev_workflowruns.yaml
│   │       └── core.oam.dev_workflowstepdefinitions.yaml
│   └── vela-cue-crds/            # Shared by kubevela + workflow
│       └── crds/
│           └── cue.oam.dev_packages.yaml

# Component-specific CRDs remain in their repos:
kube-trigger/
├── charts/
│   └── kube-trigger/
│       └── crds/
│           ├── standard.oam.dev_eventlisteners.yaml    # Only used by kube-trigger
│           └── standard.oam.dev_triggerservices.yaml   # Only used by kube-trigger

kubevela/
├── charts/
│   └── vela-core/
│       └── crds/
│           ├── core.oam.dev_applications.yaml          # Only used by kubevela
│           ├── core.oam.dev_componentdefinitions.yaml  # Only used by kubevela
│           └── core.oam.dev_traitdefinitions.yaml      # Only used by kubevela
```

**Decision Rule:** CRD will go to kubevela-common if used by 2+ repositories, will stay local if used by only one

#### Version Synchronization
- Will fix all controller-gen versions to v0.18.0
- Will establish single source for each CRD
- Will implement automated sync validation in CI

### Definition Chart Integration (builds on KVEP-0002)

```yaml
# kubevela/charts/vela/Chart.yaml - Extended with definition charts
apiVersion: v2
name: vela
version: 1.11.0  # Always matches KubeVela core version
dependencies:
  # Core (always installed)
  - name: vela-core
    version: "1.11.0"
    repository: "https://charts.kubevela.net/core"

  # Extensions (optional)
  - name: vela-workflow
    version: "0.9.0"
    repository: "https://charts.kubevela.net/workflow"
    condition: workflow.enabled

  # Definition Charts (user-configurable versions with sensible defaults)
  - name: vela-core-definitions
    version: "{{ .Values.definitions.core.version | default "1.5.2" }}"       # Allow user overrides to take fixes
    repository: "https://charts.kubevela.net/definitions"

  - name: vela-workflow-definitions
    version: "{{ .Values.definitions.workflow.version | default "1.2.1" }}"
    repository: "https://charts.kubevela.net/definitions"
    condition: workflow.enabled # only install if workflow is in use

  - name: vela-trigger-definitions
    version: "{{ .Values.definitions.trigger.version | default "1.0.3" }}"
    repository: "https://charts.kubevela.net/definitions"
    condition: trigger.enabled # only install if kube-trigger is in use
```

#### Installation Methods

**Current (Complex):**
```bash
# Step 1: Install core
vela install

# Step 2: Enable addons (imperative)
vela addon enable fluxcd
vela addon enable velaux
vela addon enable prometheus

# Step 3: Configure each separately
kubectl apply -f custom-configs/
```

**New (Simple & Declarative):**
```bash
# Enhanced vela install wraps Helm with better UX
vela install -f my-config.yaml

# Or with inline flags
vela install --set workflow.enabled=true --set velaux.enabled=true

# Under the hood, vela install will:
# 1. Validate configuration
# 2. Check prerequisites
# 3. Execute: helm install vela kubevela/vela -f my-config.yaml
# 4. Verify installation health
```

> *ISSUE*: This will work for single-cluster setups as Helm will install to the local cluster. In multi-cluster setups, it won't propagate through to the spokes which is a requirement for kube-trigger and workflow (they need a controller on each cluster vs. KubeVela which can run hub-only).

**Direct Helm (Advanced Users):**
```bash
# Power users can bypass CLI wrapper
helm install vela kubevela/vela -f my-config.yaml
```

**Configuration File (my-config.yaml):**
```yaml
# Entire setup declaratively
workflow:
  enabled: true
  replicas: 3
velaux:
  enabled: true
  ingress:
    enabled: true
    host: vela.example.com
trigger:
  enabled: false
definitions:
  beta:
    enabled: true  # Opt into beta features
```

**GitOps-Compatible:**
```yaml
# Complete installation via ArgoCD/Flux
apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: kubevela
spec:
  source:
    chart: vela
    helm:
      values: |
        workflow:
          enabled: true
        trigger:
          enabled: true
```

#### Post-Installation Management

**Enhanced CLI Commands:**
```bash
# Update definitions independently
vela def upgrade --version=1.10.4             # Update all definitions via Helm
vela def upgrade webservice --version=1.0.2   # Single definition hotfix

# How single definition upgrade will work:
# 1. Will fetch specific definition version from catalog
# 2. Will apply directly via kubectl, bypassing Helm
# 3. Next Helm upgrade will reconcile this change
# Example implementation:
#   curl https://catalog.kubevela.io/definitions/core/webservice/v1.0.2.yaml | \
#   kubectl apply -f -

# Manage extensions dynamically
vela extension enable workflow                # Enable post-install
vela extension disable velaux                 # Scale to 0, keep CRDs/configs
vela extension remove trigger                 # Complete removal

# How disable will work:
# - Scale deployments to 0 replicas (pods stopped)
# - Disable webhooks if present
# - Keep CRDs, ConfigMaps, Secrets intact
# - Add annotation: vela.io/extension-disabled: "true"
# - Re-enable later with: vela extension enable velaux

# Version management
vela version show                             # Show all component versions
vela version check                            # Check for available updates
vela version upgrade --component=definitions  # Selective upgrade

# Reconfigure existing installation
vela reconfigure -f new-config.yaml           # Apply new configuration
```

#### Values Aggregation
Will implement build-time generation of comprehensive values.yaml from component charts

### X-Definitions Organization

#### Catalog Structure
```
catalog/
├── definitions/
│   ├── core/
│   │   └── webservice/
│   │       ├── metadata.yaml
│   │       ├── README.md
│   │       ├── CHANGELOG.md
│   │       ├── stable/
│   │       │   ├── definition.cue
│   │       │   └── definition.yaml     #generated from cue
│   │       ├── beta/
│   │       │   ├── definition.cue  
│   │       │   └── definition.yaml     #generated from cue
│   │       ├── alpha/
│   │       │   ├── definition.cue
│   │       │   └── definition.yaml     #generated from cue
│   │       ├── test/
│   │       │   └── stable_test.go
│   │       └── examples/
│   │           └── basic-app.yaml
│   ├── workflow/
│   ├── policy/
│   └── common/
└── addons/              # External integrations only
```

#### Stage-based Naming
```yaml
# Stable (no suffix)
name: webservice

# Beta (suffixed for safety)
name: webservice-beta
annotations:
  definition.oam.dev/stage: beta
  definition.oam.dev/stable-name: webservice

# Alpha (suffixed for safety)
name: webservice-alpha
annotations:
  definition.oam.dev/stage: alpha
  definition.oam.dev/stable-name: webservice
```

#### Conditional Installation
```yaml
# In Helm templates
{{- if .Values.beta.enabled }}
# Beta definitions here
{{- end }}

{{- if .Values.alpha.enabled }}
# Alpha definitions here
{{- end }}
```

### Testing Framework

#### CLI Integration
```bash
# New vela CLI commands
vela def test catalog/definitions/core/webservice
vela def test catalog/definitions/core/* --vela-version=1.10.3
vela def validate catalog/definitions/core/webservice
vela def test --matrix  # Test against version matrix
```

#### Test Structure

**Test Isolation:**
```
catalog/definitions/core/webservice/
├── test/
│   ├── stable_test.go      # Tests stable webservice
│   ├── beta_test.go        # Tests beta webservice changes
│   └── fixtures/           # Test data for the definition
```

**Decoupling from Core:**
- Tests will run without full KubeVela installation
- Should mock minimal OAM runtime for testing where possible
- Should have no dependency on kubevela/kubevela test suites
- Should be able to test against multiple KubeVela versions without rebuilding

**Test Execution:**
```bash
# Run single definition test
cd catalog/definitions/core/webservice
go test ./test/...

# Or via vela CLI (which sets up minimal runtime)
vela def test ./

# Test against specific version
vela def test ./ --vela-version=1.9.0
```

**Benefits:**
- Definition maintainers can test without understanding KubeVela internals
- Fast feedback loop (seconds vs minutes)
- Parallel test execution across definitions
- Clear ownership and responsibility

### Development Workflow

#### Local Development
```makefile
# Enhanced core-install target (existing target)
core-install: manifests
	kubectl apply -f hack/namespace.yaml
	# Install local CRDs (as before)
	kubectl apply -f charts/vela-core/crds/
	# NEW: Pull and install shared CRDs from kubevela-common
	@curl -sL https://raw.githubusercontent.com/kubevela/kubevela-common/main/charts/vela-workflow-crds/crds/*.yaml | kubectl apply -f -
	# NEW: Pull and install definitions from catalog
	@curl -sL https://raw.githubusercontent.com/kubevela/catalog/main/charts/definitions/templates/stable/*.yaml | kubectl apply -f -
	@$(OK) install succeed
```

#### Release Process
1. CRDs released from kubevela-common
2. Definitions released from catalog
3. Component releases reference specific versions
4. Master chart pins all versions

### Addon Evolution

#### Internal KubeVela Addons
Convert to KubeVela `helm` component references:
```yaml
# Old: catalog/addons/vela-workflow/resources/
# Copies of CRDs, deployments, etc.

# New: catalog/addons/vela-workflow/resources/workflow-component.yaml
apiVersion: core.oam.dev/v1beta1
kind: Application
metadata:
  name: vela-workflow
spec:
  components:
  - name: vela-workflow
    type: helm  # Uses fluxcd addon's helm component
    properties:
      repoType: helm
      url: https://charts.kubevela.net
      chart: vela-workflow
      version: "0.9.0"
      targetNamespace: vela-system
      releaseName: vela-workflow
```

#### External Tool Addons
Remain as integration definitions only:
- Don't install the tool (user does that)
- Provide traits/definitions for integration
- Clear prerequisites in metadata

## Implementation Plan

### Proposed Phases

#### Phase 1: Definition Migration
1. Create directory structures in catalog
2. Copy current definitions as stable (identifying most stable version when conflicts arise between repos)
3. Create metadata.yaml for each
4. Create test directories
5. Build CUE → YAML rendering pipelines

#### Phase 2: Advanced Features
6. Implement alpha/beta suffixing
7. Generate charts with conditionals
8. Build publishing pipelines
9. Update repos to pull from catalog
10. Update umbrella chart with definition charts

#### Phase 3: Quality (Ongoing)
11. Add comprehensive tests for all definitions
12. Implement vela def test commands
13. Create contribution guidelines

### Test Plan

#### Unit Testing
- Test definition compilation and rendering in isolation
- Validate CRD schema compatibility across versions
- Test Helm chart dependency resolution
- Validate migration scripts

#### Integration Testing
- Test umbrella chart installation with various configurations
- Validate cross-component compatibility
- Test upgrade paths from current to new architecture
- Verify addon migration scenarios

#### Performance Testing
- Ensure installation time is neglibily affected
- Test definition loading performance
- Validate CI pipeline performance with new structure

#### User Acceptance Testing
- Test new installation workflows
- Validate backward compatibility scenarios
- Test definition update workflows
- Verify GitOps integration


### Production Readiness

#### Deployment Considerations
- Phased rollout of definition charts alongside umbrella chart architecture (KVEP-0002)
- Definitions can upgrade independently via Helm dependencies
- Backward compatibility maintained through existing definition installation methods

#### Operational Requirements
- Helm 3.x for definition chart dependency management
- Catalog repository for definition source management
- Container registries for definition chart hosting

#### Migration Approach
- Gradual migration path from embedded definitions to chart-based approach
- Clear version mapping between old and new definition installations
- Rollback supported via Helm revision history

## Implementation History

- 2025-09-19: Initial KVEP created
- 2025-09-19: KVEP marked as provisional for review

## Drawbacks

### Increased Complexity
- More repositories to manage during transition
- Complex dependency chains between charts
- Learning curve for new architecture

### Migration Risk
- Potential for breaking existing installations
- Coordination required across multiple teams
- Temporary increase in maintenance overhead

### Tooling Dependencies
- Reliance on Helm for dependency management
- Additional CI/CD infrastructure needed
- New failure modes in chart publishing

### Community Impact
- Contributors need to understand new structure
- Documentation needs comprehensive updates
- Support burden during transition period

## Alternatives

### 1. Consolidate into Single KubeVela Product
**Description:** Remove standalone installation capability for components like workflow and kube-trigger, consolidating everything into a unified KubeVela core product.

**Benefits:**
- Eliminates all duplication by design
- Simplified testing and integration
- Single installation and upgrade path
- Unified documentation and user experience

**Trade-offs:**
- Abandons existing standalone users (workflow, kube-trigger)
- Loses architectural flexibility for diverse use cases
- Requires extensive rework of existing standalone features
- Significant migration effort for current standalone deployments

**Why this proposal is preferred:** Preserving standalone capabilities honors the investment already made in component independence while solving duplication through centralized shared resources. This maintains user choice and existing deployment patterns.

## References

- [Resource Duplication Audit](../docs/resource-duplication-audit-report.md)
- [Duplication Summary Visual](../docs/duplication-summary-visual.md)
- [Duplication Tables](../docs/duplication-tables.md)
- [KubeVela Definition Version Control](https://kubevela.io/docs/next/end-user/definition-version-control/)