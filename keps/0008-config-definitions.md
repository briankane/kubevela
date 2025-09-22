---
title: ConfigDefinitions - Dynamic Configuration Management
kvep-number: 0008
authors:
  - "@author"
area: core
status: implementable
creation-date: 2025-09-22
last-updated: 2025-09-22
---

# KEP-0008: ConfigDefinitions - Dynamic Configuration Management

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

- Reuses existing DefinitionRevision mechanism for versioning
- ConfigTemplates are MANDATORY to ensure type safety and validation
- All ConfigDefinitions must reference a ConfigTemplate for schema enforcement

### Release Targets

TBD

## Summary

This KEP proposes ConfigDefinitions, a new KubeVela definition type that enables Just-In-Time (JIT) configuration sourcing through CUE templates. ConfigDefinitions can dynamically fetch and transform configuration from external sources (databases, APIs, Kubernetes resources, secret stores) at runtime, providing versioned, cacheable configuration that integrates with KubeVela's existing definition and config ecosystem. Every ConfigDefinition MUST reference a ConfigTemplate that defines its schema, ensuring type-safe, validated configuration that preserves the declarative nature of KubeVela Applications while enabling dynamic sourcing.

### Tracking Issue
- Issue #[TBD]: [KVEP-0008] ConfigDefinitions Implementation

### Related Issues and KVEPs
- Related to existing ComponentDefinition/TraitDefinition versioning
- Complements ConfigTemplate schema validation feature

## Motivation

Currently, KubeVela Applications have no means to load application-wide configuration that can be shared across multiple X-Definitions. This fundamental limitation forces developers to use complex workflows and data passing mechanisms to load configuration, with limited validation capabilities. Configuration is handled through static ConfigMaps, Secrets, or inline properties with these limitations:

1. **No Application-Wide Config**: No mechanism to define shared configuration used by multiple components/traits
2. **Complex Data Passing**: Requires workflows to pass configuration between definitions
3. **No JIT Sourcing**: Configuration must be pre-defined (or at least defined externally to the Application) - cannot fetch from external sources at runtime
4. **No Dynamic Integration**: Cannot query databases, APIs, or other resources for real-time config
5. **Limited Validation**: No built-in schema validation for configuration data
6. **No Versioning**: Config changes affect all consumers immediately
7. **No Reusability**: Similar configs must be duplicated across applications
8. **Performance Issues**: Repeated external queries without caching

### Goals

- Provide application-wide configuration that can be consumed by multiple components, traits, workflows or policies
- Enable JIT configuration sourcing from external systems (K8s resources, APIs, databases)
- Enable dynamic configuration generation using CUE templates
- Provide versioning through DefinitionRevision mechanism
- Implement efficient caching to reduce external queries and computation overhead
- Enforce mandatory schema validation via ConfigTemplates
- Maintain consistency with existing definition types
- Provide a mechanism similar to Terraform "data sources" that compliment workload rendering capabilities
- Preserve declarative application definitions through guaranteed type safety

### Non-Goals

- Replace existing static Config/ConfigTemplate mechanisms - this should be fully compatible with them and utilise them
- Provide external configuration storage (e.g. Redis, Vault)
- Implement configuration hot-reloading
- Create a full configuration management system

## Proposal

Add ConfigDefinition as a new definition type that:
1. Requires a ConfigTemplate reference for schema definition and validation
2. Uses CUE templates to generate configuration dynamically
3. Supports versioning via DefinitionRevisions
4. Implements caching & persistence (In-Memory -> KV Config w/ TTL-> Config Provider Logic)
5. Enforces type safety through mandatory ConfigTemplate validation
6. Follows the same patterns as Component/Trait/Policy definitions

### User Stories

#### Story 1: Abstracting Configuration Complexity
As a platform engineer, I want to expose configuration to end users through the same familiar, controlled abstraction layer they use for provisioning resources. Users should be able to consume configuration without understanding the underlying sources or complexity, just as they provision workloads without knowing the implementation details.

```yaml
# ConfigTemplate defines the schema for type-safe access
apiVersion: core.oam.dev/v1beta1
kind: ConfigTemplate
metadata:
  name: database-connection
spec:
  schematic:
    cue:
      template: |
        host: string
        port: int & >=1 & <=65535
        database: string
        username: string
        password: string
        maxConnections?: int & >=1 & <=1000
        sslMode?: "require" | "disable" | "prefer"

---
# ConfigDefinition MUST reference a ConfigTemplate
apiVersion: core.oam.dev/v1beta1
kind: ConfigDefinition
metadata:
  name: database-config
spec:
  template: "database-connection"  # REQUIRED: References the ConfigTemplate above
  scope: "application"  # Default scope
  schematic:
    cue:
      template: |
        import "vela/kube"

        parameter: {
          environment: "dev" | "staging" | "prod"
          database: string
        }

        // JIT: Fetch database service endpoint from Kubernetes
        dbService: kube.#Get & {
          resource: {
            apiVersion: "v1"
            kind: "Service"
            metadata: {
              name: "postgres-\(parameter.environment)"
              namespace: "databases"
            }
          }
        }

        // JIT: Fetch credentials from Secret
        credentials: kube.#Get & {
          resource: {
            apiVersion: "v1"
            kind: "Secret"
            metadata: {
              name: "db-creds-\(parameter.database)"
              namespace: "secrets"
            }
          }
        }

        // Output conforms to #DatabaseConnection schema
        output: {
          host: dbService.resource.spec.clusterIP
          port: dbService.resource.spec.ports[0].port
          database: parameter.database
          username: credentials.resource.data.username
          password: credentials.resource.data.password
          maxConnections: parameter.environment == "prod" ? 100 : 10
          sslMode: parameter.environment == "dev" ? "disable" : "require"
        }
```

