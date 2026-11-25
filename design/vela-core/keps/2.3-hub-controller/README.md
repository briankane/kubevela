> ⚠️ **Early concept draft.** This KEP is an early-stage exploration. It is **incomplete and may be inaccurate**, its direction is unsettled, and it should not be relied upon for implementation or as a description of committed behaviour. Expect substantial change.

# KEP-2.3: Hub Application-Controller Integration

**Status:** Drafting (Not ready for consumption)
**Parent:** [vNext Roadmap](../README.md)

---

## Overview

KEP-2.3 defines the hub application-controller's side of Component dispatch: how it renders each component, packages and signs the result as a `Component`, delivers it, tracks it, and reads its status back to drive the workflow and the Application's status.

---

## Hub: application-controller

- Parses Applications, applies policies, runs workflows and renders definitions, as today
- Packages each rendered component, per target cluster, as a signed `Component` ([KEP-2.1](../2.1-core-api/README.md))
- Delivers Components to their cluster through a Dispatcher ([KEP-2.4](../2.4-dispatchers/README.md))
- Tracks the Components it dispatched in the Application's `ResourceTracker`, and garbage-collects them with signed tombstones
- Reads `Component.status` for orchestration (workflow steps, dependsOn) and for `Application.status`

---

## Architectural Principle

**Render where the inputs are; apply where the resources are.** Workflows, policies, configs, `fromSource` and hub Secrets live on the hub, so rendering stays there. Applying, drift correction, garbage collection and health polling are the work that never ends, and doing them from the hub through cluster-gateway is what costs. They move to vela-agent in the target cluster ([KEP-2.2](../2.2-spoke-controller/README.md)).

```
Hub application-controller
  ├─ parse, policies, workflow, render            (unchanged)
  ├─ sign + package ──► Component ──► vela-agent (target cluster)
  │                                     ├─ apply, drift correction
  │                                     ├─ ResourceTracker + GC
  │                                     └─ health, observe ──► Component.status    
  └─ reads Component.status ──► workflow, dependsOn, Application.status
```

The application-controller's programming model does not move. What changes is who applies and who checks health.

---

## Effective Authority Is Bounded by the Spoke, Not by the Grant

The hub's RBAC on each spoke narrows to `Component` CRs and their payload Secrets. That improves accidental scope and audit legibility: a reviewer can see at a glance what the hub is permitted to do, and a bug in hub code cannot write a ClusterRoleBinding.

**It is not a reduction in what an attacker can do.** A credential that can create `Component` CRs is exactly as powerful as whatever vela-agent will apply on its behalf, and the spoke applies whatever the rendered definition emits. Narrow grant, unchanged blast radius.

What actually bounds authority is the spoke refusing things: a GVK allowlist, namespace scoping, and a policy over what a rendered Component may apply. Until that exists, "minimal RBAC, only needs permission to create/watch Component CRs" describes the shape of the grant rather than its power, and the phrase will be quoted in a security review as though the narrow grant were the protection.

This matters for sequencing, because the steps are independently deliverable and only one of them is security:

| Step | Buys | Security? |
|---|---|---|
| 1. Ship vela-agent and `Component` CR | status fidelity, partition tolerance | no |
| 2. Narrow the hub's RBAC to Components | audit legibility, accidental scope | no |
| 3. Add the spoke's apply policy | **bounded authority** | **yes** |
| 4. Optionally revisit pull-based delivery | removes the hub credential entirely | marginal, given 3 |

The hub signature ([KEP-2.5](../2.5-credential-model/README.md)) does not change this either: it proves a Component came from the hub, not that the hub should have asked for it.

Steps 1 and 2 are worth doing on their own merits, and each is useful without the next. Nobody should declare the credential problem solved after step 2.

Step 4 is not free with today's transport. cluster-gateway proxies with an upgrade-aware handler, so watches and streaming already pass through. But konnectivity's agent only ever *receives* `DIAL_REQ` and never sends one, so the tunnel cannot be reversed to let a spoke initiate connections to the hub. Pull needs its own channel and its own registration flow, not a reuse of the existing one.

---

## Hub Reconcile Pipeline

