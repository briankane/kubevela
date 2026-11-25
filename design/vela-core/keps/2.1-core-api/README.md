> ⚠️ **Early concept draft.** This KEP is an early-stage exploration. It is **incomplete and may be inaccurate**, its direction is unsettled, and it should not be relied upon for implementation or as a description of committed behaviour. Expect substantial change.

# KEP-2.1: `core.oam.dev/v2alpha1` API Types & CRDs

**Status:** Drafting (Not ready for consumption)
**Parent:** [vNext Roadmap](../README.md)

---

## Overview

KEP-2.1 defines `core.oam.dev/v2alpha1`: a new *version* of KubeVela's existing `core.oam.dev` API group, not a new group. The Definition types are not new resources: `ComponentDefinition`, `TraitDefinition`, and `WorkflowStepDefinition` already exist under `core.oam.dev/v1beta1` and are carried into `v2alpha1` with **structural changes** (the unified `outputs` list, directory/blueprint authoring, annotation-driven behaviour). It also defines `Component`, the rendered, signed unit the hub dispatches, which is a new kind under `core.oam.dev/v1alpha1` rather than part of `v2alpha1` (see [Component CRD](#component-crd)). This KEP establishes the `v2alpha1` structure and the conventions shared across the Definition types.

> **API versioning note:** v2 uses `core.oam.dev/v2alpha1` — the same API group as KubeVela v1 (`core.oam.dev`), but a new version. This is intentional: it enables coexistence of v1.x and v2.x types in the same cluster without group conflicts, and simplifies migration tooling since both generations share a common group. The v1.x `Application` and related types continue to live under `core.oam.dev/v1beta1`; the new v2 primitives are introduced under `core.oam.dev/v2alpha1`.

---

## API Group & Resources

**API Version:** `core.oam.dev/v2alpha1`

| Resource | Scope | Runs on | Role |
|---|---|---|---|
| `ComponentDefinition` | Cluster | Hub | Template: CUE package + schema |
| `TraitDefinition` | Cluster | Hub | Template: named trait with phase |
| `WorkflowStepDefinition` | Cluster | Hub | Template: reusable workflow step |
| `Component` (`v1alpha1`) | Namespace | Wherever its resources go (dispatched by hub) | One component's rendered resources for one cluster, signed by the hub |

---

## What Changes in v2alpha1

### Definition types: carried forward, restructured

`ComponentDefinition`, `TraitDefinition`, `WorkflowStepDefinition`, `PolicyDefinition`, and `ApplicationDefinition` are all CUE-authored, and all use the same input-schema identifier: **`parameter`** (singular), read at evaluation time as **`context.parameter`**. They differ only in packaging shape, not in that convention:

- **Blueprint shape** (`ComponentDefinition`): a multi-file blueprint referenced via `spec.source` (git/OCI) or `spec.inline`. Input schema lives in `variables.cue` as top-level `parameter`; resources live in `outputs.cue`. See the `ComponentDefinition` example below.
- **Inline-template shape** (`TraitDefinition`, `WorkflowStepDefinition`, and the simpler definition types): a `spec.template:` CUE block with `parameter` and `output` inside it. See the `TraitDefinition`/`WorkflowStepDefinition` examples below.

In both shapes the schema field is `parameter` (singular); it is never pluralised.

### Component is a First-Class CR

`Component` is the contract between the hub, which renders, and vela-agent, which applies, tracks and checks health ([KEP-2.2](../2.2-spoke-controller/README.md)). Controllers produce Components: vela-core for Applications, and cluster management for cluster planes. Users do not create them directly; whether they may later, and in what shape, is left open.

### `outputs: []` Replaces `output:`/`outputs:`

The new unified `outputs` field is a named list with `type` (defaults to `resource`), `value`, and `statusPaths` per entry. It supports `type: component` for nested Component instances.

**Output types:**
- `resource` (default, can be omitted): a Kubernetes object, applied via SSA by vela-agent in the target cluster
- `component`: a nested component; the hub renders it and dispatches it as a `Component` of its own, managed independently

