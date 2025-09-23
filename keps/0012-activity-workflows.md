---
title: Maintenance Policy for Day-2 Operations
kep-number: XXXX
authors:
  - "@author"
area: core
status: implementable
creation-date: 2025-09-23
last-updated: 2025-09-23
---

# KEP-0012: Maintenance Policy for Day-2 Operations

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

- Leverages existing workflow system for maximum flexibility
- Supports timezone configuration (defaults to UTC)
- Maintenance workflows run independently of normal application workflows

### Release Targets

- KubeVela v1.N: Alpha implementation (Basic maintenance windows with core steps)
- KubeVela v1.N+1: Beta implementation (Extended workflow steps, production patterns)
- KubeVela v1.N+2: GA release

## Summary

This KEP proposes a generic `maintenance` policy that enables scheduled Day-2 operations through KubeVela's workflow system. Rather than just pausing reconciliation, this policy can execute complex maintenance procedures including database upgrades, backup operations, scaling adjustments, and coordinated downtime windows.

### Tracking Issue
- Issue #[TBD]: Generic maintenance policy for Day-2 operations

### Related Issues and KEPs
- (6890)[https://github.com/kubevela/kubevela/issues/6890]
## Motivation

### Current Problem

Day-2 operations like database upgrades, backup procedures, and maintenance windows require:
1. **Manual intervention**: Engineers manually execute maintenance procedures
2. **External tooling**: Separate systems for maintenance automation
3. **Complex coordination**: No integrated way to coordinate app changes with maintenance
4. **Limited scheduling**: No native support for recurring maintenance tasks

Current workarounds include:
- External cron jobs and scripts
- Manual runbooks
- Separate maintenance controllers
- Ad-hoc kubectl commands

### Goals

- Enable scheduled operations using workflows
- Support both one-time and recurring maintenance windows
- Provide common maintenance workflow steps (pause-reconciliation, scale, backup, etc.)
- Allow custom maintenance procedures through workflow composition
- Maintain application state awareness during maintenance

### Non-Goals

- Implement complex scheduling (holidays, exceptions)
- Provide database-specific upgrade logic
- Replace monitoring and alerting systems

## Proposal

### User Stories

#### Story 1: Database Maintenance Window
As a DBA, I want to schedule weekly database maintenance that pauses reconciliation, creates a backup, performs upgrades, and validates health before resuming normal operations.

#### Story 2: Scaling for Peak Traffic
As an SRE, I want to automatically scale up my application before predicted peak hours (Black Friday, sporting events) and scale down afterwards, with reconciliation paused during the transition.

#### Story 3: Coordinated Downtime
As a platform engineer, I need to coordinate application downtime with infrastructure maintenance, including user notifications, graceful shutdown, and controlled restart.

#### Story 4: Backup and Compliance
As a compliance officer, I need to ensure weekly backups are taken during quiet hours with verification and audit logging, while preventing any configuration changes during the backup window.

### Risks and Mitigations

| Risk | Likelihood | Impact | Mitigation |
|------|------------|--------|------------|
| Workflow failures during maintenance | Medium | High | Rollback procedures, status reporting |
| Overlapping maintenance windows | Low | Medium | Validation and conflict detection |
| Resource conflicts | Medium | Medium | Proper RBAC and resource locking |
| Complex debugging | Medium | Low | Detailed event logging and status |

## Design Details

### Policy Definition

```yaml
apiVersion: core.oam.dev/v1beta1
kind: Application
metadata:
  name: my-app
spec:
  components:
    - name: database
      type: db
      # ...
    - name: webserver
      type: webservice
      # ...

  policies:
    - name: maintenance
      type: workflow-activity
      properties:
        # Trigger configuration (schedule, labels, or both)
        triggers:
          # Cron schedule for regular execution
          schedule: "0 2 * * 6"  # 2am Saturdays
          timezone: "America/New_York"  # IANA timezone

          # Label-based triggering
          labels:
            # Workflow triggers when these labels are added
            - "maintenance.company.org/database-upgrade"

        # Maximum workflow duration (default: 1h)
        maxDuration: "2h"

        # Start workflow suspended, requiring manual approval
        requiresApproval: false

        # Workflow definition using existing and new step types
        workflow:
          steps:
            # Notify external system of maintenance start
            - name: start-maintenance
              type: webhook
              properties:
                url: "https://maintenance.company.org/startMaintenance"
                body:
                  application: "my-app"
                  scheduledTime: "context.scheduledTime"

            # Pause application reconciliation
            - name: pause-reconciliation
              type: label
              properties:
                operation: add
                labels:
                  "controller.core.oam.dev/pause": "true"

            # Scale down for maintenance
            - name: scale-down
              type: patch-object
              properties:
                resource:
                  apiVersion: apps/v1
                  kind: Deployment
                  name: my-app-webserver
                patch:
                  spec:
                    replicas: 0

            # Maintenance window - suspend workflow
            - name: maintenance-window
              type: suspend
              properties:
                duration: "1h"

            # Resume normal reconciliation (will restore original replica count)
            - name: resume-reconciliation
              type: label
              properties:
                operation: remove
                labels:
                  - "controller.core.oam.dev/pause"

            # Notify completion
            - name: finish-maintenance
              type: webhook
              properties:
                url: "https://maintenance.company.org/finishMaintenance"
                body:
                  application: "my-app"
                  status: "completed"
```

### Workflow Step Permissions

Workflow steps must explicitly declare which contexts they're permitted in. This label-based allowlist approach provides flexibility for future workflow types while maintaining security.

**Step Definition with Permission Label:**
```yaml
# In the workflow step definition
apiVersion: core.oam.dev/v1beta1
kind: WorkflowStepDefinition
metadata:
  name: scale
  labels:
    # Comma-separated list of permitted contexts
    workflow.oam.dev/permitted-contexts: "workflow-activity,application"
  annotations:
    workflow.oam.dev/description: "Scale component replicas"
spec:
  # ... step implementation
```

**Permission Contexts:**
- `application` - Normal application workflows (default, no validation required)
- `workflow-activity` - Scheduled maintenance activities (requires explicit permission)

**Example Step Permissions:**

```yaml
# Safe for any context
metadata:
  labels:
    workflow.oam.dev/permitted-contexts: "application,workflow-activity"

# Only for maintenance activities
metadata:
  labels:
    workflow.oam.dev/permitted-contexts: "workflow-activity"

# Application workflows only (deployment operations)
metadata:
  labels:
    workflow.oam.dev/permitted-contexts: "application"
    # Or no label at all - application is implicit default
```

**Suggested Permitted for workflow-activity** (must contain "workflow-activity" in permitted-contexts):
- `label` - Add/remove labels (e.g., pause reconciliation)
- `patch-object` - Patch existing objects (e.g., scale replicas)
- `webhook` - External notifications/triggers
- `suspend` - Pause workflow for time windows
- `notification` - Send alerts
- `exec` - Run maintenance commands (with restrictions)
- `step-group` - Group maintenance steps

**Suggested NOT Permitted for workflow-activity** (only have "application" or no label):
- `apply-component` - Application state modification
- `apply-object` - Direct K8s object manipulation
- `deploy` - Deployment operations
- `deploy-cloud-resource` - Cloud resource provisioning
- All other workflow steps without explicit permission

### New Workflow Step Types (Suggested)

#### 1. label
Add or remove labels from the Application.
```yaml
- name: manage-labels
  type: label
  properties:
    operation: "add" | "remove"  # Operation to perform
    labels:                       # Labels to add/remove
      key: value                  # For add operation
      # OR
      - key                       # For remove operation (list of keys)
```

Common uses:
- Pause reconciliation: `"controller.core.oam.dev/pause": "true"`
- Mark for maintenance: `"maintenance.company.org/active": "true"`
- Trigger other workflows: `"workflow.company.org/backup": "true"`

**Important Note**: When you pause reconciliation and make changes (like scaling down),
removing the pause label will trigger reconciliation which restores the application to
its desired state. This means manual scale-up operations are typically unnecessary.

#### 2. patch-object
Patch existing Kubernetes objects (more limited than apply-object).
```yaml
- name: patch-resource
  type: patch-object
  properties:
    resource:
      apiVersion: string
      kind: string
      name: string
      namespace?: string
    patchType: "merge" | "strategic" | "json"  # Default: strategic
    patch:
      # Patch content (e.g., spec.replicas: 1)
```

### Implementation Architecture

#### Controller Logic

The controller implements the following logic during each reconciliation (after initial deployment):
1. Check if any `workflow-activity` policy has triggers configured
2. For each workflow-activity policy:
   - Check if status annotation shows "running" → skip (already executing)
   - Check skip annotation → skip if present
   - **Check label triggers** (immediate execution):
     - For each configured trigger label in policy
     - If label exists on Application AND not already processed
     - Mark for immediate trigger with reason "label:{labelname}"
     - Workflow decides whether to proceed based on business logic
   - **Check schedule trigger** (if no label triggered):
     - Get last-run timestamp from annotation (or epoch if first run)
     - Evaluate if current time matches next scheduled run
     - If yes: mark for trigger with reason "schedule"
3. If should trigger and not running:
   - **Validate workflow steps** (reject if restricted steps present)
   - Set annotations:
     - `workflow-activity.oam.dev/{name}-status: "running"`
     - `workflow-activity.oam.dev/{name}-last-run: {current-timestamp}`
     - `workflow-activity.oam.dev/{name}-next-run: {calculated-next-run}` (schedule only)
     - `workflow-activity.oam.dev/{name}-label-{sanitized-label}-processed: "true"` (label trigger only)
   - Trigger the workflow with trigger reason in context
4. On workflow completion:
   - Update status annotation to "completed" or "failed"
   - Leave last-run and next-run timestamps for visibility
   - Remove trigger label from Application (automatic - prevents re-triggering)

**Trigger Evaluation Logic:**
```
Check if workflow should trigger:
1. If status annotation shows "running" → Skip (workflow already active)
2. If skip annotation is set → Skip (manual override)
3. Check label triggers (higher priority):
   - For each configured trigger label:
     - If label exists on Application:
       - If not already processed (check processed annotation):
         - Return trigger reason as "label:{labelname}"
4. Check schedule trigger:
   - Get last-run timestamp from annotation
   - If never run:
     - Check if current time matches schedule
   - Else:
     - Parse cron schedule with timezone (default UTC)
     - Calculate next scheduled time from last run
     - If current time is past next scheduled time (within 5-minute window):
       - Return trigger reason as "schedule"
5. Return no trigger needed

Label sanitization for annotations:
- Replace "/" with "-" and "." with "-" to create valid annotation keys
```

**Step Validation Logic:**
```
Validate workflow steps for context:
1. If context is "application" or empty → Allow all steps (no validation)
2. For each step in workflow:
   - Look up WorkflowStepDefinition
   - If step definition not found → Error (unknown step type)
   - Get permitted-contexts label from step definition
   - If no label → Step only allowed in "application" context
     - If current context is not "application" → Error (step not permitted)
   - Else parse comma-separated list of permitted contexts
     - If current context not in list → Error (step not permitted)
3. If all steps valid → Allow workflow

Usage:
- workflow-activity policies → Validate with context "workflow-activity"
- application workflows → No validation (context "application")
```

```
Application Reconciliation Loop:
├── Initial deployment complete?
│   └── Yes: For each workflow-activity policy
│       ├── Check {name}-status annotation
│       │   └── If "running": Skip (workflow active)
│       ├── Check {name}-skip annotation
│       │   └── If "true": Skip (manual override)
│       ├── Check for label triggers
│       │   ├── For each configured trigger label
│       │   ├── Label present on Application?
│       │   └── Not already processed ({name}-label-{label}-processed)?
│       │       └── Yes: Trigger workflow (reason: "label:{label}")
│       ├── Check schedule trigger (if no label trigger)
│       │   ├── Get {name}-last-run annotation
│       │   ├── Calculate next scheduled time from last run
│       │   └── Current time past next scheduled time?
│       │       └── Yes: Trigger workflow (reason: "schedule")
│       ├── Trigger workflow if needed
│       │   ├── Validate workflow steps for context
│       │   ├── Set {name}-status: "running"
│       │   ├── Set {name}-last-run: current timestamp
│       │   └── If label: Set {name}-label-{label}-processed: "true"
│       └── No trigger: Continue normal reconciliation
└── No: Continue initial deployment

Workflow Execution:
├── Start workflow with trigger reason in context
├── Execute steps sequentially
├── Report progress to Application status
├── On completion:
│   ├── Set {name}-status: "completed"
│   └── Remove trigger label from Application (prevent re-trigger)
└── On failure:
    ├── Set {name}-status: "failed"
    └── Remove trigger label (prevent re-trigger on error)
```

#### Annotation Control

```yaml
metadata:
  annotations:
    # Track last execution time for each activity (prevents duplicates)
    workflow-activity.oam.dev/maintenance-last-run: "2025-09-23T02:00:00Z"

    # Next scheduled run (for visibility and debugging)
    workflow-activity.oam.dev/maintenance-next-run: "2025-09-30T02:00:00Z"

    # Set to "running" while workflow is active
    workflow-activity.oam.dev/maintenance-status: "running"

    # Manual override to skip scheduled execution
    workflow-activity.oam.dev/maintenance-skip: "true"
```

The pattern for annotations:
- Last run: `workflow-activity.oam.dev/{name}-last-run: {ISO-8601-timestamp}`
- Next run: `workflow-activity.oam.dev/{name}-next-run: {ISO-8601-timestamp}`
- Status: `workflow-activity.oam.dev/{name}-status: running|completed|failed`
- Skip: `workflow-activity.oam.dev/{name}-skip: true`

### Status Reporting

The workflow-activity status integrates with the main Application workflow status:

```yaml
status:
  workflow:
    # Standard application workflow status
    mode: "DAG"
    finished: false

    # Workflow-activity status appears here
    activities:
      maintenance:
        active: true
        startedAt: "2025-09-23T02:00:00Z"
        currentStep: "maintenance-window"
        completedSteps:
          - "start-maintenance"
          - "pause-reconciliation"
          - "scale-down"
        phase: "Suspended"
        message: "In maintenance window for 1h"

  # Next scheduled activity
  scheduledActivities:
    - name: "maintenance"
      nextRun: "2025-09-30T02:00:00Z"
```

### Example Use Cases

#### Example 1: Comprehensive Maintenance Policy - Multiple Trigger Methods
```yaml
# This example shows all trigger methods:
# - Scheduled: Runs automatically every Saturday at 2am
# - On-demand: kubectl label app my-app maintenance.company.org/database-upgrade=true
# - Emergency: kubectl label app my-app emergency.company.org/critical-patch=true

policies:
  - name: system-maintenance
    type: workflow-activity
    properties:
      triggers:
        # Regular weekly maintenance
        schedule: "0 2 * * 6"  # 2am Saturdays UTC
        timezone: "UTC"

        # Label triggers for on-demand execution
        labels:
          - "maintenance.company.org/database-upgrade"
          - "maintenance.company.org/security-patch"
          - "emergency.company.org/critical-patch"

      workflow:
        steps:
          - name: notify-start
            type: notification
            properties:
              message: "Maintenance starting - triggered by: ${trigger_reason}"
          - name: pause-reconciliation
            type: label
            properties:
              operation: add
              labels:
                "controller.core.oam.dev/pause": "true"
          - name: scale-down
            type: patch-object
            properties:
              resource:
                apiVersion: apps/v1
                kind: Deployment
                name: frontend
              patch:
                spec:
                  replicas: 1
          - name: backup-database
            type: backup
            properties:
              component: "database"
              type: "snapshot"
          - name: perform-maintenance
            type: exec
            properties:
              command: ["./scripts/maintenance.sh", "${trigger_reason}"]
          - name: health-check
            type: health-check
            properties:
              component: "all"
          - name: resume-reconciliation
            type: label
            properties:
              operation: remove
              labels:
                - "controller.core.oam.dev/pause"
              # Note: Removing pause will trigger reconciliation,
              # which will restore original replica counts
          - name: notify-complete
            type: notification
            properties:
              message: "Maintenance completed successfully"
```

#### Example 2: Label-Only Triggers
```yaml
policies:
  - name: on-demand-operations
    type: workflow-activity
    properties:
      triggers:
        # Only triggered by labels, no schedule
        labels:
          - "ops.company.org/backup"
          - "ops.company.org/restore"
          - "ops.company.org/cleanup"
      workflow:
        steps:
          - name: notify-start
            type: notification
            properties:
              message: "On-demand operation: ${trigger_reason}"
          - name: execute-operation
            type: exec
            properties:
              command: ["./scripts/run-operation.sh", "${trigger_reason}"]
          - name: health-check
            type: health-check
            properties:
              component: "all"
          - name: notify-complete
            type: notification
            properties:
              message: "Operation completed: ${trigger_reason}"
```

#### Example 3: Combined Schedule and Label Triggers
```yaml
policies:
  - name: database-maintenance
    type: workflow-activity
    properties:
      triggers:
        # Regular monthly maintenance
        schedule: "0 2 1 * *"  # First day of month at 2am
        timezone: "America/New_York"
        # Also allow on-demand via labels
        labels:
          - "db.company.org/optimize"
          - "db.company.org/vacuum"
          - "db.company.org/emergency-repair"
      workflow:
        steps:
          - name: pause-app
            type: label
            properties:
              operation: add
              labels:
                "controller.core.oam.dev/pause": "true"
          - name: backup-before
            type: backup
            properties:
              component: "database"
              type: "full"
          - name: maintenance-operations
            type: exec
            properties:
              command: ["./scripts/db-maintenance.sh"]
          - name: validate
            type: health-check
            properties:
              component: "database"
          - name: resume-app
            type: label
            properties:
              operation: remove
              labels:
                - "controller.core.oam.dev/pause"
```

### Operational Usage

#### Triggering Workflows via Labels

Label triggers provide on-demand execution of maintenance workflows:

```bash
# Trigger database maintenance
kubectl label app my-app db.company.org/optimize=true

# Check workflow status
kubectl get app my-app -o jsonpath='{.metadata.annotations.workflow-activity\.oam\.dev/maintenance-status}'

# Skip next scheduled execution
kubectl annotate app my-app workflow-activity.oam.dev/maintenance-skip=true

# Remove skip annotation
kubectl annotate app my-app workflow-activity.oam.dev/maintenance-skip-
```

### Test Plan

#### Critical: Timing Logic Tests

**Schedule Calculation Tests** (exhaustive coverage required):
- Cron expression parsing with all standard patterns
- Timezone conversions (especially DST transitions)
- Next-run calculations from various last-run times
- Edge cases:
  - Month boundaries (28/29/30/31 day months)
  - Year transitions
  - Leap years
  - DST spring forward/fall back (2am doesn't exist, 2am happens twice)
  - Invalid cron expressions
  - Invalid timezone names

**Trigger Decision Tests**:
```go
// Test matrix covering all trigger scenarios
testCases := []struct {
    name            string
    schedule        string
    timezone        string
    labels          []string
    appLabels       map[string]string
    processedLabels map[string]string
    restrictions    *Restrictions
    lastRun         time.Time
    currentTime     time.Time
    status          string
    shouldRun       bool
    triggerReason   string
}{
    {
        name:          "Should trigger - past schedule",
        schedule:      "0 2 * * 6",
        currentTime:   "2025-09-30T02:01:00Z",
        lastRun:       "2025-09-23T02:00:00Z",
        status:        "completed",
        shouldRun:     true,
        triggerReason: "schedule",
    },
    {
        name:          "Should trigger - label present",
        labels:        []string{"maintenance.org/database"},
        appLabels:     map[string]string{"maintenance.org/database": "true"},
        shouldRun:     true,
        triggerReason: "label:maintenance.org/database",
    },
    {
        name:          "Should NOT trigger - label already processed",
        labels:        []string{"maintenance.org/database"},
        appLabels:     map[string]string{"maintenance.org/database": "true"},
        processedLabels: map[string]string{
            "workflow-activity.oam.dev/test-label-maintenance-org-database-processed": "true",
        },
        shouldRun:     false,
    },
    {
        name:          "Should NOT trigger - within restricted hours",
        labels:        []string{"maintenance.org/database"},
        appLabels:     map[string]string{"maintenance.org/database": "true"},
        restrictions:  &Restrictions{RestrictedHours: "09:00-17:00"},
        currentTime:   "2025-09-30T14:00:00Z", // 2pm UTC
        shouldRun:     false,
    },
    {
        name:        "Should NOT trigger - already running",
        schedule:    "0 2 * * 6",
        currentTime: "2025-09-30T02:01:00Z",
        lastRun:     "2025-09-30T02:00:00Z",
        status:      "running",
        shouldRun:   false,
    },
    {
        name:        "DST edge case - spring forward",
        schedule:    "0 2 * * 0",  // 2am doesn't exist
        timezone:    "America/New_York",
        // ... careful handling needed
    },
    // more cases...
}
```

#### Unit Tests
- Workflow step validation and permission checking
- Annotation management (last-run, next-run, status)
- Override mechanisms (skip annotations)

#### Integration Tests
- Full maintenance workflow execution
- Controller restart during maintenance
- Rapid reconciliation loop handling
- Clock skew between controller and nodes

#### E2E Tests
**Chaos Engineering**:
- Controller crash/restart during maintenance
- Node failure during workflow steps
- Network partition during execution
- System clock changes

**Long-running Validation**:
- Multi-week test with various schedules
- Verify zero missed executions
- Verify zero duplicate executions
- Monitor for schedule drift

#### Safety Mechanisms

**Pre-production Requirements**:
1. **Dry-run mode** - Will log scheduling decisions without executing
2. **Canary testing** - Will test on non-critical applications & workflows first

**Runtime Safety Features**:
Safety properties are configured at the policy properties root level:
- `maxDuration` - Maximum workflow execution time (default: 1h)
- `requiresApproval` - Start workflow suspended, requiring manual approval (default: false)

## Implementation Plan

### Phase 1: Core Framework (Alpha - v1.N)
- Basic maintenance policy structure
- Core workflow steps (pause/resume, scale, exec)
- Simple scheduling (cron-based)
- Status reporting
- Feature gated (default = disabled)

### Phase 2: Extended Capabilities (Beta - v1.N+1)
- Advanced workflow steps (supporting Day 2 Ops)
- Multiple schedule support
- Improved error handling
- Production patterns documentation
- Feature gated (default = disabled)

### Phase 3: Production Features (GA - v1.N+2)
- Performance optimizations
- Advanced scheduling options
- Complete workflow step library
- Monitoring and observability
- Feature gated (default = enabled)

### Graduation Criteria

#### Alpha
- [ ] Basic maintenance policy functional
- [ ] Core workflow steps implemented
- [ ] Single schedule working
- [ ] Status reporting available
- [ ] Feature flag implemented

#### Beta
- [ ] Extended workflow steps available
- [ ] Multiple schedules supported
- [ ] Error handling robust
- [ ] Documentation complete

#### GA
- [ ] Production validation completed
- [ ] Full workflow step library
- [ ] Monitoring/metrics available
- [ ] Community patterns documented

## Implementation History

- 2025-09-23: KEP-0012 created for maintenance policy

## Drawbacks

1. **Complexity**: More complex than implementing specific policies
2. **Workflow knowledge**: Requires understanding workflow system
3. **Testing difficulty**: Complex maintenance procedures - hard to test
4. **Resource usage**: Additional controller and workflow executions

## Alternatives

### Alternative 1: Simple Pause Policy
Just implement pause-reconciliation without workflow support.
- **Pros**: Simpler implementation and usage
- **Cons**: Doesn't address broader Day-2 operations needs

### Alternative 2: External Maintenance Tools
Use dedicated tools like Jenkins, Ansible, or operators.
- **Pros**: Purpose-built for maintenance tasks
- **Cons**: Day-2 operations not declaratively defined alongside the application

### Alternative 3: Manual Procedures
Continue with manual runbooks and kubectl commands.
- **Pros**: No changes required
- **Cons**: Error-prone, not scalable, no automation