```
1. Parse the Application, apply policies, run the workflow          (unchanged)
2. deploy / apply-component step, per component per target cluster:
   a. render workload and traits; ship PostDispatch traits in spec.postRender (see below)
   b. resolve apply-time policies into spec.apply
   c. render health into spec.health; collect what to observe into spec.observe
   d. write the payload Secret, then the Component, signed, at the next revision
3. Record the Component in the Application's ResourceTracker
4. Read Component.status: observedRevision, health, outputs
   - step completes when observedRevision == revision and health is healthy
   - status.health ──► Application.status.services
5. Components no longer wanted: signed tombstone, then delete
```

Only `deploy` and `apply-component` dispatch Components. `apply-object`, `read-object` and other raw-resource steps keep running in-process, since there is no component to name or check health on.

---

## Opt-in and Handover

Component dispatch is behind a feature gate (off by default) and, per Application, an annotation (`app.oam.dev/dispatch: component`). One Application can move across without moving every Application.

An existing Application that takes the annotation **hands over** its resources: each new Component adopts the resources its own Application already tracks, ownership labels move to the Component, and the resources leave the Application's tracker without being deleted or recreated. Removing the annotation hands them back the same way.

A change of dispatch mode is a new ApplicationRevision, so the workflow runs again in the new mode. Trackers follow the Application's generation, which an annotation does not move, so both owners' records can sit in one tracker; the hub finishes the handover explicitly:

| Direction | While the workflow runs | Once it succeeds |
|---|---|---|
| To Components | Each Component carries `spec.apply.adoptFrom: Application/<ns>/<app>`, and vela-agent takes the resources over in place | The Application forgets the records of resources its Components now control, and collects any no Component carries, as resources no longer rendered. It then dispatches each Component again without `adoptFrom`, so outside a handover vela-agent refuses the Application's resources |
| Back to the Application | The in-process apply adopts from that component's Components | Once the component is healthy, payload Secrets are deleted, and each Component goes through its tombstone; vela-agent finds the resources are no longer its own and leaves them |

