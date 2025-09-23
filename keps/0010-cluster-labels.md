---
title: Expose Cluster Labels in Application Context
KEP-number: XXXX
authors:
  - "@author"
area: core
status: implementable
creation-date: 2025-09-23
last-updated: 2025-09-23
---

# KEP-XXXX: Expose Cluster Labels in Application Context

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

- Labels only available for managed-clusters (not supported on Hub)
- Labels are read-only from application perspective
- No schema validation on cluster labels (platform team responsibility)

### Release Targets

- KubeVela v1.N: Implementation and GA release (simple, non-breaking addition)

## Summary

This KEP proposes exposing cluster labels in the CUE application context, allowing component and trait definitions to make cluster-aware decisions based on cluster metadata. Cluster labels are already collected and used for topology policies but are not currently accessible in the rendering context.

### Tracking Issue
- Issue #[TBD]: [KEP-0010] Expose cluster labels in application context

### Related Issues and KEPs
- Multi-cluster deployment features
- Topology policy enhancements

## Motivation

### Current Problem

KubeVela collects cluster labels for topology policy selection, but these labels are not accessible within CUE templates during application rendering. This prevents custom definitions from making cluster-aware decisions such as:

- Using cloud-provider-specific resources (storage classes, load balancers)
- Adjusting configuration based on cluster capabilities (GPU nodes, high-memory nodes)
- Applying different settings for different regions or environments
- Optimizing workloads for cluster-specific characteristics

### Goals

- Expose cluster labels in the CUE context during application rendering
- Enable cluster-aware component and trait definitions
- Maintain backward compatibility (additive change only)
- Provide immediate value with minimal implementation effort

### Non-Goals

- Schema validation for cluster labels
- Modifying cluster labels from applications
- Changing how topology policies work
- Exposing node-level labels or other cluster resources

## Proposal

### User Stories

#### Story 1: Cloud Provider Awareness
As a platform engineer, I want my components to automatically use the correct storage class based on the cloud provider, so applications work correctly across AWS, GCP, and Azure clusters without modification.

```cue
template: {
    if context.clusterLabels["cloud-provider"] == "aws" {
        spec: storageClassName: "gp3"
    }
    if context.clusterLabels["cloud-provider"] == "gcp" {
        spec: storageClassName: "pd-standard"
    }
    if context.clusterLabels["cloud-provider"] == "azure" {
        spec: storageClassName: "managed-premium"
    }
}
```

#### Story 2: GPU Workload Placement
As a data scientist, I want my ML workloads to automatically configure GPU resources when deployed to GPU-enabled clusters, without needing different application definitions.

```cue
template: {
    if context.clusterLabels["gpu-enabled"] == "true" {
        spec: {
            nodeSelector: {
                "node.kubernetes.io/gpu": "true"
            }
            resources: limits: {
                "nvidia.com/gpu": "1"
            }
        }
    }
}
```

#### Story 3: Region-Specific Configuration
As a platform engineer, I want to configure region-specific endpoints and settings based on cluster location, ensuring compliance and optimal performance.

```cue
template: {
    region: context.clusterLabels["region"]

    spec: env: [{
        name: "S3_ENDPOINT"
        value: "https://s3.\(region).amazonaws.com"
    }, {
        name: "COMPLIANCE_MODE"
        value: context.clusterLabels["compliance-level"]
    }]
}
```

### Risks and Mitigations

| Risk | Likelihood | Impact | Mitigation |
|------|------------|--------|------------|
| Missing labels cause failures | Medium | Low | Use default values, document label requirements. End user conventions and governance |
| Label naming conflicts | Low | Low | Document recommended label conventions |
| Performance impact | Very Low | Low | Labels already cached, minimal overhead |

## Design Details

### Context Enhancement

```go
// Current context structure
type CUEContext struct {
    AppName      string
    Namespace    string
    Cluster      string
    // ... other fields
}

// Enhanced with cluster labels
type CUEContext struct {
    AppName       string
    Namespace     string
    Cluster       string
    ClusterLabels map[string]string  // NEW: user-defined cluster labels (excludes system labels)
    // ... other fields
}

// Label filtering logic
func getUserClusterLabels(labels map[string]string) map[string]string {
    userLabels := make(map[string]string)
    for k, v := range labels {
        // Exclude system labels
        if strings.HasPrefix(k, "cluster.core.oam.dev/") {
            continue
        }
        // Could also exclude other k8s system labels if needed
        // if strings.HasPrefix(k, "kubernetes.io/") { continue }
        userLabels[k] = v
    }
    return userLabels
}
```

### Implementation Flow

