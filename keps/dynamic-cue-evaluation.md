---
title: Dynamic CUE Evaluation in Definitions
kvep-number: 0007
authors:
  - "@author"
area: core
status: implementable
creation-date: 2025-09-22
last-updated: 2025-09-22
---

# KVEP-0007: Dynamic CUE Evaluation in Definitions

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

- Security implications of evaluating user-provided CUE code need careful consideration
- Performance impact of dynamic compilation needs monitoring
- Clear documentation needed to prevent misuse

### Release Targets

TBD

## Summary

This KEP proposes adding a `cue.#Render` function to KubeVela that enables dynamic CUE evaluation from string representations that can be passed as parameters. This allows definition authors to accept CUE expressions as parameters and evaluate them at runtime, enabling dynamic feature toggling, conditional logic, and user-provided transformations that aren't possible with static CUE templates.

This approach mirrors the successful adoption of CEL (Common Expression Language) in Kubernetes and other CNCF projects, where users can embed expressions directly in YAML manifests that are evaluated at runtime. Multiple Kubernetes projects have demonstrated the value of runtime expression evaluation for configuration flexibility. By enabling similar capabilities with CUE instead of CEL, KubeVela can provide this flexibility while maintaining consistency with its existing CUE-based ecosystem.

### Tracking Issue
- Issue #[TBD]: [KVEP-0007] Dynamic CUE Evaluation in Definitions

### Related Issues and KEPs
TBD

## Motivation

Currently, KubeVela definitions are static - all CUE logic must be written by the definition author at design time. Users can only provide data values as parameters, not logic. This limitation prevents several valuable use cases:

1. **Dynamic feature flags**: Users cannot pass conditional logic to enable/disable features
2. **Custom transformations**: Users cannot provide their own data transformation logic
3. **Runtime expressions**: Complex conditions based on runtime state cannot be evaluated
4. **Flexible validation**: Users cannot inject custom validation rules

### Goals

- Enable dynamic CUE evaluation from string parameters
- Allow users to pass CUE expressions that get evaluated at runtime
- Support conditional logic and transformations defined by users
- Maintain security through sandboxed evaluation (limit operations available)
- Preserve type safety and validation capabilities

### Non-Goals

- Replace the existing static CUE template system
- Allow arbitrary code execution beyond CUE
- Support file system or network access from evaluated expressions
- Enable cross-definition state sharing through dynamic evaluation

## Proposal

Add a `cue.#Render` function to the CueX engine that:
1. Accepts a string containing CUE code
2. Passes the Application context to the compiler for the evaluation
3. Evaluates the CUE code
4. Returns the evaluated result for use in the definition

**Return Value Handling:**
- **Single expressions**: Automatically wrapped in `$returns:` and evaluated (e.g., `"size > 10"` becomes `"$returns: size > 10"`)
- **Multi-line code**: User must explicitly set `$returns` attribute with the value to return
- The result is accessible via the `$returns` field of the evaluation result

### User Stories

#### Story 1: Dynamic Feature Toggles
As a platform user, I want to pass CUE expressions to control which features are enabled in my deployment, so I can have fine-grained control without needing multiple definition versions.

```cue
parameter: {
    enableProduction: string  // User provides: "strings.HasPrefix(context.clusterName, \"prod\")"
    // This simple expression is auto-wrapped to: "$returns: strings.HasPrefix(context.clusterName, \"prod\")"
}
```

#### Story 2: Custom Resource Transformations
As a developer, I want to provide custom transformation logic for my resources, so I can adapt the base definition to my specific needs without forking it.

```cue
parameter: {
    resourceTransform: string  // User provides CUE that modifies resource specs
}
```

#### Story 3: Dynamic Resource Naming
As a platform team, I want to allow users to provide custom logic for generating resource names based on environment context, so different teams can follow their naming conventions.

```cue
parameter: {
    nameGenerator: string
    // User provides naming logic based on context:
    // """
    // import "strings"
    // _env: strings.Split(context.clusterName, "-")[0]  // Extract env from cluster name
    // _region: strings.Split(context.clusterName, "-")[1]  // Extract region
    // $returns: "\(appName)-\(_env)-\(_region)-\(component)"
    // """
    appName: string
    component: string
}
```

### Risks and Mitigations

| Risk | Likelihood | Impact | Mitigation |
|------|------------|--------|------------|
| Code injection attacks | Medium | High | Sandbox evaluation, disable imports/network |
| Performance degradation | Medium | Medium | Cache compiled expressions, set timeouts |
| Debugging complexity | High | Medium | Clear error messages, trace evaluation |
| Misuse/over-complexity | High | Low | Documentation, best practices, linting |