### Annotations as Behaviour Contract

`component.oam.dev/apply-strategy`, `component.oam.dev/ownership`, `component.oam.dev/gc-strategy` replace feature flags, policy-driven apply options, and per-manifest cluster routing labels.

### Definition Authoring as Directories

Like Terraform modules: `metadata.cue` is the required entrypoint, all other files merged via CUE unification; supports inline and OCI source modes.

---

## Definition Authoring: Directory Structure

A `ComponentDefinition` is authored as a directory of CUE files, merged via CUE unification before evaluation. This is analogous to Terraform modules — the directory is the unit of authorship, reuse, and versioning.

```
postgres-database/
  metadata.cue      # required — identifies the directory as a Definition package
  variables.cue     # input schema (OpenAPI v3)
  outputs.cue       # output resources
  exports.cue       # exported values for dependent Components
  workflow.cue      # lifecycle steps (create, upgrade, delete)
  health.cue        # isHealthy expression
```

`metadata.cue` is the required entrypoint. All other files are optional and merged. The controller (and CLI tooling) loads and unifies all `.cue` files in the directory before evaluation.

The `ComponentDefinition` CRD supports two source modes:
- **Directory reference** — points to a git repo path or OCI artifact (primary authoring mode)
- **Inline** — embeds merged CUE directly in the CRD spec (for simple cases or generated/distributed form)

### Example: metadata.cue

```cue
metadata: {
  name: "postgres-database"
}
```

### Example: variables.cue

```cue
// Input schema — validated against Component.spec.properties at admission time
parameter: {
  version: string
  storage: string
  dbName:  string
}
```

### Example: outputs.cue

```cue
// outputs is a list; controller indexes by name for context.status resolution
// type defaults to "resource" — most users never need to specify it
outputs: [
  {
    name: "database"
    // type omitted — defaults to "resource"
    value: {
      apiVersion: "apps/v1"
      kind:       "StatefulSet"
      metadata: name: context.name
      spec: template: spec: containers: [{
        image: "postgres:\(context.parameter.version)"
      }]
    }
  },
  {
    name: "service"
    value: {
      apiVersion: "v1"
      kind:       "Service"
      metadata: name: context.name
    }
  },
  {
    name: "cache"
    type: component       // nested Component instance — full lifecycle managed independently
    value: {
      type: "redis"
      properties: { maxMemory: "256mb" }
    }
  },
]
```

### Example: health.cue

```cue
// Rendered by the hub; evaluated by vela-agent against live resources
isHealthy:
  context.status.database.readyReplicas == context.status.database.replicas &&
  context.status.database.replicas > 0
```

### Example: exports.cue

```cue
// Evaluated after isHealthy is true
// Available to dependent Components via dependsOn.imports
exports: {
  connectionString: "postgresql://\(context.status.service.clusterIP):5432/\(context.parameter.dbName)"
  host:             context.status.service.clusterIP
  port:             5432
}
```

---

## ComponentDefinition CRD

```yaml
apiVersion: core.oam.dev/v2alpha1
kind: ComponentDefinition
metadata:
  name: postgres-database
spec:
  # Option 1: directory reference (git or OCI)
  source:
    oci: "registry.example.com/blueprints/postgres-database:v1.2.0"

  # Option 2: inline merged CUE (generated or simple cases)
  inline: |
    outputs: [...]
    workflow: { ... }
    isHealthy: ...
```

---

## Component CRD

**API version: `core.oam.dev/v1alpha1`.** `Component` is a new kind, not a new version of an existing one, and `v2alpha1` would imply a v2 API that does not exist yet.

Namespace-scoped. One `Component` per component, per target cluster, named `<app>-<component>[-<replica>]-<cluster>-<hash>` in the Application's namespace: the readable parts trimmed to keep the name within 63 characters, and 8 hex characters of a hash over every part. The hub renders it ([KEP-2.3](../2.3-hub-controller/README.md)); vela-agent in the target cluster reconciles it ([KEP-2.2](../2.2-spoke-controller/README.md)).

