# KEP-0014: Component Lifecycle Workflows

## Summary

This KEP proposes a mechanism for attaching workflows to components that execute at specific lifecycle points (before/after render, dispatch, delete, etc.). Workflows are linked to components via labels, creating a bidirectional relationship that enables controlled execution with proper context passing.

## Motivation

### Goals
- Enable components to trigger workflows at specific lifecycle points
- Support Day-2 operations through policy-triggered workflow execution (KEP-0012)
- Provide controlled, auditable workflow execution tied to component lifecycle
- Pass component context and parameters to workflows automatically
- Make workflow outputs available to components

### Non-Goals
- Allow arbitrary workflow creation from within component definitions
- Replace existing application workflow mechanisms
- Support recursive workflow triggering
- Enable user-configurable workflow attachment at the application level (users should use existing application workflows for custom orchestration)

## Proposal

### Overview

This KEP enables "Operational Knowledge as Code" by allowing lifecycle workflows to be published alongside component definitions. This transforms components from deployment abstractions into self-managing operational units that encapsulate best practices for their entire lifecycle.

#### Key Benefits

1. **Encapsulated Operational Expertise**: Component authors embed operational knowledge directly into components
   - Database components ship with tested backup/restore procedures
   - Stateful services include proven data migration workflows
   - Message queues provide built-in drain and cleanup operations

2. **Built-in Day-2 Operations**: Operational procedures are no longer an afterthought
   - Maintenance workflows are pre-defined and tested
   - Upgrade paths are coded and versioned with the component
   - Rollback strategies are ready to execute when needed

3. **Portable Operational Patterns**: When sharing components, their operational workflows travel with them
   - Teams don't need to reinvent operational procedures for common components
   - Compliance and security workflows are consistently applied across deployments
   - Platform best practices are enforced through required lifecycle hooks

4. **DevOps Collaboration**: Bridges the gap between development and operations teams
   - Developers define components while operations teams contribute lifecycle workflows
   - Platform teams ensure governance through mandatory workflows
   - SRE practices become enforceable and auditable through lifecycle hooks

### Implementation Approach

Components can reference workflows through labels, and workflows can declare their attachment to components. This creates a controlled, bidirectional relationship:

```yaml
apiVersion: core.oam.dev/v1beta1
kind: Component
metadata:
  name: my-database
  labels:
    workflows.core.oam.dev/pre-deploy: "check-error-budget"
    workflows.core.oam.dev/post-delete: "cleanup-resources"
spec:
  type: helm
  properties:
    # component properties
```

```yaml
apiVersion: core.oam.dev/v1alpha1
kind: Workflow
metadata:
  name: database-backup
  labels:
    component.core.oam.dev/attached-components: "my-database"
spec:
  steps:
    - name: backup
      type: backup-database
      properties:
        database: "{{ context.component.properties.database }}"
```

### Lifecycle Hook Points

The following lifecycle hooks are supported:

1. **pre-deploy**: Executes before component rendering begins
   - Fetch component-specific configuration (feature flags, rate limits)
   - Read secrets from vault for this component
   - Validate component dependencies exist
   - Determine deployment parameters (replicas, resources)
   - Outputs available to component during rendering

2. **post-deploy**: Executes after resources are successfully applied
   - Register component with service discovery
   - Configure component-specific routing rules
   - Initialize component caches or state
   - Register component metrics endpoints
   - Set up component health checks

3. **pre-delete**: Executes before component deletion
   - Validate no active sessions/connections
   - Check for dependent services
   - Export audit logs for compliance
   - Create final state snapshot

4. **post-delete**: Executes after component deletion completes
   - Deregister from service discovery
   - Clean up DNS entries
   - Remove from load balancer pools
   - Archive logs and metrics
   - Update service inventory

### Label Schema