## Design Details

### API Changes

#### New CUE Function: `cue.#Render`

```cue
#Render: {
    expression: string       // CUE code to evaluate
    context?: {...}          // Optional context object available to the expression
    allowImports?: bool      // Default: false - whether to allow import statements
    timeout?: string         // Default: "5s" - maximum evaluation time
    // Result will have structure: { $returns: <evaluated-value> }
}
```

**Expression Types:**

1. **Simple Expression** (auto-wrapped):
```cue
expression: "size > 10"
// Internally becomes: "$returns: size > 10"
// Result: {$returns: true} or {$returns: false}
```

2. **Multi-line Expression** (explicit $returns):
```cue
expression: """
    _threshold: 10
    _multiplier: 2
    $returns: size > _threshold * _multiplier
"""
// Result: {$returns: true} or {$returns: false}
```

3. **Structured Return**:
```cue
expression: """
    $returns: {
        enabled: size > 10
        replicas: if size > 20 { 3 } else { 1 }
        resources: {
            cpu: "\(size * 100)m"
            memory: "\(size * 128)Mi"
        }
    }
"""
// Result: {$returns: {enabled: true, replicas: 3, resources: {...}}}
```

#### Example Usage

##### Environment-Based Configuration
```cue
"adaptive-deployment": {
  annotations: {}
  attributes: workload: definition: {
    apiVersion: "apps/v1"
    kind: "Deployment"
  }
  description: "Deployment that adapts based on runtime context"
  labels: {}
  type: "component"
}

template: {
  parameter: {
    image: string
    scalingRule: string  // User provides: 'context.namespace == "prod" ? 5 : context.namespace == "staging" ? 2 : 1'
    resourceRule: string // User provides: 'strings.HasPrefix(context.clusterName, "gpu-") ? {cpu: "4", memory: "16Gi", "nvidia.com/gpu": "1"} : {cpu: "500m", memory: "1Gi"}'
  }

  // Evaluate scaling logic based on namespace
  _replicas: cue.#Render & {
    expression: parameter.scalingRule
    context: {
      namespace: context.namespace
      clusterName: context.clusterName
    }
  }

  // Evaluate resource allocation based on cluster type
  _resources: cue.#Render & {
    expression: """
      import "strings"
      $returns: \(parameter.resourceRule)
    """
    context: {
      clusterName: context.clusterName
    }
  }

  output: {
    apiVersion: "apps/v1"
    kind: "Deployment"
    spec: {
      replicas: _replicas.$returns
      template: spec: {
        containers: [{
          name: context.name
          image: parameter.image
          resources: {
            requests: _resources.$returns
            limits: _resources.$returns
          }
        }]
      }
    }
  }
}
```

##### Dynamic Resource Calculation
```cue
template: {
  parameter: {
    image: string
    resourceLogic: string  // e.g., "if size == \"large\" { cpu: \"2000m\", memory: \"4Gi\" }"
    size: string
  }

  _resources: cue.#Render & {
    expression: """
      $returns: {
        \(parameter.resourceLogic)
      }
    """
    context: {
      size: parameter.size
    }
  }

  output: {
    apiVersion: "apps/v1"
    kind: "Deployment"
    spec: template: spec: containers: [{
      resources: _resources.$returns
    }]
  }
}
```

### Implementation Architecture

#### 1. CUE Render Function
```go
// pkg/cue/cuex/functions/render.go
type CueRenderFunction struct {
    sandbox *sandbox.SandboxedCompiler
    cache   *lru.Cache
}

func (f *CueRenderFunction) Render(ctx context.Context, args cue.Value) (cue.Value, error) {
    // 1. Extract expression string and context from args
    expression := args.LookupPath(cue.ParsePath("expression")).String()
    contextVal := args.LookupPath(cue.ParsePath("context"))

    // 2. Check cache for previously compiled expression
    cacheKey := hash(expression)
    if cached, ok := f.cache.Get(cacheKey); ok {
        return f.evaluate(cached, contextVal)
    }

    // 3. Auto-wrap simple expressions with $returns
    if !strings.Contains(expression, "$returns") {
        expression = fmt.Sprintf("$returns: %s", expression)
    }

    // 4. Compile in sandbox with context
    compiled, err := f.sandbox.CompileWithContext(ctx, expression, contextVal)
    if err != nil {
        return cue.Value{}, fmt.Errorf("compilation failed: %w", err)
    }

    // 5. Cache compiled result and return
    f.cache.Add(cacheKey, compiled)
    return compiled, nil
}
```