#### Story 2: Versioned Configuration Updates
As a KubeVela User, I want to update configuration schemas gradually in my Applications, allowing some applications to use the old version while others adopt the new version.

```yaml
# Application using specific config version
apiVersion: core.oam.dev/v1beta1
kind: Application
metadata:
  name: my-app
spec:
  config:
    - name: db
      type: database-config@v2  # Pin to specific version
      properties:
        environment: prod
        database: myapp
```

#### Story 3: External API Integration
As a Platform Engineer, I want to fetch configuration from external APIs (service discovery, feature flags, rate limits) and cache the results to avoid repeated external calls.

```yaml
# ConfigTemplate defines the service discovery schema
apiVersion: core.oam.dev/v1beta1
kind: ConfigTemplate
metadata:
  name: service-discovery-schema
spec:
  schematic:
    cue:
      template: |
        endpoints: [...{
          host: string
          port: int & >=1 & <=65535
        }]
        loadBalancer?: string
        healthCheck?: string
        rateLimit: {
          rps: int & >=1
          burst: int & >=1
        }

---
# ConfigDefinition must reference the ConfigTemplate (for initial phases)
apiVersion: core.oam.dev/v1beta1
kind: ConfigDefinition
metadata:
  name: service-discovery
  annotations:
    config.oam.dev/ttl: "5m"  # TTL for the Config resource before re-evaluation
spec:
  template: "service-discovery-schema"  # REQUIRED: References the ConfigTemplate above
  scope: "application"
  schematic:
    cue:
      template: |
        import "vela/http"

        parameter: {
          service: string
          region: string
        }

        // JIT: Query external service registry
        discovery: http.#Get & {
          url: "https://registry.example.com/api/v1/services/\(parameter.service)?region=\(parameter.region)"
          headers: {
            "Authorization": "Bearer ${SERVICE_REGISTRY_TOKEN}"
          }
        }

        // JIT: Get rate limits from API gateway
        rateLimit: http.#Get & {
          url: "https://gateway.example.com/limits/\(parameter.service)"
        }

        output: {
          endpoints: discovery.response.body.endpoints
          loadBalancer: discovery.response.body.loadBalancer
          healthCheck: discovery.response.body.healthCheck
          rateLimit: {
            rps: rateLimit.response.body.requestsPerSecond
            burst: rateLimit.response.body.burstSize
          }
        }
```

#### Story 4: Dynamic Policy Configuration from Metadata
As a platform engineer, I want to automatically assign topology policies based on external metadata rather than requiring users to manually specify deployment constraints.

```yaml
# ConfigTemplate defines the topology schema
apiVersion: core.oam.dev/v1beta1
kind: ConfigTemplate
metadata:
  name: app-topology-schema
spec:
  schematic:
    cue:
      template: |
        #AppTopology: {
          clusters: [...string] & len(>=1)
        }

---
# ConfigDefinition MUST reference the ConfigTemplate
apiVersion: core.oam.dev/v1beta1
kind: ConfigDefinition
metadata:
  name: app-topology
  annotations:
    config.oam.dev/ttl: "10m"
spec:
  template: "app-topology-schema"  # REQUIRED: References the ConfigTemplate above
  scope: "application"
  schematic:
    cue:
      template: |
        import "vela/http"

        parameter: {
          appName: string
        }

        // JIT: Query CMDB for app topology requirements
        appMetadata: http.#Get & {
          url: "https://cmdb.company.com/api/v1/apps/\(parameter.appName)/topology"
        }

        output: {
          clusters: appMetadata.response.body.clusters
        }

---
# Application uses simple config reference
apiVersion: core.oam.dev/v1beta1
kind: Application
metadata:
  name: my-app
spec:
  config:
    - name: topology-config
      type: app-topology
      properties:
        appName: my-app

  components:
    - name: backend
      type: webservice
      properties:
        image: myapp:v1

  policies:
    - name: topology-policy
      type: topology
      properties:
        fromConfig: topology-config.clusters
        path: clusters
```

### Risks and Mitigations

| Risk | Likelihood | Impact | Mitigation |
|------|------------|--------|------------|
| Cache invalidation bugs | Medium | High | Comprehensive testing, clear cache key design |
| Version migration complexity | Medium | Medium | Clear migration documentation, tooling |
| Performance regression | Low | Medium | Benchmarking, monitoring, cache metrics |
| Schema evolution issues | Medium | Medium | Validation rules, compatibility checks |

## Design Details

### Multi-Cluster Design Considerations

ConfigDefinitions are designed to work seamlessly in multi-cluster environments by respecting execution scope boundaries:

#### Cache Isolation
- **Application-level configs** are cached once per application and shared across all clusters
- **Cluster-level configs** are cached separately per cluster, ensuring isolation
- Cache keys automatically include cluster name when in cluster scope
- This prevents cross-cluster cache pollution while maximizing efficiency