#### Component Labels
```yaml
# Simple form (no parameters)
workflows.core.oam.dev/pre-deploy: "<workflow-name>"

# With parameters (using annotations for complex data)
workflows.core.oam.dev/pre-deploy: "<workflow-name>"
workflows.core.oam.dev/pre-deploy-params: |
  retentionDays: 30
  compressionEnabled: true
  storageClass: "premium"   # would need to be validated (via AST?) to assure type safety
```

**Note**: Workflow names are resolved ONLY in the component's namespace. Cross-namespace references are not supported.

#### Workflow Labels
```yaml
component.core.oam.dev/attached-components: "<component-name>,<component-name2>"    # Comma-separated list
# OR use wildcards for pattern matching:
component.core.oam.dev/attached-components: "postgres-*"                            # Matches postgres-primary, postgres-replica, etc.
component.core.oam.dev/attached-components: "*-database"                            # Matches mysql-database, mongo-database, etc.
component.core.oam.dev/attached-components: "*"                                     # Matches any component (use with caution)

component.core.oam.dev/allowed-hooks: "pre-deploy,post-deploy"              # Optional: if unset, workflow can be triggered by any hook

# Parameter schema using CUE for type safety and validation (optional but recommended)
component.core.oam.dev/parameter-schema: |
  retentionDays: *30 | int & >=1 & <=365
  compressionEnabled: *false | bool
  backupType: *"full" | "incremental" | "differential"
  storageClass?: "standard" | "premium" | "archive"                                 # component parameters should be validated against this schema
```

### Context Passing

Workflows receive the standard application context that components already have access to, extended with additional lifecycle-specific information:

```yaml
context:
  # Standard application context (existing)
  name: string                      # Application name
  namespace: string                 # Application namespace
  appName: string                   # Application name (alias)
  appRevision: string               # Application revision
  appRevisionNum: int               # Application revision number
  ...
  # Additional lifecycle context (new)
  component:
    name: string
    type: string
    properties: map[string]any      # Component properties (from component spec)
    traits: []trait
    outputs: any                    # Component outputs if post-deploy

  hook:
    type: string                    # "pre-deploy", "post-delete", etc.
    timestamp: string

  # Workflow-specific parameters (new)
  parameters: map[string]any        # Parameters passed to the workflow
```

The context extends the existing application context that components use, ensuring consistency while adding lifecycle-specific information.

### Output Handling

For pre-render workflows, outputs are made available to components through the existing workflow output mechanism. All step outputs are collated into the context:

```yaml
# Pre-render workflow that reads configuration
apiVersion: core.oam.dev/v1alpha1
kind: Workflow
metadata:
  name: read-deployment-config
spec:
  steps:
    - name: read-environment-config
      type: read-object
      properties:
        apiVersion: v1
        kind: ConfigMap
        name: environment-config
      outputs:
        - name: region
          valueFrom: output.data.region
        - name: replicas
          valueFrom: output.data.replicas
        - name: storageClass
          valueFrom: output.data.storageClass
    - name: read-tls-config
      type: read-object
      properties:
        apiVersion: v1
        kind: Secret
        name: tls-certificates
      outputs:
        - name: certName
          valueFrom: output.data["tls.crt"]
        - name: tlsEnabled
          valueFrom: output.data["tls.enabled"]
```

These outputs are then available in the component's CUE context during rendering:

