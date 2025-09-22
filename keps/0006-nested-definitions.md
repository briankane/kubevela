---
title: Nested Definition Rendering
kvep-number: XXXX
authors:
  - "@author"
area: core
status: implementable
creation-date: 2025-09-22
last-updated: 2025-09-22
---

# KVEP-0006: Nested Definition Rendering

## Release Signoff Checklist

- [ ] Enhancement issue in repository issues list
- [ ] KVEP approvals from maintainers
- [ ] Design details are appropriately documented
- [ ] Test plan is in place, with review from appropriate contributors
- [ ] Graduation criteria is in place
- [ ] "Implementation History" section is up-to-date for milestone
- [ ] User-facing documentation has been created in [kubevela/kubevela.github.io](https://github.com/kubevela/kubevela.github.io)
- [ ] Supporting documentation completed (design docs, discussions, PRs)

### Notes/Constraints/Caveats

- Requires careful cycle detection to prevent infinite recursion
- Performance impact needs monitoring with deep nesting levels
- RBAC integration must be thoroughly tested

### Release Targets

- KubeVela v1.12: Alpha implementation (No public documentation - use at own risk. Feature flagged)
- KubeVela v1.13: Beta implementation (Early access documentation provided. Feature flagged)
- KubeVela v1.14: GA release

## Summary

This KVEP proposes adding nested definition referencing functionality to KubeVela, allowing one KubeVela ComponentDefinition/TraitDefinition to reference and compose other definitions through a new `def.#Render` function. This enables multi-layered abstraction where complex definitions can be wrapped with different parameter interfaces for various audiences while maintaining a single source of truth for the underlying logic.

### Tracking Issue
- Issue #[TBD]: [KVEP-XXXX] Nested Definition Referencing in KubeVela

### Related Issues and KVEPs
- Related to component composition and abstraction patterns

## Motivation

### Goals

- Enable definition composition and layered abstraction in KubeVela
- Reduce maintenance overhead by allowing definition reuse and wrapping
- Support different parameter interfaces for different audiences (internal devs, external tenants, specialized use cases)
- Maintain backward compatibility with existing definitions
- Leverage RBAC to control access to different definition layers

### Non-Goals

- Replace existing definition system
- Add complex dependency management between definitions
- Support circular references between definitions

## Proposal

### User Stories

#### Story 1: Multi-Audience Component Exposure
As a platform team, I want to create a base Crossplane Composition wrapped in a KubeVela Component with complex CUE logic, then expose simplified versions of this component to different audiences:
- Internal developers get access to advanced parameters
- External tenants get a simplified parameter interface
- Special use cases get elevated access parameters

All versions reference the same base definition, ensuring consistent behavior and reduced maintenance.

#### Story 2: Progressive Abstraction
As a platform engineer, I want to build layered abstractions where:
- Level 1: Raw Resource
- Level 2: KubeVela Component with validation and transformation logic
- Level 3: Simplified user-facing component that references Level 2
- Level 4: Domain-specific component (e.g., "DatabaseForApp") that references Level 3

### Risks and Mitigations

| Risk | Likelihood | Impact | Mitigation |
|------|------------|--------|------------|
| Circular references | Medium | High | Implement cycle detection in the compiler |
| Performance degradation | Low | Medium | Cache resolved definitions and implement depth limits |
| Complex debugging | Medium | Medium | Provide clear error messages with reference chain |
| Security concerns | Low | High | Maintain RBAC boundaries and validate referenced definitions |

## Design Details

### API Changes

#### New CUE Function: `def.#Render`

```cue
// In a ComponentDefinition template
parameter: {
    // Simplified parameters for this layer
    database_name: string
    database_size: *"small" | "medium" | "large"
    environment: *"staging" | "production"
}

// Reference to base definition with parameter transformation
output: def.#Render & {
    definition: "postgres-advanced"  // Name of base ComponentDefinition
    parameters: {
        // Transform simplified parameters to complex base parameters
        name: parameter.database_name
        spec: {
            if parameter.database_size == "small" {
                resources: {cpu: "500m", memory: "1Gi", storage: "10Gi"}
            }
            if parameter.database_size == "medium" {
                resources: {cpu: "1000m", memory: "2Gi", storage: "50Gi"}
            }
            if parameter.database_size == "large" {
                resources: {cpu: "2000m", memory: "4Gi", storage: "100Gi"}
            }
        }
        environment: parameter.environment
        advanced_config: {
            backup_retention: 30
            monitoring_enabled: true
        }
    }
}
```

#### Function Signature

```cue
#Render: {
    definition: string          // Name of the ComponentDefinition/TraitDefinition to reference
    namespace?: string          // Optional: namespace of the definition (defaults to current)
    parameters: {...}           // Parameters to pass to the referenced definition
}
```

#### Abstract Definition Annotation

Definitions can be marked as abstract using the `def.oam.dev/abstract: "true"` annotation. Abstract definitions:
- **Cannot be instantiated directly** by users in Applications
- **Can only be rendered** through `def.#Render` calls from other definitions
- **Provide controlled abstraction** by forcing users through wrapper definitions

```yaml
apiVersion: core.oam.dev/v1beta1
kind: ComponentDefinition
metadata:
  name: webserver
  annotations:
    def.oam.dev/abstract: "true"  # This definition cannot be used directly
spec:
  # ... definition spec
```

### Implementation Architecture

#### 1. Definition Resolver
```go
type DefinitionResolver struct {
    client     client.Client
    maxDepth   int
    cache      map[string]*ResolvedDefinition
}

type ResolvedDefinition struct {
    Template   string
    Parameters interface{}
    Metadata   map[string]interface{}
}
```

#### 2. Render Function Implementation
The `def.#Render` function would be implemented as a CUE builtin that:

1. **Validates the referenced definition exists and is accessible**
2. **Allows access to abstract definitions** (marked with `def.oam.dev/abstract: "true"`)
3. **Resolves the definition recursively** (with cycle detection)
4. **Merges parameters and context** from the calling definition
5. **Executes the referenced definition's template** with the provided parameters
6. **Returns the rendered output** for composition within the calling definition

#### 3. Abstract Definition Enforcement
The KubeVela application controller would be enhanced to:

1. **Block direct instantiation** of definitions marked with `def.oam.dev/abstract: "true"`
2. **Allow indirect access** through `def.#Render` calls
3. **Provide clear error messages** when users attempt to use abstract definitions directly

#### 3. Integration Points

**CUE Builtin Function Integration:**

The `def.#Render` function would be implemented as a CUE builtin function that integrates with the existing CUE compiler. The function would:

1. **Resolve at compile time** - During CUE template compilation, when `def.#Render` is encountered
2. **Use existing Kubernetes client** - Leverage the same client context used for other CUE operations
3. **Return pure CUE values** - Output structured data that can be assigned to `output` or used in further CUE expressions

No changes to the `AbstractEngine` interface are required - the resolution happens entirely within the CUE compilation phase.

### Example Usage

#### Base Definition (webserver) - Abstract
```cue
"webserver": {
  annotations: {
    "def.oam.dev/abstract": "true"  // Cannot be used directly by users
  }
  attributes: workload: definition: {
    apiVersion: "apps/v1"
    kind: "Deployment"
  }
  description: "Base web server with full configuration options - abstract definition"
  labels: {}
  type: "component"
}

template: {
  output: {
    apiVersion: "apps/v1"
    kind: "Deployment"
    spec: {
      replicas: parameter.replicas
      selector: matchLabels: "app.oam.dev/component": context.name
      template: {
        metadata: labels: "app.oam.dev/component": context.name
        spec: containers: [{
          name: context.name
          image: parameter.image
          ports: [{containerPort: parameter.port}]
          resources: parameter.resources
          env: parameter.env
        }]
      }
    }
  }
  outputs: {}
  parameter: {
    image: string
    port: *8080 | int
    replicas: *1 | int
    resources: {
      requests: {
        cpu: string
        memory: string
      }
      limits: {
        cpu: string
        memory: string
      }
    }
    env: [...{name: string, value: string}]
  }
}
```

#### Simple User-Facing Definition
```cue
"simple-web": {
  annotations: {}
  attributes: workload: definition: {
    apiVersion: "apps/v1"
    kind: "Deployment"
  }
  description: "Simple web app with size-based configuration"
  labels: {}
  type: "component"
}

template: {
  // Reference base definition with parameter transformation
  output: def.#Render & {
    definition: "webserver"
    parameters: {
      image: parameter.image
      port: 8080
      replicas: parameter.size == "large" ? 3 : 1
      resources: {
        if parameter.size == "small" {
          requests: {cpu: "100m", memory: "128Mi"}
          limits: {cpu: "200m", memory: "256Mi"}
        }
        if parameter.size == "large" {
          requests: {cpu: "500m", memory: "512Mi"}
          limits: {cpu: "1000m", memory: "1Gi"}
        }
      }
      env: [
        {name: "APP_ENV", value: parameter.environment}
      ]
    }
  }
  outputs: {}
  parameter: {
    image: string
    size: *"small" | "large"
    environment: *"dev" | "prod"
  }
}
```

### Security Considerations

1. **Controlled Abstraction via RBAC**: The `def.#Render` function enables controlled abstraction through RBAC policies. When KubeVela runs as the system user, it can access any definition regardless of user permissions. This allows:
   - **Hidden base definitions**: Complex internal definitions can be restricted from end users via RBAC (if enabled)
   - **Controlled exposure**: User-facing definitions can reference restricted base definitions
   - **System-level resolution**: KubeVela resolves references using system credentials, not user credentials
   - **Layered security**: Users only see simplified interfaces while the system handles complex underlying logic

2. **Namespace Isolation**: Definitions should only be permitted to reference definitions in the same namespace or the vela-system namespace.

3. **Cycle Detection**: The implementation includes cycle detection to prevent infinite recursion.

4. **Depth Limits**: A configurable maximum nesting depth prevents resource exhaustion.

### Test Plan

#### Unit Tests
- Definition resolver with valid/invalid references
- Abstract definition enforcement (block direct use, allow through `def.#Render`)
- Parameter transformation and merging
- Cycle detection
- Error handling and validation

#### Integration Tests
- End-to-end definition composition
- RBAC policy enforcement
- Cross-namespace reference behavior
- Performance under load

#### E2E Tests
- Real-world scenarios with Crossplane Compositions
- Multi-layer abstraction chains
- Different audience parameter interfaces

## Implementation Plan

### Proposed Phases

#### Phase 1: Core Implementation (Alpha - v1.12)
Implement basic nested definition rendering functionality with safety controls.
- Core CUE builtin function development
- Safety mechanisms (cycle detection, depth limits, namespace limitations)
- Initial testing
- Feature flag implementation

#### Phase 2: Integration & Security (Beta - v1.13)
Add abstract definition enforcement, security controls, and production optimizations.
- Abstract definition annotations
- Security and RBAC testing (full e2e)
- Performance optimization
- Real-world validation

#### Phase 3: Production Ready (GA - v1.14)
Polish, comprehensive documentation, and community adoption.
- Production hardening
- Complete documentation
- Community feedback integration

### Graduation Criteria

#### Alpha
- [ ] Core `def.#Render` CUE builtin function implemented
- [ ] DefinitionResolver with cycle detection
- [ ] Basic error handling and validation
- [ ] Unit test coverage >80%
- [ ] Feature flagged at compiler level
- [ ] Basic usage documentation

#### Beta
- [ ] Abstract definition annotation enforcement (`def.oam.dev/abstract`)
- [ ] RBAC integration and security controls
- [ ] Performance optimizations and benchmarks
- [ ] Integration tests and E2E scenarios
- [ ] Real-world validation with Crossplane examples
- [ ] Draft user-facing documentation

#### GA
- [ ] Production-ready performance validated
- [ ] Comprehensive documentation and examples
- [ ] Community feedback integrated
- [ ] Migration tooling available
- [ ] Monitoring and observability features

### Production Readiness

- **Scalability:** Tested with deep nesting (>3 levels) and high concurrency
- **Monitoring:** Metrics for definition resolution time and cache hit rates
- **Troubleshooting:** Clear error messages with resolution chain context
- **Rollback:** Graceful degradation when feature is disabled

## Implementation History

- 2025-09-22: KVEP-XXXX created with initial design

## Drawbacks

1. **Increased Complexity**: Adds another layer of abstraction that developers need to understand
2. **Debugging Difficulty**: Nested references can make troubleshooting more complex
3. **Performance Overhead**: Additional resolution steps may impact compilation time
4. **Potential for Overuse**: May lead to overly complex definition hierarchies
5. **Difficult to Inherit Health Data**: Health status and messaging may be repetitive across definitions

## Alternatives

### Alternative 1: Definition Inheritance
Instead of composition, implement inheritance where definitions can extend base definitions.
- Less flexible than composition
- Harder to maintain clear boundaries between layers
- Doesn't support the multi-audience use case as well
- Much more extensive changes required

### Alternative 2: External Templating System
Use an external templating system like Helm or Kustomize.
- Would break the unified CUE-based definition system
- Adds external dependencies
- Doesn't integrate well with KubeVela's parameter validation
- Can be handled separately through KEP-0004 which would improve Helm support and templating

### Alternative 3: Definition Imports
Allow CUE imports of definition modules.
- CUE imports are static, not dynamic
- Doesn't support runtime parameter transformation
- Would require significant changes to CUE compilation process
- Already somewhat supported through CUE Packages - but aimed more at reusable logic than template abstraction