---
title: Local Cluster Label Support
KEP-number: XXXX
authors:
  - "@author"
area: core
status: implementable
creation-date: 2025-09-23
last-updated: 2025-09-23
---

# KEP-0011: Local Cluster Label Support

## Release Signoff Checklist

- [ ] Enhancement issue in repository issues list
- [ ] KEP approvals from maintainers
- [ ] Design details are appropriately documented
- [ ] Test plan is in place, with review from appropriate contributors
- [ ] Graduation criteria is in place
- [ ] "Implementation History" section is up-to-date for milestone
- [ ] User-facing documentation has been created in [kubevela/kubevela.github.io](https://github.com/kubevela/kubevela.github.io)
- [ ] Supporting documentation completed (design docs, discussions, PRs)

### Notes/Constraints/Caveats

- Must maintain backward compatibility with existing "local" cluster behavior
- Labels are metadata only, don't affect cluster connectivity
- Should align with managed cluster labeling patterns

### Release Targets

- KubeVela v1.N: Implementation and GA release (simple addition)

## Summary

This KEP proposes adding label support for the local (hub) cluster in KubeVela. Currently, only managed clusters (stored as Secrets or Cluster resources) support labels, while the local cluster is hard-coded without metadata storage. This creates an inconsistency and prevents cluster-aware definitions from working on the hub cluster.

### Tracking Issue
- Issue #[TBD]: [KEP-XXXX] Local cluster label support

### Related Issues and KEPs
- KEP-0010: Expose cluster labels in application context
- Multi-cluster deployment features

## Motivation

### Current Problem

The local cluster in KubeVela is treated as a special case:

```go
// Current implementation (simplified)
if clusterName == "local" || clusterName == "" {
    // Use in-cluster config directly
    // No place to store/retrieve labels
    return &Cluster{Name: "local", Config: inClusterConfig}
}
```

This creates several issues:

1. **Inconsistent behavior**: Cluster-aware definitions that will use `context.clusterLabels` (KEP-0010) fail on local cluster
2. **Cannot apply governance**: Local cluster can't have governance labels like managed clusters
3. **Testing difficulties**: Can't test cluster-label-based logic without remote clusters
4. **Platform limitations**: Can't use labels for hub cluster characteristics (region, provider, etc.)

### Goals

- Enable label storage and retrieval for the local cluster
- Maintain backward compatibility with existing local cluster usage
- Provide consistent labeling interface across all clusters
- Allow hub cluster to participate in cluster-aware deployments

### Non-Goals

- Change how local cluster authentication works
- Require labels on local cluster
- Modify local cluster connectivity

## Proposal

### User Stories

#### Story 1: Consistent Cluster Metadata
As a platform engineer, I want to label my hub cluster with the same metadata (region, environment, compliance-level) as my managed clusters, so my cluster-aware definitions work consistently across all clusters.

#### Story 2: Hub Cluster Testing
As a developer, I want to test my cluster-label-based definitions on the local cluster without needing remote clusters, so I can develop and test locally.

#### Story 3: Hub Cluster Governance
As a platform team, I want to apply governance labels to the hub cluster for compliance and cost tracking, ensuring all clusters including the hub follow organizational standards.

#### Story 4: Meaningful Hub Cluster Naming
As a platform engineer, I want to give the hub cluster a meaningful display name like "production-hub" instead of just "local", making it clearer in multi-cluster views while maintaining backward compatibility with existing scripts using "local".

#### Story 5: Hub vs Managed Cluster Detection
As a definition author, I want to detect whether my workload is deploying to a hub or managed cluster using a standard `type` label, so I can adjust behavior appropriately (e.g., different resource limits, security policies).

### Risks and Mitigations

| Risk | Likelihood | Impact | Mitigation |
|------|------------|--------|------------|
| Breaking existing local cluster usage | Low | High | Make labels optional, default behavior unchanged |
| Confusion about where labels are stored | Medium | Low | Clear documentation, CLI feedback |
| Label persistence issues | Low | Medium | Use ConfigMap with proper RBAC |

## Design Details

### Storage Options

#### Option 1: ConfigMap in vela-system
```yaml
apiVersion: v1
kind: ConfigMap
metadata:
  name: local-cluster-metadata
  namespace: vela-system
  labels:
    app.kubernetes.io/managed-by: kubevela
    cluster.core.oam.dev/local: "true"
data:
  labels: |
    alias: production-hub
    region: us-west-2
    # ...
```

**Pros**:
- Simple, uses existing Kubernetes resources
- Easy to backup and manage

**Cons**:
- Inconsistent with managed cluster storage (they use Secrets)
- Requires different handling logic

#### Option 2: Virtual Secret (Recommended)
Create a Secret similar to managed clusters but with special handling to avoid interference:

```yaml
apiVersion: v1
kind: Secret
metadata:
  name: local-cluster-metadata  # Different name to avoid conflicts
  namespace: vela-system
  labels:
    # Special label to identify this as local cluster metadata
    cluster.core.oam.dev/cluster-type: "local-metadata"
    # User labels
    alias: production-hub
    type: hub  # Default, can be overridden
    region: us-west-2
    environment: hub
    cloud-provider: aws
    cluster-type: eks
    compliance-level: high
type: Opaque
data:
  # Empty or minimal data to maintain Secret validity
  placeholder: ""  # Base64 encoded empty string
```

Key differences from managed cluster Secrets:
- **Name**: Uses `local-cluster-metadata` instead of just `local` to avoid any naming conflicts
- **Identifier label**: Uses `cluster.core.oam.dev/cluster-type: "local-metadata"` instead of `cluster-credential-type`
- **No credentials**: Contains only placeholder data since local cluster uses in-cluster config

**Pros**:
- Consistent with managed cluster storage format (both use Secrets)
- Same label management patterns
- GitOps friendly

**Cons**:
- Slightly more complex to distinguish from real cluster secrets

### Implementation Approach

When retrieving any cluster:
1. If cluster name is "local" or empty:
   - Create cluster object with in-cluster config
   - Set cluster.Type = "hub" (internal field)
   - Try to load Secret "local-cluster-metadata" from vela-system namespace
   - If Secret exists:
     - Extract labels from Secret metadata
     - Ensure `type=hub` label exists (add if missing)
     - If "alias" label exists, use as display alias
   - If Secret doesn't exist:
     - Use minimal default labels: `type=hub`
   - Return cluster with labels

2. For managed clusters:
   - Retrieve cluster normally (Secret or Cluster CRD)
   - Skip if Secret has label `cluster.core.oam.dev/cluster-type: "local-metadata"`
   - Set cluster.Type = "managed" (internal field)
   - Ensure `type=managed` label exists (for topology policy selection)
   - If "alias" label exists, use as display alias
   - Return cluster

Display logic:
- Show alias if exists, otherwise show cluster name
- Internal operations still use original cluster name ("local")

Context behavior in CUE templates (applies to ALL clusters):
- `context.cluster`: Always returns actual cluster name for compatibility (e.g., "local", "staging-west")
- `context.clusterType`: Returns "hub" or "managed" (from internal cluster type, not a label)
- `context.clusterAlias`: Returns the "alias" label if set, otherwise falls back to cluster name
- `context.clusterLabels["alias"]`: Returns the alias if set (e.g., "production-hub", "staging-us-west")
- This ensures backward compatibility while providing a consistent way to get display names and cluster types

Example usage:
```cue
template: {
    metadata: {
        annotations: {
            // Shows friendly name for any cluster (local or managed)
            "deployed-to": context.clusterAlias

            // Always shows actual cluster name for compatibility
            "cluster-name": context.cluster

            // Shows cluster type (from internal field)
            "cluster-type": context.clusterType  // "hub" or "managed"
        }
    }

    // Different behavior based on cluster type
    if context.clusterType == "hub" {
        resources: limits: {cpu: "2", memory: "4Gi"}
    }
    if context.clusterType == "managed" {
        resources: limits: {cpu: "1", memory: "2Gi"}
    }
}
```

Topology policy usage:
```yaml
policies:
- name: deploy-to-hubs
  type: topology
  properties:
    clusterLabelSelector:
      type: hub  # Selects all hub clusters using the label
- name: deploy-to-production
  type: topology
  properties:
    clusterLabelSelector:
      environment: production
      type: managed  # Only production managed clusters
```

### CLI Support

```bash
# Label the local cluster with alias
vela cluster label local alias=production-hub region=us-west-2 environment=hub

# Label a managed cluster with alias
vela cluster label staging-west alias=staging-us-west

# View clusters with aliases
vela cluster list
NAME            ALIAS             TYPE     PROVIDER  LABELS
local           production-hub    hub      -         type=hub,alias=production-hub,region=us-west-2,environment=hub
staging-west    staging-us-west   managed  aws       type=managed,alias=staging-us-west,region=us-west-2,environment=staging

# Still reference by actual name for operations
vela cluster get local
NAME    ALIAS           LABELS
local   production-hub  type=hub,alias=production-hub,region=us-west-2,environment=hub

# Remove labels
vela cluster label local region-
```

### Migration Path

No migration required - absence of Secret means no labels (current behavior). No breaking changes.

The Secret name and identifying label are specifically chosen to avoid any conflicts with existing managed cluster secrets.

### Test Plan

#### Unit Tests
- Local cluster label storage and retrieval
- Backward compatibility when ConfigMap missing
- Label parsing and validation

#### Integration Tests
- CLI label management for local cluster
- Application deployment using local cluster labels
- Mixed local and managed cluster deployments

#### E2E Tests
- Real deployments with cluster-aware definitions on hub
- Label-based routing including local cluster

## Implementation Plan

### Single Phase (Straight to GA): Core Implementation (v1.N)
- Add Secret-based label storage for local cluster
- Update cluster retrieval logic with conflict avoidance
- CLI support for local cluster labels
- Documentation

### Graduation Criteria

Single phase to GA due to simplicity:
- [ ] Local cluster labels stored and retrieved
- [ ] CLI commands functional
- [ ] Backward compatibility verified
- [ ] Documentation complete

## Implementation History

- 2025-09-23: KEP-0012 created for local cluster label support

## Drawbacks

1. **Special casing**: Additional logic needed to handle local cluster labels (well-isolated and optional)

## Alternatives

### Alternative 1: Treat Local as Managed Cluster
Register local cluster as a managed cluster with special handling.
- **Pros**: Consistent storage and management
- **Cons**: Complex changes, backward compatibility issues. Overkill for simple labelling requirements.

### Alternative 2: Cluster CRD for All
Create Cluster CRD instances for all clusters including local.
- **Pros**: Unified model
- **Cons**: Requires significant refactoring. Overkill for simple labelling requirements.

### Alternative 3: Do Nothing
Keep local cluster without labels.
- **Pros**: No changes needed
- **Cons**: Inconsistent behavior, limits platform capabilities