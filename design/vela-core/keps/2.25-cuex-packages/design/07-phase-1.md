# KEP-2.25 design 07: phase 1, foundations

The plan for the [KEP](../README.md)'s first phase: what changes for a user, how it is built, and the order it lands in across `kubevela/pkg`, `workflow` and `kubevela`. It brings no new runtime and no registries. Every Package in phase 1 carries its CUE inline, as today, or is a built-in; one that sets `spec.compilers` opts into the new rules. What changes is how Packages are loaded, seen, refused and described.

## What changes for a user

| | Today | After phase 1 |
| --- | --- | --- |
| A Package is created or changed | seen after a controller restart, since the watch is off by default | seen within seconds; the watch is always on |
| Two Packages claim one path | both load, and map order decides which a render gets | the older keeps it; the other is Ready `False`, reason `Conflict`, naming the holder |
| A Package is named after a built-in (`kube`) | it replaces `vela/kube`'s provider | it is Ready `False`, reason `Conflict` |
| A team's Package that sets `spec.compilers` | n/a | importable only from definitions in its namespace, as `local/` plus its path, and only on the compilers it lists |
| A team's Package written the old way | importable from every definition | the same while the legacy flag is on, with a warning condition |
| `kubectl get packages -A` | the Packages people applied | those, plus a read-only `builtin-<name>` per built-in, all with status: path, conditions, functions per compiler |
| A render that makes one call, 200 Packages installed | 557 to 589 µs | about 45 µs, whatever is installed |
| Finding what a definition can import | read the controller's source | `vela cuex package list -n team-a` |
| A package identifier in `healthPolicy` | an unresolved-reference error | `packages are not available in healthPolicy; compute the value in the template and read it from output` |

## Not in phase 1

`spec.source` and registries (phase 2), dependencies and revision digests (phase 2), dependency ranges (4), WebAssembly (3), `@vN` resolution and the compatibility check (4), servers (5, 6), signatures (7).

**The CRD gains only what phase 1 uses**: `spec.compilers`, `spec.provider.insecureSkipVerify`, optional `spec.path` and `spec.templates`, and the status subresource. `source`, `pullSecrets`, `wasm` and `server` arrive with the phase that reads them, since a field the API server accepts and the controller ignores is a field someone sets and trusts. The CRDs need re-applying on each upgrade anyway, since `helm upgrade` never updates them.

## One loader per process

Today each compiler keeps its own `PackageManager`: four of them list every Package at startup, three may each run their own informer, and each parses every Package again.

```
today                                          phase 1

WorkloadCompiler   list, informer if flagged   the manager's Package informer, always on,
                                               shared with the leader's Package controller
SourceCompiler     list once                           |
workflow compiler  list, informer if flagged   loader: parse each Package once,
DefaultCompiler    list, informer if flagged           build a view on each change
ConfigCompiler     built-ins only                      |
                                               +-------+-------+--------+--------+
                                               workload source workflow default config
                                               each selects from the one view, by target
```

`kubevela/pkg` gains a `Loader` holding the current view (design 06). A compiler is built with `loader.For("workload")`, a `Selector` that sees only Packages targeting that compiler; built-ins stay per compiler, compiled into the process. `workflow`'s WorkflowRun controller builds the same loader for its one compiler.

**One watch per process.** The `Loader` owns no informer: it registers an event handler on one it is given. kubevela and the WorkflowRun controller pass their controller-runtime manager's Package informer, which runs on every replica, leader or not, and is the same one the leader's Package controller reconciles from. Leader election gates the controller, never the cache, so a follower's view is as current as the leader's. Offline tools have no manager and list once, or load built-ins only with `--enable-external-cue-package=false`.

