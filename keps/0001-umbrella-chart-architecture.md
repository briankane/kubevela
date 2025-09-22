---
title: Umbrella Chart Architecture and Unified Installation
kvep-number: 0001
authors:
  - "@briankane"
area: core
status: draft
creation-date: 2025-09-19
last-updated: 2025-09-19
---

# KEP-0001: Umbrella Chart Architecture and Unified Installation

## Release Signoff Checklist

- [ ] Enhancement issue in repository issues list
- [ ] KEP approvals from maintainers
- [ ] Design details are appropriately documented
- [ ] Test plan is in place, with review from appropriate contributors
- [ ] Rollout strategy is in place
- [ ] Implementation History section is up-to-date for milestone
- [ ] User-facing documentation has been created in [kubevela/kubevela.github.io](https://github.com/kubevela/kubevela.github.io)
- [ ] Supporting documentation completed (design docs, discussions, PRs)

### Notes/Constraints/Caveats

- Maintains backward compatibility with existing installation methods
- Requires coordination across multiple repositories for CRD centralization

### Release Targets

- KubeVela v1.12: direct to stable (no graduation stages)

## Introduction

This enhancement proposes an umbrella chart architecture that unifies KubeVela's fragmented installation experience into a single, declarative, GitOps-compatible deployment while solving resource duplication issues through proper dependency management.

### Tracking Issue

- Issue #[TBD]: [KVEP-0001] Umbrella Chart Architecture and Unified Installation
- 

### Related Issues and KVEPs

- Related to resource duplication issues documented in audit report [TBD]
- Enables future KVEP-0003: Definition Catalog and Independent Versioning

## Motivation

KubeVela suffers from **critical resource version mismatches and fragmentation** across repositories that creates operational risks:

### CRD Version Conflicts
**Critical operational issue:** Workflow CRDs at v0.18.0 in workflow repo but v0.9.0 in kubevela charts
- **Runtime failures:** Mismatched CRD versions can cause controller failures
- **Manual coordination required:** Each CRD update needs PRs to 2-3 repositories
- **Version drift inevitable:** No single source of truth for dependencies
- **Release blocking:** Teams cannot release independently

### Resource Duplication Maintenance
**500+ duplicated resources** across repositories requiring manual synchronization
- **Copy-paste errors:** Same CRDs exist with different versions
- **Update overhead:** Changes must be manually synchronized across repos
- **Coordination burden:** 105 developer hours/month wasted on sync overhead

### Installation Complexity (Consequence)
This fragmentation manifests as a poor user experience:
- **Multi-step process:** `vela install` → `vela addon enable` → `kubectl apply`
- **Not GitOps-friendly:** Cannot capture entire configuration declaratively
- **State drift:** Imperative commands create untracked configuration

## Goals

1. **Unified Installation Experience**
   - **Single command:** `vela install -f config.yaml` or `helm install vela kubevela/vela -f config.yaml`
   - **Fully declarative:** Entire KubeVela configuration captured in one file
   - **GitOps compatible:** Works seamlessly with ArgoCD, Flux, and other GitOps tools

2. **Eliminate Resource Duplication**
   - **Helm dependency management:** Proper chart dependencies replace manual copying
   - **Single source of truth:** Each CRD and resource defined once, consumed everywhere
   - **Automated version consistency:** Dependency versions enforced by Helm

3. **Extension Management**
   - **Optional extensions:** Enable/disable workflow, velaux, trigger via configuration
   - **Post-install flexibility:** Add or remove extensions after initial installation
   - **Independent versioning:** Extensions can update independently of core

## Non-Goals

- Changing the OAM specification or core KubeVela APIs
- Breaking backward compatibility for existing installations
- Forcing migration - old installation methods will continue to work
- Definition catalog reorganization (separate KVEP-0003)
- Comprehensive testing framework for definitions (separate KVEP-0003)

## Proposal

Create an umbrella Helm chart that orchestrates KubeVela's ecosystem through proper dependency management, replacing the current fragmented installation approach.

### User Stories

#### As a Platform Engineer managing KubeVela deployments
- I want to install KubeVela with extensions using a single command so that my entire infrastructure deployment is declarative and Git-tracked
- I want to deploy KubeVela declaratively through GitOps with no CLI commands required so that everything is version-controlled and reproducible
- I want to manage extensions independently (enable/disable/upgrade/rollback) without affecting core KubeVela so that I can maintain different configurations per environment and handle incidents quickly
- I want visibility into extension versions across my fleet and their CRD dependencies so that I can track compliance and avoid compatibility issues

#### As a KubeVela Core Developer
- I want to update shared CRDs in one place without manually creating PRs across multiple repositories so that I can ship fixes faster
- I want CRD changes to automatically propagate to all consuming repositories so that I don't spend hours per release coordinating versions
- I want CRD versions to be managed centrally so that version drift between repositories is impossible and deployments never fail with schema mismatches

#### As a new KubeVela User
- I want to install KubeVela with examples and common definitions in one command so that I can start building applications immediately
- I want the installation to include workflow capabilities by default so that I don't have to research and manually enable required addons

## Design Details

### 1. Umbrella Chart Structure

```yaml
# kubevela/charts/vela/Chart.yaml
apiVersion: v2
name: vela
version: 1.11.0  # Always matches KubeVela core version
dependencies:
  # Core (always installed)
  - name: vela-core
    version: "1.11.0"  # Matches umbrella chart version
    repository: "https://charts.kubevela.net/core"

  # Optional extensions (independently versioned)
  - name: vela-workflow
    version: "0.9.0"  # Independent versioning
    repository: "https://charts.kubevela.net/workflow"
    condition: workflow.enabled

  - name: kube-trigger
    version: "0.2.0"  # Independent versioning
    repository: "https://charts.kubevela.net/trigger"
    condition: trigger.enabled

  - name: vela-prism
    version: "1.11.0"  # Independent versioning
    repository: "https://charts.kubevela.net/prism"
    condition: prism.enabled
```

#### 2.1 Installation Methods

> **ISSUE**: This should work for single-cluster setups as Helm will install to the local cluster. In multi-cluster setups, it won't propagate through to the spokes which is a requirement for kube-trigger and workflow (they need a controller on each cluster vs. KubeVela which can run hub-only).
>
> Need to solve for how we can rollout to ALL clusters at install time in a multicluster setup. Currently the fluxcd addon is required to use Helm charts - but we cannot depend on the installation of an addo during the initial core installation.

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

#### 2.2 Post-Installation Management

**Standard Helm Operations (v1.12):**
```bash
# Users can manage extensions using standard Helm
helm upgrade vela kubevela/vela --set workflow.enabled=false
helm upgrade vela kubevela/vela --set trigger.enabled=true
helm upgrade vela kubevela/vela -f updated-config.yaml
```

**Future CLI Enhancements (Separate KVEP Required):**
```bash
# Note: These commands would require a separate KVEP to design and implement
vela extension enable workflow   # Wrapper around Helm operations
vela extension disable velaux    # Graceful component shutdown
vela version show                # Component version visibility
vela reconfigure -f config.yaml  # Declarative reconfiguration
```

#### 2.3 Values Aggregation
Will implement build-time generation of comprehensive values.yaml from component charts

### 3. Shared CRD Management

To eliminate version mismatches, shared CRDs will be centralized in a new `kubevela-common` repository (forked from the existing `pkg` repo):

#### 3.1 CRD Organization
```
kubevela-common/
├── charts/
│   ├── vela-workflow-crds/       # Shared CRDs
│   │   └── crds/
│   │       ├── core.oam.dev_workflows.yaml
│   │       └── core.oam.dev_workflowruns.yaml
│   └── vela-cue-crds/
│       └── crds/
│           └── cue.oam.dev_packages.yaml
```

**Decision Rule:** CRDs used by 2+ repositories go to kubevela-common, component-specific CRDs stay in their respective repos.

#### 3.2 Dependency Integration
The umbrella chart will include shared CRDs as dependencies, ensuring version consistency:
```yaml
dependencies:
  - name: vela-workflow-crds
    version: "0.18.0"
    repository: "https://charts.kubevela.net/common"
```

### 4. Development Workflow

#### 4.1 Local Development
```makefile
# Enhanced core-install target (existing target)
core-install: manifests
	kubectl apply -f hack/namespace.yaml
	# Install local CRDs (as before)
	kubectl apply -f charts/vela-core/crds/
	# NEW: Pull and install shared CRDs from kubevela-common
	@curl -sL https://raw.githubusercontent.com/kubevela/kubevela-common/main/charts/vela-workflow-crds/crds/*.yaml | kubectl apply -f -
	@$(OK) install succeed
```

#### 4.2 Release Process
1. Shared CRDs released from kubevela-common
2. Component releases reference specific CRD versions
3. Master chart pins all component versions

### 5. Addon Evolution

#### 5.1 Internal KubeVela Extensions (Legacy Addons)
Convert to KubeVela `helm` component references during transition period, then deprecate:

**Transition Strategy:**
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

**Deprecation Timeline:**
- **v1.12 (Immediate):** Addons converted to Helm component references (maintains backward compatibility) and deprecation warnings added

**Rationale:** Internal addons for KubeVela extensions (workflow, velaux, trigger) duplicate functionality now provided by the umbrella chart. The transition period allows users to migrate while maintaining existing workflows.

#### 5.2 External Tool Addons
No impact - external addon functionality will remain entirely intact. These remain as addons since they integrate external tools rather than extending KubeVela itself.

## Implementation Plan

### Proposed Phases

All phases target Kubvela v1.12:

#### Phase 1: CRD Centralization
1. Create kubevela-common repository (fork from pkg)
2. Migrate shared CRDs to kubevela-common
3. Establish CI/CD pipelines for CRD publishing
4. Fix version mismatches across repositories

#### Phase 2: Umbrella Chart Architecture
5. Create umbrella Helm chart in kubevela/kubevela
6. Define extension dependencies and versioning
7. Implement conditional installation flags
8. Update extension repos to publish to Helm repositories

#### Phase 3: Documentation and UX
9. Enhance `vela install` to support umbrella chart installation
10. Build comprehensive configuration documentation
11. Create GitOps examples and migration guides
12. Update existing documentation to reflect new installation method

**Note:** Advanced CLI features like `vela extension` commands are out of scope and will be handled by follow up KVEPs for design and implementation.

### Test Plan

- **Integration testing:** Umbrella chart installation in multiple environments
- **Backward compatibility:** Verify existing installations remain functional
- **Extension isolation:** Test individual extension enable/disable
- **CLI validation:** Test enhanced installation and management commands

### Rollout Strategy

- **Direct to stable:** Umbrella chart architecture ships as the standard installation method in v1.12
- **Backward compatibility maintained:** Existing `vela install` and addon commands continue to work
- **Migration documentation:** Clear guides for users to transition from addon-based to umbrella chart installations
- **Deprecation timeline:** Current addon-based approach deprecated after update to Helm based installs

## Success Metrics

**Primary Metrics:**
- **Community satisfaction:** Positive feedback from users on installation experience through Discord, GitHub issues, and community calls
- **Reduced CRD-related issues:** 75% reduction in GitHub issues related to CRD version conflicts, installation problems, and resource duplication within 6 months post-deployment

**Supporting Technical Metrics:**
1. **Single-command installation:** Users can install entire KubeVela stack with `vela install -f config.yaml`
2. **Zero CRD version conflicts:** Eliminate version mismatches through dependency management
3. **Reduced maintenance overhead:** 90% reduction in manual resource synchronization across repositories

## Migration Support

- **Backward compatibility** via archived pkg repo
- **Gradual migration** path for addons
- **Version mapping** documentation

## Risks and Mitigations

| Risk | Impact | Mitigation |
|------|--------|------------|
| Breaking existing installations | High | • Careful version management<br>• Gradual rollout |
| Complex migration | Medium | • Phase approach<br>• Rollback capability |
| Increased CI complexity | Low | • Build automation upfront |
| User confusion | Medium | • Extensive user-friendly documentation<br>• Migration guides |

## Implementation History

- 2025-09-19: KVEP created and initial draft completed
- TBD: Enhancement issue created for community discussion
- TBD: Implementation started

## Drawbacks

- **Increased complexity during transition:** Forces testing of CRD updates (but this should be happening regardless)
- **Learning curve:** Users need to understand new Helm-based installation approach
- **CI/CD changes required:** All repositories need pipeline updates for new chart structure
- **Increased release coordination** due to the additional chart

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

### 2. Version Synchronization Only
**Description:** Keep current repository structure, only fix CRD version mismatches.

**Benefits:**
- Minimal disruption to existing workflows
- Quick implementation
- Low risk of breaking changes

**Trade-offs:**
- Doesn't address the 500+ duplicated resources
- Manual synchronization still required
- No solution for independent definition versioning

**Why this proposal is preferred:** While version sync addresses immediate pain, the proposed architecture eliminates duplication entirely and enables independent versioning.