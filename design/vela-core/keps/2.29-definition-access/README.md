# KEP-2.29: Who May Use a Definition

**Status:** Proposed
**Parent:** [vNext Roadmap](../README.md)
**Alternative to:** [KEP-2.26](../2.26-definition-permissions/README.md) (Definition Permissions)
**Related:** [KEP-2.25](../2.25-cuex-packages/README.md) (the same restrictions on CueX package functions), [KEP-2.16](../2.16-source-definition/README.md) (SourceDefinitions)

A definition already says which namespaces may use it, with `spec.restrictions`. This KEP lets it say which people may, by naming the roles whose members can use it, or by requiring an RBAC `use` grant, checked for whoever writes the Application. It keeps to one place for the rule (the definition) and one place for membership (RBAC bindings), and it is offered as an alternative to KEP-2.26, which proposed a separate policy object for the same job.

```yaml
apiVersion: core.oam.dev/v1beta1
kind: ComponentDefinition
metadata:
  name: aws-rds
  namespace: vela-system
spec:
  restrictions:
    namespaces: [team-*]                                   # exists today
    requireRoles: [{kind: ClusterRole, name: db-owners}]   # new: and its writer is a member
    message: Ask #platform for db-owners to create databases.
```

## Motivation

### A definition's power is its author's, not its user's

A definition renders and runs with the controller's credentials, whatever the user behind the Application may do themselves. Most definitions are harmless to hand anyone. Some are not:

| Definition | What using it grants |
| --- | --- |
| `componentdefinition/k8s-objects` | apply arbitrary objects |
| `sourcedefinition/configmap` | read any ConfigMap, in any namespace |
| `workflowstepdefinition/read-object` | read any object by kind and name |
| a team's `aws-rds` | spend on the platform's cloud account |

### What exists today