**Important Note**: Application-scoped ConfigDefinitions are resolved once at the application level, but their values are still accessible to components and traits when they're dispatched to clusters. The key difference is that application-scoped configs use the same resolved values across all clusters, while cluster-scoped configs can have different values per cluster.

#### Execution Model
- **Just-In-Time Resolution**: Configs are resolved when needed, not pre-computed
- **Lazy Evaluation**: Cluster-specific configs only evaluate in the target cluster
- **Parallel Execution**: Each cluster resolves its configs independently during dispatch

#### Context Propagation
- The KubeVela controller automatically propagates the correct context based on execution scope
- `context.cluster` is only available when components/traits are being dispatched to specific clusters
- ConfigDefinitions can detect their scope and adapt behavior accordingly

#### Best Practices for Multi-Cluster Configs
1. **Use cluster-agnostic names** in ConfigDefinitions, derive region from cluster context
2. **Centralize cluster-to-region mapping** in the ConfigDefinition template
3. **Provide sensible defaults** for unmapped clusters
4. **Cache appropriately** - longer TTLs for stable configs, shorter for dynamic ones
5. **Test failover scenarios** - ensure configs work when clusters are unavailable

### API Changes

#### New ConfigDefinition CRD

```yaml
apiVersion: core.oam.dev/v1beta1
kind: ConfigDefinition
metadata:
  name: app-config
  namespace: vela-system
  labels:
    definition.oam.dev/revision: "2"  # Current revision
  annotations:
    definition.oam.dev/description: "Application configuration"
    config.oam.dev/ttl: "5m"  # Config resource TTL before re-evaluation
    config.oam.dev/template: "app-template-v1"  # REQUIRED: ConfigTemplate reference
spec:
  scope: "application"  # or "cluster" - defaults to "application"
  schematic:
    cue:
      template: |
        parameter: {
          // Input parameters
        }
        output: {
          // Generated configuration
        }
```

#### Application Config Reference

```yaml
apiVersion: core.oam.dev/v1beta1
kind: Application
metadata:
  name: my-app
spec:
  # Application-wide configuration
  config:
    - name: database
      type: database-config
      properties:
        environment: prod
        database: myapp

  components:
    - name: backend
      type: webservice
      properties:
        image: myapp:v1
        env:
          - name: DB_HOST
            fromConfig: database.host
            path: DB_HOST
          - name: DB_PORT
            fromConfig: database.port
            path: DB_PORT
```

### Versioning Implementation

#### ConfigDefinition Types

```go
// apis/core.oam.dev/v1beta1/configdefinition_types.go
type ConfigDefinitionSpec struct {
    // Template is the REQUIRED reference to ConfigTemplate for schema validation
    // +kubebuilder:validation:Required
    Template string `json:"template"`

    // Scope defines where this config can be executed
    // +kubebuilder:default=application
    // +kubebuilder:validation:Enum=application;cluster
    Scope string `json:"scope,omitempty"`

    // Schematic defines the configuration generation logic
    Schematic common.Schematic `json:"schematic"`
}

type ConfigScope string

const (
    ConfigScopeApplication ConfigScope = "application"
    ConfigScopeCluster     ConfigScope = "cluster"
)
```

#### Extend DefinitionType Enum

```go
// apis/core.oam.dev/common/types.go
type DefinitionType string

const (
    ComponentType    DefinitionType = "Component"
    TraitType        DefinitionType = "Trait"
    PolicyType       DefinitionType = "Policy"
    WorkflowStepType DefinitionType = "WorkflowStep"
    ConfigType       DefinitionType = "Config"  // NEW
)
```

#### Update DefinitionRevisionSpec

```go
// apis/core.oam.dev/v1beta1/definitionrevision_types.go
type DefinitionRevisionSpec struct {
    Revision       int64                  `json:"revision"`
    RevisionHash   string                 `json:"revisionHash"`
    DefinitionType common.DefinitionType  `json:"definitionType"`

    // Existing fields...

    // NEW: ConfigDefinition snapshot
    ConfigDefinition ConfigDefinition `json:"configDefinition,omitempty"`
}
```

### Validation

ConfigDefinitions enforce strict validation at multiple levels to ensure type safety and prevent runtime errors. **ConfigTemplates are MANDATORY** for all ConfigDefinitions to:
- Preserve the declarative nature of Applications by guaranteeing predictable outputs
- Enable compile-time validation of all configuration references
- Ensure type compatibility between configuration and consumers
- Provide clear contracts for what configuration will be available
- Prevent runtime surprises from dynamic configuration sources

#### Scope Validation

The system validates that ConfigDefinitions are used in appropriate contexts:

```go
// pkg/controller/validation/config_validator.go
func ValidateConfigScope(app *Application) error {
    for _, policy := range app.Spec.Policies {
        for _, configRef := range policy.ConfigRefs {
            configDef := getConfigDefinition(configRef.Type)
            if configDef.Spec.Scope == "cluster" {
                return fmt.Errorf("policy %s cannot reference cluster-scoped config %s",
                    policy.Name, configRef.Type)
            }
        }
    }

    for _, workflow := range app.Spec.Workflow.Steps {
        for _, configRef := range workflow.ConfigRefs {
            configDef := getConfigDefinition(configRef.Type)
            if configDef.Spec.Scope == "cluster" {
                return fmt.Errorf("workflow step %s cannot reference cluster-scoped config %s",
                    workflow.Name, configRef.Type)
            }
        }
    }

    return nil
}
```