```cue
// In component definition - ONLY for pre-render workflows
context: {
    workflow: {
        // Merged outputs from all steps (in step execution order)
        // Later steps override earlier ones if output names conflict
        outputs: {
            region: "us-west-2"           // from read-environment-config
            replicas: "3"                 // from read-environment-config
            storageClass: "fast-ssd"      // from read-environment-config
            certName: "prod-tls-cert"     // from read-tls-config
            tlsEnabled: "true"            // from read-tls-config
        }

        // Individual step outputs (for cases where you need specific step outputs)
        "read-environment-config": {
            outputs: {
                region: "us-west-2"
                replicas: "3"
                storageClass: "fast-ssd"
            }
        }
        "read-tls-config": {
            outputs: {
                certName: "prod-tls-cert"
                tlsEnabled: "true"
            }
        }
    }
}

// Component uses the configuration during rendering
import "strconv"

template: {
    output: {
        spec: {
            // Simple access via merged outputs
            replicas: strconv.Atoi(context.workflow.outputs.replicas)

            // Configure storage based on config
            volumeClaimTemplates: [{
                spec: {
                    storageClassName: context.workflow.outputs.storageClass
                }
            }]

            // Conditionally add TLS configuration
            if context.workflow.outputs.tlsEnabled == "true" {
                tls: {
                    enabled: true
                    certificateRef: context.workflow.outputs.certName
                }
            }

            // Or access specific step outputs if needed
            affinity: {
                nodeAffinity: {
                    requiredDuringSchedulingIgnoredDuringExecution: {
                        nodeSelectorTerms: [{
                            matchExpressions: [{
                                key: "failure-domain.beta.kubernetes.io/region"
                                operator: "In"
                                values: [context.workflow["read-environment-config"].outputs.region]
                            }]
                        }]
                    }
                }
            }
        }
    }B
}
```

**Important Notes**:
- Only **pre-deploy** workflows can provide outputs to the component context
- Other lifecycle hooks (post-deploy, etc.) execute too late for their outputs to be used during component rendering
- Outputs are available in two forms:
  - `context.workflow.outputs[output-name]`: Merged outputs from all steps (later steps override earlier ones)
  - `context.workflow[step-name].outputs[output-name]`: Individual step outputs
- Common pre-render use cases include:
  - Reading environment-specific configuration from ConfigMaps
  - Fetching secrets or certificates
  - Querying external systems for deployment parameters
  - Validating prerequisites before rendering

### Exposing Workflow Outputs to Application Workflows

Component workflow outputs can be exposed to application workflows through the component's outputs field using the `workflow` reference:

```yaml
apiVersion: core.oam.dev/v1beta1
kind: Application
spec:
  components:
    - name: database
      type: postgres-db
      properties:
        size: large
      outputs:
        # Reference pre-render workflow outputs directly
        - name: region
          valueFrom: workflow.outputs.region
        - name: storageClass
          valueFrom: workflow.outputs.storageClass
        # Or reference specific step outputs
        - name: tlsEnabled
          valueFrom: workflow["read-tls-config"].outputs.tlsEnabled

  workflow:
    steps:
      - name: deploy-database
        type: deploy
        properties:
          policies: ["default"]

      - name: configure-backup
        type: backup-config
        inputs:
          # Use the component outputs in subsequent workflow steps
          - from: region
            parameterKey: properties.region
          - from: storageClass
            parameterKey: properties.storage
```

The `workflow` reference in component outputs:
- **Only works for pre-deploy workflows** since they execute before component deployment
- Provides access to the same workflow context that the component CUE template sees
- Supports both merged outputs (`workflow.outputs.<name>`) and step-specific outputs (`workflow["step-name"].outputs.<name>`)
- Makes workflow data available to the application workflow without creating intermediate resources

This approach:
1. Maintains clean separation - components explicitly choose what to expose
2. Avoids creating unnecessary ConfigMaps/Secrets just for data passing
3. Uses the existing component output mechanism
4. Provides type-safe data flow from workflow → component → application

### Policy-Triggered Execution

Component lifecycle workflows integrate with the proposed workflow-activity policy from KEP-0012. Applications can trigger attached workflows through scheduled or label-triggered policies:

```yaml
apiVersion: core.oam.dev/v1beta1
kind: Application
spec:
  components:
    - name: my-database
      type: helm
      labels:
        workflows.core.oam.dev/on-maintenance: "database-upgrade"
  policies:
    - name: weekly-maintenance
      type: workflow-activity
      properties:
        triggers:
          schedule: "0 1 * * 0"  # Weekly on Sunday
        workflow:
          steps:
            - name: run-db-upgrade
              type: component-workflow
              properties:
                component: my-database
                workflow: database-upgrade  # Executes the attached workflow
                # Optional: override component properties
                properties:
                  backupFirst: true
                  maintenanceMode: true
```

The `component-workflow` step type:
- Validates the workflow is attached to the component (bidirectional labels)
- Executes the workflow with component context
- Optionally overrides component properties for this execution
- Returns workflow outputs for use in subsequent steps

### Parameter Validation

When a component references a workflow with parameters, the system validates the provided parameters against the workflow's declared schema:

1. **Schema Declaration**: Workflows declare parameter schemas using CUE in the `component.core.oam.dev/parameter-schema` annotation
2. **Development-time Validation**: Parameters are validated when:
   - Component definition/template is created or updated
   - Workflow attachment labels are modified on the component
   - The workflow-to-component binding is established
3. **Type Safety**: The validation ensures:
   - Required workflow parameters are provided in component labels/annotations
   - Parameter types match the schema (string, int, bool, etc.)
   - Values satisfy constraints (ranges, enums, patterns)
   - Default values are applied for optional parameters
4. **No Application-level Validation Required**:
   - End users create Applications with standard YAML (no changes needed)
   - Component parameters from the Application are passed to workflows via context automatically
   - Additional workflow-specific parameters are already validated at the component level
   - The Application creation process remains unchanged - all validation was done at development time

Example validation flow:
```yaml
# Workflow declares schema
component.core.oam.dev/parameter-schema: |
  retentionDays: *30 | int & >=1 & <=365
  compressionEnabled: *false | bool
  storageClass?: "standard" | "premium" | "archive"

# Component provides parameters
workflows.core.oam.dev/pre-deploy-params: |
  retentionDays: "invalid"  # ERROR: Type mismatch - expected int, got string
  # compressionEnabled uses default (false)
  storageClass: "ultra"      # ERROR: Value not in allowed enum
```

### Execution Model

1. **Validation Phase**:
   - Verify workflow exists in the same namespace as component
   - Verify bidirectional labels match (supporting wildcard patterns if present)
   - Validate component parameters against workflow schema (if schema is defined)
   - Check workflow is valid
   - If `allowed-hooks` label is present, validate the requested hook is in the allowed list
   - Reject if workflow is in a different namespace (no cross-namespace lookup)
   - Reject if parameter validation fails

2. **Execution Phase**:
   - Create WorkflowRun with component context
   - Execute workflow steps
   - Capture outputs
   - Store execution state in Application Status

3. **Integration Phase**:
   - Make outputs available to component
   - Update Application status with workflow results
   - Continue with lifecycle operation

### Workflow Execution Tracking

Component workflow executions are tracked in the Application Status:

```yaml
apiVersion: core.oam.dev/v1beta1
kind: Application
metadata:
  name: my-app
status:
  phase: running
  components:
    - name: api-gateway
      healthy: true
      workflowStatus:
        pre-deploy:
          name: fetch-gateway-config
          phase: succeeded
          lastExecutionTime: "2024-01-15T10:30:00Z"
          outputsRef:
            # Reference to ConfigMap created by workflow execution
            name: my-app-api-gateway-pre-deploy-outputs
            namespace: default
          cache:
            ttl: "1h"
            expiresAt: "2024-01-15T11:30:00Z"
          retries: 0
        post-deploy:
          name: register-service
          phase: succeeded
          lastExecutionTime: "2024-01-15T10:31:00Z"
          outputsRef:
            name: my-app-api-gateway-post-deploy-outputs
            namespace: default
    - name: database
      healthy: false
      workflowStatus:
        pre-deploy:
          name: validate-dependencies
          phase: failed
          lastExecutionTime: "2024-01-15T10:30:00Z"
          message: "Required database 'users-db' not found"
          retries: 3
          maxRetriesReached: true
          nextRetryTime: "2024-01-15T11:30:00Z"  # After TTL expiry
```