| Control | Says | Falls short |
| --- | --- | --- |
| `spec.restrictions.namespaces` / `namespaceSelector` (#7388) | which namespaces' Applications may use it | cannot tell people apart within a namespace |
| `spec.restrictions.quota` (#7390) | how many uses a namespace may hold | not about who |
| `ValidateDefinitionPermissions` | the Application's writer may `get` each definition | `get` is also what VelaUX and the CLI need to show a schema, so seeing a definition means being able to use it; and it is on for every definition or none |
| placement | a definition in a team namespace is local to it | one team, not "these people" |

The question platforms keep asking is the missing row: *only these people, or this team's automation, may use `aws-rds`*. Namespaces got their answer in #7388. People need one too, and it should read the same way.

## Design

### Two new fields, and two small ones

`spec.restrictions` gains:

| Field | Means |
| --- | --- |
| `requireRoles` | the Application's writer must be a member of one of these roles |
| `requireGrant` | the Application's writer must hold the RBAC verb `use` on this definition |
| `mode` | `Enforce` (the default, as restrictions are today) or `Warn`, which admits with a warning naming what would have been refused |
| `message` | shown with a refusal or warning, so it says where to go rather than only no |

The namespace rule, the roles and the grant are all checked; an Application is admitted when every one that is set passes. Most definitions need only namespaces. Roles and grants are for telling people apart within a namespace.

```go
// apis/core.oam.dev/common: added to DefinitionRestrictions
type DefinitionRestrictions struct {
	Namespaces        []string              `json:"namespaces,omitempty"`
	NamespaceSelector *metav1.LabelSelector `json:"namespaceSelector,omitempty"`
	Quota             []NamespaceQuota      `json:"quota,omitempty"`

	// RequireRoles are roles any one of whose members may use the definition.
	// +optional
	RequireRoles []RoleRef `json:"requireRoles,omitempty"`
	// RequireGrant requires RBAC use on the definition, asked in its namespace.
	// +optional
	RequireGrant bool `json:"requireGrant,omitempty"`
	// Mode is Enforce or Warn. Quota keeps its own warn and limit.
	// +kubebuilder:validation:Enum=Enforce;Warn
	// +optional
	Mode RestrictionMode `json:"mode,omitempty"`
	// Message is shown with a refusal or warning.
	// +optional
	Message string `json:"message,omitempty"`
}

// RoleRef names a ClusterRole, or a Role in the definition's namespace.
type RoleRef struct {
	// +kubebuilder:validation:Enum=Role;ClusterRole
	Kind string `json:"kind"`
	Name string `json:"name"`
}
```

### A required role

```yaml
# The platform's existing role, or one made for this
kind: ClusterRoleBinding
metadata: {name: db-owners}
roleRef: {apiGroup: rbac.authorization.k8s.io, kind: ClusterRole, name: db-owners}
subjects:
  - {kind: Group, name: payments-leads}
  - {kind: ServiceAccount, name: argocd-payments, namespace: argocd}
```

The writer is a member when a binding to the role names its user, one of its groups, or its service account. Kubernetes has no API for membership, so vela-core watches ClusterRoleBindings and RoleBindings and matches subjects itself, as RBAC does. Its chart role gains `get`, `list` and `watch` on both, which it lacks today with authentication on.

- **Only bindings where the definition lives count**: ClusterRoleBindings, and RoleBindings in the definition's namespace. RBAC lets a subject bind any role whose permissions it already holds, and a role made only to be required may hold none, so a RoleBinding in a team's own namespace would let its admin make themselves a member. For a vela-system definition, only the platform can bind.
- **Membership is by name, not by permission.** A namespace-admin role with `*` on `*` is not a member of `db-owners`, and neither is cluster-admin until bound. The rule reads exactly as written.
- **Only RBAC bindings are seen.** Membership given another way, by an authorization webhook or cloud IAM mapped straight to permissions, is not. Identity provider groups bound through RBAC are, which is the usual shape, EKS access entries mapped to groups included.

### A required grant

| Asked of the SubjectAccessReview | Value |
| --- | --- |
| verb | `use` |
| resource | the definition's kind, `componentdefinitions`, `traitdefinitions`, ... in `core.oam.dev` |
| name | the definition's name, without any `@v1` revision |
| namespace | the definition's |
| user and groups | the Application's writer |

```yaml
kind: Role
metadata: {name: use-aws-rds, namespace: vela-system}
rules:
  - apiGroups: [core.oam.dev]
    resources: [componentdefinitions]
    resourceNames: [aws-rds]       # leave out for every ComponentDefinition
    verbs: [use]
```

A grant works with any authorizer, which a role does not, and it is what to use where an authorization webhook decides access. It is asked in the definition's namespace for the same reason roles count only there: a team's wildcard role in its own namespace would otherwise hold `use` on everything. `use` is separate from `get`, so a definition can be visible to everyone and usable by some.

### Who is checked

The Application's writer: whoever creates or updates it, as the admission request names them. That is the same identity `ValidateDefinitionPermissions` uses, and it depends on how Applications reach the cluster:

| Applied by | Checked | Give roles and grants to |
| --- | --- | --- |
| a person, with kubectl or VelaUX | the user and their groups | usually a group |
| GitOps or CI | its service account | that service account; the repository's merge rules then decide who writes |
| vela-core, creating a child Application with authentication on | the parent's user, whom vela-core impersonates | that user |
| vela-core as itself, creating a child Application (an addon or module component) | the parent Application, from the `app.oam.dev/name` and `app.oam.dev/namespace` labels vela-core sets | a parent in vela-system, an addon, has vela-core's roles and grants; any other parent has none |

The last row exists because vela-core runs as cluster-admin by default, so without it any Application could create a child that passes every check. With authentication off, that Application's writer can still have vela-core do anything else cluster-admin can; restrictions close the easy path there, not that one.

Two consequences to state in the docs:

- **Per-person roles collapse to automation under GitOps.** If one Argo CD service account applies every team's Applications, the check sees only it. Roles are useful per person only where each team's automation runs as its own service account, or where people apply directly.
- **Teams cannot give themselves either.** Both are decided where the definition lives, where for vela-system only the platform binds.

### What is checked, and when

The same definitions restrictions already reach: components and their traits, policies, workflow steps and their sub-steps, sources, and the types an override policy or a referenced `Workflow` brings in. A pinned `webservice@v1` is checked as `webservice`.

**At admission, on create and update.** An update is checked for each use whose type or properties changed, and for uses it adds; a use left as it was is not checked again. A use is matched by its component's name, and a trait by its type, so reordering components changes nothing and renaming one is a new use. A use brought in by another object, a referenced `Workflow` or `Policy`, is checked on every write, since that object can change without the Application. So a teammate without `db-owners` can still change an Application's other components, but not its database. This differs from today's namespace restrictions, which recheck every use on update so an Application admitted before a restriction is frozen; for a per-person rule that would lock the whole Application away from anyone without the role. Namespace restrictions keep their current behaviour.

**Not at reconcile.** An Application admitted keeps running when a role is later taken away, as it does for namespace restrictions; its next change to a restricted use is refused. Stopping running workloads because of a membership change would be the wrong failure.

### Refusals

```text
admission webhook denied the request: spec.components[2].type: ComponentDefinition "aws-rds" is restricted:
the writer must be a member of ClusterRole/db-owners. Ask #platform for db-owners to create databases.
```

A namespace refusal still says only that the definition is restricted, never what it is restricted to, as today. A role or grant refusal names the roles or grant, since the writer needs them to ask for access and RBAC already names what is missing. `message` follows either.

In `Warn` mode the Application is admitted, with the same text as a warning, and the webhook logs it with the writer. That is how a restriction is introduced on a busy cluster: see who it would refuse, then switch to `Enforce`.

### Where the spec is owned elsewhere

A definition installed by the chart or an addon has its spec rewritten on upgrade. Namespace restrictions already have an annotation for that, `definition.oam.dev/restrict-namespaces`. Roles get one beside it:

```yaml
metadata:
  annotations:
    definition.oam.dev/restrict-namespaces: "team-*"
    definition.oam.dev/require-roles: "ClusterRole/db-owners,ClusterRole/platform-ops"
```

A grant needs no annotation: it is RBAC already, written outside the definition. Spec and annotation combine as they do for namespaces today.

### Authoring and tools

- **defkit**: `RequireRoles(...)`, `RequireGrant()`, `RestrictionMode(...)` and `RestrictionMessage(...)` beside the existing restriction builders.
- **`vela def show`** lists a definition's restrictions. **`vela def access <name> --as <user> [--group ...] -n <namespace>`** answers whether that writer may use it there and why, from the same code the webhook runs.
- **VelaUX** shows the restrictions on a definition's page and leaves restricted definitions out of the pickers for users who cannot use them.

### Relationship to `ValidateDefinitionPermissions`

Unchanged. It asks `get` for every definition when on; this asks `use`, or membership, for the definitions that set it. Both apply when both are set. Whether the gate is deprecated once this lands is left open.

### Relationship to KEP-2.25

A CueX `Package` restricts its functions with the same shape: `namespaces`, `namespaceSelector`, `requireRoles` and `requireGrant`, per package or per function group. A function there may sit in several groups (`$groups`) and must pass for each, as GitHub asks a token for every permission an endpoint needs; a definition is one thing, so a definition's restriction is one check. A package's restrictions say which namespaces' definitions may call its functions; a definition's say who may use the definition. The namespace matcher and the membership check move to `kubevela/pkg` so both use one implementation.

The two meet in one writer. KEP-2.25 checks a definition's calls against whoever last changed the definition, from the `cue.oam.dev/writer` stamp a mutating webhook writes on every definition kind; this KEP checks a definition's use against whoever writes the Application, from the admission request. Neither needs the other's stamp, and one Role can serve both: the GitHub sample's generated `github-issues-write`, bound to a platform's GitOps service account, lets its definitions call `issues:write` functions, and a `use` grant on those definitions decides which teams may use them.

## Security considerations

- **Adding a restriction can only refuse, never allow.** It is checked after namespace restrictions and `ValidateDefinitionPermissions`, and passes nothing they refuse.
- **Roles and grants are decided where the definition lives**, so a team cannot satisfy either from its own namespace.
- **Membership reads only RBAC bindings**, which auditors already read. There is no second place where access is given.
- **With authentication off**, vela-core is cluster-admin and does what any Application asks of it. Restrictions stop the direct path, a child Application, and not the rest. The docs say so, and point at `authentication.enabled`.

## Compatibility

Every field is optional and new, so nothing changes until a definition sets one. The CRDs grow, and `helm upgrade` never updates CRDs: a field set before the CRDs are re-applied is pruned on write, so the upgrade notes say to apply them first. A controller that predates this KEP ignores the fields; the annotation is ignored too, so a downgrade opens what was restricted, which the upgrade notes also say.

## Test plan

- Unit: membership through user, group and service account subjects; ClusterRoleBindings and RoleBindings in the definition's namespace counting, and RoleBindings elsewhere not; a wildcard role not counting as membership; a grant asked in the definition's namespace and a wildcard role in the Application's namespace not satisfying it; the annotation; `Warn`; the update rule for changed, added and unchanged uses.
- E2E on KWOK: each definition kind and each indirect use (override policy, referenced Workflow, sub-steps); a child Application from a vela-system parent and from a team parent; the refusal text.
- Acceptance on a real cluster: GitOps with a per-team service account; an identity provider group through a ClusterRoleBinding.

A prototype covers the unit tier and RBAC itself: the fields, the annotation, `pkg/definition/access`, and the admission check with the child-Application and update rules, with envtest specs whose bindings and SubjectAccessReviews are decided by a real API server, including a namespace admin's wildcard refused as a grant and membership read through informers. It was also run end to end on k3d, with the chart installed and impersonated users writing through a running vela-core: roles bound cluster-wide and in the definition's namespace, a binding in the team's namespace refused with the definition's message, a grant, the wildcard, Warn, and the update rule, with an admitted Application rendering. Those specs sit in the definition e2e suite.

## Alternatives considered

### KEP-2.26: a policy object per definition

KEP-2.26 puts the rule in a `Config` of a `vela-definition-permission` ConfigTemplate: allow and deny lists for namespaces, users, groups and namespace labels, a CUE script, and audit mode by default, with a new `+unique` marker so one definition has one policy.

| | KEP-2.26 | This KEP |
| --- | --- | --- |
| Where the rule lives | a Config in vela-system, one per definition | the definition's `spec.restrictions`, or its annotation |
| Namespaces | allow and deny lists, labels | `namespaces`, `namespaceSelector`, which shipped since (#7388) |
| People | `allowUsers`, `allowGroups`, deny lists | membership of named roles, or an RBAC grant |
| Escape hatch | a CUE script | none |
| Trying it first | audit mode by default | `mode: Warn` |
| New machinery | `+unique` on ConfigTemplates, one-policy-per-definition races | none beyond the fields |

KEP-2.26's case for a separate object was that RBAC cannot express the namespace axis. Since then `spec.restrictions` has given definitions that axis, on the definition itself, so the remaining question is people, and RBAC already answers who people are: bindings name users, groups and service accounts, and auditors read them. A user list in a Config would be a second place where access is given, out of sight of anyone auditing RBAC. What this gives up is deny lists (RBAC has no deny; a role that excludes someone is a role without them) and the script. Its good ideas, trying a rule out before enforcing it and saying where to go on refusal, are kept as `mode` and `message`.

### Verbs alone, or roles alone

A grant alone (`use`) works with any authorizer but makes every restriction a `resourceNames` Role, which is the plumbing platforms avoid writing today. A role alone reads as written but sees only RBAC bindings. Both are offered, because both kinds of cluster exist.

### Checking at reconcile

Refusing to reconcile an Application whose writer has lost a role would stop running workloads over a membership change. Admission is where the writer is known; reconcile has only a recorded name, which goes stale.

### An external policy engine

Kyverno or Gatekeeper cannot see which definition `type: webservice` resolves to (the Application's namespace, then vela-system, then revision pinning) without reimplementing KubeVela's lookup. KEP-2.26 makes this case in full and it holds here: the decision needs the definition KubeVela resolved.

## Open questions

1. **Deprecating `ValidateDefinitionPermissions`** once `requireGrant` covers it per definition.
2. **Parameters.** "Only db-owners may set `multiAZ: true`" is the same question one level down, and would need parameters tagged, as KEP-2.25's functions are with `$groups`. Left for a follow-up if asked for.
3. **WorkflowRuns.** The workflow engine's own WorkflowRun admission uses step definitions too; whether it applies these restrictions is for the workflow repository.