#### Reference Validation

All `fromConfig` references are validated at compile time:

```go
func ValidateConfigReferences(app *Application) error {
    // Build map of available configs
    availableConfigs := make(map[string]*ConfigDefinition)
    for _, config := range app.Spec.Config {
        availableConfigs[config.Name] = getConfigDefinition(config.Type)
    }

    // Validate all fromConfig references
    for _, component := range app.Spec.Components {
        for _, envVar := range component.Properties.Env {
            if envVar.FromConfig != "" {
                configName, path := parseConfigRef(envVar.FromConfig)

                // Check config exists in application
                if _, exists := availableConfigs[configName]; !exists {
                    return fmt.Errorf("component %s references undefined config %s",
                        component.Name, configName)
                }
            }
        }
    }

    return nil
}
```

#### Schema Validation

ConfigTemplates are **mandatory** and provide schema validation for type safety:

```go
func ValidateConfigDefinition(configDef *ConfigDefinition) error {
    // Template reference is mandatory
    if configDef.Spec.Template == "" {
        return fmt.Errorf("ConfigDefinition %s must reference a ConfigTemplate",
            configDef.Name)
    }

    // Get the referenced ConfigTemplate
    configTemplate := getConfigTemplate(configDef.Spec.Template)
    if configTemplate == nil {
        return fmt.Errorf("ConfigTemplate %s not found", configDef.Spec.Template)
    }

    // Validate output against template schema
    outputSchema := configTemplate.Spec.Schematic.CUE.Template
    outputValue := configDef.Spec.Schematic.CUE.Output

    // Use CUE to validate schema compliance
    ctx := cuecontext.New()
    schema := ctx.CompileString(outputSchema)
    output := ctx.CompileString(outputValue)

    // Unify to check if output conforms to schema
    unified := schema.Unify(output)
    if err := unified.Validate(); err != nil {
        return fmt.Errorf("config output does not match template schema: %w", err)
    }

    return nil
}
```

#### Type Matching Validation

The system ensures type compatibility between config values and their usage:

```go
func ValidateTypeCompatibility(app *Application) error {
    for _, component := range app.Spec.Components {
        componentDef := getComponentDefinition(component.Type)

        for propName, propValue := range component.Properties {
            if ref := extractFromConfig(propValue); ref != "" {
                configName, path := parseConfigRef(ref)
                config := getConfig(app, configName)
                configTemplate := getConfigTemplate(config.Template)

                // Get expected type from component parameter schema
                expectedType := getParameterType(componentDef, propName)

                // Get actual type from config template schema
                actualType := getConfigAttributeType(configTemplate, path)

                // Validate type compatibility
                if !isTypeCompatible(expectedType, actualType) {
                    return fmt.Errorf(
                        "type mismatch: component %s property %s expects %s but config %s.%s provides %s",
                        component.Name, propName, expectedType, configName, path, actualType)
                }
            }
        }
    }

    return nil
}
```

#### Validation Errors

Clear, actionable error messages guide users to fix issues:

```yaml
# Example validation errors:

Error: Policy "deploy-policy" cannot reference cluster-scoped config "regional-database"
  Reason: Policies execute at application scope and cannot access cluster context
  Fix: Use an application-scoped config or reference the config in components instead

Error: Component "api" references undefined config "service-discovery"
  Reason: Config "service-discovery" is not defined in the application
  Fix: Add the config to spec.config:
    config:
      - name: service-discovery
        type: service-discovery-config
        properties: {...}

Error: Type mismatch: component "api" property "port" expects int but config database.port provides string
  Reason: The config template defines port as string but the component expects int
  Fix: Update the ConfigTemplate to define port as int or use type conversion

Error: Config "database" attribute "hostname" does not exist in template schema
  Reason: The ConfigTemplate does not define a "hostname" field
  Available fields: host, port, database, username, password
  Fix: Use "host" instead of "hostname" in fromConfig reference
```

#### Compile-Time vs Runtime Validation

| Validation Type | When Performed | What's Validated |
|----------------|----------------|------------------|
| Scope Validation | Compile-time | ConfigDefinition scope matches usage context |
| Reference Validation | Compile-time | All fromConfig references exist |
| Schema Validation | Compile-time | Config output matches ConfigTemplate |
| Type Matching | Compile-time | Config types match component parameter types |
| Path Validation | Compile-time | Referenced paths exist in ConfigTemplate |
| Value Validation | Runtime | Actual values meet constraints (min/max, regex, etc.) |

### Execution Scoping

ConfigDefinitions declare their execution scope in the `spec.scope` field (defaults to `"application"`). This determines when and how they execute:

#### Application-Level Scope (`scope: "application"`)

ConfigDefinitions with `scope: "application"` execute at the application level:

- **Execution**: Once per application, before cluster dispatch
- **Resolution**: Values are resolved once and cached, then available to all components/traits during dispatch
- **Context Available**:
  - `context.appName` - The application name
  - `context.appNamespace` - The application namespace
  - `context.appRevision` - The application revision
  - `context.appLabels` - Application labels
  - `context.appAnnotations` - Application annotations
  - `context.publishVersion` - The publish version
- **Context NOT Available**:
  - `context.name` - No component name (not in component context)
  - `context.namespace` - No target namespace (pre-dispatch)
  - `context.cluster` - No cluster context at this level
  - `context.clusterVersion` - No cluster version info
