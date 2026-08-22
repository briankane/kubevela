# KEP-2.26: Definition Permissions

## Summary

A first-class way to say **who may use a Definition**, expressed as policy rather
than as RBAC plumbing, and enforced at Application admission.

A `Config` written against a `vela-definition-permission` ConfigTemplate names one
Definition and states which namespaces, users and groups may bind it. The
Application validating webhook — which already resolves every Definition an
Application uses — consults it and refuses what the policy refuses.

## Motivation

### A Definition's power is the author's, not the caller's

A Definition's template runs with the **controller's** credentials. That is true of
every kind: a `ComponentDefinition` can render anything, a workflow step can read
anything, and the workflow engine explicitly clears impersonation
(`workflow.go:333`, "clear the user info in context"). It is the model KubeVela
already has, and this KEP does not change it.

The consequence is that granting someone a Definition grants them whatever that
Definition can do. For most Definitions that is unremarkable. For some it is a
cluster-wide read:

| Definition | What binding it actually grants |
| --- | --- |
| `sourcedefinition/configmap` | read any ConfigMap, in any namespace, in any cluster |
| `componentdefinition/k8s-objects` | apply arbitrary objects |
| `workflowstepdefinition/read-object` | read any object by apiVersion/kind/name/namespace |

These are useful — addons need them. They are not things to hand every tenant.

### Today there is no way to say that

The only existing control is `ValidateDefinitionPermissions`, which issues a
`SubjectAccessReview` for `get` on the Definition object. Three problems:

1. **It defaults off**, so most clusters have no control at all.
2. **It needs `resourceNames`-scoped Roles** to name a single Definition, which
   few operators will write.
3. **It cannot express the namespace axis.** The Definition lives in
   `vela-system`; the subject lives elsewhere. "Addons may, tenants may not" is
   not a sentence RBAC says naturally.

Placement is the only other lever: a Definition in `vela-system` is global, one in
a namespace is local to it. There is no "these namespaces".

### Why not Kyverno or Gatekeeper

This is the first question a reviewer will ask, and the answer is not "we prefer
CUE".

**The user never names the Definition.** They submit an Application referring to
`type: webservice`. Resolving that to an object means walking four locations in
order — the Application's namespace, the configured `XDefinitionNamespace`,
`vela-system`, then a cluster-scoped fallback for old clusters
(`pkg/oam/util/helper.go:194`) — before `@v1` revision pinning, which goes through
`GetCapabilityDefinition` and widens under `autoUpdate`. An external engine would
have to reimplement that from the outside and keep it in step as it changes.

**And the authorisation question is not policy-shaped.** The existing check asks
the API server, via SubjectAccessReview, whether this user may `get` this
Definition. That is not something an external policy engine answers.

So the resolution and the authorisation both live inside the controller. Anything
outside is working with worse information.

**This is not a general admission policy engine.** Policy about the Application
object itself — replica counts, image registries, labels — is Kyverno's job and
Kyverno is better at it. This KEP covers exactly the decisions that need
post-resolution KubeVela context.

## Design

### Substrate: ConfigTemplate and Config

No new CRD. A `vela-definition-permission` ConfigTemplate gives schema validation,
`vela config` management and storage for free.

Two properties of Configs make this work, both already true:

- **`outputs:` can emit arbitrary objects.** A policy Config renders a plain,
  readable ConfigMap alongside the Secret that records it, so policy is greppable
  rather than base64.
- **Deletion cascades.** `DeleteConfig` reads the stored `ObjectReferences` and
  deletes each output object (`pkg/config/factory.go:713`). No owner references
  needed, and it works across namespaces where an owner reference could not.

### One policy per Definition, enforced by `+unique`

The obvious failure is two Configs claiming the same Definition. Rather than
defining a merge, forbid it: a new `// +unique` marker on a ConfigTemplate field,
validated when a Config is created, rejecting a second Config whose marked field
collides.

`+unique` is a general ConfigTemplate capability, not a permissions feature —
anything modelling "one config per thing" gets it.

```cue
parameter: {
    // +unique
    // +usage=The Definition this policy governs, kind-qualified
    definition: string
}
```

**The key must be kind-qualified.** `configmap` is shipped today as *both* a
TraitDefinition and a SourceDefinition. A policy keyed on the bare name would
silently govern both, or reject a legitimate second policy for a genuinely
different Definition.

