# KEP-2.25 design 06: selection and visibility

Phase 1 of the [KEP](../README.md): how a compilation comes to see only the packages its template imports, from the namespaces its definition may see, and what that costs against today. Which definition a compilation is for, and where that is set, is in [design 01](01-api.md#which-definition-a-compile-is-for).

Figures are from the spike in `kubevela/pkg` (`cue/cuex/selectspike`), through the real compiler on an Apple M2 Max, one goroutine, five runs each.

## What exists today

| | Today | Problem |
| --- | --- | --- |
| Imports | every loaded package's instance is attached to every compilation | the compiler's scans for provider calls walk all of them, so a render slows with every package installed anywhere |
| Providers | one map keyed by Package name, built on every resolve | a Package named after a built-in replaces it; two namespaces' Packages of one name, the last loaded wins |
| Visibility | none | a team's package is importable from every definition in the cluster |
| Shared instances | CUE completes and resolves an instance's files on every build | a data race between concurrent renders; every writer stores the same value, which is why it has held (see `GetImports`) |
| Versioned paths | the call scope names an import by its last path element | `acme.com/naming@v1` binds `naming@v1`, so `naming.#Tag` is not recognised as a call and never runs, silently. Latent today, since no path carries a version |

What today does **not** do: an unimported package with broken CUE never breaks a render. CUE builds only the imports a file references, so the spike's broken package beside a working one changed nothing. Selection's gains are visibility, providers and cost, not blast radius.

## The view

Every loaded Package is held in a **view**: an immutable snapshot, rebuilt when a Package changes and swapped in whole. A compilation takes the current view once and keeps it to the end, so a change mid-render is never half-seen.

Building a view admits Packages one at a time, oldest first, and refuses the newcomer on a clash with any holder visible beside it, then refuses what cannot resolve, a round at a time until nothing else falls, so the outcome does not depend on the order within a round:

| Refused | Reason | Message, as the spike writes it |
| --- | --- | --- |
| a built-in's path | `Conflict` | `vela/kube is a built-in` |
| `vela/` outside vela-system | `ReservedPath` | `vela/ is reserved for vela-system` |
| `local/` in vela-system | `ReservedPath` | `local/ is for packages outside vela-system` |
| a path its own namespace holds | `Conflict` | `local/acme.com/dup@v1 is held by team-a/first` |
| a cycle | `DependencyCycle` | `team-a/local/acme.com/ping@v1 -> team-a/local/acme.com/pong@v1 -> team-a/local/acme.com/ping@v1` |
| an import its namespace cannot see | `DependencyMissing` | `acme.com/gone@v1 is not visible from team-a` |
| an import of something refused | `DependencyMissing` | `local/acme.com/orphan@v1 is not visible from team-a` |

A vela-system package importing a team's package is the last-but-one row: vela-system sees only vela-system, for its dependencies as for its definitions.

**A Package outside vela-system imports under `local/`**: `acme.com/naming` installed in team-a is `local/acme.com/naming@v1` there, and vela-system cannot use `local/`. So a team's package and vela-system's never collide, whichever came first, and no lookup has an order to it. Two teams may share a `local/` path, since neither sees the other's. The spike checks both orders, and that the bare path from a team still reaches vela-system's.

Each admitted Package gets one CUE instance per view, built from the files parsed when it loaded, with its own imports wired to its dependencies as **its** namespace sees them. The instance is completed before the view is published. CUE completes an instance on every build and only skips the write once it is done, so completing it first removes that half of the shared-instance race. The other half, CUE trimming each file's unresolved identifiers on every build, is CUE's own: it writes the same value each time, today's compiler does it too, and the fix belongs upstream.

## Selecting

Per compilation, before anything is built:

1. **Whose it is**: the owner's namespace, or vela-system when there is none.
2. **What it imports**: the file's import paths that are packages, which are those under `vela/` or `local/`, those whose first element has a dot, and any path a legacy Package holds exactly. Everything else is CUE's standard library and is left to CUE.
3. **Where each is**: one lookup in what the owner can see, its namespace and vela-system, where a path is held at most once, so there is no order to them and nothing to fall back to; built-ins come from the process. A path found nowhere fails the compilation: `package "local/acme.com/naming@v1" is not installed where team-b can see it (team-b or vela-system)`.
4. **The providers**: those of the selected packages and of everything they depend on, built-ins included, and nothing else.
5. **The gate**: providers of packages with restrictions are wrapped, so each call is checked against whoever made it (see [Restricted functions](#restricted-functions)).

The selection then replaces both of today's global lookups: its instances are the compilation's imports, and its providers are the only ones its calls can reach.

### No cache

A selection shared per namespace and import set was measured against selecting every time: the uncached selection is within noise of the cached one, a handful of allocations more, because nothing in it is parsed or built, only looked up. Instances are what cost, and those are built once per view either way. Phase 1 selects per compilation and keeps no cache, so there is nothing to invalidate.

## Providers

A compilation's providers are keyed as the CUE names them, by `#provider`:

| Package | `#provider` | Can two collide in one compilation? |
| --- | --- | --- |
| a Package in this KEP's layout | set by the host to the Package's identity, namespace and name | no |
| a Package written the old way | hand-written, equal to the Package name | yes: a team's and vela-system's of one name, both imported |

The second case is refused for that compilation, naming both: `team-a/naming and vela-system/naming both answer #provider "naming"; import one of them`. Each still works where imported alone. Refusing it at admission instead would break a team's working Package the day vela-system installs one of the same name, for templates that never import both.

## Restricted functions

A Package's `spec.restrictions` has a definition's fields, `namespaces` (names or `path.Match` globs) and `namespaceSelector`, plus `functions`, a list of entries that each match functions by name or `group:<name>` and carry the same fields. The package and each entry may also carry `requireRoles` and `requireGrant`.

| A call from an importer in namespace N to function F is allowed when | Absent means |
| --- | --- |
| N matches the package's `namespaces` or `namespaceSelector` | any namespace |
| for each of F's `$groups`, N matches the first entry whose `match` includes F or that group | no entry, or an entry with no matcher: any namespace |
| where that entry, or the package, has `requireRoles`: the importer's writer is a member of one of them | no role needed |
| where that entry, or the package, has `requireGrant: true`: the importer's writer may `call` F for each group it applies to | no grant needed |

The importer is the definition whose template makes the call, or the `Package` whose CUE does, so a team cannot wrap vela-system's `githubwrite` in a `local/` package and hand the wrap to namespaces that may not call it. vela-system is matched like any other namespace, as definitions are: a platform lists it to let its own definitions call. Restrictions that do not parse deny everything, as a definition's do, and are refused at admission. The matcher is the one definitions use (`nsrestrict.Allows`), moved to `kubevela/pkg` so both share it.

**Groups** come from the package: a function's `$groups`, a list, optional. A function in several groups must pass for each, as GitHub asks a token for every permission an endpoint needs; an entry selected for several of them is checked once. A function in none takes the first entry matching its name. Status lists each function with its groups and whether any restriction applies to it.

**At the call.** The selection hands the compiler a check, which it runs where it dispatches each call, before the provider sees it, on the serial path and on `@concurrency`'s alike. Wrapping the provider is not enough: the concurrent path decodes each call with no context, so a wrapper cannot tell whose compilation it serves. The check gets the package, the `#do` and the call value, and returns nothing for a package with no restrictions. The namespace check reads labels from the namespace informer, with nothing to ask. A refusal fails the call, and the Application's condition, without saying where the function is allowed, as a definition's refusal does not:

```text
team-a/close-stale may not call example.com/githubwrite#issues/update: it is restricted
```

**Who wrote the call.** A call is a unification, and each of its conjuncts keeps the file it came from. A conjunct from a Package's file was written by that Package; any other was written by the template. Every writer found must pass, so a `#do` built by hand beside a platform wrapper's call, or a template reaching into the wrapper's call to set its parameters, is checked as the template. A Package calling its own functions is not checked. This needs the loader to name each Package's files uniquely, by namespace and name, so a file name means one Package.

| A call to restricted `githubwrite`, from team-a, whose writer holds nothing | Checked as | Result |
| --- | --- | --- |
| through vela-system's `notify` wrapper, written by the platform's GitOps service account | `notify`'s writer | allowed |
| `#do` written in the template, beside `notify` | the template | refused |
| the template setting fields inside `notify`'s call | the template, and `notify` | refused |
| through a team-a wrapper | the wrapper's writer | refused unless that writer passes |

**At admission**, the functions a template or package refers to by definition are checked against its namespace, and its writer where a role or a grant is required. This is feedback, not the gate: it cannot see a `#do` built by hand, nor a package that does not exist yet.

A compilation with no owner is checked as vela-system with no writer, so it fails any required role or grant; a call site that forgets its owner fails safe here as it does for visibility.

### A required role

`requireRoles` lists roles, any one of which will do: `{kind: ClusterRole, name: github-admins}`, or `{kind: Role, name: ...}` for a Role in the Package's namespace. The writer is a member when a binding to the role names the writer's user, one of its groups, or its service account. Kubernetes has no API for membership, so vela-core watches RoleBindings and ClusterRoleBindings and matches subjects itself, with nothing to ask per call. Its chart role gains `get`, `list` and `watch` on both.

**Only bindings where the Package lives count**: ClusterRoleBindings, and RoleBindings in the Package's namespace. RBAC lets a subject bind any role whose permissions it already holds, and a role made only to be required may hold none, so a RoleBinding to it in the importer's namespace would let a team admin make themselves a member. In vela-system, only the platform can bind.

**Membership is by name, not by permission.** A wildcard role is not a member of `github-admins`, and neither is cluster-admin until bound. Membership granted outside RBAC bindings, by an authorization webhook or a cloud IAM mapping straight to permissions, is not seen; identity provider groups bound through RBAC are.

A refusal names the roles that would allow it.

### A required grant

| Asked of the SubjectAccessReview | Value |
| --- | --- |
| verb | `call` |
| resource | `packages` in `cue.oam.dev` |
| name | for each group, any of `<path>`, `<path>#group:<group>`, `<path>#<do>`, where `<path>` is the import path without its major |
| namespace | the Package's |
| user and groups | at admission, the request's; at a call, the importer's writer |

The namespace is the Package's, not the importer's: a team's namespace-admin role with every verb on every resource would otherwise hold `call` on every package, and that namespace axis is what `namespaces` and `namespaceSelector` are for. The import path is the name because it already means one package wherever it is visible, and leaving out the major keeps a grant across `@v1` and `@v2`; `#` separates the function because paths carry slashes of their own. A grant works with any authorizer, which a role does not. A refusal names the grants that would allow it. Answers are cached per writer, groups and name for `--cuex-access-recheck-interval` (default 5 minutes), and dropped when the importer or the Package changes.

**The writer.** A mutating webhook on definitions and `Package` objects records who changed the spec, in `cue.oam.dev/writer` and `cue.oam.dev/writer-groups`, overwriting whatever the object carries, as the Application webhook does with `app.oam.dev/username`. A write that leaves the spec alone keeps the stamp, so the controller's own writes never become the writer. A DefinitionRevision embeds its definition, metadata included, so it carries the stamp of the content it froze, and a pinned render is checked as whoever wrote that revision. vela-core refuses to start when it cannot resolve its own identity, since its writes would otherwise be stamped as its service account and hold its roles and grants. An object with no stamp has no role or grant. The stamp records every group the request carries, not only those matching `--authentication-group-pattern`, since roles and grants usually go to an identity provider's groups. The webhook fails open: on an upgrade its configuration is live before the new pods serve it, and failing closed refuses every definition write until they do, the chart's own built-in definitions included. vela-core pins it to `Ignore`, whatever `--webhook-failure-policy` sets on the rest. Nothing is lost by it: a definition written while nothing stamps it has no writer, so its restricted calls are refused until it is written again.

Users and service accounts are both RBAC subjects; what differs is who writes in practice:

| Applied by | The stamp holds | Roles and grants go to |
| --- | --- | --- |
| a person, with kubectl | the user and their groups | usually a group |
| GitOps or CI | its service account, with `system:serviceaccounts` and `system:serviceaccounts:<ns>` | that service account, in the namespace it writes to; the repository's merge rules decide who authors |
| vela-core with authentication on | the Application's user, whom vela-core impersonates, or `--authentication-default-user` | that user |
| vela-core as itself, from an Application | `application:<namespace>/<name>`, from the `app.oam.dev/name` and `app.oam.dev/namespace` labels vela-core sets | an Application in vela-system, an addon, holds vela-core's; any other holds none |
| vela-core as itself, from anything else | `vela-core` | none |

The vela-core rows exist because vela-core runs as cluster-admin by default; without them a `k8s-objects` component could ship a definition that passes every role and grant. With authentication off, that Application's creator can still have vela-core do anything else cluster-admin can, so restrictions close the easy path, not that one.

- **Give roles and grants to automation, not people.** A definition's calls keep working only while its last writer has them; binding the GitOps service account keeps them working as people come and go.
- **Groups are as they were at the write.** Removing a binding takes effect within the cache interval; removing someone from an identity provider's group waits for the next write.
- **Teams cannot give themselves either.** Both are decided where the Package lives, where for vela-system only the platform binds.

**What it does not cover.** Whoever may create Applications in team-a uses what team-a's definitions expose, within those definitions' own restrictions. Offline tools render from schemas and call nothing, so they are not checked. Anyone who can label a namespace can bring it into a selector, as for definitions. `spec.restrictions` is changed by whoever may update the Package.

## Measured

Renders through the compiler, with `n` packages the size of `vela/kube` installed in vela-system, and a team-a owner. Each template with a call makes one call to a pure provider.

| Template | n | Today | Selected | Allocated, today / selected |
| --- | --- | --- | --- | --- |
| `webservice` (imports nothing) | 0 | 762 to 809 µs | 775 to 812 µs | 831 / 830 KB |
| | 20 | 795 to 840 µs | 797 to 813 µs | 835 / 830 KB |
| | 200 | 826 to 847 µs | 762 to 835 µs | 875 / 830 KB |
| one call to `vela/base64` | 0 | 63 to 68 µs | 47 to 54 µs | 86 / 70 KB |
| | 20 | 118 to 124 µs | 48 to 57 µs | 96 / 69 KB |
| | 200 | 557 to 589 µs | 44 to 48 µs | 186 / 69 KB |
| one call to an installed package | 20 | 152 to 162 µs | 85 to 96 µs | 133 / 113 KB |
| | 200 | 590 to 614 µs | 85 to 96 µs | 224 / 113 KB |

Ranges drop one outlier per row. Today, a render that makes a call grows by about 2.6 µs for every package installed, whether it imports it or not; with 200 installed, a one-call step costs twelve times what it does selected. Selected, it stays flat, and is cheaper even with nothing installed, since the built-ins a template does not import are no longer scanned. A render with no calls barely moves either way: CUE builds only what it references, and the call scan stops at the first question.

This is the phase 1 acceptance test, re-run in the controller against today's render time.

## Restrictions, prototyped

The check was prototyped in the compiler with real RBAC on k3s v1.35: 17 cases (namespaces, selectors, a function entry narrowing its package, roles through each kind of binding, grants through a group, a team's wildcard role, no writer, no owner, and the four rows above) plus the same call under `@concurrency(4)`, which is refused before any call runs. Repeat renders within the recheck interval ask the API server nothing. The writer webhook was prototyped in kubevela, on every definition kind and on `Package`, with a metadata-only update keeping the stamp; so was membership through informers, where a binding made later is seen with no request per check. On k3d, with the chart installed, the stamp held through impersonated users: on create, over a claimed stamp, through a metadata-only update, on a spec change and its DefinitionRevision, on a `Package`, and as `application:<namespace>/<name>` for a definition vela-core dispatched. Admission feedback was prototyped in the spike: it reads each `<import>.#Definition` a source names and refuses what a call would, and sees nothing of a `#do` built by hand, which the call still checks.

The generated GitHub `githubwrite` package, restricted to grants, was then rendered against its generated Roles: `github-issues-write` reaches `issues/create` and not `issues/add-assignees`, adding `github-pull-requests-write` reaches both, `github-issues-read` reaches neither, and a function with no published permission needs a grant on the package.

### Built ahead of phase 1

The gate was then built for real, as a thin slice across the three repos, without selection, registries or Package status: `spec.restrictions`, the check in the compiler's dispatch, a cluster-backed `Authorizer` (bindings from the cache, grants by `SubjectAccessReview` remembered for `--cuex-access-recheck-interval`), the owner at every render (components, traits, policies, workflow steps and sources), the check installed on every vela-core compiler and on the WorkflowRun controller's, and Package admission refusing restrictions it cannot read. A compiler with no check refuses a restricted Package outright.

On k3d, with the generated `github` package restricted to grants and `repos/get` written as a workflow step in a team namespace by two impersonated users:

| Case | Result |
| --- | --- |
| A Package with `namespaces: ["team-["]` | refused at admission, naming `spec.restrictions.namespaces[0]` |
| The step written by a user with no grant | the step fails, naming the three grants any one of which would do |
| The step written by a member of a group bound to `github-metadata-read` | runs, and the repository lands in a ConfigMap |
| The Package also restricted to vela-system callers | refused: `it is restricted` |
| Someone without the grant edits the granted step's spec | the stamp is theirs now, and the call is refused as them |
| Restrictions removed | the ungranted user's step runs |

Components, traits, sources and the standalone WorkflowRun controller are covered by unit tests, each checked by removing its wiring and watching it fail.

## The shipped definitions

Every shipped definition under `vela-templates/definitions/internal` was rendered with all built-ins attached and with selection, with kubevela's provider CUE loaded as packages with no functions. 107 render identically. The other 60 fail both ways, because they import `vela/op`, which kubevela assembles at run time and the spike does not load. Phase 1 runs the same comparison inside kubevela, where `vela/op` exists.

## Found on the way

| Finding | Where it goes |
| --- | --- |
| The call scope names a versioned import wrongly and its calls never run | phase 1's `kubevela/pkg` fixes: the scope uses the import path's qualifier, as CUE does |
| An instance completed on first build races between renders | completed when the view is built |
| CUE writes each file's unresolved list on every build | upstream CUE; not ours to fix |
| An unimported broken package never broke a render | selection is argued on providers and cost, not blast radius |
| A provider wrapper cannot gate `@concurrency` calls: their decode gets no context | the check runs in the compiler's dispatch, phase 2 |
| Package files named only by path make a call's writer ambiguous | the loader names them by namespace and name, phase 1 |
| Loops answered before resolving (`WithOptimise`) call providers from a path of their own, a third beside one at a time and together | the gate travels in the compile's context and is set there too; each path has its own test |
| A source resolves inside its component's render, so it compiled as the component | each source compiles as its own SourceDefinition, or as no owner when that is unknown; an empty owner clears the outer one |
| With `authentication.enabled`, vela-core's ClusterRole had no rule for `cue.oam.dev` | the chart's role reads Packages |
| An `extends` chain compiles as the leaf, so a call in a parent is checked as the leaf's writer | fails closed but can refuse what the parent's writer could do; per-level owners come with design 01's `inherit.Level` change, phase 1 |