| Rule | Why |
| --- | --- |
| **Every replica builds the same view.** Packages are admitted in order of creation time, then namespace and name, so the older of two keeps a path everywhere | a definition admitted on one replica must render on another |
| **A replica serves no webhook until its first view is built** | today's synchronous list at startup gives this; an informer does not, and a cold replica would refuse every definition that imports a package |
| **A Package not targeting a compiler is invisible to it**, with the reason: `acme.com/x@v1 does not target source (it targets workload, workflow)` | targets are the policy; a near miss should say so |
| **A path means one package wherever it is visible, across every compiler**: a Package outside vela-system imports under `local/`, so it never meets vela-system's | a read and a write variant are two paths (`acme.com/github`, `acme.com/githubwrite`), as the KEP has it |
| **A dependency must target every compiler its dependent does**, or the dependent is `DependencyMissing`, naming the compiler | otherwise a package would load on a compiler where its imports cannot |
| **A view is rebuilt whole on every change** | instances are built from files parsed on load, with no parsing in the rebuild; the benchmark measures it at 200 Packages, and rebuilding only the changed Package and what imports it is the fallback if that shows |

The flags: `--enable-external-cue-package=false` still means built-ins only, which offline tools rely on. `--list-watch-external-cue-package` is accepted for one release and ignored, with a deprecation warning.

## The validation view

Validation and schema generation compile with every package any target lists, schema only, so a package aimed at one compiler cannot break the schema of a definition that imports it. Today's validation sites already pass `DisableResolveProviderFunctions`; they move to an explicit `cuex.ForValidation{}` option, which implies it and selects across every target. A site that means "no calls" without meaning validation keeps the old option and its compiler's own view.

| Site | Repo |
| --- | --- |
| `pkg/appfile/validate.go`, `validate_policy_ctx.go` | kubevela |
| `pkg/controller/utils/inherit.go` | kubevela |
| `pkg/definition/definition.go`, `gen_sdk/gen_sdk.go` | kubevela |
| the definition webhooks' `compileForValidation` | kubevela |
| the definition controllers' schema generation | kubevela |

## Packages written the old way

| A Package | Loads into | Visible from | Condition |
| --- | --- | --- | --- |
| in vela-system, no `spec.compilers` | `workload`, `source`, `workflow`, `default` | everywhere | `Unchecked`, and a warning on apply |
| elsewhere, no `spec.compilers`, flag on | the same four | everywhere, at its bare path, with no `local/`: it sits in vela-system's scope, so a path clash with vela-system is decided by age like any other | `Legacy`, a warning |
| elsewhere, no `spec.compilers`, flag off | nothing | nowhere | Ready `False`, `LegacyNotAllowed` |
| anywhere, with `spec.compilers` | what it lists | its namespace, and everywhere if in vela-system | every rule applies |

The flag is `--allow-namespaced-legacy-packages`: on in the release that ships phase 1, off in the next, then removed.

## Status, and who writes it

Every replica loads; only the leader writes status, from the same view, so status always says what every replica decided.

| Field | From |
| --- | --- |
| `conditions`: Ready, and Ready `False` with `Conflict`, `ReservedPath`, `DependencyCycle`, `DependencyMissing`, `InvalidCUE` or `LegacyNotAllowed`; `Unchecked` and `Legacy` beside it | the view's admission and refusals, with the messages in design 06 |
| `path` | the import path the view admitted it under, `local/` included |
| `compilers[].functions` | the CUE: every definition with a `#do`, its description from the comment above it |
| `observedGeneration` | the spec the view was built from |

**Built-in Packages**: the leader creates `builtin-<name>` in vela-system for each built-in, labelled `cue.oam.dev/builtin`, with no `spec.path` and no provider, its status listing the functions under each compiler that carries it (`vela/http` has `#Get` under `workload`, `#HTTPGet` under `workflow`). It rewrites them on upgrade and deletes ones the binary no longer has. A controller that predates phase 1 loads them as empty packages that claim nothing.

**Admission** refuses what the view would refuse anyway, so the person applying hears it at once: a held, built-in or reserved path, and edits to a built-in Package by anyone but the controller. The view still decides, for two Packages applied at once through different replicas. A definition importing a package that does not exist or is not Ready is admitted with a warning (KEP, Admission).

## Every compile says whose it is