#### 2. Security Sandbox Implementation

##### Sandboxed Compiler Architecture
```go
// pkg/cue/cuex/sandbox.go
type SandboxedCompiler struct {
    compiler *cuex.Compiler
    config   SandboxConfig
}

type SandboxConfig struct {
    AllowedImports   []string        // e.g., ["strings", "math", "list"]
    Timeout          time.Duration   // Default: 5s
    MaxDepth         int            // Maximum recursion depth
    MaxIterations    int            // Maximum resolve iterations
    DisableProviders bool           // Block all provider functions
}
```

##### Import Control
- **Parse and validate AST** before compilation to detect and block unauthorized imports
- **Allowlist approach**: Only permit safe built-in packages (`strings`, `math`, `list`, `struct`)
- **Block dangerous imports**: `exec`, `http`, `file`, `net`, `os`, `tool/*`
- Implementation:
  ```go
  func (s *SandboxedCompiler) validateImports(f *ast.File) error {
      for _, imp := range f.Imports {
          path := strings.Trim(imp.Path.Value, "\"")
          if !s.isAllowedImport(path) {
              return fmt.Errorf("import %q is not allowed", path)
          }
      }
      return nil
  }
  ```

##### Provider Function Restrictions
- Create a `SandboxedPackageManager` with no registered providers
- This prevents access to:
  - HTTP operations (`http.#Get`, `http.#Post`)
  - File system operations
  - Command execution (`exec.#Run`)
  - Database connections
- Users can only use pure CUE evaluation, no external I/O

##### Timeout Enforcement
- Leverage existing deadline checking in `Resolve()` function
- Wrap compilation with `context.WithTimeout()`
- Default 5-second timeout, configurable per invocation
- Example:
  ```go
  func (s *SandboxedCompiler) CompileString(ctx context.Context, src string) (cue.Value, error) {
      ctx, cancel := context.WithTimeout(ctx, s.config.Timeout)
      defer cancel()
      return s.compiler.CompileStringWithOptions(ctx, src,
          DisableResolveProviderFunctions,
      )
  }
  ```

##### Resource Limits
- **Iteration limits**: Cap resolve loop iterations to prevent infinite loops
- **Depth limits**: Restrict recursion depth in CUE evaluation
- **Size limits**: Limit the size of generated values
- Monitor using counters in the resolve loop:
  ```go
  iterations := 0
  for {
      if iterations > s.config.MaxIterations {
          return value, ErrMaxIterationsExceeded
      }
      iterations++
      // ... resolve logic
  }
  ```

#### 3. Integration Points

The `cue.#Render` function integrates with the existing CueX compiler as a builtin function, similar to other CueX extensions.

### Example Use Cases

#### Simple Boolean Expression (Auto-wrapped)
```cue
parameter: {
  enableFeature: "environment == \"prod\" && replicas > 2"
  environment: string
  replicas: int
}

_shouldEnable: cue.#Render & {
  expression: parameter.enableFeature
  context: {
    environment: parameter.environment
    replicas: parameter.replicas
  }
}
// Result: _shouldEnable.$returns will be true or false
```

#### Dynamic Environment Configuration (Explicit $returns)
```cue
parameter: {
  envLogic: """
    $returns: {
      if environment == "prod" {
        replicas: 3
        resources: {limits: {cpu: "2000m", memory: "4Gi"}}
      }
      if environment == "dev" {
        replicas: 1
        resources: {limits: {cpu: "500m", memory: "1Gi"}}
      }
    }
  """
  environment: "prod" | "dev"
}

_config: cue.#Render & {
  expression: parameter.envLogic
  context: {
    environment: parameter.environment
  }
}
// Use: _config.$returns.replicas, _config.$returns.resources
```

#### User-Defined Validation
```cue
parameter: {
  validation: """
    $returns: {
      valid: replicas > 0 && replicas <= 10
      message: if replicas <= 0 { "Replicas must be positive" } else { "Replicas must be 10 or less" }
    }
  """
  replicas: int
}

_validation: cue.#Render & {
  expression: parameter.validation
  context: {
    replicas: parameter.replicas
  }
}

// Use validation result
if !_validation.$returns.valid {
  errs: [_validation.$returns.message]
}
```

### Security Considerations

