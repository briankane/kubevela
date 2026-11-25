> ⚠️ **Early concept draft.** This KEP is an early-stage exploration. It is **incomplete and may be inaccurate**, its direction is unsettled, and it should not be relied upon for implementation or as a description of committed behaviour. Expect substantial change.

# KEP-2.2: vela-agent

**Status:** Drafting (Not ready for consumption)
**Parent:** [vNext Roadmap](../README.md)

---

## Overview

KEP-2.2 defines vela-agent, the controller that reconciles `Component` CRs in the cluster it runs in. It verifies each Component's signature, applies the rendered resources the Component carries, keeps them converged, tracks them in a `ResourceTracker`, garbage-collects what drops out, evaluates health, and writes status back onto the Component.

It renders one thing only: PostDispatch traits, which read the live workload, from templates the hub signs into the Component (see [PostDispatch Traits](#postdispatch-traits)). The hub application-controller parses the Application, applies policies, runs the workflow and renders everything else, as it does today (KEP-2.3). What vela-agent takes over is the never-ending part: apply, drift correction, garbage collection and health polling.

---

## Where Rendering Happens: the Hub

Earlier drafts of this KEP had the spoke render from hub-shipped definition snapshots. That is settled the other way.

| | Spoke renders (rejected) | Hub renders (adopted) |
|---|---|---|
| Workflows and hub-only inputs (configs, `fromSource`, hub Secrets) | Must move to the spoke, or be packaged and shipped | Stay where they are |
| CUE engine and provider versions | Must match across the fleet | One place |
| Definition governance | Spoke needs the definition set; versions drift | Central, no skew |
| Hub compute | Distributed | Rendering stays on the hub; apply, drift and health polling move off it |
| Payload | Parameters and a definition snapshot | Rendered resources, kept private (see [KEP-2.1](../2.1-core-api/README.md#payload)) |
| Partition behaviour | Spoke keeps rendering | Spoke keeps reconciling what it last received |

The deciding row is the first. Workflows are designed to run on the hub and many of their inputs only exist there; moving rendering would drag all of that across. The traffic that actually hurts today is the apply and health polling through cluster-gateway, and that is what moves.

---

## Keep the Controller Source-Agnostic

**vela-agent is a local controller over `Component` CRs in its own cluster, and nothing else.** It watches, verifies, applies, tracks, evaluates health, and writes status back onto the object. It has no opinion about how those objects arrived.

That is what lets delivery change underneath it without a rewrite. Three things would break it:

- an inbound endpoint the hub calls, rather than the controller watching its own API
- status reported anywhere other than the local object's status field
- cluster context injected by the hub that only makes sense because the hub wrote it

The third is the one most easily done by accident.

---

## Delivery Phases

The controller is the same in every phase. What changes is where the `Component` is written and who writes it.

| Phase | Component lives on | Written by | Reconciled by |
|---|---|---|---|
| 1. Local | The hub, for components targeting the hub itself | vela-core | vela-agent, running inside vela-core |
| 1. Push (with a stand-in) | The spoke, for remote targets | vela-core, through cluster-gateway | vela-core on the spoke, with only vela-agent enabled |
| 2. Push | The spoke | vela-core, through cluster-gateway | vela-agent, as its own binary |
| 3. Pull | The spoke | The spoke fetching its own (OCM work agent, or the agent pulling) | vela-agent |

```
HUB (vela-core)                                       SPOKE
Application ─► parse ─► policies ─► workflow
                                      │
                                    render
                                      │
                           sign + package ──► Component ──────► vela-agent
                                      │   (local, gateway push,     ├─ verify signature + payload digest
                                      │    or pulled)               ├─ apply (+ drift correction)
                                      │                             ├─ ResourceTracker / GC
                                      │                             └─ health, observe   
                                      │                                        │
            deploy step, status ◄─────┴──── reads Component.status ◄───────────┘
```

No KubeVela component writes to the hub from a spoke. The hub reads `Component.status` back through cluster-gateway; under OCM, the work agent carries it back as `ManifestWork` status. That is OCM's transport, not ours, and it carries only what the hub asked for.

---

## Reconcile Pipeline

```
1. Verify: signature over the whole spec, with a trusted hub key (see KEP-2.5)
2. Verify: spec.target.cluster names this cluster
3. Verify: spec.revision >= status.observedRevision
4. If spec.delete (tombstone): garbage-collect everything tracked, release the finalizer, stop
5. Read the payload, check its digest against spec.payload.sha256, decode
6. Check every decoded resource against spec.resources (identity and sha256), and that none names a cluster
   (`app.oam.dev/cluster`): a Component's resources apply only where it is delivered
7. Apply through the ResourceKeeper, honouring spec.apply
8. Evaluate health from spec.health against live resources
9. Once the workload is healthy, render spec.postRender against it and apply the result the same way
10. Garbage-collect resources tracked by earlier revisions and absent from this one, once step 9 has rendered
11. Copy spec.observe from live resources into status.observed, sealing sensitive values into status.sealedObserved
12. Write status: observedRevision, conditions, health, outputs
```

Any verification failure stops the reconcile before anything is applied, sets `Verified=False` and `Stalled=True` (see [Status](#status)) and emits a warning event. There is no fail-open mode.

Between hub updates the controller re-runs steps 7 to 12 on its own resync, so drift is corrected and health stays current whether or not the hub is reachable.

---

## The Component It Reconciles

The full type is in [KEP-2.1](../2.1-core-api/README.md). What the controller relies on:

| Field | Used for |
|---|---|
| `spec.target.cluster` | Refusing Components addressed elsewhere |
| `spec.revision` | Refusing replays of older revisions |
| `spec.delete` | The signed tombstone that releases garbage collection |
| `spec.resources` | The plaintext index of what will be applied, each with a sha256 |
| `spec.payload` | Where the rendered resources are, their format and digest |
| `spec.apply` | Apply-time behaviour: take-over, read-only, apply-once, shared resources, resource update, GC |
| `spec.health` | Health, customStatus and details CUE for the workload and each trait, with the render-time context and parameters they read |
| `spec.observe` | Fields the hub needs back from live resources, some of them sealed |
| `spec.signature` | Key ID and signature over all of the above |

---

## Apply Behaviour

The apply-time policies of a v1 Application are fields of `spec.apply`. The hub resolves them from the Application's policies at render time; policies themselves never reach the controller.

| v1 policy | `spec.apply` |
|---|---|
| `garbage-collect` | `garbageCollect` |
| `apply-once` | `applyOnce` |
| `take-over` | `takeOver` |
| `read-only` | `readOnly` |
| `shared-resource` | `sharedResource` |
| `resource-update` | `resourceUpdate` |

`topology`, `override`, `replication`, `env-binding` and `debug` are resolved on the hub and never reach the controller.

Shared resources are keyed by owner, so two Components (from one Application or two) may share a resource, and the last owner to let go deletes it.

`apply.adoptFrom` names an owner key whose resources this Component takes over in place, with no delete or recreate. The hub sets it on the revisions of a Component that replaces an Application's in-process apply, until the Application has forgotten what the Component adopted (KEP-2.3, Opt-in and Handover). Being in the spec, it is signed like everything else.

---

## ResourceTracker

Each Component owns its own trackers, through the same library the Application controller uses with a different owner.

```
HUB                                        COMPONENT'S CLUSTER
Application                                Component shop-backend-prod-use1 (rev 7)
  └─ ResourceTracker                         └─ ResourceTracker
       ├─ Component shop-backend-prod-use1        ├─ Deployment team-a/backend
       ├─ Component shop-backend-prod-euw1        ├─ Service    team-a/backend
       └─ Component shop-frontend-prod-use1       └─ Secret     team-a/backend-db
```

| Tracker | Tracks | Garbage-collects when |
|---|---|---|
| Application's (hub) | The Components it dispatched, per cluster | A component leaves the Application, or a cluster leaves its topology |
| Component's | The resources the controller applied | A resource drops out between revisions, or a tombstone arrives |

A real tracker rather than an inventory in `Component.status`: status is not durable (backup and restore tools drop it, and by convention it is rebuildable), and this record decides what gets deleted. Versioned trackers also carry the history that `keepLegacyResource` and shared resources depend on.

Component trackers carry `owner.oam.dev/{kind,name,namespace,uid}` labels and **not** `app.oam.dev/name`, so a same-named Application never adopts them. They are found by label, never by name.

---

## Deletion

A Component disappearing is not enough to garbage-collect its resources: the controller cannot tell a hub-driven removal from a `kubectl delete`, and with broad apply rights behind it, an unsigned delete would be a way to tear down anything it manages.

Deletion is two steps:

1. The hub writes a **tombstone**: the next revision, `spec.delete: true`, signed as usual.
2. The controller garbage-collects everything the Component applied, and only then sets `status.observedRevision` to the tombstone's revision (`Applied=False`, reason `Deleting` while it collects, `Deleted` once done).
3. The hub sees the tombstone observed and removes the Component.

The controller holds a finalizer and garbage-collects only after verifying a tombstone. A Component deleted without one keeps its resources in place, releases its finalizer, and raises a warning event naming what was left behind.

---

## Health

Health is evaluated where the resources are, against live objects, using the health CUE the hub rendered into `spec.health`. It is plain CUE with no providers: the same evaluation the Application controller runs today, extracted so both share one implementation.

The result is written to `Component.status.health` in the component status shape (`common.ApplicationComponentStatus`), so the hub copies it into `Application.status.services` as is.

Health that today is special-cased in Go (Terraform's `Configuration` status) is expressed as health CUE by the hub instead, so the controller has no type-specific code.

vela-agent does not watch what it applied, so it evaluates a Component that is not ready again on a timer: as long again as it has been not ready at this revision, from a second up to 10s (`HealthRecheck`). A workload that turns ready a moment after it is applied is seen a moment later, and one that takes minutes is evaluated every 10s, not every second. A ready Component is evaluated again on the resync.

---

## Status

A Component says how far its revision got, one condition per stage, in the order it passes them:

| Stage | `True` when |
|---|---|
| `Verified` | Signature, target cluster, revision and payload check out |
| `Applied` | The payload is applied and tracked |
| `WorkloadHealthy` | The workload's health policy passes |
| `PostRendered` | PostDispatch traits are rendered and applied (`NoneDeclared` with none) |
| `TraitsHealthy` | Every trait's health policy passes (`NoneDeclared` with none); the message names any that do not |
| `Collected` | Nothing an earlier revision applied is left |

Every reconcile writes every stage, against the current `metadata.generation`. A stage after the first that has not passed is `Unknown`, reason `NotReached`: it says nothing about this revision, so a refusal never leaves `Applied` or `WorkloadHealthy` reading `True` from the last one. `Healthy` stays as `WorkloadHealthy` and `TraitsHealthy` together.

Three more conditions summarise them for kstatus, and so for `kubectl wait --for=condition=Ready`, Flux and Argo CD:

| Condition | Rule |
|---|---|
| `Ready` | `True` when every stage has passed; otherwise `False`, with the first stage that has not as its reason and message |
| `Reconciling` | `True` while that stage can still pass on its own: health, a render, collection |
| `Stalled` | `True` when it cannot until something else changes: a refusal, a stale tracker, a render vela-agent will not do, an apply the API server rejects, health that does not evaluate |

`status.observedGeneration` names the generation they describe. There is no phase field: `Ready`'s reason already names where a revision is, and `kubectl get components` shows it as `READY` and `REASON` (`MESSAGE` with `-o wide`).

The hub reads `Ready` alongside the health report: when a Component is not ready for any reason but health, the Application's service message is vela-agent's ("vela-agent on cluster prod-use1: UntrustedKey: ..."), rather than a bare wait.

---

## Observations

Some hub-side rendering reads a component's live state: `$(component.<name>)` reads in later components and workflow steps. The hub cannot read live objects once it no longer reaches the spoke, so it asks for exactly what it needs, by resource role:

```yaml
spec:
  observe:
    - {resource: output, name: readyReplicas, path: .status.readyReplicas}
    - {resource: outputs.service, name: clusterIP, path: .spec.clusterIP}
    - {resource: outputs.creds, name: password, path: .data.password, sensitive: true}
status:
  observed:
    output:          {readyReplicas: 2}
    outputs.service: {clusterIP: 10.0.0.5}
  sealedObserved:
    outputs.creds:   {password: <sealed>}
  sealedWith: hub-2026-11
```

The observations are signed with the rest of the spec. They are small, explicit, and map directly onto OCM feedback rules when OCM delivery arrives. An empty `path` is the whole resource, without its managed fields.

A sensitive observation (anything read from a Secret, or a path a definition marks `+sensitive`) never appears in plain text. vela-agent seals it to the sealing key of the hub key that signed the spec ([KEP-2.5](../2.5-credential-model/README.md#sealing-keys)), and only the hub can open it. Without that sealing key, the value is left out and a `CannotSeal` event says so.

The names are deliberately not `outputs`: these are the hub's view of live fields, not a component's declared outputs.

---

## PostDispatch Traits

A PostDispatch trait reads the workload once it is healthy, so vela-agent renders it, where the workload is, in the same revision as the rest:

1. The hub signs the trait's templates, resolved parameters, metadata and render context into `spec.postRender` ([KEP-2.1](../2.1-core-api/README.md#postdispatch-traits)).
2. Once `spec.health` reports the workload healthy, vela-agent renders each trait with the same trait engine the hub uses, against the workload as rendered with its live `.status`.
3. It names and labels what it rendered as the hub would, then applies it through the same keeper as the payload, so drift correction, garbage collection, handover and `status.resources` treat it alike.
4. It renders again on every health recheck and resync, so a trait follows the status it reads without a new revision. An unchanged render is skipped by its hash.

Until the workload is healthy, nothing is rendered and `PostRendered` is `False` (`WaitingForWorkload`); what an earlier revision rendered stays in place, and garbage collection waits. A render that fails sets `PostRendered=False` with the error and an event, and keeps what was applied.

vela-agent renders the CUE standard library only, and checks this again whatever the hub sent: a template that imports anything else or declares `processing` is refused. Running alone, it loads no external CUE packages, so its cluster need not serve `Package` CRs.

---

## Lifecycle Workflows

Application workflows run on the hub, in the Application controller, as they do today. In the phases above, a Component carries no workflow and vela-agent embeds no workflow engine.

**Component-level workflows are planned as a later addition**: lifecycle steps a definition author attaches to a component (create, upgrade, delete), which earlier drafts of this KEP placed on the spoke. Where they run, how they relate to a Component's revisions, and what state they persist are left to that work. Until then, a step that needs a component's live state reads `status.observed` through `spec.observe`.

The KEP-2.15 Operations model, which runs steps spoke-side, is unresolved against this.

---

## Deployment

| Phase | vela-agent ships as |
|---|---|
| 1 | Part of vela-core, behind a feature gate. On spokes, vela-core runs with only vela-agent enabled. |
| 2 onwards | Its own binary, image and chart, with no rendering, workflow or provider code. |

Either way, the controller needs RBAC to apply what Components carry, which is broad. The signature (KEP-2.5) is what stops anyone but the hub directing it, and spoke-side guardrails (KEP-2.3) are what bound what the hub itself may direct.

---

## Cross-KEP References

- **API types**: `Component` and the definition types, [KEP-2.1](../2.1-core-api/README.md).
- **Hub pipeline**: rendering, signing and dispatching Components, [KEP-2.3](../2.3-hub-controller/README.md).
- **Dispatchers**: how a Component is delivered and its status read back, [KEP-2.4](../2.4-dispatchers/README.md).
- **Signing and keys**: [KEP-2.5](../2.5-credential-model/README.md).
- **Operations**: conflicting spoke-side execution, [KEP-2.15](../2.15-operations/README.md).
- **WorkflowRun controller**: standalone WorkflowRun bundling, [KEP-2.7](../2.7-workflowrun/README.md).