This tracking enables:
- **State persistence**: Workflow state survives reconciliations
- **Cache management**: Know when cache expires
- **Retry tracking**: Track retry attempts and backoff
- **Debugging**: Clear visibility into workflow execution history
- **Secure output storage**: Outputs stored in ConfigMaps, not in Application status
- **RBAC control**: Can restrict access to sensitive output ConfigMaps

**Output Security**:
- Workflow outputs are stored in separate ConfigMaps (created by workflow execution)
- Application status only contains references, not actual values
- Sensitive outputs (secrets, tokens) remain in ConfigMaps with appropriate RBAC
- ConfigMaps are garbage collected with the Application (via OwnerReferences)
- Component rendering retrieves outputs from ConfigMaps during reconciliation

**Extensibility**: The `workflowStatus` structure supports any number of named workflows:
- Lifecycle workflows: `pre-deploy`, `post-deploy`, `pre-delete`, `post-delete`
- Policy-triggered workflows (from KEP-0012): `backup-schedule`, `security-scan`, `compliance-check`
- Custom workflows: Any workflow executed in the component context

```yaml
workflowStatus:
  pre-deploy: {...}           # Lifecycle workflow
  post-deploy: {...}          # Lifecycle workflow
  daily-backup: {...}         # Policy-triggered workflow
  security-scan: {...}        # Policy-triggered workflow
  custom-operation: {...}     # Ad-hoc or custom workflow
```

This unified structure means:
- All component-related workflows are tracked in one place
- Consistent caching and retry semantics across workflow types
- Single source of truth for component workflow state
- Easy to extend for future workflow patterns

**Note on Scalability**: As the Application CRD grows with additional features, the status section may approach etcd size limits (1.5MB). If this becomes a concern, workflow state could be externalized to ConfigMaps with only references stored in the Application status. This refactoring would be transparent to users and maintain backward compatibility.

### Security Considerations

1. **Bidirectional Validation**: Both component and workflow must reference each other and set the permissions contract
2. **Strict Namespace Isolation**:
   - Workflows MUST be in the same namespace as the component
   - Cross-namespace workflow discovery is explicitly prohibited
   - No fallback to system namespaces (e.g., vela-system)
   - This prevents privilege escalation through workflow attachment
3. **Hook Restrictions**: Workflows can optionally declare which hooks they support via `allowed-hooks` label (if unset, allows all hooks)
4. **Execution Permissions**:
   - Component lifecycle workflows ALWAYS execute under the system service account
   - The bidirectional labeling contract explicitly defines the permission relationship between component and workflow
   - If a component depends on its workflows for functionality, users with access to the component automatically get the workflow functionality
   - This ensures components cannot be broken due to insufficient user permissions for attached workflows
5. **Audit Trail**: All workflow executions are logged with triggering context including the user who initiated the component operation

### Error Handling and Caching

**Default Execution Behavior**:
- Workflows execute on component changes AND periodically during reconciliation
- Default TTL: 1 hour (prevents excessive executions while catching external changes)
- Force refresh: Update application to trigger immediate workflow execution

```yaml
apiVersion: core.oam.dev/v1alpha1
kind: Workflow
metadata:
  name: fetch-config
  labels:
    component.core.oam.dev/attached-components: "api-gateway"
  annotations:
    # Optional: Override default TTL
    workflow.oam.dev/ttl: "10m"  # Default is 1h
    workflow.oam.dev/max-retries: "3"  # Default is 3
```

**Execution Logic**:
1. Workflow executes when:
   - Component is created or updated (properties/version change)
   - Cached results are older than TTL (default 1h)
   - Application is manually updated (forces refresh)

2. Workflow results are cached:
   - On successful execution
   - Cache key includes component version and properties hash
   - Cache respects TTL for automatic refresh