- **Use Cases**:
  - Fetching application metadata from Backstage
  - Loading feature flags from LaunchDarkly
  - Retrieving compliance policies from governance systems
  - Getting application-wide secrets from Vault

#### Cluster-Level Scope (`scope: "cluster"`)

ConfigDefinitions with `scope: "cluster"` execute at the cluster level:

- **Execution**: Once per cluster during component dispatch
- **Context Available**:
  - All application-level context PLUS:
  - `context.name` - The component name
  - `context.namespace` - The target namespace for deployment
  - `context.cluster` - The target cluster name
  - `context.clusterVersion` - Cluster version details (major, minor, gitVersion, platform)
- **Use Cases**:
  - Region-specific database endpoints
  - Cluster-specific service discovery
  - Cloud provider regional resources
  - Per-cluster credentials and certificates

#### Scope Resolution Table

| Referenced By | Execution Scope | App Context | Component Context | Cluster Context | Cache Key Includes Cluster |
|--------------|-----------------|-------------|-------------------|-----------------|---------------------------|
| Workflow Step | Application | Yes | No | No | No |
| Policy | Application | Yes | No | No | No |
| Component | Cluster | Yes | Yes | Yes | Yes |
| Trait | Cluster | Yes | Yes | Yes | Yes |

### Caching Architecture

#### TTL Hierarchy

ConfigDefinitions support multiple TTL levels for different caching layers:

1. **Config Resource TTL** (`config.oam.dev/ttl` annotation on ConfigDefinition)
   - Controls how long the Config (Secret) resource is valid before re-evaluation
   - Default: 5 minutes (configurable per ConfigDefinition)
   - Stored in `config.oam.dev/expires-at` annotation on the Config resource
   - When expired, provider logic re-executes to fetch fresh data

2. **In-Memory Cache TTL** (controller configuration)
   - Controls how long resolved values stay in memory
   - Default: 1 minute (shorter than Config TTL for faster invalidation)
   - Reduces Kubernetes API calls for frequently accessed configs
   - Automatically cleared when Config resource is updated

3. **Provider-Specific TTL** (optional)
   - External sources may have their own caching/TTL requirements
   - Respected when fetching from external APIs
   - Can influence but not exceed Config resource TTL

Example flow:
```
Request → Check Memory (1min TTL) → Check Config Resource (5min TTL) → Execute Provider → Update Config & Memory
```

#### Cache Key Design

```
# Application-level (Workflow/Policy context):
{namespace}/{config-def-name}:{revision}/{app-name}:{app-generation}/{hash(properties)}

# Cluster-level (Component/Trait context):
{namespace}/{config-def-name}:{revision}/{app-name}:{app-generation}/{cluster}/{hash(properties)}
```

Examples:
- Application-level: `vela-system/backstage-entity:v2/my-app:5/sha256:abc123`
- Cluster-level: `vela-system/database-config:v2/my-app:5/us-west-2/sha256:def456`

#### Configuration Resolution Flow (Prelim - AI Assisted)

```go
// pkg/controller/config/resolver.go
import (
    "github.com/oam-dev/kubevela/pkg/config"
    "github.com/oam-dev/kubevela/pkg/cue/process"
)

type ConfigResolver struct {
    // In-memory cache for fast lookups
    cache *MemoryCache

    // Kubernetes client for Config resources
    client client.Client

    // Provider executor for fetching external data
    provider *ProviderExecutor

    metrics *ResolverMetrics
}

type MemoryCache struct {
    entries sync.Map
    ttl     time.Duration
}

type CacheEntry struct {
    Data              map[string]interface{}
    CreatedAt         time.Time
    ExpiresAt         time.Time
    ConfigDefRevision string
    AppGeneration     int64
    PropertiesHash    string
}
```

#### Resolution Logic

