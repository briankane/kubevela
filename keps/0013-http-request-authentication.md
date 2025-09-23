---
title: HTTP Request Authentication for Workflow Steps
kep-number: 0013
authors:
  - "@author"
area: workflow
status: implementable
creation-date: 2025-09-23
last-updated: 2025-09-23
---

# KEP-0013: HTTP Request Authentication for Workflow Steps

## Release Signoff Checklist

- [ ] Enhancement issue in repository issues list
- [ ] KEP approvals from maintainers
- [ ] Design details are appropriately documented
- [ ] Test plan is in place, with review from appropriate contributors
- [ ] Graduation criteria is in place
- [ ] "Implementation History" section is up-to-date for milestone
- [ ] User-facing documentation has been created in [kubevela/kubevela.github.io](https://github.com/kubevela/kubevela.github.io)
- [ ] Supporting documentation completed (design docs, discussions, PRs)

## Summary

This KEP proposes adding authentication support to the existing `request` workflow step. Currently, KubeVela can make HTTP requests from workflows but has no means to provide authentication credentials, limiting its ability to integrate with secured APIs and services.

### Tracking Issue
- Issue #[TBD]: Add authentication support to HTTP request workflow step

### Related Issues and KEPs
- Existing `request` workflow step implementation
- Config and Secret management in KubeVela

## Motivation

### Current Problem

The existing `request` workflow step only supports unauthenticated HTTP requests. This severely limits integration capabilities:

1. **No API authentication**: Cannot call secured REST APIs, webhooks, or services
2. **Security concerns**: No secure way to handle API keys, tokens, or certificates
3. **Limited integrations**: Cannot integrate with common services that require authentication

### Goals

- Enable authenticated HTTP requests in workflow steps
- Support common authentication methods (Bearer tokens, Basic auth, API keys)
- Integrate with KubeVela's existing Secret and Config management
- Maintain backward compatibility with existing unauthenticated requests

### Non-Goals

- Complex authentication flows (OAuth2, SAML)
- Custom authentication protocols
- Certificate management beyond basic client certificates

## Proposal

### User Stories

#### Story 1: API Integration with Bearer Token
As a developer, I want to call a secured REST API from my workflow using a Bearer token stored in a Kubernetes Secret, so I can integrate with external services securely.

#### Story 2: Webhook Notifications with API Key
As an operator, I want to send webhook notifications with API key authentication during deployment workflows, so I can notify external monitoring systems.

#### Story 3: Basic Authentication
As a system integrator, I want to authenticate to legacy systems using basic authentication credentials stored in KubeVela Configs, so I can automate legacy system interactions.

#### Story 4: Multiple Custom Headers
As a platform engineer, I want to send metrics to DataDog using both DD_API_KEY and DD_APP_KEY headers from Kubernetes secrets, so I can securely integrate with monitoring services that require multiple authentication headers.

## Design Details

### Enhanced Request Step Properties

```yaml
- name: authenticated-request
  type: request
  properties:
    url: "https://api.example.com/deploy"
    method: "POST"
    body: |
      {"status": "deployed", "version": "v1.2.3"}

    # New authentication section
    auth:
      type: "bearer"  # bearer | basic | headers

      # Option 1: From Kubernetes Secret
      secretRef:
        name: "api-credentials"
        namespace: "vela-system"  # optional, defaults to app namespace
        key: "token"  # key within the secret

      # Option 2: From KubeVela Config
      configRef:
        name: "api-config"
        key: "auth-token"

      # Option 3: Custom headers (for multiple auth headers)
      headers:
        - header: API_KEY
          secretRef:
            name: "api-credentials"
            key: "api-key"
        - header: APP_KEY
          secretRef:
            name: "api-credentials"
            key: "app-key"
```

### Authentication Types

#### Bearer Token Authentication
```yaml
auth:
  type: "bearer"
  secretRef:
    name: "api-secret"
    key: "token"
# Results in: Authorization: Bearer <token-value>
```

#### Basic Authentication
```yaml
auth:
  type: "basic"
  secretRef:
    name: "basic-auth-secret"
    key: "credentials"  # expects "username:password" format
# Results in: Authorization: Basic <base64-encoded-credentials>
```

#### Custom Headers Authentication
```yaml
auth:
  type: "headers"
  headers:
    - header: X-API-Key
      secretRef:
        name: "api-secret"
        key: "key"

    # Multiple keys (e.g. DataDog)
    - header: DD_API_KEY
      secretRef:
        name: "datadog-auth"
        key: "api-key"
    - header: DD_APP_KEY
      secretRef:
        name: "datadog-auth"
        key: "app-key"

# Results in headers:
# X-API-Key: <api-key-value>
# DD_API_KEY: <api-key-value>
# DD_APP_KEY: <app-key-value>
```

### Implementation Architecture

#### Secret Resolution
```
1. Controller resolves secret/config reference during workflow execution
2. Retrieves credential value from specified key
3. Applies authentication to HTTP request headers
4. Makes authenticated HTTP request
5. Credential values never logged or exposed in status
```

#### Error Handling
```yaml
# Authentication failures result in workflow step failure with sanitized errors
status:
  conditions:
    - type: Failed
      reason: AuthenticationError
      message: "Failed to authenticate request to https://api.example.com (credentials not found)"
```

### Backward Compatibility

Existing `request` workflow steps continue to work unchanged. The `auth` property is optional and defaults to no authentication.

### Security Considerations

1. **No credential logging**: Authentication values are never logged or stored in workflow status
2. **Namespace isolation**: Secrets are resolved within appropriate namespace context
3. **RBAC enforcement**: Standard Kubernetes RBAC controls access to secrets and configs
4. **Sanitized errors**: Authentication failures provide minimal information to prevent credential leakage

#### Security Warning: Cross-Namespace Access

**By default, KubeVela workflows can read Secrets and Configs from any namespace** because the controller typically runs with cluster-admin or broad permissions. This is a security risk; users could potentially access credentials from namespaces they shouldn't have access to.

**Mitigation Options:**

1. **Enable User Impersonation**:
   ```yaml
   # In KubeVela Helm values
   authentication:
     enabled: true
     withUser: true  # Forces impersonation of requesting user
   ```
   When enabled, KubeVela impersonates the requesting user, enforcing their RBAC permissions and preventing unauthorized cross-namespace access.

2. **Use Config Distribution**:
   - Store sensitive configs in `vela-system`
   - Use `vela config distribute` to copy configs to application namespaces
   - Reference configs without namespace (defaults to current namespace)
   - Note: Config distribution creates an Application that uses `ref-objects` to copy the Secret to target namespaces, making credentials available locally without requiring cross-namespace access

3. **Implement Namespace Restrictions**:
   - Consider adding validation to restrict `secretRef.namespace` to the current application namespace (default)

**Best Practice**: For production deployments with sensitive credentials:
- If possible, enable authentication with user impersonation
- Distribute configs to application namespaces rather than using cross-namespace references

## Implementation Plan

### Single Phase: Stable Release
- Implement `auth` property parsing
- Add Secret and Config resolution
- Support Bearer token, Basic authentication, and custom headers
- Error handling and security measures
- Documentation and examples
- Complete test coverage

## Test Plan

### Unit Tests
- Authentication type parsing and validation
- Secret/Config resolution logic
- HTTP header construction
- Error handling scenarios

### Integration Tests
- End-to-end workflow execution with authentication
- Secret rotation and update scenarios
- RBAC permission enforcement
- Cross-namespace secret access

### Security Tests
- Credential leakage prevention
- Error message sanitization
- Unauthorized access attempts

## Implementation History

- 2025-09-23: KEP-0013 created for HTTP request authentication

## Drawbacks

1. **Added complexity**: More configuration options for workflow authors
2. **Security responsibility**: Users must properly manage secrets and RBAC
3. **Limited auth types**: Only supports common authentication patterns initially

## Alternatives

### Alternative 1: External Authentication Proxy
Use a sidecar or proxy to handle authentication.
- **Pros**: Separates concerns, supports complex auth
- **Cons**: Additional infrastructure, increased complexity