3. **Retry behavior**:
   - Failed workflows retry up to `max-retries` times (default 3)
   - After max retries exhausted, workflow enters permanent failed state
   - Application marked unhealthy during failures
   - Retries spread across reconciliation cycles with exponential backoff

4. **Failed state recovery**:
   - After reaching max retries, workflow stops retrying automatically
   - Recovery options:
     - Component change (new version/properties) resets retry counter
     - Manual application update (annotation change) forces retry
     - TTL expiry (1 hour default) triggers fresh attempt
   - This prevents infinite retry loops while allowing recovery

**Benefits of 1-hour default TTL**:
- **Catches external changes**: Config updates, rotated credentials, etc.
- **Minimizes load**: Hourly is reasonable for most external systems
- **Allows manual refresh**: SREs can force update via application touch
- **Sensible default**: Works well for both stable and dynamic configurations

This simple approach:
- **Reduces unnecessary executions** - Most workflows only need to run on changes
- **Respects external systems** - Prevents hammering APIs
- **Keeps component developers in control** - They set sensible TTLs
- **Maintains simplicity** - No complex cache invalidation rules

## Examples

### Fetch Component Configuration Before Deployment

```yaml
apiVersion: core.oam.dev/v1beta1
kind: Component
metadata:
  name: api-gateway
  labels:
    workflows.core.oam.dev/pre-deploy: "fetch-gateway-config"
    workflows.core.oam.dev/post-delete: "cleanup-routes"
spec:
  type: webservice
  properties:
    image: nginx:latest
    port: 8080
---
apiVersion: core.oam.dev/v1alpha1
kind: Workflow
metadata:
  name: fetch-gateway-config
  labels:
    component.core.oam.dev/attached-components: "*-gateway"  # Reusable for all gateways
    component.core.oam.dev/allowed-hooks: "pre-deploy"
spec:
  steps:
    - name: read-feature-flags
      type: read-object
      properties:
        apiVersion: v1
        kind: ConfigMap
        name: feature-flags
      outputs:
        - name: rateLimitEnabled
          valueFrom: output.data.rateLimitEnabled
        - name: corsEnabled
          valueFrom: output.data.corsEnabled
    - name: fetch-rate-limits
      type: read-object
      if: context["read-feature-flags"].outputs.rateLimitEnabled == "true"
      properties:
        apiVersion: v1
        kind: ConfigMap
        name: rate-limit-config
      outputs:
        - name: requestsPerSecond
          valueFrom: output.data.requestsPerSecond
        - name: burstSize
          valueFrom: output.data.burstSize
    - name: fetch-cors-config
      type: read-object
      if: context["read-feature-flags"].outputs.corsEnabled == "true"
      properties:
        apiVersion: v1
        kind: ConfigMap
        name: cors-config
      outputs:
        - name: allowedOrigins
          valueFrom: output.data.allowedOrigins
        - name: allowedMethods
          valueFrom: output.data.allowedMethods
```

### Register Service After Deployment

```yaml
apiVersion: core.oam.dev/v1beta1
kind: Component
metadata:
  name: web-app
  labels:
    workflows.core.oam.dev/post-deploy: "register-service"
spec:
  type: webservice
  properties:
    image: myapp:v2
---
apiVersion: core.oam.dev/v1alpha1
kind: Workflow
metadata:
  name: register-service
  labels:
    component.core.oam.dev/attached-components: "web-app"
    component.core.oam.dev/allowed-hooks: "post-deploy"
spec:
  steps:
    - name: get-service-info
      type: read-object
      properties:
        apiVersion: v1
        kind: Service
        name: "{{ context.component.name }}"
      outputs:
        - name: clusterIP
          valueFrom: output.spec.clusterIP
        - name: port
          valueFrom: output.spec.ports[0].port
    - name: register-consul
      type: http-request
      properties:
        url: "http://consul-server:8500/v1/agent/service/register"
        method: PUT
        body:
          ID: "{{ context.component.name }}-{{ context.namespace }}"
          Name: "{{ context.component.name }}"
          Address: "{{ context["get-service-info"].outputs.clusterIP }}"
          Port: "{{ context["get-service-info"].outputs.port }}"
          Tags:
            - "version:{{ context.component.properties.image }}"
```