```go
type ExecutionScope int

const (
    ApplicationScope ExecutionScope = iota
    ClusterScope
)

// determineScope checks if we're executing in cluster context
func determineScope(ctx context.Context) ExecutionScope {
    // Check if cluster context is available
    if cluster := process.GetContextData(ctx, "cluster"); cluster != nil {
        return ClusterScope
    }
    return ApplicationScope
}

// buildCacheKey creates a cache key that includes cluster when in cluster scope
func buildCacheKey(configDef *ConfigDefinition, params map[string]interface{}, scope ExecutionScope) string {
    base := fmt.Sprintf("%s/%s:%s/%s:%d",
        configDef.Namespace,
        configDef.Name,
        configDef.Revision,
        params["appName"],
        params["appGeneration"])

    // Include cluster in key when executing in cluster scope
    if scope == ClusterScope && params["cluster"] != nil {
        base = fmt.Sprintf("%s/%s", base, params["cluster"])
    }

    propHash := hashProperties(params)
    return fmt.Sprintf("%s/%s", base, propHash)
}

// buildSecretData creates the Secret's data field with input-properties and output fields
func buildSecretData(params map[string]interface{}, output map[string]interface{}) map[string][]byte {
    data := make(map[string][]byte)

    // Always include input-properties with the user's input parameters
    inputJSON, _ := json.Marshal(params)
    data["input-properties"] = inputJSON

    // Add each field from the template output
    for key, value := range output {
        if strVal, ok := value.(string); ok {
            data[key] = []byte(strVal)
        } else {
            jsonVal, _ := json.Marshal(value)
            data[key] = jsonVal
        }
    }

    return data
}

func (r *ConfigResolver) Resolve(ctx context.Context, configDef *ConfigDefinition, params map[string]interface{}) (map[string]interface{}, error) {
    // Determine execution scope
    scope := determineScope(ctx)

    // Include cluster in params if in cluster scope
    if scope == ClusterScope {
        if cluster := process.GetContextData(ctx, "cluster"); cluster != nil {
            params["cluster"] = cluster.(string)
        }
    }

    cacheKey := buildCacheKey(configDef, params, scope)

    // Step 1: Check in-memory cache
    if entry, ok := r.cache.Get(cacheKey); ok {
        return entry.Data, nil
    }

    // Step 2: Check for existing Config
    configName := generateConfigName(cacheKey)
    factory := config.NewConfigFactory(r.client)

    existing, err := factory.GetConfig(ctx, configDef.Namespace, configName)
    if err == nil {
        // Check if Config resource has exceeded its TTL
        expiresAt, _ := time.Parse(time.RFC3339, existing.Annotations["config.oam.dev/expires-at"])
        if time.Now().Before(expiresAt) {
            // Config exists and is still valid, use cached data
            data := existing.Data
            r.cache.Set(cacheKey, data) // Refresh in-memory cache
            return data, nil
        }
        // Config exists but TTL expired, will re-evaluate below
    }

    // Step 3: Execute provider logic to fetch fresh data
    data, err := r.provider.Execute(ctx, configDef, params)
    if err != nil {
        return nil, err
    }

    // Step 4: Create or update Config using KubeVela Config Factory
    factory := config.NewConfigFactory(r.client)

    configSpec := config.CreateConfigOptions{
        Name:      configName,
        Namespace: configDef.Namespace,
        Template:  configDef.Template,
        Properties: params,
        Labels: map[string]string{
            "config.oam.dev/definition": configDef.Name,
            "config.oam.dev/revision": configDef.Revision,
            "config.oam.dev/app": params["appName"].(string),
        },
        Annotations: map[string]string{
            "config.oam.dev/expires-at": calculateExpiry(configDef.TTL),
        },
    }

    // Include cluster in labels if in cluster scope
    if scope == ClusterScope && params["cluster"] != nil {
        configSpec.Labels["config.oam.dev/cluster"] = params["cluster"].(string)
    }

    if err := factory.CreateOrUpdateConfig(ctx, configSpec, data); err != nil {
        return nil, err
    }

    // Step 5: Populate in-memory cache
    r.cache.Set(cacheKey, data)

    return data, nil
}
```

#### KubeVela Config Resources (Secrets)

```yaml
apiVersion: v1
kind: Secret
metadata:
  name: config-cache-{hash}
  namespace: vela-system
  labels:
    config.oam.dev/catalog: "velacore-config"
    config.oam.dev/type: "database-config"
    config.oam.dev/definition: "database-config"
    config.oam.dev/revision: "v2"
    config.oam.dev/app: "my-app"
  annotations:
    config.oam.dev/ttl: "300s"
    config.oam.dev/expires-at: "2024-01-15T10:05:00Z"
    config.oam.dev/template-ref: "database-connection"
type: catalog.config.oam.dev/database-config
data:
  # input-properties stores the user input properties as base64-encoded JSON
  # Example decoded: {"environment":"prod","database":"myapp"}
  input-properties: eyJlbnZpcm9ubWVudCI6InByb2QiLCJkYXRhYmFzZSI6Im15YXBwIn0=

  # Additional keys from template output
  # Example decoded: "prod.db.example.com"
  host: cHJvZC5kYi5leGFtcGxlLmNvbQ==
  # Example decoded: "5432"
  port: NTQzMg==
  # Example decoded: "myapp"
  database: bXlhcHA=
  # Example decoded: "100"
  maxConnections: MTAw
  # Example decoded: "require"
  sslMode: cmVxdWlyZQ==
```

### Cache Invalidation (Prelim - AI Assisted)

```go
func (r *ConfigResolver) InvalidateApp(namespace, appName string) {
    // Clear in-memory cache entries for this app
    pattern := fmt.Sprintf("%s/*/%s:*", namespace, appName)
    r.cache.DeletePattern(pattern)

    // Secrets remain but will be re-evaluated on next access if their TTL has expired
}

func (r *ConfigResolver) InvalidateConfigDef(namespace, name string) {
    // Clear in-memory cache entries for this ConfigDefinition
    pattern := fmt.Sprintf("%s/%s:*", namespace, name)
    r.cache.DeletePattern(pattern)

    // Mark related Secrets for re-evaluation
    secrets := &v1.SecretList{}
    r.client.List(context.Background(), secrets, client.MatchingLabels{
        "config.oam.dev/catalog": "velacore-config",
        "config.oam.dev/definition": name,
    })

    for _, secret := range secrets.Items {
        // Set expiry to force re-evaluation on next access
        secret.Annotations["config.oam.dev/expires-at"] = time.Now().Format(time.RFC3339)
        r.client.Update(context.Background(), &secret)
    }
}
```

### Example Usage

#### Backstage Service Catalog Integration