```
definition: "sourcedefinition/configmap"
```

The check is racy — two simultaneous creates could both pass — which is
acceptable for an operator-driven object, and the webhook fails closed if it ever
does find two (see below).

### Evaluation: three stages, in order

```
1. RBAC        if ValidateDefinitionPermissions is enabled, the SAR must pass
2. Allow       the subject must match at least one allow rule
3. Deny        the subject must match no deny rule
```

**RBAC is never overridden.** If the SAR denies, the answer is no, whatever the
policy says. Otherwise "can write vela-system Secrets" would quietly become "can
grant Definition access" — a second, weaker admin surface for the same privilege,
invisible to anyone auditing RoleBindings.

The property that falls out is worth stating plainly: **adding a policy can never
grant access, only remove it.** That is what makes policies safe to write and
review. The worst a wrong policy does is deny too much, which is loud.

**A gate that is not enabled does not participate.** `ValidateDefinitionPermissions`
defaults off, so on most clusters stage 1 is skipped and the policy decides alone.
The converse is a real interaction to document: enabling the RBAC gate on a cluster
already running policies can start refusing Definitions for reasons unrelated to
any policy.

**Deny wins over allow**, always. Fixed precedence is what makes allow and deny on
the same axis well-defined rather than ambiguous, and it lets cross-axis rules say
useful things:

```
allowGroups:    ["platform"]
denyNamespaces: ["sandbox"]
```

*Platform group, except in sandbox.* Neither list alone expresses that.

**More than one matching policy is a denial**, not a merge. A security control that
resolves disagreement by picking one is worse than one that refuses — a refusal is
debuggable. This is the backstop for `+unique`'s race.

### Fields

```cue
parameter: {
    // +unique
    definition: string            // "sourcedefinition/configmap"

    // Absent means unconstrained. Present means must match.
    allowNamespaces?:      [...string]
    allowUsers?:           [...string]
    allowGroups?:          [...string]
    allowNamespaceLabels?: [string]: string

    denyNamespaces?:      [...string]
    denyUsers?:           [...string]
    denyGroups?:          [...string]
    denyNamespaceLabels?: [string]: string

    mode:     *"audit" | "enforce"
    message?: string
}
```

`req.UserInfo.Username`, `.Groups` and the Application's namespace are already in
hand at admission, so the subject axes need no new plumbing.

**`mode` defaults to `audit`.** Creating a policy otherwise flips a Definition from
open to closed in one step — a large effect from a small object. Audit logs what
*would* be denied, so the scary operation becomes two deliberate ones.

**`message` is shown on denial.** It is the difference between a refusal and a
support ticket:

> `configmap` is restricted to platform namespaces; use `configmap-local` instead

### Namespace labels

Name lists and globs mean editing the policy for every new tenant. Labels invert
it: the policy is written once and namespaces are onboarded by labelling them,
which is how platform teams already provision namespaces.

Precedent is direct — Pod Security Admission works exactly this way
(`pod-security.kubernetes.io/enforce: restricted` on the namespace, enforced at
admission).

**Label a tier, not each Definition:**

```yaml
# the namespace declares what it is
metadata:
  labels:
    definitions.oam.dev/tier: privileged
```

```
# the policy declares which tiers may bind this Definition
definition:           "sourcedefinition/configmap"
allowNamespaceLabels: { "definitions.oam.dev/tier": "privileged" }
```

Per-Definition labels would mean fifty Definitions eventually needing fifty labels
on every namespace. A tier means one label, and adding a Definition to the
privileged set is a policy edit rather than a sweep across every namespace. It
also sidesteps a mechanical problem: label *values* cannot contain `/`, so
`sourcedefinition/configmap` cannot be one.

**Prerequisite, and it is load-bearing.** This is only sound if tenants cannot edit
their own namespace metadata. If they can, they can label themselves `privileged`
— self-service escalation, and quiet. In most clusters namespaces are
platform-provisioned and tenants hold no `update` on them, but unlike RBAC this is
not self-evidently enforced. Operators must be told to check it.

### Wildcards

Glob `*` in every lookup — namespaces, users, groups. Without them this is
exact-match, which RBAC already does.

Not regex: a policy nobody can read at a glance is a policy nobody audits.