1. **Sandboxed Execution**: All dynamic CUE evaluation runs in a restricted environment
2. **No Side Effects**: Evaluated code cannot modify external state
3. **Resource Limits**: CPU, memory, and time limits prevent resource depletion
4. **Audit Logging**: Log all dynamic evaluations
5. **Feature Flag**: Can be globally disabled if security concerns arise

### Test Plan

#### Unit Tests
- Expression evaluation with various inputs
- Security sandbox enforcement
- Timeout and resource limit handling
- Cache behavior
- Error handling

#### Security Tests (Prelim - AI Assisted)
```go
// pkg/cue/cuex/sandbox_test.go
func TestSandboxSecurity(t *testing.T) {
    tests := []struct {
        name        string
        expression  string
        shouldFail  bool
        errorMsg    string
    }{
        {
            name:       "block file import",
            expression: `import "file"
                        $returns: file.Read("/etc/passwd")`,
            shouldFail: true,
            errorMsg:   "import \"file\" is not allowed",
        },
        {
            name:       "block http import",
            expression: `import "http"
                        $returns: http.Get("http://malicious.com")`,
            shouldFail: true,
            errorMsg:   "import \"http\" is not allowed",
        },
        {
            name:       "block exec import",
            expression: `import "exec"
                        $returns: exec.Run("rm -rf /")`,
            shouldFail: true,
            errorMsg:   "import \"exec\" is not allowed",
        },
        {
            name:       "allow safe imports",
            expression: `import "strings"
                        $returns: strings.ToUpper("hello")`,
            shouldFail: false,
        },
        {
            name:       "timeout enforcement",
            expression: `$returns: {for i in range(1000000) {x: i}}`,
            shouldFail: true,
            errorMsg:   "evaluation timeout",
        },
    }
}
```

#### Integration Tests
- End-to-end definition rendering with dynamic evaluation
- Performance evaluation
- Security boundary testing

#### E2E Tests
- Real-world scenarios with user-provided expressions
- Environment-based configuration
- Dynamic resource allocation
- Custom naming logic

## Implementation Plan

### Proposed Phases

#### Phase 1: Core Implementation (Alpha)
Implement basic dynamic CUE evaluation with security controls.
- Core `cue.#Render` function
- Security sandbox implementation
- Basic caching and timeout handling
- Feature flag control

#### Phase 2: Enhancement & Optimization (Beta)
Add advanced features and optimize performance.
- Performance optimizations
- Enhanced error messaging
- Context passing improvements
- Real-world validation

#### Phase 3: Production Ready (GA)
Production hardening and comprehensive tooling.
- Complete documentation
- Best practices guide
- Community feedback integration

### Graduation Criteria

#### Alpha
- [ ] Core `cue.#Render` function implemented
- [ ] Security sandbox operational
- [ ] Basic timeout and resource limits
- [ ] Unit test coverage >80%
- [ ] Feature flagged at compiler level

#### Beta
- [ ] Sandboxed compiler fully implemented
- [ ] All security tests passing (import blocking, timeout, resource limits)
- [ ] Performance optimizations complete
- [ ] Integration tests passing
- [ ] Real-world use case validation
- [ ] Draft documentation available

#### GA
- [ ] Production performance validated (<20ms for typical expressions - TBC)
- [ ] Security audit completed by external review
- [ ] Penetration testing performed
- [ ] Comprehensive documentation with security best practices
- [ ] Monitoring and observability for security events
- [ ] Community adoption confirmed

### Production Readiness

- **Performance**: Sub-20ms evaluation for typical expressions (TBC)
- **Security**: Penetration testing completed
- **Monitoring**: Metrics for evaluation time, cache hits, errors
- **Rollback**: Feature flag for immediate disable

## Implementation History

- 2025-09-22: KVEP-0007 created with initial design

## Drawbacks

1. **Security Risks**: Evaluating user code always carries risk
2. **Performance Impact**: Dynamic compilation is slower than static
3. **Debugging Difficulty**: Runtime errors harder to trace
4. **Complexity**: Another concept for users to understand
5. **Potential Misuse**: Could lead to overly complex definitions

## Alternatives

### Alternative 1: CEL Integration
Use Common Expression Language instead of CUE for dynamic evaluation.
- CUE serves much of the same purpose, CEL support would needlessly add further language requirements
- Less integrated with existing CUE templates
- More limited transformation capabilities

### Alternative 2: Webhook-Based Logic
Allow external webhooks to provide dynamic logic.
- Network latency and reliability concerns
- More complex deployment architecture
- Security implications of external calls
- Harder to test and debug
- CueX can already leverage network based integrations & package functionality