The hub finishes what it safely can before every state keep, a suspended workflow's included, since forgetting only drops records of resources the other owner already controls. The Application's `Handover` condition is `False` (`InProgress`) while a handover has work left, naming what it waits on (vela-agent adopting, the component's health, a tombstone), and `True` (`Complete`) once it has none.

---

## Hub Dispatcher Interface

Delivery is where a Dispatcher ([KEP-2.4](../2.4-dispatchers/README.md)) plugs in: its templates choose targets and the shape written for each, and the backend below writes it and reads status back. The backend only needs to deliver a `Component` (and its payload) and read its status back.

```go
// Dispatcher delivers a Component and its payload to the Component's target cluster.
type Dispatcher interface {
    Dispatch(ctx context.Context, component *Component, payload *corev1.Secret) error
    Status(ctx context.Context, component *Component) (*ComponentStatus, error)
    Delete(ctx context.Context, component *Component) error
}
```

---

## Rendering

Rendering is today's: the hub resolves each component's `ComponentDefinition` and `TraitDefinition`s, renders them with the Application's context, and gets back the workload and trait resources. The result is packaged rather than applied:

| Rendered | Goes to |
|---|---|
| Workload and trait resources | Compressed into the payload; identities and sha256 into `spec.resources` |
| Apply-time policies (`garbage-collect`, `apply-once`, `take-over`, `read-only`, `shared-resource`, `resource-update`) | `spec.apply` |
| Health, customStatus and details CUE, their parameters and context | `spec.health` |
| PostDispatch traits: templates, resolved parameters, metadata and context | `spec.postRender`, rendered by vela-agent |
| Live fields that `$(component.<name>)` reads and exports need | `spec.observe` (sensitive ones sealed) |

`topology`, `override`, `replication`, `env-binding` and `debug` are resolved here and never reach the Component.

Health that today is special-cased in Go (Terraform's `Configuration` status) is rendered as health CUE, so vela-agent has no type-specific code.

### PostDispatch traits

A PostDispatch trait reads the live workload, so the hub does not render it: it ships the trait in `spec.postRender` with the rest of the revision, and vela-agent renders it once the workload is healthy ([KEP-2.2](../2.2-spoke-controller/README.md#postdispatch-traits)). The hub resolves what only it can read first: `$(source.x)` and `$(component.x)` in the trait's parameters, the `extends` chain, and the labels and annotations it would stamp. A later change to a component read reaches the trait with the next revision, where an in-process Application re-reads it on every reconcile.

What vela-agent cannot render as the hub would is refused at dispatch, naming the trait:

| Refused | Because |
|---|---|
| A template importing anything but the CUE standard library, or declaring `processing` | Providers run on the hub |
| `controlPlaneOnly` | Its resources live on the hub, away from the workload it reads |
| `podDisruptive` | It changes the signed workload |
| `workloadRefPath` | The hub writes the reference during its own render |
| An override policy patching trait outputs | The patch is applied during the hub's render |
| Another component reading one of its outputs | The hub observes only the resources it rendered |

A component is dispatched again only when the Application's current revision has not yet recorded its Components and payloads, as an in-process component is applied again only when its revision's tracker lacks its resources. A change that cuts no revision, such as an annotation, dispatches nothing.

### Component reads

The hub no longer reads live objects, so `$(component.<name>)` reads in later components and workflow steps go through what the producer's Component reports: each read's path becomes an observation on the producer's Component, and a read of a Secret (or a `+sensitive` path) is sealed, so only the hub reads it. A read waits while vela-agent has not reported on the producer's current revision.

---

## Tracking

The Application's `ResourceTracker` records the Components it dispatched, per cluster. Each Component's own tracker records the resources vela-agent applied.

| Change | Hub action | Result |
|---|---|---|
| A component leaves the Application | Tombstone, then delete its Components | Their resources are garbage-collected |
| A cluster leaves the topology | Tombstone, then delete the Component for that cluster | Same, on that cluster |
| The Application is deleted | Tombstones for every Component | Same, everywhere |

The delete waits on vela-agent: the hub holds it until the tombstone's revision is observed, which vela-agent reports only once it has collected everything. A cluster that never answers (no agent, or unreachable) keeps its Component and its resources, and the Application's `Deleting` condition and a `WaitingForTombstone` event name the Component and cluster it waits on. Nothing is orphaned silently; an operator who gives up on a cluster removes the Component by hand, which leaves its resources in place.

A Component's name is `<app>-<component>[-<replica>]-<cluster>-<hash>`, the readable parts trimmed so the name stays a valid label value (vela-agent labels what it applies with it), and the hash over the Application's namespace and name, the component, replica key and cluster, so dash-joined parts that read the same still name different Components. A hub control-plane Component puts the placement's cluster and namespace in place of the cluster and ends `-cp`. The hub still refuses to update a Component whose labels name a different Application, component or cluster.

Tooling that walks the Application's tracker to list resources (`vela status --tree`, prism's `ApplicationResourceTracker`, VelaUX resource views) will find Components there, and must follow each into its resources. `vela status` and `Application.status.services` work unchanged, since they come from `Component.status.health`.

---

## Trait Injection from Policies

**Policies inject traits** — policies are purely application-layer concerns; pure functions over application state that attach traits to matching Components before dispatch.

**Policies never reach the spoke** — the Component controller only sees traits, regardless of whether they were user-declared or policy-injected.

The three trait phases:

| Phase | Input | Can do |
|---|---|---|
| `pre` | `context.parameters` | Mutate input properties; override dispatcher |
| `default` | Full context except `context.status` | Patch existing outputs; add new outputs |
| `post` | Full context including `context.status` | Dispatch additional resources after health confirmed |

---

## Dispatching Component CRs

One `Component` per component, per target cluster. The full shape is in [KEP-2.1](../2.1-core-api/README.md#component-crd).

> **On the name.** `Component` is free at the API level: no `Component` kind is registered under `core.oam.dev` and no such CRD ships today; `component_types.go` holds only constants, and the inline type in `spec.components[]` is `ApplicationComponent`. The ambiguity is in prose rather than in the API: "component" will mean both an entry in `spec.components[]` and a dispatched CR, and the two are one-to-many (one per cluster) but not the same object. `ComponentWork` was considered and rejected as needless once the collision turned out not to exist.

The payload Secret is written before the Component. A reader that sees the new Secret under the previous spec fails the digest check and retries.

---

## Watching Component.status

The hub reads `Component.status` through the Dispatcher. For cluster-gateway that is a direct read of the Component through the proxy; for OCM, `ManifestWork` feedback rules carry `observedRevision`, `health` and `status.observed` back.

The hub reads it again on the Application's next reconcile. While vela-agent has yet to report a Component healthy, the hub renders the Application no more often than `--component-agent-render-interval` (default 5s), with the workflow's backoff above it, since every render costs the whole Application. Between renders it reads only the Components it waits on, every `--component-agent-health-interval` (default 1s), and renders as soon as one changes, so the Application reports running about a second after vela-agent does. Any change to the Application itself renders at once. Once the backoff passes the render interval, the backoff alone paces it, so a cluster whose vela-agent is down is not read every second.

The hub stamps each Component revision it writes with `component.oam.dev/dispatched-at`. If vela-agent has not applied that revision within `--component-reconciler-timeout` (default 2m), the Application says so by cluster, in the component's service message, the deploy step's message and a `ComponentNotApplied` event, rather than waiting silently: "waiting since T for anything on cluster X to reconcile Component ns/name: is vela-agent running there?" when `observedRevision` is empty, or "waiting since T for vela-agent on cluster X to apply revision N of Component ns/name (last applied M)" when it is behind. T is when the revision was written, so the report stays the same until something changes and its events aggregate. The Application keeps waiting, so an agent that comes back carries on with no manual step.

---

## Application.status.placements Model

The hub maintains `Application.status.placements` — a map from component name to the set of clusters the Component was dispatched to, along with per-cluster health status:

```yaml
status:
  placements:
    my-postgres:
      - cluster: prod-cluster-1
        namespace: team-a
        healthy: true
    my-cache:
      - cluster: prod-cluster-1
        namespace: team-a
        healthy: true
```

This is populated from `Component.status.health` as read through the Dispatcher.

---

## fromDependency Hub-Mediated Resolution and Ordering

`fromDependency` wires a component's input properties to an upstream component's exported values. The hub mediates this:

1. The hub dispatches upstream components first (determined by the dependency graph)
2. The hub watches the upstream `Component.status.health`
3. Once healthy, the hub evaluates the upstream's `exports` from its `Component.status.observed`
4. The hub injects the imported values into the downstream Component's `spec.properties` (replacing the `fromDependency` directives)
5. The hub dispatches the downstream Component

Dependent Components receive their imports in `context.parameters` — transparent to the Definition template.

For OCM, the `statusPaths` behind those exports are `ManifestWork` feedback rules on `Component.status.observed`.

**Schema validation at admission:** The `from*` family is validated at admission time — the hub admission webhook resolves both the consuming component's `parameter{}` schema and the supplying Definition's `exports{}` schema and validates type compatibility between the two. See the cross-KEP section below.

---

## ApplicationRevision Snapshot

When an `ApplicationRevision` is created, the hub resolves and snapshots all Definition definitions the Application was rendered against. The snapshot set in vNext includes:

| Definition type | Purpose |
|---|---|
| `ComponentDefinition` | Component rendering and exports schema |
| `TraitDefinition` | Trait pipeline rendering |
| `WorkflowStepDefinition` | Workflow step execution |
| `PolicyDefinition` | Policy rendering |
| `SourceDefinition` | Source resolution schema and CueX template |

Re-renders and rollbacks of a historical `ApplicationRevision` always use the snapshotted Definition versions — not whatever is currently installed in the cluster. For `SourceDefinition` this is particularly important: the `output{}` schema in the snapshot determines the `ConfigTemplate` version used for cache lookups, ensuring cache correctness across controller restarts and Definition upgrades.

---

## Application Parameters & fromParameter

Applications support an optional `spec.parameters` map — a free-form set of values that represent application-level constants shared across components, traits, policies, and operations. This avoids duplicating cross-cutting values (regions, tier names, cost centres) across every component's properties.

### Templated Applications

When an Application is created from an `ApplicationDefinition`, `spec.parameters` is validated against the Definition's `parameter{}` schema at admission time. The Application controller renders the Application spec from the Definition template with `parameter` values injected — component properties, trait properties, and policy properties are all CUE-rendered, so `parameter.*` references resolve naturally.

### Non-Templated Applications

Non-templated Applications also accept `spec.parameters` as a free-form map. Rather than a CUE render pass, property values may reference parameters via `fromParameter` — an explicit substitution directive that sits inline within `properties`:

```yaml
apiVersion: core.oam.dev/v1beta1
kind: Application
spec:
  parameters:
    region: us-west-2
    tier: production
    enableMultiAz: true

  components:
    - name: my-db
      type: aws-rds
      properties:
        # regular value — no substitution
        instanceClass: db.t3.medium
        # fromParameter resolves before Definition rendering
        region:
          fromParameter: region
        multiAz:
          fromParameter: enableMultiAz

    - name: my-cache
      type: elasticache
      properties:
        region:
          fromParameter: region
        tier:
          fromParameter: tier
```

The Application controller resolves all `fromParameter` references before handing properties to the Definition renderer. If a referenced key is absent from `spec.parameters`, the controller surfaces a validation error at reconcile time with a clear message identifying the missing key.

`fromParameter` is valid within `properties` at any depth — including nested objects and array entries. It is not valid as a map key, only as a value.

### Operations

`spec.parameters` is available in Operations via `context.appParams`. For templated Applications with a known schema, `OperationDefinition` authors may declare `requiredAppDefinition` to rely on specific keys safely. For non-templated Applications, authors should use CUE defaults (`context.appParams.region | "us-east-1"`) or declare dependencies explicitly in the Operation's own `parameter{}` schema.

`fromParameter` is not available in `OperationDefinition` templates — Operations use `context.appParams` directly.

---

## The `from*` Family — Render-Time Resolution & Schema Validation

The `from*` family (`fromParameter`, `fromSource`, `fromDependency`) is fully specified in [KEP-2.21](../2.21-from-resolution/README.md). The hub application-controller is the execution point for the resolution model described there.

The hub's specific responsibilities in this area:

- Executing the single-pass recursive resolution for `fromParameter` and `fromSource` at render time
- Deferring and re-queuing components whose `fromDependency` references are not yet resolvable (upstream not yet healthy)
- Running admission-time schema validation via the hub admission webhook for all three directive types
- Topologically ordering component dispatch based on the `fromDependency` graph

---

## ApplicationDefinition Integration

When an Application references an `ApplicationDefinition` via `spec.definition`, the hub:

1. Resolves the `ApplicationDefinition`
2. Evaluates the CUE template with the provided `spec.parameters`
3. Gets back a fully-formed Application spec
4. Reconciles the resolved spec exactly as if the developer had written it by hand

The Application CR becomes minimal — just a definition reference and parameters:

```yaml
apiVersion: core.oam.dev/v1beta1
kind: Application
metadata:
  name: my-app
  namespace: team-a
spec:
  definition: web-service
  parameters:
    image:    "myapp:v1.2.0"
    replicas: 3
    domain:   "myapp.example.com"
    dbSize:   "20Gi"
```

> **Cross-KEP note:** `ApplicationDefinition` (App Templates) is detailed in [KEP-2.9](../2.9-app-templates/README.md).

---

## Hub / Spoke Architecture Summary

```
Hub application-controller
  ├─ renders, signs, dispatches Component (local, cluster-gateway push, or OCM)
  │    └─ vela-agent in the target cluster
  │         ├─ verifies signature and payload digest
  │         ├─ applies, corrects drift
  │         ├─ manages ResourceTracker + GC (on signed tombstones)
  │         └─ writes Component.status: health, outputs
  └─ reads Component.status ──► workflow, dependsOn, PostDispatch traits, Application.status
```

### Why the OCM Problem Is Resolved

OCM wraps a single `Component` in a `ManifestWork`. vela-agent does the applying and health evaluation locally. OCM feeds back a handful of declared fields from `Component.status` (`observedRevision`, health, and the `statusPaths` outputs). No per-resource status path declarations and no push proxies. Before OCM delivery, the payload moves from a Secret into the Component, encrypted, so the `ManifestWork` stored on the hub only ever holds ciphertext ([KEP-2.1](../2.1-core-api/README.md#payload)).

---

## Cross-KEP References

- **API types** — `Component`, `ComponentDefinition`, `Dispatcher` CRDs — [KEP-2.1](../2.1-core-api/README.md).
- **vela-agent**: what happens after a Component lands, [KEP-2.2](../2.2-spoke-controller/README.md).
- **Dispatcher implementations** — local, cluster-gateway, OCM — [KEP-2.4](../2.4-dispatchers/README.md).
- **Credential model**: hub signing key, trust distribution, rotation, [KEP-2.5](../2.5-credential-model/README.md).
- **App Templates (`ApplicationDefinition`)** — [KEP-2.9](../2.9-app-templates/README.md).
- **fromSource** — SourceDefinition and lazy render-scoped resolution — [KEP-2.16](../2.16-source-definition/README.md).
- **fromDependency** — Component exports and dependency graph — [KEP-2.17](../2.17-component-exports/README.md).