```cue
"backstage-entity-config": {
  annotations: {}
  attributes: config: definition: {
    apiVersion: "v1"
    kind: "Secret"
  }
  description: "Fetch service metadata from Backstage catalog"
  labels: {}
  type: "config"
  scope: "application"  // Executes once at application level
}

import (
  "encoding/json"
)

template: {
  parameter: {
    // Optional: override the entity name (defaults to app name from context)
    entityName?: string
    // Backstage instance URL
    backstageUrl: *"https://backstage.internal.company.com" | string
  }

  #backstageEntity: {
    // Use context.appName if entityName not provided
    entityId: parameter.entityName | context.appName

    // Fetch entity data from Backstage API
    response: http.Get & {
      url: "\(parameter.backstageUrl)/api/catalog/entities/by-name/component/default/\(entityId)"
      headers: {
        "Accept": "application/json"
        "Authorization": "Bearer \(context.backstageToken)"  // Token from context/secret
      }
    }

    // Parse the Backstage entity response
    entity: json.Unmarshal(response.body)
  }

  output: {
    // Extract key metadata from Backstage entity
    entityName: #backstageEntity.entity.metadata.name
    entityDescription: #backstageEntity.entity.metadata.description
    owner: #backstageEntity.entity.spec.owner
    lifecycle: #backstageEntity.entity.spec.lifecycle
    type: #backstageEntity.entity.spec.type

    // Extract annotations that might contain deployment config
    pagerdutyIntegrationKey: #backstageEntity.entity.metadata.annotations["pagerduty.com/integration-key"]
    slackChannel: #backstageEntity.entity.metadata.annotations["slack.com/channel"]
    runbookUrl: #backstageEntity.entity.metadata.annotations["runbook.io/url"]

    // Extract relations (dependencies, parts of, etc.)
    dependencies: [ for dep in #backstageEntity.entity.relations if dep.type == "dependsOn" {dep.target}]
    partOf: [ for rel in #backstageEntity.entity.relations if rel.type == "partOf" {rel.target}][0] | ""

    // Custom metadata from Backstage
    tier: #backstageEntity.entity.metadata.labels["tier"] | "standard"
    criticality: #backstageEntity.entity.metadata.labels["criticality"] | "medium"
    costCenter: #backstageEntity.entity.metadata.annotations["company.com/cost-center"]
  }
}
```

##### Usage in Application

```yaml
apiVersion: core.oam.dev/v1beta1
kind: Application
metadata:
  name: my-service
spec:
  config:
    - name: service-metadata
      type: backstage-entity-config
      properties:
        # entityName defaults to "my-service" from app name
        backstageUrl: "https://backstage.platform.company.com"

  components:
    - name: api
      type: webservice
      properties:
        image: my-service:latest
        env:
          - name: SERVICE_OWNER
            value: fromConfig: service-metadata.owner
          - name: PAGERDUTY_KEY
            value: fromConfig: service-metadata.pagerdutyIntegrationKey
          - name: SERVICE_TIER
            value: fromConfig: service-metadata.tier
```

#### Cluster-Specific Database Endpoints

This example shows how ConfigDefinitions can provide different configurations based on the cluster they're executed in:

```cue
"regional-database-config": {
  annotations: {}
  attributes: config: definition: {
    apiVersion: "v1"
    kind: "Secret"
  }
  description: "Region-specific database endpoints"
  labels: {}
  type: "config"
  scope: "cluster"  // Executes per cluster with cluster context
}

template: {
  parameter: {
    database: string
    environment: "dev" | "staging" | "prod"
  }

  // Map clusters to regions
  #regionMapping: {
    "us-west-1-cluster": "us-west-1"
    "us-west-2-cluster": "us-west-2"
    "us-east-1-cluster": "us-east-1"
    "eu-west-1-cluster": "eu-west-1"
    "ap-southeast-1-cluster": "ap-southeast-1"
  }

  // Determine region from cluster context (only available when referenced by components/traits)
  region: #regionMapping[context.cluster] | "us-east-1"  // Default if cluster not mapped

  // Region-specific database endpoints
  #endpoints: {
    "us-west-1": {
      host: "db-usw1.company.internal"
      readReplica: "db-usw1-read.company.internal"
      port: 5432
    }
    "us-west-2": {
      host: "db-usw2.company.internal"
      readReplica: "db-usw2-read.company.internal"
      port: 5432
    }
    "us-east-1": {
      host: "db-use1.company.internal"
      readReplica: "db-use1-read.company.internal"
      port: 5432
    }
    "eu-west-1": {
      host: "db-euw1.company.internal"
      readReplica: "db-euw1-read.company.internal"
      port: 3306  // Different port for EU
    }
    "ap-southeast-1": {
      host: "db-apse1.company.internal"
      readReplica: "db-apse1-read.company.internal"
      port: 5432
    }
  }

  output: {
    // Primary write endpoint
    host: #endpoints[region].host
    port: #endpoints[region].port

    // Read replica endpoint
    readHost: #endpoints[region].readReplica
    readPort: #endpoints[region].port

    // Database specifics
    database: "\(parameter.environment)_\(parameter.database)"

    // Connection pool settings vary by region
    maxConnections: {
      if region == "us-east-1" { 200 }  // Larger pool for primary region
      if region == "eu-west-1" { 150 }
      100  // Default for other regions
    }

    // SSL requirements vary by region
    sslMode: {
      if region == "eu-west-1" { "require" }  // EU requires SSL
      if parameter.environment == "prod" { "require" }
      "prefer"
    }

    // Additional regional configuration
    connectionTimeout: region == "ap-southeast-1" ? 30 : 10
    statementTimeout: region == "ap-southeast-1" ? 60000 : 30000
  }
}
```