**Wildcards are safe in deny and dangerous in allow.** `denyNamespaces: ["team-*"]`
failing to match a new namespace means it is not denied — bad, but visible.
`allowNamespaces: ["team-*"]` silently permits every future `team-`-prefixed
namespace, including one created by someone who noticed the pattern. This belongs
next to the examples in the docs, where a first-time policy author will see it.

### CUE escape hatch

For decisions the fields cannot express, a policy may carry a **plain CUE** script,
compiled from string and evaluated against the Application context, the resolved
Definition, its parameters, and the namespace's labels.

Plain CUE, not CueX: policy is a decision over data it is handed, so it needs
unification and comprehensions, not provider functions. See the constraints below.

This reopens the one case the fields deliberately cannot reach: gating on
**resolved Definition parameters**, so *"`configmap` is allowed, but only with
`namespace` unset"* becomes expressible. That may be the strongest argument for
including it.

Three constraints, all non-negotiable:

**Plain CUE, not CueX.** Compile with `cuecontext.New()`, as
`webhookutils.ValidateCueTemplate` already does — not the CueX compiler with
providers switched off. The distinction
matters: disabling providers is a flag someone can get wrong or a future refactor
can quietly drop, whereas plain CUE has no provider functions to reach for in the
first place. `kube.#Get` is not disabled, it is absent.

That rules out I/O on the admission path — latency, a webhook that fails when an
unrelated API call does, and a policy that could read cluster state and fold it
into its decision. Policy is a pure function of what it is handed, structurally
rather than by configuration.

**Fail closed.** An error or an incomplete evaluation is a denial. Which means a
typo denies every Application using that Definition, so `audit` mode and a
dry-run path are not optional extras.

**Compiled policies are cached**, keyed on the script text. KEP-2.16 does this
for source cache policies and it was worth roughly a 4x saving on an already-cached
resolve; admission is a hot path and compilation is the expensive part.

**Ship the fields first and the script second**, so the common path is proven
before the escape hatch exists to bypass it. "Who can use `configmap`?" should be
answerable by reading, not by evaluating a script against every possible input.

## Non-goals

- **A general admission policy engine.** Policy on the Application object itself is
  Kyverno's or Gatekeeper's job.
- **Changing the execution identity model.** Definitions run as the controller.
  That is KubeVela's existing model across every Definition kind, and revisiting it
  is separate work.
- **Parameter-level restriction via the declarative fields.** Splitting a
  Definition does the same job more simply: ship `configmap` safe and
  `configmap-any` powerful-and-restricted. The CUE escape hatch covers the cases
  where splitting is not enough.

## Dependencies and open questions

- **The Config controller.** Reconciliation of Configs is being developed
  separately. It decides two things this KEP leaves open: whether output objects
  are owned and re-applied on drift, and whether uniqueness can be enforced at
  admission rather than checked with a race. This KEP should depend on that work
  rather than duplicate it.
- **Which object the webhook reads.** Outputs are applied once, with nothing
  reconciling them, so a directly-edited ConfigMap diverges from the Config that
  records it. Reading the **Config** keeps one source of truth and makes the
  ConfigMap a human-readable mirror; reading the ConfigMap is simpler but makes
  direct edits authoritative. The Config controller's behaviour decides this.
- **Globs or label selectors.** Globs are more legible; selectors do not couple
  policy to a naming convention that breaks when someone creates
  `team-admin-tools`. Migrating between them later means rewriting every policy, so
  it is worth deciding deliberately.
- **Which subject axis gets the documentation.** On a GitOps cluster every
  Application arrives via one controller ServiceAccount, so users and groups are
  inert and namespace is everything. On a human-driven cluster it is the reverse.
  Both are real; the expected model should be stated.

## Worked example

The case that motivated this. `configmap` is useful to addons and dangerous to
tenants:

```bash
vela config create configmap-policy \
  -t vela-definition-permission \
  --namespace vela-system
```

```yaml
definition:           "sourcedefinition/configmap"
allowNamespaceLabels: { "definitions.oam.dev/tier": "privileged" }
mode:                 "audit"
message:              "configmap reads any namespace; tenants should use configmap-local"
```

Addon namespaces carry `definitions.oam.dev/tier: privileged`; tenant namespaces
do not. The operator watches the audit log, confirms only addons would be
affected, and flips `mode` to `enforce`.

Nothing else on the cluster changes, and no RoleBinding was written.