```
1. Application controller initiates rendering
2. Cluster information retrieved (already happening)
3. Cluster labels extracted from cluster object (NEW)
4. System labels filtered out (cluster.core.oam.dev/*) (NEW)
5. User labels added to CUE context (NEW)
6. CUE template rendered with cluster-aware context
```

### CUE Context Structure

```cue
context: {
    appName: string
    appRevision: string
    namespace: string
    cluster: string
    ...
    // NEW: User-defined cluster labels (system labels filtered out)
    clusterLabels: {
        [string]: string
    }
}
```

### Example Cluster Resources

KubeVela manages clusters through multiple resources:

```yaml
# Secret-based cluster (most common)
apiVersion: v1
kind: Secret
metadata:
  name: prod-west-cluster
  namespace: vela-system
  labels:
    cluster.core.oam.dev/cluster-credential-type: X509Certificate
    # Custom labels for cluster metadata
    region: us-west-2
    environment: production
    cloud-provider: aws
    gpu-enabled: "false"
    cluster-type: eks
    compliance-level: high
type: Opaque
data:
  endpoint: # base64 encoded
  ca.crt: # base64 encoded
  client.crt: # base64 encoded
  client.key: # base64 encoded
```

### Usage Examples

#### Dynamic Resource Allocation
```cue
template: {
    spec: {
        // Adjust resources based on cluster type with default fallback
        resources: {
            if context.clusterLabels["cluster-type"] == "edge" {
                requests: {cpu: "100m", memory: "128Mi"}
                limits: {cpu: "500m", memory: "512Mi"}
            }
            if context.clusterLabels["cluster-type"] == "datacenter" {
                requests: {cpu: "1000m", memory: "2Gi"}
                limits: {cpu: "4000m", memory: "8Gi"}
            }
            // Default case when label is missing or has unexpected value
            if context.clusterLabels["cluster-type"] == _|_ ||
               (context.clusterLabels["cluster-type"] != "edge" &&
                context.clusterLabels["cluster-type"] != "datacenter") {
                requests: {cpu: "500m", memory: "1Gi"}
                limits: {cpu: "1000m", memory: "2Gi"}
            }
        }
    }
}
```

#### Conditional Features
```cue
template: {
    spec: {
        // Enable features based on cluster capabilities
        if context.clusterLabels["istio-enabled"] == "true" {
            annotations: {
                "sidecar.istio.io/inject": "true"
            }
        }

        if context.clusterLabels["linkerd-enabled"] == "true" {
            annotations: {
                "linkerd.io/inject": "enabled"
            }
        }
    }
}
```

#### Label Propagation to Crossplane S3 Bucket
```cue
template: {
    output: {
        apiVersion: "s3.aws.upbound.io/v1beta1"
        kind: "Bucket"
        metadata: {
            name: context.name + "-bucket"
            labels: {
                // Merge all cluster labels with governance prefix
                for k, v in context.clusterLabels {
                    "org.company.governance/\(k)": v
                }
                // ... other labels
            }
        }
        spec: {
            forProvider: {
                region: context.clusterLabels["region"]

                // AWS resource tags for governance
                tags: {
                    // Propagate all cluster labels as AWS tags
                    for k, v in context.clusterLabels {
                        "\(k)": v
                    }
                    "ManagedBy": "crossplane"
                    // ... other tags
                }
                // ... other bucket configuration
            }
        }
    }
}
```

### Test Plan

#### Unit Tests
- Context builder correctly adds cluster labels
- Missing cluster labels handled gracefully
- Label key/value validation

#### Integration Tests
- Multi-cluster deployment with different labels
- Application rendering uses correct cluster labels
- Topology policy and cluster labels work together

#### E2E Tests
- Real cluster deployments with label-based configuration
- Cross-cloud deployments with provider-specific settings

## Implementation Plan

### Phase 1: Core Implementation (v1.N)
Single phase implementation due to simplicity:
- Add ClusterLabels field to CUE context
- Populate labels during rendering
- Documentation and examples

### Graduation Criteria

Since this is a simple, additive change, it can go straight to GA:
- [ ] Cluster labels exposed in context
- [ ] Documentation complete
- [ ] Test coverage >80%
- [ ] Examples provided

## Implementation History

- 2025-09-23: KEP-0010 created for cluster labels in context

## Drawbacks

1. **No Schema Validation**: Labels are unvalidated strings - must be driven by conventions
2. **Platform Team Dependency**: Requires consistent cluster labeling practices
3. **String-Only Values**: Labels are strings, requiring parsing for numbers/booleans

## Alternatives

None - this is a minor additive change to expose data already collected and serves as a nice to have feature.