##### Usage in Multi-Cluster Application

```yaml
apiVersion: core.oam.dev/v1beta1
kind: Application
metadata:
  name: global-app
spec:
  components:
    - name: api
      type: webservice
      properties:
        image: api:latest
        env:
          # These will resolve to different values in each cluster
          - name: DB_HOST
            fromConfig: regional-db.host
            path: host
          - name: DB_READ_HOST
            fromConfig: regional-db.readHost
            path: readHost
          - name: DB_MAX_CONNECTIONS
            fromConfig: regional-db.maxConnections
            path: maxConnections

  policies:
    - name: deploy-to-regions
      type: topology
      properties:
        clusters: ["us-west-2-cluster", "eu-west-1-cluster", "ap-southeast-1-cluster"]

  # Config is referenced in components, so it executes per-cluster
  config:
    - name: regional-db
      type: regional-database-config
      properties:
        database: myapp
        environment: prod
```

When this application deploys:
- In `us-west-2-cluster`: DB_HOST becomes `db-usw2.company.internal`
- In `eu-west-1-cluster`: DB_HOST becomes `db-euw1.company.internal` with port 3306 and SSL required
- In `ap-southeast-1-cluster`: DB_HOST becomes `db-apse1.company.internal` with longer timeouts

### Test Plan

#### Unit Tests
- ConfigDefinition CRD validation
- Cache key generation and hashing
- TTL expiration logic
- Version resolution
- Cache hit/miss scenarios

#### Integration Tests
- End-to-end config generation
- Multi-version support
- Cache invalidation triggers
- Application reconciliation with configs
- ConfigTemplate validation integration

#### E2E Tests
- Real-world configuration scenarios
- Version migration workflows
- Cache performance under load
- Cross-namespace config references

## Implementation Plan

### Proposed Phases

TBD

### Graduation Criteria

TBD

### Production Readiness

- **Scalability**: Tested with 1000+ ConfigDefinitions and 10000+ cache entries
- **Performance**: P99 config resolution <50ms with cache
- **Monitoring**: Prometheus metrics for cache hit/miss, latency, errors
- **Reliability**: Graceful degradation when cache unavailable

## Implementation History

- 2025-09-22: KVEP-0008 created with initial design

## Future Enhancements

### Embedded ConfigTemplates

In a future iteration, we could support embedding ConfigTemplate definitions directly within ConfigDefinitions for cases where the schema is specific to a single configuration and won't be reused.

#### Motivation
- Simplify authoring for single-use configurations
- Co-locate schema and implementation in one resource
- Reduce the number of resources to manage for simple cases
- Improve developer experience for quick prototyping

#### Proposed Syntax

```yaml
apiVersion: core.oam.dev/v1beta1
kind: ConfigDefinition
metadata:
  name: app-specific-config
  namespace: default
spec:
  scope: "application"
  # Future: embedded template definition instead of reference
  configTemplate:
    schematic:
      cue:
        template: |
          serviceName: string
          environment: "dev" | "staging" | "prod"
          port: int & >=1 & <=65535
          replicas: int & >=1 & <=10
  schematic:
    cue:
      template: |
        parameter: {
          service: string
          env: string
        }

        output: {
          serviceName: parameter.service
          environment: parameter.env
          port: 8080
          replicas: parameter.env == "prod" ? 5 : 2
        }
```

#### Implementation Challenges

1. **Naming Conflicts**: Need deterministic, unique names for auto-created ConfigTemplates
   - Can't use ConfigDefinition UID (chicken-egg problem)
   - Content-based hash of namespace/name is most viable

2. **Creation Timing**: ConfigDefinition requires template reference at creation time
   - Would need webhook/admission controller to set reference before creation
   - Or make template field conditionally optional

3. **Lifecycle Management**: Ensuring proper cleanup of auto-created templates
   - OwnerReferences would handle this once both resources exist
   - Need to handle edge cases like orphaned templates

#### Decision

**Deferred to future phase** to keep initial implementation simple and focused. The current model with mandatory external ConfigTemplate references:
- Is simpler to implement and validate
- Provides clear separation of concerns
- Avoids complex creation timing issues
- Can be enhanced later without breaking changes

Users who need co-location can keep ConfigTemplate and ConfigDefinition in the same file/namespace for now.

## Drawbacks

1. **Added Complexity**: Another definition type to understand and manage
2. **Cache Consistency**: Potential for stale cache issues
3. **Migration Burden**: Existing apps need updates to use ConfigDefinitions
4. **Resource Overhead**: Additional memory and K8s resources for caching

## Alternatives

### Alternative 1: Extend ComponentDefinition
Add configuration generation to existing ComponentDefinitions
- Pros: No new CRD, familiar pattern
- Cons: Conflates concerns, breaks single responsibility
- Workflows still required for data passing

### Alternative 2: External Configuration Service
Use external service like Vault, Consul, or ConfigMap controller
- Pros: Mature solutions, rich features
- Cons: External dependency, doesn't integrate with KubeVela patterns

### Alternative 3: Static ConfigMaps Only
Continue using only static ConfigMaps/Secrets
- Pros: Simple, well-understood
- Cons: No dynamic generation, no versioning, duplication