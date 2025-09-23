---
title: Config and ConfigTemplate Validation Webhooks
kvep-number: XXXX
authors:
  - "@author"
area: core
status: implementable
creation-date: 2025-09-23
last-updated: 2025-09-23
---

# KVEP-0009: Config and ConfigTemplate Validation

## Release Signoff Checklist

- [ ] Enhancement issue in repository issues list
- [ ] KVEP approvals from maintainers
- [ ] Design details are appropriately documented
- [ ] Test plan is in place, with review from appropriate contributors
- [ ] Graduation criteria is in place
- [ ] "Implementation History" section is up-to-date for milestone
- [ ] User-facing documentation has been created in [kubevela/kubevela.github.io](https://github.com/kubevela/kubevela.github.io)
- [ ] Supporting documentation completed (design docs, discussions, PRs)

### Release Targets

- KubeVela v1.N: Alpha implementation (Existing CLI validations w/ opt-in webhook validation)
- KubeVela v1.N+1: Beta implementation (Opt-In Webhook validation disables CLI validation)
- KubeVela v1.N+2: GA release (webhook validation is the default)

## Summary

This KEP proposes adding ValidatingAdmissionWebhooks to enforce schema validation for KubeVela Config and ConfigTemplate resources at the API server level. Currently, validation only occurs in the CLI, allowing users to bypass schema checks by using `kubectl apply` (or GitOps) directly, which creates security and reliability concerns for declarative deployments.

### Tracking Issue
- Issue #[TBD]: [KEP-0009] Config and ConfigTemplate Validation Webhooks

### Related Issues and KVEPs
- Related to ConfigDefinitions KVEP (nested definition referencing)
- GitOps and declarative deployment reliability

## Motivation

### Current Problem

KubeVela's Config and ConfigTemplate validation currently only happens at the CLI level:

```bash
# CLI validation works
vela config create api-config --template=api-config value=test      # Validates against ConfigTemplate

# Direct kubectl bypasses validation
kubectl apply -f config.yaml                                        # No validation, accepts invalid data
```

This creates several critical issues:

1. **Security bypass**: Users can create Configs that don't match ConfigTemplate schemas
2. **GitOps incompatibility**: Declarative deployments can't rely on validation
3. **Runtime failures**: Invalid configurations are only caught during application execution
4. **ConfigTemplate corruption**: Invalid ConfigTemplates can be created without validation
5. **CLI Inconsistencies**: Invalid naming schemes can be applied, which break CLI commands

### Goals

- Move validation from CLI-only to API server level using ValidatingAdmissionWebhooks
- Ensure Config resources conform to their corresponding ConfigTemplate schemas
- Validate ConfigTemplate CUE syntax and structure at creation time
- Maintain backward compatibility with existing valid resources
- Support declarative deployments and GitOps workflows
- Provide clear, actionable error messages for validation failures

### Non-Goals

- Replace existing CLI validation
- Add new validation rules beyond existing ConfigTemplate schema checking
- Support for custom validation logic beyond CUE schema validation

## Proposal

### User Stories

#### Story 1: GitOps Security
As a platform team using GitOps, I want Config resources applied through ArgoCD/Flux to be validated against their ConfigTemplate schemas, so that invalid configurations are rejected before they can cause runtime failures or security issues.

#### Story 2: Developer Safety
As a developer, when I accidentally apply an invalid Config using `kubectl apply`, I want to receive immediate feedback with clear error messages explaining what validation rules were violated, rather than discovering the issue later during application deployment.

#### Story 3: ConfigTemplate Integrity
As a platform engineer, I want ConfigTemplate resources to be validated for proper CUE syntax and required metadata when created via any method (CLI, kubectl, GitOps), ensuring integrity across the system.

#### Story 4: Feature Development Confidence
As a KubeVela developer, I want confidence in the Config and ConfigTemplate schema validations so that additional features can be built around the functionality without worrying about data integrity issues or validation bypasses.

### Risks and Mitigations

| Risk | Likelihood | Impact | Mitigation |
|------|------------|--------|------------|
| Webhook failures cause system lockout | Low | High | Fail-open mode for webhook failures, comprehensive testing |
| Performance degradation | Medium | Medium | Performance benchmarks, add caching in future iterations |
| Breaking existing invalid resources | High | Medium | Migration tooling, gradual rollout with feature flags |

## Design Details

### Webhook Architecture

```go
// Pseudo-code for validation webhook structure
type ConfigValidator struct {
    // Core dependencies
    client        // K8s client for fetching ConfigTemplates
    decoder       // Admission request decoder
    cueCompiler   // CUE context for schema compilation
}

// Single validation handler for both Configs and ConfigTemplates
func ValidateConfigResource(request) Response {
    // Resource type determines validation path:
    // - Secret + catalog label = Config validation
    // - ConfigMap + catalog label = ConfigTemplate validation

    if resourceType == "Secret" {
        // Config validation:
        // 1. Extract config.oam.dev/type label
        // 2. Load ConfigTemplate ConfigMap (config-template-{type})
        // 3. Compile schema from template's data.schema field
        // 4. Validate Secret's input-properties against schema
        // 5. Return structured validation errors
    } else if resourceType == "ConfigMap" {
        // ConfigTemplate validation:
        // 1. Parse CUE from data.template field
        // 2. Validate CUE compilation
        // 3. Generate and validate JSON schema
        // 4. Check metadata annotations
        // 5. Return validation result
    }
}
```

### Validation Logic

#### Config Validation Flow
```
1. Extract config.oam.dev/type label → template name
2. Fetch ConfigTemplate from K8s API
3. Compile CUE schema from template.parameter
4. Parse config data (stringData or decoded data)
5. Validate parsed data against CUE schema
6. Return structured errors with field paths
```

#### ConfigTemplate Validation Flow
```
1. Parse CUE content from ConfigMap data
2. Validate CUE compilation (syntax check)
3. Check required blocks exist (metadata, template, parameter)
4. Validate metadata fields (name, scope, sensitive, etc.)
5. Ensure parameter usage matches template references
6. Return validation result
```

### Implementation Components

#### 1. Webhook Server
```yaml
apiVersion: admissionregistration.k8s.io/v1
kind: ValidatingAdmissionWebhook
metadata:
  name: kubevela-config-validator
webhooks:
- name: config.validation.kubevela.io
  clientConfig:
    service:
      name: kubevela-config-webhook
      namespace: vela-system
      path: "/validate"
  rules:
  - operations: ["CREATE", "UPDATE"]
    apiGroups: [""]
    apiVersions: ["v1"]
    resources: ["secrets", "configmaps"]
  objectSelector:
    matchLabels:
      config.oam.dev/catalog: "velacore-config"
  failurePolicy: Fail
```

#### 2. Error Response Format
```json
{
  "allowed": false,
  "status": {
    "code": 400,
    "message": "Config validation failed: parameter.endpoint: string value required, got: null"
  }
}
```

### Migration Strategy
Validation should use the existing Config / Config Template APIs, so validation is simply occurring through an alternative route making migration simple. 

#### Phase 1:  Parallel Validation w/ Opt-In (Alpha)
- Feature flag will control opt-in

#### Phase 2: CLI or Webhook Validation (Beta)
- Webhook validation will disable the CLI level validation
- Beta users will opt-in

#### Phase 3: Mandatory Validation
- All Config/ConfigTemplate resources validated at webhook by default
- Remove legacy CLI-only validation paths
- Full declarative deployment support

### Test Plan

#### Unit Tests
- Webhook validation logic for valid/invalid configs
- ConfigTemplate CUE compilation and validation
- Error message formatting and clarity
- Performance benchmarks for validation operations

#### Integration Tests
- End-to-end webhook deployment
- Config creation/update through kubectl with validation
- Addon testing 
- Failure mode testing (webhook unavailable)

#### E2E Tests
- Real-world config validation scenarios
- Performance testing under load
- Migration from CLI-only to webhook validation

## Implementation Plan

### Phase 1: Core Webhook Implementation (Alpha - v1.N)
- ValidatingAdmissionWebhook implemented for Config and ConfigTemplate
- Opt-in validation with feature flag

### Phase 2: Production Hardening (Beta - v1.N+1)
- Refine error messages and user experience
- Necessary performance optimizations and caching
- Comprehensive testing and documentation

### Phase 3: Full Rollout (GA - v1.N+2)
- Default-enabled validation for all new resources
- Community feedback integration

### Graduation Criteria

#### Alpha
- [ ] ValidatingAdmissionWebhook deployed and functional
- [ ] Basic Config schema validation working
- [ ] ConfigTemplate CUE syntax validation
- [ ] Opt-in validation mechanism
- [ ] Unit test coverage >80%

#### Beta
- [ ] Refined validation error messages
- [ ] Performance benchmarks meet requirements
- [ ] Integration test coverage complete
- [ ] Real-world validation with community feedback

#### GA
- [ ] Production-ready performance validated
- [ ] Default-enabled validation for new resources
- [ ] Community adoption and positive feedback - no impacts identified

## Production Readiness

- **Scalability**: Tested with high-throughput config creation/updates
- **Monitoring**: Metrics for validation success/failure rates and latency
- **Troubleshooting**: Clear documentation for webhook debugging
- **Rollback**: Ability to disable webhooks without system disruption

## Implementation History

- 2025-09-23: KEP-0009 created identifying validation bypass issue

## Drawbacks

1. **Added Complexity**: Webhook deployment
2. **Potential Brittleness**: Webhook failures could impact system availability
3. **Performance Overhead**: Additional validation step for all Config operations

## Alternatives

### Alternative 1: CRD OpenAPI Schema Validation
Use Kubernetes CRD OpenAPI schemas instead of webhooks.
- **Pros**: Native Kubernetes validation, no webhook complexity
- **Cons**: Limited validation capabilities, can't validate CUE syntax, doesn't support ConfigTemplate references

### Alternative 2: Client-Side Validation Only
Strengthen CLI validation and provide better tooling.
- **Pros**: No server-side complexity, easier to implement
- **Cons**: Still allows kubectl bypass, doesn't solve declarative deployment issues

### Alternative 3: Custom Resource Definitions
Convert Config and ConfigTemplate to proper CRDs.
- **Pros**: Native Kubernetes validation and versioning
- **Cons**: Breaking change, loses Secret/ConfigMap native tooling compatibility. Goes against the "use what's already there" principle.