```yaml
apiVersion: core.oam.dev/v1alpha1
kind: Component
metadata:
  name: shop-backend-prod-use1
  namespace: team-a
  labels:
    app.oam.dev/name: shop
    app.oam.dev/component: backend
    component.oam.dev/type: webservice
    component.oam.dev/cluster: prod-use1
spec:
  target:
    cluster: prod-use1             # refused anywhere else
  revision: 7                      # monotonic; refused if lower than status.observedRevision
  delete: false                    # true marks a signed tombstone (see KEP-2.2, Deletion)
  resources:                       # plaintext index of what will be applied
    - {apiVersion: apps/v1, kind: Deployment, namespace: team-a, name: backend, role: output,
       definition: {kind: Component, type: webservice}, sha256: 9f2c...}
    - {apiVersion: v1, kind: Service, namespace: team-a, name: backend, role: outputs.service,
       definition: {kind: Trait, type: gateway}, sha256: 41ab...}
  apply:                           # apply-time policies as fields
    takeOver: false
    readOnly: false
    applyOnce: {enable: false}
    garbageCollect: {keepLegacyResource: false}
    adoptFrom: Application/team-a/shop   # during a handover: take over that owner's resources in place
  health:                          # evaluated against live resources
    context: {name: backend, appName: shop, appRevision: shop-v3}
    workload:
      healthPolicy: |
        isHealth: context.output.status.readyReplicas == parameter.replicas
      customStatus: |
        message: "Ready: \(context.output.status.readyReplicas)"
      parameter: {replicas: 2}
    traits:
      - type: gateway
        healthPolicy: |
          isHealth: len(context.outputs.ingress.status.loadBalancer.ingress) > 0
  observe:                         # live fields the hub needs back
    - {resource: output, name: readyReplicas, path: .status.readyReplicas}
  postRender:                      # PostDispatch traits, rendered by vela-agent once the workload is healthy
    context: {name: backend, namespace: team-a, appName: shop, appRevision: shop-v3, ...}
    traits:
      - type: expose               # the trait-type label its resources carry
        templates:                 # its own, then what it extends, nearest parent first
          - {name: expose, template: "outputs: service: {...}"}
        parameter: {port: [8080]}  # $(source.x) and $(component.x) already resolved
        labels: {app.oam.dev/name: shop, ...}
        annotations: {...}
        filterAnnotationKeys: [...]  # dropped from every rendered resource, as the hub drops them
  payload:
    secretRef: {name: shop-backend-prod-use1-payload}
    format: gzip+json/v1
    sha256: 5be0...
  signature:
    keyID: hub-2026-11
    value: <Ed25519 signature over the whole spec>
status:
  observedGeneration: 4            # the metadata.generation the conditions describe
  observedRevision: 7
  conditions: [...]                # a stage each, then Ready, Reconciling, Stalled (see KEP-2.2, Status)
  health:                          # common.ApplicationComponentStatus, copied into Application.status.services
    healthy: true
    workloadHealthy: true
    message: "Ready: 2"
    traits: [{type: gateway, healthy: true}]
  observed:                        # the fields spec.observe asked for; sensitive ones sealed, in sealedObserved
    output: {readyReplicas: 2}
  resources:                       # what vela-agent applied and tracks for observedRevision
    - {apiVersion: apps/v1, kind: Deployment, namespace: team-a, name: backend, role: output,
       definition: {kind: Component, type: webservice}}
    - {apiVersion: v1, kind: Service, namespace: team-a, name: backend, role: outputs.service,
       definition: {kind: Trait, type: gateway}}
```

`status.resources` holds what vela-agent rendered from `spec.postRender` as well as what the payload carried.

Each resource names its `definition`: the ComponentDefinition that rendered it (the workload and any auxiliary outputs) or the TraitDefinition. `status.resources` is how tools list a dispatched Application's resources (`vela status --tree`, VelaQL, VelaUX): from the Component, never from the ResourceTrackers in the target cluster, which the hub cannot reach once spokes pull.