The owner context and every load point are in [design 01](01-api.md#which-definition-a-compile-is-for). `cuex.WithOwner` and `cuex.OwnerFrom`, the `workflow` loader returning the owner, and the owner at every render (components, traits, policies, steps and sources) landed ahead with restricted functions ([design 06](06-selection.md#built-ahead-of-phase-1)). Phase 1 adds the rest of design 01's load points, the ones that matter for what a compile can import rather than what it may call: admission, schema generation, `vela def` and the tools on it, and a per-level owner for `extends` chains. Each gets a test that a compile it starts carries the owner.

## Discovery

`vela cuex compiler list`, `vela cuex compiler show <name> [-n ns]`, `vela cuex package list [-n ns] [--compiler x]` and `vela cuex package show <path> [--compiler x]`, all reading Package status, as the KEP describes. VelaUX's Packages pages read the same status and follow separately.

## The `kubevela/pkg` fixes

| Fix | Today | After | Breaks |
| --- | --- | --- | --- |
| Internal packages keyed one way | by path in `WithInternalPackage`, by name in `LoadInternalPackages` | by path | nothing |
| Internal packages cannot be removed | loaded for the life of the process | removable, as built-in Packages need | nothing |
| Providers looked up globally | one map by Package name; an external one replaces a built-in of the same name | per compile, from the selection | a Package relying on replacing a built-in: it is Ready `False`, reason `Conflict` |
| A versioned import's calls | bound as `naming@v1`, so never run | bound by the path's qualifier, as CUE does | nothing; latent until versioned paths |
| External provider TLS | not verified | verified; `spec.provider.insecureSkipVerify` opts out, with a warning | providers on self-signed certificates until they opt out |
| A non-2xx response | read as data | an error, with the body in it | providers answering a failure as JSON with a 4xx |

The breaking two land in a PR of their own, so the release note points at one change.

## Landing

Each repo pins the one before it. Every PR stands alone: nothing merged changes behaviour until the `kubevela` PR that turns it on.

| # | Repo | PR | Depends on |
| --- | --- | --- | --- |
| P1 | `kubevela/pkg` | internal packages keyed by path; the `@vN` call scope | |
| P2 | `kubevela/pkg` | API: `compilers`, `insecureSkipVerify`, optional `path` and `templates`, status; CRD regenerated | |
| P3 | `kubevela/pkg` | `WithOwner`, `OwnerFrom`, `ForValidation` | |
| P4 | `kubevela/pkg` | `Loader`, view, `Selector`; the compiler selects per compile when given one, and behaves as today when not; the render benchmark | P1 to P3 |
| P5 | `kubevela/pkg` | external client: TLS verified, non-2xx an error | P2 |
| W1 | `workflow` | bump pkg; its compiler on a `Loader`; the loader returning the owner; the custom task setting it | P4 |
| K1 | `kubevela` | bump pkg and workflow; the five compilers named and on one `Loader`; webhooks wait for the first view; the flags | W1 |
| K2 | `kubevela` | the owner at every load point, with a test each; `ForValidation` at the validation sites | K1 |
| K3 | `kubevela` | the Package controller: status, built-in Packages | K1 |
| K4 | `kubevela` | Package admission; the definition webhook's warning on an unresolved import; the legacy flag and conditions | K3 |
| K5 | `kubevela` | the `healthPolicy` and status message | |
| K6 | `kubevela` | the discovery commands | K3 |
| K7 | `kubevela` | acceptance: the shipped-definitions comparison and the benchmark in the controller; e2e; the chart's CRD and the upgrade notes | K2, K4 |

## Done when

| Check | Bar |
| --- | --- |
| Render benchmark, in the controller | a one-call render with 200 Packages installed within 10% of one with none |
| Shipped definitions | all 167 render identically with and without selection, `vela/op` included |
| Load points | each has a test that its compiles carry the owner |
| e2e, webhook and controller on separate replicas | a Package applied on one replica is importable through the other; a cold replica refuses nothing while its view builds |
| Upgrade from the previous release | legacy Packages in and out of vela-system keep rendering, with and without re-applied CRDs |
| Downgrade | built-in Packages present, nothing shadowed |
| WorkflowRun controller | the same Packages, seen the same way |
