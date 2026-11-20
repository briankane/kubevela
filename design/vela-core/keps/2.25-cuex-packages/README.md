# KEP-2.25: CueX Packages

**Status:** Proposed
**Parent:** [vNext Roadmap](../README.md)
**Depends on:** [KEP-2.18](../2.18-config-crds/README.md) (registries as Configs), [KEP-2.13](../2.13-addons/README.md) (registry kinds)
**Related:** [KEP-2.27](../2.27-cue-schema-compatibility/README.md) (the compatibility check), [KEP-2.11](../2.11-definition-testing/README.md) (definition tests), [KEP-2.16](../2.16-source-definition/README.md) (SourceDefinitions), [KEP-2.23](../2.23-plugins/README.md) (plugins), [KEP-2.29](../2.29-definition-access/README.md) (who may use a definition)
**Supersedes:** the earlier draft of this number, "`@uses`, CUE Imports from a Registry" (see [Alternatives](#per-definition-uses-resolution))

A CueX package is CUE, optionally with functions behind it. This KEP makes a package something you write in a project, test, publish to any registry KubeVela already knows, and install with one `Package` object, whatever runs its functions: nothing (a library), a WebAssembly module loaded into the controller, or a server the controller runs for you over HTTP or gRPC. A template imports it by path and major version, `import "acme.com/naming@v2"`, and only the packages a template imports take part in its compilation.

Design documents: [01, API and compile ownership](design/01-api.md); [02, registries, resolution and loading](design/02-registries.md); [03, WebAssembly](design/03-webassembly.md); [04, versions](design/04-versions.md); [05, servers](design/05-servers.md); [06, selection and visibility](design/06-selection.md); [07, phase 1](design/07-phase-1.md).

## Summary

```
vela cuex package init naming                 # a project: cuex.yaml, CUE, functions, tests
vela cuex package test                        # go test, build, then *_test.cue as vela def test runs them
vela cuex package push ghcr.io/acme/naming:2.1.0
kubectl apply -f naming.package.yaml          # the cluster loads it, or runs its server
```

```cue
import "acme.com/naming@v2"

name: naming.#DNSLabel & {$params: name: parameter.service}
```

| A package that is | Runs its functions | Suits |
| --- | --- | --- |
| a library | it has none: types, defaults, helpers | shared `#Labels`, presets, conventions |
| a WebAssembly module | in the controller, sandboxed, with nothing granted | logic CUE cannot express: parsing, naming, semver, CIDR maths, diffing |
| a server | in an Application the controller runs for it, over HTTP or gRPC | anything needing a network, client-go or a cloud SDK |

## Problem

CUE has an import system, and KubeVela only half uses it. A template's `import` reaches two things: packages compiled into the binary (`vela/kube`, `vela/http`, ...) and `cue.oam.dev/v1alpha1` `Package` objects. Everything about the second is hand-made:

- **The CUE lives in a YAML string** in the `Package`, so its source of truth leaves Git and is re-applied to every cluster by hand.
- **A provider is two things to keep in step.** The CUE is inline in the `Package`; the code is a server the platform deploys itself, at an endpoint it writes into the same object. Nothing checks the two agree.
- **`#provider` is hand-written** in the CUE and must equal the `Package`'s name, or every call fails.
- **The import namespace is global and flat.** There are no versions, so two versions cannot run side by side. Probed: two `Package` objects claiming one path both load, and which one a render sees depends on map order. No error.
- **Names collide, including with built-ins.** Providers are looked up by `metadata.name` alone, and external Packages are consulted after built-ins, so the last one wins: `naming` in two namespaces overwrite each other, and a `Package` named `kube` in any namespace replaces the real `vela/kube` for every render.
- **Anyone who can write a definition can call any provider.** A team's definition reaches vela-system's providers, and whatever key they hold, with no grant to say it may.
- **Every `Package` takes part in every render**, imported or not: its provider can answer another package's calls, and every render slows with each one installed.
- **Packages load at startup.** Watching for changes is off by default, so a new or changed `Package` is not seen until the controller restarts.
- **Packages cannot import packages.** A `Package`'s CUE is built on its own, so it cannot use `vela/kube`'s definitions or a shared types library.
- **Nothing reports.** A `Package` has no status. A failure is a controller log line, and the template author sees `connection refused` or `field not found`.
- **Compilers are anonymous.** The source compiler is carefully curated (its `vela/kube` cannot write), yet every external `Package` is loaded into it, which undoes the curation.
- **The HTTP client loses errors.** A response must be a JSON object, so a function returning a string fails; the status code is ignored, so a 500 with a text body surfaces as a parse error. gRPC is declared in the API and answers "not supported yet". The client skips TLS verification by default.
- **Nobody can see what is loaded.** VelaUX lists built-ins from the KubeVela library it was compiled against, which can differ from the controller it talks to; the CLI has nothing.
- **Writing a provider starts from nothing**: no scaffold, no build, no test loop, no publish step.

Library CUE has the same problems, plus copy-paste: every definition that wants a shared `#Labels` carries its own copy.

## Goals

- One concept, a **package**: CUE, optionally with functions, whatever runs them.
- One **layout** for a package, carried by **every registry kind KubeVela already supports** (Git, Gitee, GitLab, OSS, Helm repositories, OCI).
- **Versioned import paths** with majors side by side, minor and patch releases in place.
- **Only imported packages take part in a compilation.**
- **Named compilers** that a package targets explicitly.
- **A package can say where its functions may be called**, by namespace as a definition does, and optionally by RBAC grant, per package, group or function.
- **Status on every package**, built-ins included, read the same way by `kubectl`, the CLI and VelaUX.
- **A full authoring loop** in the CLI: scaffold, build, test, vet, render, publish.
- **Revisions record the package versions they used**, so a diff or an audit can say what changed.
- **Existing `Package` objects keep working** until they opt in.

## Non-Goals

- **Multi-cluster distribution.** This KEP covers a single cluster and the hub. When rendering moves to spokes (KEP-2.2), spokes must see the same Packages and resolved versions the hub did; how they get them belongs to the dispatcher work.
- **Modules shipping packages** (KEP-2.20). Later.
- **Plugins** (KEP-2.23). Plugins call operator CUE from Go, the reverse direction, and already offer packages under `vela/plugin/`. KEP-2.23 is to be revisited in the light of this one; see [Relationship to other KEPs](#relationship-to-other-keps).
- **Declared effects.** Functions do not declare whether they compute, read or write. A package targets compilers, and the only rules enforced are the ones that can be proved. See [Effects](#declared-effects).
- **CUE's own module system.** No `cue.mod`. Import paths follow its `@vN` shape, so adopting it later is not foreclosed; `vela/` and `local/` are KubeVela's own prefixes, as `vela/` already is.
- **A proxy for calling server packages from outside the cluster.** Offline tools render from schemas instead; see [Offline tools](#offline-tools).

## Mental model

```
 project            registry                cluster                         compilation
 -------            --------                -------                         -----------
 cuex.yaml          OCI / Helm / Git /      Package naming-v2               template:
 naming.cue  push   OSS: the package        spec.source: catalog,           import "acme.com/naming@v2"
 functions.go ───►  layout, pinned by  ───► naming@~2.1          ───────►         │
 *_test.cue         digest or commit         status: Ready, 2.1.3,                │ only what is
                                             sha256:..., functions               ▼ imported
                                                 │                         naming.#DNSLabel
                                                 ├─ wasm: loaded in-process
                                                 └─ server: an Application
```

## Concepts

### A package

| Kind | Layout | Functions run |
| --- | --- | --- |
| library | `cuex.yaml`, `*.cue` | none |
| wasm | `cuex.yaml`, `*.cue`, `module.wasm` | in every controller process, through the Extism runtime |
| server | `cuex.yaml`, `*.cue`, a server image pinned by digest | in an Application the controller creates |

`cuex.yaml` describes it:

```yaml
path: acme.com/naming          # the import path, without the major; `naming` names the package
version: 2.1.3                 # required; the major goes into the path
runtime: wasm                  # library | wasm | server
toolchain: tinygo              # wasm and server: how it is built
transport: http                # server only: http (default) | grpc
maturity: stable               # alpha | beta | stable
dependencies:                  # other packages its CUE imports, with a semver range
  - acme.com/types@v1 >=1.3
```

A package's CUE declares its functions as it does today, with `#do` naming the function. It never writes `#provider`: the host sets it to the package's identity, so a package cannot route calls to another.

```cue
// +usage=Turn any string into a valid DNS-1123 label
#DNSLabel: {
	#do: "dnsLabel"
	$params: {
		name:       string
		maxLength?: int & >0 & <=63
	}
	$returns?: string
}
```

### Examples

**A library**, read in place from Git:

```
platform-cue/            # a Git registry entry, "platform"
  labels/
    cuex.yaml            # path: acme.com/labels, version: 1.4.0, runtime: library
    labels.cue
```

```cue
// labels/labels.cue
package labels

_managed: "app.kubernetes.io/managed-by": "kubevela"

#Labels: {
	app: string
	out: _managed & {"app.kubernetes.io/name": app}
}
```

```yaml
kind: Package
metadata: {name: labels-v1, namespace: vela-system}
spec:
  source: {registry: platform, package: labels, version: "1.4.2"}
```

```cue
import "acme.com/labels@v1"

output: metadata: labels: (labels.#Labels & {app: context.name}).out
```

**A server**, reading a pull request's state from GitHub for a workflow step:

```yaml
# cuex.yaml
path: acme.com/github
version: 1.0.0
runtime: server
toolchain: go
```

```cue
// github.cue
#PullRequest: {
	#do: "pullRequest"
	$params: {repo: string, number: int}
	$returns?: {state: "open" | "closed" | "merged", mergeable: bool}
}
```

```go
// functions.go: plain Go, served over HTTP by the SDK
func pullRequest(ctx context.Context, p prParams) (prState, error) { ... }
```

```yaml
kind: Package
metadata: {name: github-v1, namespace: vela-system}
spec:
  source: {registry: catalog, package: github, version: "1.0.3"}
  compilers: [workflow]
  server: {replicas: 2, serviceAccountName: github}
```

### Registries

A registry is a `Config` of the `cuex-package-registry` `ConfigTemplate` (KEP-2.18): declarative, validated at admission, and applied from Git like anything else. Credentials come from a Secret through `fromSecrets`.

```yaml
apiVersion: config.oam.dev/v1alpha1
kind: Config
metadata:
  name: catalog
  namespace: vela-system
spec:
  templateRef:
    name: cuex-package-registry
  properties:
    type: oci                          # oci | helm | git | gitee | gitlab | oss
    url: oci://ghcr.io/acme/packages
    namespaces: [vela-system, team-a]  # who may use it; default vela-system only
  fromSecrets:
    - name: catalog-token              # the credential
```

- **Only Configs in vela-system are registries.** The list is the allowlist: a `Package` names one, so a cluster runs only what comes from registries the platform added, and a team namespace may use a registry only if it is in that registry's `namespaces`. `spec.pullSecrets` in the Package's namespace add credentials, never registries.
- **`vela cuex package registry add|update|remove|list`** writes and reads these Configs.
- **Addon and module registries** keep their ConfigMap store for now; this KEP proposes they move to Configs the same way, through 2.18's migration pattern, as a separate change.

| Registry kind | A package is | Pinned by |
| --- | --- | --- |
| OCI | a Helm chart of the layout, as modules are published | manifest digest |
| Helm repository | the same chart, listed in the index | the index entry's digest |
| Git, Gitee, GitLab | a directory, read in place, versioned by tags `<package>/vX.Y.Z` found with `git ls-remote` | commit SHA |
| OSS | a directory; no versions, so Packages pin a digest | content digest |

Fetching, checking and caching are in [design 02](design/02-registries.md). The kinds match addon registries', but reading a package is new work for most of them: today's component readers refuse Git, and module registries read OCI only. `vela cuex package push` publishes to OCI or a Helm repository. A WebAssembly module in Git works and is discouraged.

### Versions and import paths

The major version is part of the import path, CUE-style, and is always written, v0 and v1 included:

```cue
import (
	"acme.com/naming@v2"        // imports as naming
	old "acme.com/naming@v1"    // two majors side by side need an alias
)
```

- **Minor and patch releases replace a package in place.** A new major is a new `Package` beside the old one.
- **A `Package` pins a digest or follows a semver range** within its major, in Helm's range syntax. A range is resolved to a digest on a configurable interval and on every spec change; status records the version, the digest and when it was checked. In `@v0`, minors are breaking, so a range follows patches unless it is written out wider.
- **Ranges are checked, not trusted.** A release a range resolves to must accept every input the previous one did and return nothing it could not; one that fails is held, and the Package keeps serving with `Resolved` `False`, reason `Incompatible`. `vet --against` and `push` run the same check before publishing. A pinned digest skips it. Details in [design 04](design/04-versions.md).
- **Maturity is a field, not part of the path.** Importing an alpha or beta package is an admission warning, never a refusal; so is `vela cuex package vet` and `vela def test`.
- **A deprecated line keeps working.** A release or a `Package` may carry `deprecated: "use @v3"`; a definition that newly imports it gets a warning.
- **Bare paths** remain for built-ins and for `Package` objects written the old way, which may also add `@vN` to `spec.path` under the same rules. A legacy path is matched exactly, whatever its first element, as long as it is not one of CUE's standard library paths, which is `ReservedPath`.
- **Importing two majors of one package without aliases is refused** by `vet` and at admission. CUE itself reports the unused import, but the compiler drops that error and the reference resolves to nothing.

Packages are separately versioned artifacts, which is why their notation is CUE's `@vN` and not KEP-2.20's `/vN/` segments for definition API lines. Tested: `acme.com/naming@v2` imports as `naming`.

### Visibility

Packages follow definitions:

- **A `Package` in vela-system is cluster-wide.** Its path is under `vela/` or anywhere else.
- **A `Package` in any other namespace is local** to it, and imports under `local/`: a package published as `acme.com/naming` installed in team-a is `import "local/acme.com/naming@v1"` there. Only vela-system may use paths outside `local/`, and only vela-system may use `vela/`. The exception is a Package [written the old way](#packages-written-the-old-way) while `--allow-namespaced-legacy-packages` is on: it keeps its bare path and sits in vela-system's scope, so a clash with vela-system is decided by age.
- **The definition's namespace decides what it sees**, not the Application's. A vela-system definition sees only vela-system packages, so it renders the same in every namespace. A team's definition sees its namespace's and vela-system's.
- **A path means one package wherever it is visible**, and there is no lookup order between namespaces. A team's packages and vela-system's cannot collide, since one is under `local/` and the other cannot be; two teams may share a `local/` path, since neither sees the other's. The import says where a package comes from, and moving a package into vela-system is dropping `local/` from its importers.

### Conflicts

A `Package` claiming a path already held in its own namespace is refused: Ready is `False` with reason `Conflict`, naming the holder; the holder, the older of the two, keeps serving. Built-in paths cannot be claimed at all; `vela/` is vela-system's and `local/` is everyone else's (`ReservedPath`). A `Package` may alias the path its source declares with `spec.path`, which is how an artifact published as `vela/naming` is installed in a team namespace (as `local/` plus the alias).

### Restricted functions

Visibility says what a namespace can see. A `Package` can also say where its functions may be called, with `spec.restrictions`, the same shape a definition uses to say which namespaces may use it. The installer writes the restrictions; the author tags functions with groups they can name:

```yaml
# The Package: the platform decides where its functions may be called
spec:
  restrictions:                                 # a definition's fields: namespaces, namespaceSelector
    namespaces: [team-*, vela-system]           # whose definitions may import it at all
    functions:                                  # the first entry matching a function applies
      - match: [group:administration, repos/delete]
        namespaces: [platform-ops]
        requireRoles: [{kind: ClusterRole, name: github-admins}]   # and its writer is a member
        # requireGrant: true                    # or holds RBAC `call` on it
      - match: [group:issues, group:pulls]
        namespaceSelector: {matchLabels: {github-write: "true"}}
```

```cue
// The package's CUE: the author tags each function with a group
#IssuesCreate: {
	#do:       "issues/create"
	$group:    "issues"            // optional; the name restrictions and grants can use
	$requires: {issues: "write"}   // what the server's key needs
	...
}
```

- **The namespace decides first.** A call is allowed where the importer's namespace (the definition's, or a depending `Package`'s) matches the package's restrictions and the first function entry that matches. Absent restrictions mean anywhere, and vela-system is matched like any other namespace, as it is for definitions.
- **A role or a grant is optional, per entry**, for telling people apart within a namespace; most restrictions need only the namespace. Whoever last changed the importer's spec must then be a member of one of `requireRoles` (bound by a ClusterRoleBinding, or a RoleBinding in the Package's namespace), or, with `requireGrant`, hold the RBAC verb `call` on the package, the function's group or the function, checked in the Package's namespace. Either is decided where the Package lives, so a team's own namespace-admin role cannot satisfy it. An entry with both needs both.
- **The call is the gate.** Each call to a restricted function is checked as the provider dispatches it. A template cannot get round it by building its own `#do`, and a definition admitted before the package existed, or whose namespace or writer has since lost access, fails its renders instead of calling. Admission and `vela def vet` check the functions a template refers to, as early feedback.
- **It gates the author, not the caller.** Whoever may write a definition in a namespace that may call `issues/create` chooses to expose it. Who may use that definition is the definition's own `spec.restrictions`, and `ValidateDefinitionPermissions`, a layer up.
- **A role or grant follows whoever writes**, person or service account. Both are best given to the GitOps service account, so definitions keep working as people come and go. When vela-core writes a definition as itself, from an Application's `k8s-objects` component, only an Application in vela-system (an addon) holds vela-core's roles and grants.
- **Groups are declared, not inferred.** `$requires` describes the key, which can change; `$group` names what a platform restricts. A generated package takes its groups from what its API publishes, as GitHub's permission categories give about 30 groups for 638 write functions.

The detail is in [design 06](design/06-selection.md#restricted-functions).

## Compilers and targets

Five compilers render CUE in vela-core, and each now has a name:

| Name | Is | Renders | Loads `Package` objects today |
| --- | --- | --- | --- |
| `workload` | `WorkloadCompiler` | components, traits | yes |
| `source` | `SourceCompiler` | SourceDefinitions | yes, at startup only |
| `workflow` | the workflow providers compiler | workflow steps; also validation and schema generation for every definition kind (`vela def`, VelaUX forms), dry-run | yes |
| `default` | `cuex.DefaultCompiler`, guarded | application policies at reconcile, admission of non-workload policies, `vela cuex eval`, adopt | yes |
| `config` | `ConfigCompiler` | Config scripts | no |

The standalone WorkflowRun controller has its own compiler, which also loads every `Package`; it is `workflow` there.

A `Package` lists the compilers it targets:

```yaml
spec:
  compilers: [source, workflow]
```

The rules are a policy on external packages, kept to what can be stated without classifying every function. Built-ins are outside it: `workload` already carries network and side-effecting built-ins (`vela/http`, `vela/kube`, `vela/helm`), vetted with KubeVela itself.

| Kind | May target | With no `spec.compilers` |
| --- | --- | --- |
| library | any | all five |
| wasm | any: a module with no host functions can only compute | all five |
| server | any but `workload` and `default`, which render components and policies at reconcile, where a network call cannot be shown to be repeatable | **refused** until it lists them |

**Validation sees every target.** `workflow` also validates and generates schemas for every definition kind, so a library targeting only `workload` would otherwise break the OpenAPI schema of every component that imports it. Validation and schema generation therefore compile with every package any target lists, schema only: no function is called.

A package that reads and one that writes are simply two packages, `acme.com/github@v1` and `acme.com/githubwrite@v1`, aimed at different compilers. That is how the built-ins already work: `vela/kube` for workflows, a read-only `vela/kube` for sources.

**Built-ins differ per compiler**, and discovery shows them per compiler: `vela/http` under `workload` has `#Get`, under `workflow` `#HTTPGet`.

### Only what a template imports

The compiler reads a template's `import` declarations and attaches only those packages, and the packages they depend on, to that compilation, and only their providers can answer its calls. A package nothing imports cannot take over a call or slow a render, and a render cannot depend on what another definition loaded first. Details in [design 06](design/06-selection.md).

A package's own imports are wired the same way, per compilation, so a package can import built-ins and other packages it declares in `dependencies`. They resolve exactly as a definition's imports in the package's namespace do: a bare path is vela-system's, `local/` is its own namespace's. Nothing is rewritten, so a package that imports `local/` can be installed only outside vela-system, and one meant for either imports only vela-system packages. Cycles are refused.

A dependency is a Package that must already exist where the dependent can see it; nothing is installed on its behalf. Each names a semver range (`>=1.3`, `~1.3`, `1.3.2`), checked against the one version of that path loaded, since a namespace loads one version per major. A dependency that is missing leaves the dependent waiting (`DependencyMissing`); one that moves outside the range, from phase 4, makes it not Ready (`DependencyUnsatisfied`, naming both), and its importers fail retryably until either side changes. A minimum is the usual choice: a tight pin breaks as soon as the dependency moves.

**It costs less than today.** Every compilation still shares instances built once, since building them per compile costs about eight times as much: they are built when a Package changes, not when a template renders. Selecting from them is a lookup, so it needs no cache. Today a render that makes a provider call slows with every package installed, because the compiler's scan for calls walks all of them; measured through the compiler, a one-call step takes 557 to 589 µs with 200 packages installed and 44 to 48 µs selected, flat whatever is installed. Phase 1 re-runs the benchmark in the controller as its acceptance test.

**Every compilation says whose it is.** Visibility depends on the definition's namespace, so the definition being rendered (its kind, namespace and name) travels in the context every compile already takes, set where definitions are loaded and per level of an `extends` chain, rather than at each of the sixty or so call sites that compile them. A compilation without one sees vela-system packages only. A call site that forgets therefore fails safe: it sees less, never another namespace's packages. Schema generation and validation of a team's definition must pass it too, or the team's own imports fail there; phase 1 lists and covers every such call site.

## The Package resource

`cue.oam.dev/v1alpha1` gains optional fields and a status subresource. Nothing about existing objects changes.

```yaml
apiVersion: cue.oam.dev/v1alpha1
kind: Package
metadata:
  name: naming-v2
  namespace: vela-system
spec:
  source:
    registry: catalog                # a registry Config in vela-system
    package: naming                  # repository, chart or directory within it
    version: "~2.1"                  # a semver range within the major, or
    # digest: sha256:...             # a pinned digest or commit
  path: acme.com/naming              # optional alias for the source's path
  compilers: [workload, workflow]    # optional for library and wasm
  restrictions:                      # optional; where its functions may be called
    namespaces: [team-*, vela-system]
    functions: [{match: [group:admin], namespaces: [platform-ops], requireRoles: [{kind: ClusterRole, name: github-admins}]}]
  pullSecrets: [{name: acme-registry}]
  wasm:                              # runtime wasm only, all optional
    memory: 32Mi                     # per instance, within the namespace's caps
    timeout: 2s
    instances: 4
  server:                            # runtime server only, all optional
    replicas: 2
    resources: {...}
    serviceAccountName: naming
    secrets: [{name: naming-key}]   # the server's own credentials, mounted
status:
  conditions:
    - {type: Ready, status: "True"}  # also Resolved, Loaded, ServerReady, Progressing,
                                     # Unchecked, Legacy, Deprecated, InCluster
    # Ready False reasons: Conflict, ReservedPath, DependencyCycle, DependencyMissing,
    #   DependencyUnsatisfied, InvalidCUE, LegacyNotAllowed
    # Resolved False: Incompatible; ServerReady False: Starting, ImageNotAllowed,
    #   TemplateMismatch, InvalidServerDefinition
  path: acme.com/naming@v2
  version: 2.1.3
  digest: sha256:4f2c...
  checkedAt: "2026-11-18T10:14:02Z"
  runtime: wasm
  maturity: stable
  dependencies: [{path: acme.com/types@v1, digest: sha256:9a1e...}]
  endpoint: naming-v2-4f2c1a9e.vela-system.svc:9443   # server only
  compilers:
    - name: workload
      functions:
        - do: dnsLabel
          definition: "#DNSLabel"
          description: Turn any string into a valid DNS-1123 label
          group: naming               # from $group, if the author set one
          restricted: false           # from spec.restrictions
          available: "true"           # server only: "true", "false" or "unknown", from Describe
          cue: |
            #DNSLabel: {
              #do: "dnsLabel"
              $params: {name: string, maxLength?: int & >0 & <=63}
              $returns?: string
            }
```

**Health and status CUE cannot import packages.** `healthPolicy`, `customStatus` and `status.details` compile on a bare CUE context, outside any compiler, so a package identifier there is an error telling the author to compute the value in the template and read it from output.

**A Package that is not Ready fails the renders that import it**, with its condition message as the error, rather than letting a half-loaded package produce a quieter failure later.

**Built-ins are Packages too.** vela-core creates a read-only `Package` for each built-in in vela-system, named `builtin-<name>` with no `spec.path` and no provider, a `cue.oam.dev/builtin` label, and its status (path, functions per compiler) written from the running binary by the leader. The name and the empty spec matter on a downgrade: a controller that predates this KEP loads them as empty packages that claim no path and shadow no provider. They are rewritten on upgrade and edits are refused at admission. `kubectl get packages -A` then lists everything a template can import. They describe the built-ins; they do not supply them. A render always uses the built-ins compiled into the process doing it, so during a rolling upgrade an old replica renders with its own built-ins while the leader rewrites the Packages.

### Admission

| A definition | Gets |
| --- | --- |
| importing a package that does not exist yet, or is not Ready | admitted with a warning; Applications using it fail with a retryable condition until it is Ready, then recover. Validation still compiles the template: only the unresolved import is downgraded to the warning, and any other error in the template is reported as usual |
| importing an alpha, beta or deprecated package | admitted with a warning |
| referring to a server function its Package's key cannot call | admitted with a warning naming what the key lacks |
| referring to a function its namespace may not call, or, where an entry requires a role or a grant, from someone without it | refused |
| importing two majors of one package without aliases | refused |
| a ComponentDefinition named `cuex-server` outside vela-system | refused: the name is vela-system's |

| A Package | Gets |
| --- | --- |
| claiming a held or built-in path | refused (`Conflict`) |
| claiming `vela/` outside vela-system, or `local/` in it | refused (`ReservedPath`) |
| naming a registry that does not exist, or that its namespace may not use | admitted, and waits with a condition until it can, so a registry and its Packages can arrive in one sync |
| a server with no `spec.compilers`, or targeting `workload` or `default` | refused |
| depending on a package whose functions it refers to and may not call, as for a definition | refused |
| restrictions with a glob or selector that does not parse | refused, as a definition's are |
| a restrictions entry naming no function or group the package has | admitted with a warning |
| deleted while definitions import it | allowed, with a warning listing them; their renders then fail with "package not found" |

### Packages written the old way

A `Package` with inline `templates` and an endpoint loads today into `workload`, `source`, `workflow` and `default`, once at startup. After this KEP:

- **Packages are always watched**, so a change is seen without a restart. This is a behaviour change: today watching is off by default.
- **In vela-system, legacy Packages keep working** as they do today until they set `spec.compilers`, with an `Unchecked` condition and a warning on apply.
- **Elsewhere, legacy Packages work only while a flag is on**: on by default for one release, off by default in the next, then removed. Meanwhile they carry a `Legacy` condition, and with the flag (`--allow-namespaced-legacy-packages`) off, Ready is `False` with reason `LegacyNotAllowed`. Until the flag is off, the security claims below do not hold for them: they are global, may name any endpoint, and skip the registry list.
- **Setting `spec.compilers` opts a legacy Package into every rule** above.

Changes that apply to legacy Packages either way, and are breaking:

| Change | Breaks | Mitigation |
| --- | --- | --- |
| TLS verified by default | providers on self-signed certificates | `spec.provider.insecureSkipVerify` per Package, opt-in, with a warning |
| A non-2xx response is an error | providers answering a failure as JSON with a 4xx and expecting it read as data | none needed beyond the release note; the body is in the error |
| Conflict refusal | two Packages already sharing a path, which today both load in map order | the older `Package` (by creation time) keeps the path; the other is Ready `False`, reason `Conflict` |
| No name shadowing | a Package named after a built-in, which today replaces it | the Package is Ready `False`, reason `Conflict` |

## Runtimes

### Library

No runtime. Its CUE is imported like any other package's.

### WebAssembly

Modules are [Extism](https://extism.org) plugins, run by wazero (already a dependency through CUE). Extism is kept behind `kubevela/pkg`'s `cue/cuex/wasm` package, so moving to a fork or to KubeVela's own host is a change to one package.

- **The package's CUE files are the contract; a module exports only functions**, one per `#do`, taking and returning JSON. The host takes the function list from the CUE and refuses a module missing an export or exporting one no `#do` names. Any language with an Extism SDK can write one; the Go SDK is a thin `Run[P, R](fn)` over Extism's Go PDK.
- **Nothing from the host.** No filesystem, no network, no environment, fake clocks and a fixed random source. The one host function, `cuex.log`, only takes output: a rate-limited line in the controller log at verbosity 4, and stderr under `vela cuex package test` and `render`. Extism's HTTP host function is never granted a host, and a call through it fails without breaking the instance.
- **Bounded.** A memory limit per instance and a deadline per call, defaulting by toolchain; a call that hits either retires its instance. Details in [design 03](design/03-webassembly.md).
- **A fresh instance per call** for TinyGo modules, so a function's result depends on its parameters alone. Modules from the standard Go toolchain take milliseconds to start and reuse instances instead, so their globals survive between calls and that guarantee is the author's to keep. The mode comes from the toolchain and travels in the package's metadata.
- **Limited per namespace.** Each loaded module costs memory in every controller replica (about 8 MiB for TinyGo, far more for Go), so the number of WebAssembly Packages and their memory are capped per namespace. A Package may set its own `spec.wasm` memory, timeout and instances within those caps. Controller flags set the defaults, with package size, dependencies per package and dependency depth; an annotation on the Namespace, which only the platform can set, overrides them. The defaults come from phase 3's benchmarks.
- **ABI checked.** `build` records the Extism ABI a module was built against, and the host refuses one it does not support, so a host upgrade cannot quietly break an old module.
- **Cached on disk** by digest, module and compiled code both, so a restart neither pulls nor compiles again.

Measured on the prototype (Apple M-series, 200 calls):

| | TinyGo, fresh instance | TinyGo, reused | Go, reused |
| --- | --- | --- | --- |
| Module size | 0.6 to 0.8 MiB | | 4.3 MiB |
| One call | 130 to 250 µs | 18 to 39 µs | 52 to 96 µs |
| Load after a restart, from the disk cache | 7 to 10 ms | | 38 ms |
| Controller memory per loaded module | about 8 MiB | | about 60 MiB with Kubernetes API types |

TinyGo needs `-scheduler=none`: without it, its goroutine support adds milliseconds to every call.

WebAssembly suits functions small in dependencies, not in logic. A module importing `k8s.io/api` builds only with the standard toolchain, at 17 MiB; declaring the few fields a function reads keeps it under a megabyte.

### Server

The controller runs the server through an Application: one per server Package, in its namespace, owned by the Package, whose component is of type `cuex-server`. KubeVela deploys it like any other Application, so it shows in `vela status` and VelaUX, and the Package controller never creates a Deployment itself. Details in [design 05](design/05-servers.md#what-the-controller-creates).

- **Shaped by a definition the platform owns.** `cuex-server` ships in vela-system and renders a Deployment, a Service, a NetworkPolicy admitting ingress only from vela-core (on a CNI that enforces policies), and a PodDisruptionBudget and HPA when scaled. It runs Pod Security "restricted" with egress open, since that is why a package is a server. The platform may edit it like any definition; it may import only built-ins, and no other namespace may define a `cuex-server`.
- **Scaled by its parameters.** `spec.server.replicas`, or `spec.server.autoscaling` (`minReplicas`, `maxReplicas`, `targetCPUUtilization`). Images are built for amd64 and arm64. A server counts against its namespace's ResourceQuota like any workload, so it has no cap of its own.
- **Owned by the controller.** The Package controller reconciles the Application's spec and reverts edits to it; the Application controller reverts drift in what it renders, the image included. So the image that runs is the one the registry and the `Describe` check approved. `spec.server.serviceAccountName` must name an account in the Package's namespace, which a team could use for its own pods anyway.
- **Its key is the Package's.** `spec.server.secrets` mounts Secrets from the Package's namespace into the server, and vela-core never reads them. One image on two keys is two Packages; a team wanting its own key installs its own copy under `local/`. A function may declare what the key needs (`$requires`), and the server reports in `Describe` what its key cannot call, so status, `vela cuex package show`, admission and `vet` show it before anything runs, and a call to it fails without reaching the server. Details in [design 05](design/05-servers.md#credentials-and-what-a-server-can-call).
- **Images come from where the registry allows.** A registry Config lists the image repositories its packages may run (`images`, defaulting to an OCI registry's own repository); a package whose image is elsewhere is `ServerReady` `False`, reason `ImageNotAllowed`.
- **Calls use mutual TLS.** The controller keeps a CA in vela-system and issues each server a certificate for its Service name, and vela-core a client certificate. The SDK accepts only vela-core's, and vela-core accepts only the certificate for that Package's Service, so neither a team nor another server can stand in for either side. This replaces the `header` field legacy Packages use.
- **Ready means matching.** A server answers `Describe` with the CUE files built into it; the Package is Ready only when they match the package's own. The controller canonicalises both sides itself (comments and layout removed, files in any order), so formatting, or a server built with another CUE version, never trips it. An image built from another commit is `ServerReady` `False`, reason `TemplateMismatch`, not a silent drift.
- **Updates start beside the old version.** A new version's component starts beside the old one in the same Application, becomes healthy and passes the check, then the CUE and the endpoint switch together and the old one drains and is removed. The Package stays Ready throughout, with `Progressing` naming the new digest.

Two transports, one SDK. `kubevela/pkg`'s server SDK serves the same registered functions over either, and answers `Describe` from the embedded CUE. Errors carry one of three codes on both: `InvalidArgument` (the parameters; fails again), `Unavailable` (retryable) and `Internal`.

**HTTP** (the default): `POST /cuex/call/<function>` with `$params` as JSON, answered with `$returns` as any JSON value, plus `GET /cuex/describe` and `GET /cuex/healthz`. The render's deadline travels in a header. The client pools connections and the SDK closes old ones, so calls reach replicas added later.

**gRPC**, for packages returning large results, about 25% faster than HTTP at 64 KiB and level with it on small calls. One service for every package, so the CUE stays the only contract:

```proto
service Provider {
  rpc Describe(DescribeRequest) returns (Description);  // CUE files, functions, version
  rpc Call(CallRequest) returns (CallResponse);         // {function, params: JSON} -> {returns: JSON}
}
```

Its Service is headless and the client balances calls across its pods, since kube-proxy balances connections and gRPC keeps one.

**Legacy Packages** get the client fixes: TLS verified by default and a non-2xx response reported with its body in phase 1, then any JSON result, the shared connection pool and a size limit with servers.

## Revisions

A revision records the digest of every package its renders used, so `vela live-diff`, an audit or a reviewer can see which package versions produced it and what changed between two revisions.

Re-rendering a stored revision uses the package versions current at the time. Reproducing an old revision exactly, with the package content it used, needs a store for old content, several versions of one path loaded at once and protection for content a revision references; that is a follow-up, not part of this KEP.

A Package following a range behaves like a definition update when it resolves a new version: live Applications that import it re-render on their next reconcile and get a new revision if their output changed. Pin a digest to freeze. A SourceDefinition's cache key includes the digests of the packages its template imports, so a new version is not served stale results until the TTL expires.

## Offline tools

`vela dry-run`, `vela def render`, CI and VelaUX previews run outside the cluster's network, so:

- **Library and WebAssembly packages run locally, for real.** They are fetched with the user's own credentials (their docker config, or `--registry-config`), since the registry Secrets in vela-system are the controller's. A package the user cannot fetch renders from its schema, like a server package, with the reason stated.
- **Server packages render from their schema.** `$params` are checked against the function's CUE and `$returns` stays its declared type, marked unresolved:

```cue
greeting: naming.#Hello & {$params: name: "team"}
// offline: greeting: $returns: string    (unresolved: acme.com/naming@v1 runs as a server)
```

An offline render therefore runs in a mode that accepts unresolved values and lists them, where today's dry-run would fail on anything non-concrete; a value derived from an unresolved `$returns`, such as a name built from it, is reported as unresolved rather than as an error. Exact values come from a render in the cluster or from mocks in tests.

## High availability

Every controller replica renders (the webhooks run on all of them), so every replica loads packages, while only the leader writes status, resolves ranges and runs servers.

- **Only the leader resolves.** Replicas load what `status.digest` names, so a cold replica resolves nothing; the leader resolves versions to digests, and from phase 4 re-resolves ranges on each registry's interval, with jitter and a concurrency cap per registry.
- **Libraries survive a registry outage.** A library's CUE is also kept in a ConfigMap per Package and digest, owned by the Package, so a replica restarting while the registry is down still loads it. WebAssembly modules rely on the disk cache, which an emptyDir loses on restart; a library too large for a ConfigMap does too, with a condition saying so.
- **Loading happens in the background.** A replica fetches and compiles modules from its informer, never inside an admission request or a render, so a cold replica cannot stall a webhook past its timeout. Fetches for different packages run in parallel.
- **Status names the target; each replica reports what it has.** `status.digest` is the version every replica must serve. A replica that has not loaded it yet fails renders importing that package with a retryable condition rather than using an older one, so admission on one replica and a render on another never disagree about the version. Each replica reports the digests it has loaded, and failures to load, as metrics and events.

## Observability

Metrics per package: load time and failures, call count, latency and errors per function, instance pool use for WebAssembly, and the age of a range's last resolution. Events on the Package for every resolution, rollout step and failed load.

## Discovery

```
vela cuex compiler list                               # the five compilers, what uses each, package counts
vela cuex compiler show workload [-n team-a]          # every package loaded there, built-in or not
vela cuex package list [-n team-a] [--compiler X]     # what a definition in team-a can import
vela cuex package show acme.com/naming@v2             # functions, their CUE, source, digest, compilers, dependencies
vela cuex package show vela/kube --compiler workflow  # built-ins per compiler
```

Everything is read from Package status, so the CLI, VelaUX and `kubectl` agree with the controller. Schemas print as CUE, or `-o openapi`. `-n` shows what a definition in that namespace sees. Without a cluster, the commands show the CLI's own built-ins under a banner saying so, and `package show` reads a project directory or a package file.

## Authoring

`vela cuex package` (alias `pkg`), beside the existing `vela cuex eval`:

| Command | Does |
| --- | --- |
| `init <dir>` | a project: `cuex.yaml`, the package's CUE in its own file, functions as plain Go with a Go test, a `*_test.cue` file and an example |
| `build` | in the toolchain's container image, pinned by digest, with any docker-compatible runtime; the same source builds the same digest anywhere |
| `test` | `go test` in the container, a build, then the `*_test.cue` files as `vela def test` runs them, with the package; a server project's server runs in a container for it |
| `vet` | loads the package as the controller would: CUE parses, every `#do` has an export, no `#provider`, no unaliased majors |
| `render <file>` | renders CUE against local packages |
| `push <ref>` | publishes to OCI or a Helm repository and prints the `Package` |
| `registry add\|update\|remove\|list` | the registry Configs in vela-system |

**Addons ship packages as resources**: a registry Config and the Packages naming it, installed in vela-system with the rest of the addon. The package keeps its own version, apart from the addon's.

**defkit** already writes imports (`WithImports`) and calls built-ins (`Builtin` in workflow steps, typed builders such as `KubeRead`). It gains a generic call usable in every definition kind, which adds the import itself:

```go
label := defkit.Package("acme.com/naming@v2").Call("#DNSLabel", map[string]defkit.Value{"name": defkit.ParamRef("name")})
```

Typed Go bindings generated from a package's CUE are a follow-up.

Toolchains are a table: Go and TinyGo first. Adding a language is an entry naming its builder image and build command, plus an SDK, which Extism already provides for most languages. A server project's Dockerfile is scaffolded on a pinned base, and the project may edit it.

### Tests

Package tests are `vela def test` cases, so labels, `@pending`, the Ginkgo-compatible filters and the report are the ones definition authors already use:

```cue
import (
	"vela/test"
	"acme.com/naming@v2"
)

"keeps a valid name": test.#PackageCall & {
	call: naming.#DNSLabel & {$params: name: "payments"}
	expect: returns: "payments"
}

"refuses an empty name": test.#PackageCall & {
	call: naming.#DNSLabel & {$params: name: ""}
	expect: error: =~"no characters"
}
```

`vela def test --package <dir|file|ref>` makes a package importable by any test file, so a definition that calls it is tested with the real thing, unmocked.

## Security considerations

These hold for Packages with `spec.source`, and for legacy Packages once the legacy flag is off (see [above](#packages-written-the-old-way)).

- **A package is code the controller runs or calls.** The registry Configs are the boundary: only someone who can write Configs in vela-system adds a registry, a `Package` cannot name a URL, and a team namespace can use only the registries that list it. Signature verification per registry follows in hardening: a registry Config gains `verify`, with public keys from Secrets or a keyless identity (issuer and subject), and a package from a verifying registry that is unsigned or signed otherwise is refused. Git registries verify signed tags against keys.
- **A team using a registry uses the platform's credential for it**, so an allowed registry is allowed whole. A platform that wants teams kept to part of a registry gives them a registry entry of their own.
- **WebAssembly is sandboxed by construction**: no host capability is granted, memory and time are bounded, and with fresh instances a module's result cannot depend on anything but its parameters. Its cost is capped per namespace.
- **A server has the network and its own identity**, which is why it never enters `workload` or `default`, runs locked down and owned by the controller, accepts calls only from vela-core over mutual TLS, runs only images its registry allows, and must match its CUE before it is Ready. `Describe` is self-reported: it catches a build that does not match, not a server built to lie, which is what the registry boundary and signatures are for.
- **Local packages stay local.** Whoever may create Packages in a namespace takes its risk there and nowhere else. A team's package cannot change what a platform definition renders, because a vela-system definition never sees it; it imports under `local/`, so it can never take or block a path the platform uses; and no package can shadow a built-in.
- **A restricted function is called only where it is allowed.** Its key, or whatever else it guards, is reached only by definitions and packages in namespaces its restrictions match, and, where an entry requires a role or a grant, whose last writer has it; checked at every call, with admission as early feedback. Roles and grants are decided where the Package lives, so for a vela-system package only the platform can give them: a team's wildcard role in its own namespace counts for neither. With authentication off, anyone who may create an Application can have vela-core do what cluster-admin can, so restricted functions close the easy path there, not that one.
- **A server Package grants nothing its author could not already do.** Admission checks, by SubjectAccessReview, that whoever creates or changes the server spec of a Package may create Deployments in its namespace, since the controller creates one for it and `spec.server.serviceAccountName` can name any service account there.
- **The external provider client verifies TLS by default** from this change on.

## Compatibility

- **Legacy Packages**: see [Packages written the old way](#packages-written-the-old-way), including what breaks either way.
- **The CRD changes.** `spec.path` and `spec.templates` are required today and must become optional; `compilers`, `provider.insecureSkipVerify` and a status subresource are added in phase 1, and `source`, `pullSecrets`, `wasm` and `server` each with the phase that reads them. `helm upgrade` never updates a chart's CRDs, so until they are re-applied the API server prunes the new fields or refuses the new-style Packages; the upgrade notes say to re-apply them. The chart's CRD already lacks `provider.header`, which `kubevela/pkg`'s types have, and is regenerated from them.
- **`kubevela/pkg` fixes found on the way**: internal packages are keyed by path in one place and by name in another, cannot be removed, providers are looked up by name with externals overriding built-ins, the external client skips TLS verification by default and reads a non-2xx response as data, and the call scope names a versioned import by its last path element (`naming@v1`), so its calls never run. All are fixed in the first phase ([design 07](design/07-phase-1.md#the-kubevelapkg-fixes)).
- **Two Packages written the old way with one name**, a team's and vela-system's, both imported by one template, fail that render naming both: their hand-written `#provider` cannot tell them apart. Each still works where imported alone.
- **`ApplicationRevision`** gains the package digests it rendered with. `kubevela-core-api` picks the field up on its release-aligned sync.
- **Landing order**: `kubevela/pkg`, then `workflow` (whose WorkflowRun controller has its own compiler and loads every Package), then `kubevela`, each pinning the previous.

## Test plan

- Unit tests per runtime, per registry kind and per admission rule.
- e2e for restricted functions: package and function entries by name, glob and selector; a required role through a ClusterRoleBinding, a RoleBinding in the Package's namespace, and one in the importer's namespace that does not count; a required grant at each of its three levels, and a wildcard role in the importer's namespace that does not satisfy it; refused at admission and, for a hand-built `#do`, at the call; a render refused once a namespace is relabelled out or the writer loses the role; a writer stamp that cannot be forged, and a metadata-only change that keeps it.
- Benchmarks for per-compilation selection against today's render time, and every shipped definition rendered with and without selection, compared.
- e2e with the webhook and the controller on separate replicas, covering background loading, a replica that fails to load, and range resolution reaching replicas at different times.
- Upgrade from the previous release with legacy Packages in and out of vela-system, with and without re-applied CRDs; downgrade with built-in Packages present.
- A WorkflowRun controller test for the same Packages.
- e2e for a server Package: mutual TLS refusals, a rollout with calls running throughout and none failing, calls spreading across replicas after a scale-up, and drift reverted.

## Phases

| # | What |
| --- | --- |
| 1 | Foundations ([design 07](design/07-phase-1.md)): one loader per process with an always-on watch, the CRD changes, per-compilation selection with its benchmark, the definition compile option at every call site, named compilers and targets with the validation view, Package status, conflict refusal and no name shadowing, reserved paths, namespace visibility, built-ins as Packages, discovery, the legacy flag, the `kubevela/pkg` fixes |
| 2 | Registry Configs; package layout and `spec.source` from every registry kind; libraries, kept in-cluster; exact versions and digests, resolved by the leader; dependencies; restricted functions; revisions recording digests; background loading and per-replica reporting; defkit calls |
| 3 | WebAssembly on Extism, with the ABI check and per-namespace limits; `vela cuex package init/build/test/vet/render/push`; `test.#PackageCall` |
| 4 | Versions: `@vN`, ranges and their re-resolution, dependency ranges, the compatibility check, maturity and deprecation, `vela cuex package diff` |
| 5 | Servers over HTTP: SDK, the remaining client fixes (any JSON result, pool, size), mutual TLS, the server's Application and the `cuex-server` definition, server credentials and function availability, allowed images, the `Describe` check, side-by-side rollout, offline schema rendering |
| 6 | gRPC: the transport, a headless Service and client-side balancing |
| 7 | Hardening: signatures per registry, keys or keyless |

Each phase is useful on its own, and each lands in `kubevela/pkg`, then `workflow`, then `kubevela`.

## Alternatives considered

### Per-definition `@uses` resolution

The earlier draft of this KEP. A definition declares `@uses("catalog:networking/ingress.cue@v1.2.0")`; a pass before compilation fetches the files, builds them into a package at a content-addressed import path, injects the `import`, and locks the result into the revision. It isolates each definition's dependencies, pins per definition and needs no installation step.

It was set aside for one concept, not two. A library and a package with functions are the same thing from where a template sits: an import. Keeping libraries per definition and providers installed would mean two delivery paths, two version stories and a rule about which to reach for. The installed model answers `@uses`'s objections to it directly: only imported packages take part in a compilation, revisions record the digests they used, majors run side by side, and admission tolerates a package that does not exist yet (no install ordering). What it gives up is pinning a minor version per definition, and, until the follow-up lands, re-rendering an old revision with exactly the content it used, which `@uses` locks provided. `@uses` may return later as a convenience that installs what it names.

### Declared effects

Each function, or each package, declaring whether it computes, reads or writes, with each compiler capped at one. It was the first design, and it asks every author to classify every function, while the declaration of a server's function cannot be verified anyway. Targets say the same thing at the level people already reason about, as the built-in package sets do, and the two rules that can be proved (WebAssembly only computes, a server is never repeatable) are enforced without anyone declaring anything.

### KubeVela's own WebAssembly interface

Prototyped first: five exports (`cuex_alloc`, `cuex_free`, `cuex_template`, `cuex_functions`, `cuex_call`) and a Go SDK. No dependency, and a fresh instance per call about 80 µs cheaper. Every other language would need an SDK written for it. Extism provides those, and Helm 4 uses it for its plugins. Its Go host library is maintained slowly (no release since March 2025), which is why it is wrapped and why its HTTP capability is never granted.

### A proxy for offline tools

Tools outside the cluster calling server packages through vela-core, authorised by RBAC on the Package. It gives real values in dry-runs at the cost of a new remote-call surface on the controller. Schema-based rendering catches parameter and type errors with no network at all, and real values remain available in the cluster and through mocks.

### gRPC as the default transport

The first draft's choice, on the assumption that gRPC is faster. Measured, it is level with pooled HTTP/1.1 on small calls (16 µs against 14) and about 25% faster on 64 KiB results: payloads are JSON either way, so protobuf's encoding gains nothing. It also needs a headless Service and client-side balancing before a second replica takes any calls. HTTP is the default and stays curl-able; gRPC is there for packages with large results.

### A per-Package token

A shared secret the controller mounts into each server, sent on every call. Anyone who can read Secrets in the team's namespace can read it and call the server, and TLS is still needed besides. Mutual TLS with the controller's CA does both, and the client key never leaves vela-system.

### RBAC grants alone

An earlier version of this design checked only RBAC `call`, for whoever last wrote the importer. It needs a writer recorded on every definition and rules for every identity that writes one, and it answers a finer question than most platforms ask. KubeVela's tenancy unit is the namespace, and definitions already restrict by it, so a package does the same and keeps a role or a grant for the entries that need one. A named role reads as written and reuses roles a platform already has; a grant works with any authorizer, not only RBAC bindings. Both are offered.

### Whole packages only

Restricting a package whole works for read against write, but not for "issues, not administration". Names cannot be wildcards, in RBAC or in restrictions, so per function alone would mean listing hundreds; declared groups give the middle level.

### Groups inferred from `$requires`

`$requires` already sorts GitHub's functions by permission. It describes what a key needs, though, which is a different thing from what a platform grants, and it can name several permissions at once. A group is one name the author states.

### Checking only the import

Checking which packages a definition imports, at admission and render, was enough for whole-package grants. Per function it is not: a template can build `{#do: "repos/delete"}` against a package it imported for something harmless, so the check sits where the call is dispatched.

### Gating whoever triggers the call

Checking the user behind each Application at every call was considered. KubeVela already gates that layer: with `ValidateDefinitionPermissions`, an Application is admitted only if its creator may use each definition it references. A definition is where the decision to expose a function is made, so the writer is checked for the function and the caller for the definition.

### The controller creating the Deployment itself

The first design: the Package controller creating and reverting a Deployment, Service, NetworkPolicy, PodDisruptionBudget and HPA per server. It is a small deployer of its own, with a field on the Package for every way a platform might want a server shaped. An Application reuses KubeVela's apply, health, drift and garbage collection, puts the shape in a definition the platform already knows how to edit, and shows the server where every other workload shows.

### cert-manager

It would issue and renew the certificates, but it is an optional dependency KubeVela cannot assume. The controller issues the few it needs itself.

### A per-package gRPC service

Generating a proto per package would make the CUE and the proto two descriptions of one contract. The generic service carries JSON and keeps the CUE the only one.

### Splicing CUE in as text

Two libraries defining `#Config` collide, hidden fields leak, and errors point into a string that exists nowhere. Real imports give scoping and error attribution for free.

### CUE's module system

Where the ecosystem is going, and not usable here yet: it wants a filesystem and a module context, and a KubeVela template is one string in an object. Import paths follow its shape so that adopting it later is not foreclosed.

## Relationship to other KEPs

- **KEP-2.20**: definitions carry API lines as `/vN/` segments; packages, as separately versioned artifacts, use `@vN`. Both float on semver within a line and record what they resolved. Modules shipping packages is left to a follow-up.
- **KEP-2.23**: its offered packages under `vela/plugin/` and its `#Invoke` to a plugin's own service are candidates to become packages (a plugin's service as a server package; offered packages as Packages with versions and status). To be revisited once this KEP settles.
- **KEP-2.16**: SourceDefinitions render on the `source` compiler, which external packages enter only by targeting it.
- **KEP-2.11**: `test.#PackageCall` and `--package` extend the definition testing framework.
- **KEP-2.18**: package registries are Configs of a `ConfigTemplate`, and addon and module registries are proposed to follow.
- **KEP-2.13**: the registry kinds packages share with addons and modules.
- **KEP-2.29** (an alternative to KEP-2.26): who may use a definition, with the same `requireRoles` and `requireGrant` on a definition's `spec.restrictions`, checked for the Application's creator. Package restrictions are the same question one layer down, and share the matcher and the membership check.

## Open questions

1. **Exact reproduction of old revisions**: a content store, side-by-side loading of minor versions, and deletion protection, as a follow-up.