### Cleanup Service Registration After Delete

```yaml
apiVersion: core.oam.dev/v1alpha1
kind: Workflow
metadata:
  name: cleanup-routes
  labels:
    component.core.oam.dev/attached-components: "*-gateway"
    component.core.oam.dev/allowed-hooks: "post-delete"
spec:
  steps:
    - name: deregister-consul
      type: http-request
      properties:
        url: "http://consul-server:8500/v1/agent/service/deregister/{{ context.component.name }}-{{ context.namespace }}"
        method: PUT
    - name: cleanup-route-table
      type: exec-command
      properties:
        command:
          - "kubectl"
          - "delete"
          - "httproute"
          - "{{ context.component.name }}-routes"
          - "--ignore-not-found=true"
```

## Implementation Plan

### Phase 1: Core Hook System
- Implement label validation and matching
- Add hooks for pre/post-deploy
- Basic context passing
- WorkflowRun creation and execution

### Phase 2: Extended Lifecycle
- Add remaining lifecycle hooks
- Implement output handling
- Add timeout and retry support

### Phase 3: Policy Integration
- Implement workflow-trigger policy type
- Add scheduling support
- Enhanced error handling and observability

## Design Rationale

### Stateless Workflow Design

Component lifecycle workflows must be stateless operations that:

1. **Read external configuration** - Fetch data from ConfigMaps, Secrets, or external services
2. **Pass data to components** - Provide configuration for the component to use during rendering
3. **Integrate with external systems** - Register/deregister components, update routing tables
4. **Perform validations** - Check preconditions without maintaining state

Workflows must NOT:
- Maintain state between executions
- Store persistent data (components handle their own state)
- Make assumptions about previous executions
- Depend on workflow execution history

All stateful operations belong within the component itself. Workflows are purely for orchestration and integration, ensuring components remain self-contained and portable.

### Separation of Concerns

Component lifecycle workflows are intentionally separate from application workflows:

1. **Component lifecycle workflows** are part of the component's contract and behavior, defined by the component author
2. **Application workflows** provide user-defined orchestration and remain the primary mechanism for custom workflow logic
3. Users requiring custom workflow behavior should use existing application workflows rather than modifying component lifecycles

For example, if a user wants to send a Slack notification after deploying their application, they should use an application workflow:

```yaml
apiVersion: core.oam.dev/v1beta1
kind: Application
spec:
  components:
    - name: web-app
      type: webservice
      properties:
        image: myapp:v2
  workflow:
    steps:
      - name: deploy
        type: deploy
        properties:
          policies: ["default"]
      - name: notify-slack
        type: notification
        properties:
          slack:
            channel: "#deployments"
            message: "Application {{ context.name }} deployed successfully"
```

Rather than trying to attach a notification workflow to the component's lifecycle, which would affect all instances of that component type.

This separation ensures component behavior remains consistent and predictable while preserving full flexibility at the application level.

## Alternatives Considered

1. **Inline workflow definitions in components**: Rejected due to security concerns and complexity
2. **Annotation-based linking**: Labels provide better visibility and queryability
3. **Custom resource for workflow attachment**: Adds unnecessary complexity
4. **Single-direction linking**: Bidirectional provides better security and validation

## References

- [Kubernetes Lifecycle Hooks](https://kubernetes.io/docs/concepts/containers/container-lifecycle-hooks/)
- [KubeVela Workflow Design](https://kubevela.io/docs/platform-engineers/workflow/overview)