### Payload

The rendered resources travel out of the spec, compressed, because rendered `Secret` data would otherwise sit in plain view in a non-Secret object, and because etcd caps an object at about 1.5MiB.

| Phases | Payload | Protected by |
|---|---|---|
| Local and push (cluster-gateway) | A Secret beside the Component, `<component>-payload`, owned by it and overwritten per revision | Secret RBAC in the Component's cluster |
| Before OCM or pull | Encrypted to a key vela-agent holds, carried in `spec.payload` | The key; an OCM `ManifestWork` on the hub only ever holds ciphertext |

The payload's sha256 is in the signed spec, so a swapped or edited payload fails verification. A compressed payload over about 900KiB is refused at dispatch, naming the component and its size.

### PostDispatch traits

A PostDispatch trait renders against the live workload once it is healthy (an `expose` reading the Deployment it fronts, say). Rendering it on the hub would mean a second signed revision per placement, and another every time the status it reads moves. So the hub ships the trait instead, in `spec.postRender`, and vela-agent renders it against the workload it applied:

| Field | What it holds |
|---|---|
| `context` | The component's render context, without `output` and `outputs`, which vela-agent fills from the workload with its live status and the component's other outputs |
| `namespace` | A topology override's namespace, which every rendered resource goes to; unset, a resource that names none goes to the context's |
| `traits[].templates` | The trait's own template, named as the Application names the trait, then those of the definitions it extends |
| `traits[].parameter` | The trait's parameters, with `$(source.x)` and `$(component.x)` resolved at dispatch |
| `traits[].labels`, `annotations` | What the hub stamps on a trait's output, so a rendered resource carries the same metadata either way |
| `traits[].filterAnnotationKeys` | The annotations the hub drops from a trait's output (the defaults and `app.oam.dev/filter-annotation-keys`), dropped even when the template sets them |

vela-agent finishes each output with the same functions the hub uses: the labels, the name of an output its template left unnamed, the annotations and the namespace, which a `Namespace` never gets.

vela-agent renders only the CUE standard library. A PostDispatch trait that imports a provider package, declares `processing`, is `controlPlaneOnly` or `podDisruptive`, sets `workloadRefPath`, or is patched by an override policy is refused at dispatch, naming the trait.

### Signing

The hub signs the whole spec: target, revision, tombstone flag, resource index, apply options, health, status paths, post render, and the payload reference and digest. vela-agent verifies before it applies anything. Keys, trust distribution and rotation are in [KEP-2.5](../2.5-credential-model/README.md).

---

## TraitDefinition CRD

```yaml
apiVersion: core.oam.dev/v2alpha1
kind: TraitDefinition
metadata:
  name: apply-once
spec:
  phase: default                   # pre | default | post  (default if omitted)
  template: |
    patch: outputs: [ for o in context.outputs if list.Contains(parameter.selector.resourceTypes, o.value.kind) {
      name: o.name
      metadata: annotations: "component.oam.dev/apply-strategy": "once"
    }]
```

---

## WorkflowStepDefinition CRD

Executed on the hub by the Application workflow. A step that needs a component's live state reads it through `statusPaths`.

```yaml
apiVersion: core.oam.dev/v2alpha1
kind: WorkflowStepDefinition
metadata:
  name: register-service
spec:
  template: |
    #do: "http-request"
    method: "POST"
    url:     "http://\(parameter.registry)/register"
    body: {
      name:     context.name
      endpoint: context.status.service.clusterIP
    }
```

---

## CUE Context

The same `context` object is available across all CUE evaluation phases. Rendering runs on the hub; `context.status` comes from the live fields each Component reports in `status.observed`.

```cue
context: {
  name:       string
  namespace:  string
  parameter: { ... }        // input properties; mutated by pre traits; imports injected here

  // available during default trait phase and later:
  outputs: [...]            // rendered output list

  // available after dispatch and health check:
  status: {
    [outputName]: {
      [fieldName]: _        // from Component.status.observed, per the output's statusPaths
    }
  }
}
```

---

## Behaviour Annotations

Traits set well-known `component.oam.dev/` annotations on output resources. vela-agent reads these when applying resources. (Note: these annotation keys retain the `component.oam.dev/` prefix as stable, well-known annotation names; they are not API group references.)

| Annotation | Values | Effect |
|---|---|---|
| `component.oam.dev/apply-strategy` | `once`, `always` | Control re-apply behaviour |
| `component.oam.dev/ownership` | `read-only`, `take-over`, `shared` | Resource ownership semantics |
| `component.oam.dev/gc-strategy` | `retain`, `delete` | Garbage collection on Component deletion |

Note: `component.oam.dev/force-local` is no longer needed: vela-agent always runs in the cluster its resources go to.

---

## Status & Health

### statusPaths

`statusPaths` declared on each output name the live fields that rendering, `isHealthy`, `exports` and workflow steps read back:

```cue
{
  name: "database"
  value: { ... }
  statusPaths: [
    { name: "readyReplicas", path: ".status.readyReplicas" },
    { name: "replicas",      path: ".status.replicas" },
  ]
}
```

The hub copies them into `Component.spec.observe`. vela-agent reads each resource in its own cluster and writes the values to `Component.status.observed` (sensitive ones sealed, in `status.sealedObserved`), which the hub reads as `context.status.database.readyReplicas` and so on.

### isHealthy

The hub renders the health expression into `Component.spec.health`; vela-agent evaluates it against live resources:

```cue
isHealthy:
  context.status.database.readyReplicas == context.status.database.replicas &&
  context.status.database.replicas > 0
```

The result is `Component.status.health`, which the hub reads to track Component health.

---

## Exports & Dependencies

`exports` are evaluated on the hub once the Component reports healthy, from the values in `Component.status.observed`. Their inputs reach the hub through `statusPaths`.

The hub injects imported values into dependent Components before rendering them. Dependent Components receive their imports in `context.parameter`, transparent to the Definition template.

For OCM, `statusPaths` map onto `ManifestWork` feedback rules, so the hub reads `status.observed` from `ManifestWork` status.

---

## Upgrade & Drift

- **Drift correction**: vela-agent continuously reconciles diverged resources, whether or not the hub is reachable
- **Re-render**: on the hub, when the Application, its definitions or its inputs change; a changed render is dispatched as the next `revision`
- **Rollback**: revert the Application on the hub; the hub renders and dispatches the older state as a new, higher revision
- **Revision tracking**: `Component.status.observedRevision` is the last revision verified and applied
- **Version skew**: upgrade vela-agent before the hub. An older vela-agent prunes a spec field it does not know, so the signature no longer matches and nothing is applied (`BadSignature`): it fails closed, never half-applied

No `ComponentDefinitionRevision` CRD needed. Definition versioning is managed at the source level (git, OCI) and pinned via `ComponentDefinition.spec.source`.

---

## Cross-KEP References

- **`from*` family** (`fromParameter`, `fromSource`, `fromDependency`) — render-time value resolution directives that operate inline within `properties`. These are cross-KEP concerns spanning KEP-2.3 (hub), KEP-2.16 (`fromSource`), and KEP-2.17 (`fromDependency`). Not duplicated here; see those KEPs.
- **vela-agent pipeline**: what happens to a Component once it lands is defined in [KEP-2.2](../2.2-spoke-controller/README.md).
- **Hub reconcile pipeline**: how the hub renders, signs and dispatches Components is defined in [KEP-2.3](../2.3-hub-controller/README.md).
- **Signing and keys**: the hub signing key, trust distribution and rotation are defined in [KEP-2.5](../2.5-credential-model/README.md).
- **Dispatcher implementations** — local, cluster-gateway, and OCM Dispatcher types are detailed in [KEP-2.4](../2.4-dispatchers/README.md).
