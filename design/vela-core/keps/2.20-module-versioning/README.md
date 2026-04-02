# KEP-2.20: Module & API Line Versioning

**Status:** Draft
**Parent:** [vNext Roadmap](../README.md)
**Depends on:** [KEP-2.13](../2.13-addons/README.md)

This KEP covers the module identity model, API line naming convention, type reference resolution, and API line deprecation lifecycle. The versioning model is implemented at the **X-Definition level** — `spec.module` and `spec.apiVersion` are fields on the definitions themselves, not on the addon. Addons are the primary delivery mechanism that makes this versioning operational at scale and distributable, but the identity model applies to any definition regardless of how it was installed. The declarative addon lifecycle (Addon CR, reconciliation, drift correction) is covered in [KEP-2.13](../2.13-addons/README.md).

## Problem

KubeVela Applications act as a contract between Platform Engineering teams and platform users, abstracting away underlying platform complexity. The X-Definition suite (`ComponentDefinition`, `TraitDefinition`, `WorkflowStepDefinition`, `PolicyDefinition`) is that contract — the stable surface through which users declare what they need.

But today, definitions have no stable identity model. This creates several concrete problems:

- **No stable type reference**: Upgrading an addon replaces its definitions in place — there is no stable `type` syntax that survives upgrades. Applications break silently when the definitions they depend on are replaced out from under them.
- **Fragile API contracts**: While two semantically versioned definitions can technically coexist, there is no enforcement mechanism. Pinning is troublesome, and within explicit pins the API is still fragile — an update to a definition can introduce breaking changes without consumers being protected.
- **Semver is a release concept, not an API stability concept**: Addon semver tracks which *release* shipped a definition, not whether the parameter contract remains fulfillable across upgrades. Users pin to specific addon versions as a workaround, but this is operationally fragile: definitions may not be present on freshly built clusters, may be garbage-collected, and the pin provides no guarantee against breaking parameter changes.
- **No deprecation lifecycle**: There is no mechanism to defer definition removal until Applications have migrated to a new API line. Disable removes definitions immediately, with no transition window.

The new model inverts this: the **API version** (`v1`, `v1beta1`) is the stable contract that users bind to — essentially guaranteeing that the contract established by parameters remains fulfillable. If the contract is broken, for example by a new mandatory field, then this should be handled by a new API version.

## Goals

- **Treat the X-Definition suite as a versioned API surface** — platform teams can publish, version, and deprecate capability APIs using the same conventions engineers already know from Kubernetes API evolution (`v1alpha1` → `v1beta1` → `v1`) and REST APIs. Addons are the delivery and distribution mechanism; the versioning model is the contract.
- Introduce the `module` + `apiVersion` identity model at the X-Definition level — fields on the definitions themselves, populated by the addon controller at install time
- Install definitions from module-aware addons under a stable naming convention encoding module, definition name, and API line
- Enable API line coexistence — `v1` and `v2` lines installed simultaneously, so consumers can migrate at their own pace without a flag day
- Implement context-aware line installation via CueX-evaluated `enabled` in `_version.cue`
- Implement API line deprecation and removal driven by line presence/absence in new addon versions, with blocking-reference checks enforcing that no consumer is broken silently
- Maintain full backwards compatibility with existing addons and un-versioned definitions — the module system is opt-in

## Definition Identity — module, apiVersion, name

Two new optional fields are added to all Definition CRD specs:

```go
// Added to ComponentDefinition, TraitDefinition,
// WorkflowStepDefinition, and PolicyDefinition.

// Module is the globally unique module identifier this definition belongs to.
// Typically set by the addon controller when installing module-managed definitions,
// but may also be set manually on hand-authored definitions to opt into the module
// identity and API line versioning model.
// Absent for legacy definitions outside the module system.
// +optional
Module string `json:"module,omitempty"`

// APIVersion is the API line identifier for this definition (e.g. "v1", "v2", "v1beta1").
// Must follow the Kubernetes API stability level convention: v{N}, v{N}beta{M}, or v{N}alpha{M}.
// Validated by the admission webhook against the pattern ^v\d+(alpha\d+|beta\d+)?$
// Typically set by the addon controller when installing module-managed definitions,
// but may also be set manually on hand-authored definitions.
// Absent for legacy definitions outside the module system.
// +optional
APIVersion string `json:"apiVersion,omitempty"`
```

| Field | Example | Set by | Visible to |
|---|---|---|---|
| `spec.module` | `aws-s3` | addon controller or manual | Operators, resolution logic |
| `spec.apiVersion` | `v1` | addon controller or manual | Application authors (for `type: v1/bucket`) |
| `spec.version` | `v1.2.3` | addon controller | Operators (internal, not for pinning) |

`spec.version` continues to work exactly as today — it is the addon release semver stamped by the controller and used for internal versioning. Application authors should never reference it directly. `spec.apiVersion` is the user-facing stable contract identifier.

`spec.apiVersion` is validated at admission against the pattern `^v\d+(alpha\d+|beta\d+)?$` — the Kubernetes API stability level convention. Values such as `1.0`, `v1.2`, `latest`, or arbitrary strings are rejected. Valid examples: `v1`, `v2`, `v1beta1`, `v1alpha2`.

## Definition Naming Convention

Definitions installed by the module system are named using the pattern:

```
{module}-{apiVersion}-{definition-name}
```

The API version sits before the definition name, reflecting the containment hierarchy: the definition belongs to an API line which belongs to a module. This also means DefinitionRevision counters (appended as `-v{N}`) are unambiguously attached to the definition name rather than the API line:

| Module | Definition | API Version | Installed Name | DefinitionRevision example |
|---|---|---|---|---|
| `aws-s3` | `bucket` | `v1` | `aws-s3-v1-bucket` | `aws-s3-v1-bucket-v3` |
| `aws-s3` | `bucket` | `v2` | `aws-s3-v2-bucket` | `aws-s3-v2-bucket-v1` |
| `aws-s3` | `encryption-policy` | `v1beta1` | `aws-s3-v1beta1-encryption-policy` | `aws-s3-v1beta1-encryption-policy-v2` |
| `postgres` | `database` | `v1` | `postgres-v1-database` | `postgres-v1-database-v1` |

If the derived name exceeds 253 characters, it is truncated and an 8-character hash suffix appended:

```
{truncated-prefix}-{8-char-hash}
```

The hash is computed over the full untruncated name — stable across reconcile cycles.

**Why not use DefinitionRevisions for coexistence?** `DefinitionRevision` tracks history within a single definition — only one revision is "current". Applications pinning `type: bucket@v5` are frozen on a historical snapshot with no maintained evolution path. API lines are different: `aws-s3-bucket-v1` and `aws-s3-bucket-v2` are two *simultaneously active, independently maintained* definitions — both receive updates, both are "current", and teams migrate between them on their own schedule. The two mechanisms are complementary: DefinitionRevisions still track the update history *within* each API line.

**Breaking change note**: This naming convention is a breaking change for tooling that hardcodes definition names. Legacy definitions (installed by addons without `_version.cue`) keep their existing names. Migration guidance must address tools that reference definition names directly.

## Definition Labels and Annotations

All module-managed definitions carry a standard label and annotation set:

**Labels** — used for fast selector-based lookups:

```yaml
labels:
  definition.oam.dev/module: aws-s3
  definition.oam.dev/api-version: v1
  definition.oam.dev/name: bucket
  addon.oam.dev/name: aws-s3
```

**Annotations** — internal metadata:

```yaml
annotations:
  addon.oam.dev/version: v1.2.3
  addon.oam.dev/managed-by: controller
  definition.oam.dev/full-name: aws-s3-v1-bucket
```

**Lifecycle annotations** (set dynamically):

```yaml
annotations:
  definition.oam.dev/deprecated: "true"
  definition.oam.dev/deprecated-at: "2026-03-24T10:00:00Z"
  definition.oam.dev/no-references-since: "2026-03-25T10:00:00Z"
  definition.oam.dev/disabled: "true"    # set when enabled evaluates to false
```

## Type Reference Syntax

Applications reference definitions using an extended type syntax:

```yaml
components:
  # Form 1: Unqualified — existing behaviour, latest stable version wins
  - name: my-bucket
    type: bucket

  # Form 2: API line qualified — admission error if multiple modules define v1/bucket
  - name: my-bucket
    type: v1/bucket

  # Form 3: Module scoped, latest stable version within module
  - name: my-bucket
    type: aws-s3/bucket

  # Form 4: Fully qualified — most explicit, no ambiguity possible
  - name: my-bucket
    type: aws-s3/v1/bucket
```

The `type` field is parsed by segment count and first-segment shape:

| Segments | First segment | Form | Pattern |
|---|---|---|---|
| 1 | — | Form 1 | `{definition-name}` |
| 2 | matches `^v\d+` | Form 2 | `{apiVersion}/{definition-name}` |
| 2 | does not match `^v\d+` | Form 3 | `{module}/{definition-name}` |
| 3 | — | Form 4 | `{module}/{apiVersion}/{definition-name}` |

The order mirrors both the installed name convention (`{module}-{apiVersion}-{definition-name}`) and the Kubernetes API URL structure (`/apis/{group}/{version}/{resource}`). There is no separate `module` field on the component — module scoping is expressed entirely within `type`.

### Resolution Rules

All resolution is performed via label selector queries — no full scan.

- **Form 4** (`{module}/{apiVersion}/{definition-name}`): Direct lookup of `aws-s3-v1-bucket` (or label selector if truncated). Unconditionally stable.
- **Form 3** (`{module}/{definition-name}`): Selector `definition.oam.dev/module=aws-s3, definition.oam.dev/name=bucket`. Picks the most stable API version within the module. Stable only when a single API line exists.
- **Form 2** (`{apiVersion}/{definition-name}`): Selector `definition.oam.dev/name=bucket, definition.oam.dev/api-version=v1`. Single match resolves; multiple modules → ambiguity error.
- **Form 1** (`{definition-name}`, priority-ordered):
  1. Exact name lookup for a legacy definition named `bucket` — if found, use it. Preserves existing behaviour during the legacy → module transition.
  2. If no exact match: label selector `definition.oam.dev/name=bucket`. Picks the **most stable API version** using Kubernetes API stability level ordering — `v2 > v1 > v1beta2 > v1beta1 > v1alpha1`.

**API version ordering (highest first):** `vN` (stable, N desc) → `vNbetaM` (beta, N desc, M desc) → `vNalphaM` (alpha, N desc, M desc). This follows the [Kubernetes API versioning convention](https://kubernetes.io/docs/reference/using-api/#api-versioning), not semver.

**Stability note:** Forms 1 and 3 are convenience forms — the resolved definition may change as new API lines are added. For production Applications, use Form 2 (`type: v1/bucket`) when uniqueness is guaranteed, or Form 4 (`type: aws-s3/v1/bucket`) for unconditional stability in multi-tenant or open clusters. Platform teams who want to mandate Form 4 can enforce this via admission policy.

Ambiguity errors surface at admission time:

```
component "my-bucket": type "v1/bucket" is ambiguous — multiple modules match:
  - aws-s3-v1-bucket (module: aws-s3)
  - gcp-gcs-v1-bucket (module: gcp-gcs)
Use "type: aws-s3/v1/bucket" to disambiguate.
```

## Module Structure in Addon Source Tree

The addon controller scans `modules/` for `_module.cue` files to discover modules, then looks for `_version.cue` in subdirectories to discover API lines. The full addon controller reconciliation loop — source resolution, drift correction, owned Application management — is covered in [KEP-2.13](../2.13-addons/README.md). This section describes only the module source tree layout that KEP-2.13's controller consumes.

```
my-addon/
  metadata.yaml
  template.cue
  resources/                # Tier 1: addon infrastructure; compiled into owned Application
  definitions/              # legacy path; still supported with deprecation warning
  modules/
    aws-s3/
      _module.cue           # module identity
      _context.cue          # module-level cluster context (optional)
      v1/
        _version.cue        # API line v1 metadata
        bucket.cue
        encryption-policy.cue
        auxiliary/          # Tier 2: API-line resources (XP/KRO compositions, XRDs, etc.)
      v2/
        _version.cue        # API line v2 metadata
        bucket.cue
        auxiliary/
    postgres/
      _module.cue
      v1/
        _version.cue
        database.cue
        auxiliary/
```

### Two-Tier Resource Model

The addon source tree supports two categories of deployable resources with distinct semantics and installation ordering:

**Tier 1 — Addon Application resources (`resources/`)**

The top-level `resources/` directory contains version-agnostic infrastructure the addon as a whole requires. These files are compiled into the addon's owned Application CR and support the full OAM workflow model — ordered component deployment, health gates, workflow steps, and conditions. Typical contents: operator deployments (Crossplane, KRO, cert-manager), CRDs, RBAC.

**Tier 2 — API-line auxiliary resources (`auxiliary/`)**

Each API line may contain an `auxiliary/` directory with resources specific to that line's contract — the infrastructure that the definitions in the line depend on at runtime. Typical contents: versioned Crossplane `Composition` and `CompositeResourceDefinition` objects, KRO `ResourceGraphDefinition` resources, or similar versioned compositions that component definitions will create instances of.

Auxiliary resources are intentionally simpler than Tier 1: they are YAML manifests or CUE templates (for parameter-driven generation) and do not support OAM workflow steps. They are applied by the addon controller via server-side apply.

**Installation ordering**

Definitions are applied last — they are the go-live gate that signals the API is open for business:

```
1. Create/update owned Application from top-level resources/
2. Wait for owned Application → Healthy
3. Apply auxiliary/ resources for each enabled API line
4. Wait for auxiliary resources → Ready
5. Apply definitions (ComponentDefinitions etc.) via server-side apply
```

This ordering has two important guarantees:

- **Infrastructure-before-logic**: Crossplane Compositions and XRDs are never applied before the Crossplane CRDs exist. Applying them before the operator is running fails immediately; the Application health gate prevents that.
- **Logic-before-API**: Definitions are never visible to Application authors until the compositions backing them are ready. Without this, a user who writes `type: aws-s3/v1/bucket` immediately after an addon install will get a reconciliation failure from a missing Composition rather than a working bucket. Definitions land only once the full stack beneath them is operational.

On upgrade the same ordering applies — updated definitions land only after updated auxiliary resources are ready, preventing a window where the old definition renders against a new incompatible composition.

If the owned Application does not reach Healthy, auxiliary resources and definitions are not applied and the Addon CR transitions to `Failed` with a condition identifying the blocking Application component. If auxiliary resources do not reach Ready, definitions are withheld and the condition identifies the unready resource.

**Auxiliary resource content**

Auxiliary files are plain YAML (`.yaml`) or CUE (`.cue`). CUE files in `auxiliary/` are evaluated with the same context available to `_version.cue` — including `parameter`, `context.apiVersion`, and any user-defined context values — before being applied. This allows auxiliary resources to be parameterised by addon parameters or cluster context where needed.

```yaml
# modules/aws-s3/v1/auxiliary/xrd.yaml
apiVersion: apiextensions.crossplane.io/v1
kind: CompositeResourceDefinition
metadata:
  name: xbuckets.s3.aws.example.com
spec:
  # ...

# modules/aws-s3/v1/auxiliary/composition.yaml
apiVersion: apiextensions.crossplane.io/v1
kind: Composition
metadata:
  name: aws-s3-v1-bucket
spec:
  # ...
```

The `AddonInclude` type in KEP-2.13 includes a separate `auxiliary` category alongside `resources`, `definitions`, `configTemplates`, and `views` — allowing addon composition scenarios to install API-line auxiliary resources independently of top-level Application resources.

## _module.cue Fields

```cue
// modules/aws-s3/_module.cue (CUE source module)
module:  "aws-s3"
type:    "cue"      // "cue" (default) | "defkit"
version: "v1.5.3"  // semver tag; authoritative version for vela module publish
```

`type: "defkit"` signals that the module was authored in Go and compiled to CUE at publish time. The source artifact for each API line is declared in `_version.cue` — see below.

`version` is the module's own semver tag, independent of the addon version. It is read by `vela module publish` to determine the registry tag — no `--version` flag needed in the common case. It is also used by `vela module validate --check-breaking` to identify which previously-published artifact to compare against.

### `instance` — Multi-Instance Addons

The optional `instance` field is a CUE expression that, when present, marks the addon as multi-instance and determines the Addon CR name suffix. It is evaluated against the same context injected into `_version.cue` — including `parameter` (the addon's `spec.parameters`):

```cue
// modules/tenant-infra/_module.cue
module:   "tenant-infra"
type:     "cue"
instance: parameter.tenant   // Addon CR name = "tenant-infra-{parameter.tenant}"
```

The resulting Addon CR name is `{addon-name}-{instance}`. The owned Application created in `vela-system` follows the `addon-` prefix convention: `addon-{addon-name}-{instance}` (e.g. `addon-tenant-infra-acme`). The instance value must be a non-empty string after evaluation; if it evaluates to `_|_` or empty, the controller surfaces a validation error and transitions the Addon CR to `Failed`.

Because `instance` is a full CUE expression, compound discriminators are natural:

```cue
// One Addon CR per tenant per region:
// Addon CR name        → "tenant-infra-acme-us-east-1"
// Owned Application    → "addon-tenant-infra-acme-us-east-1"
instance: "\(parameter.tenant)-\(parameter.region)"
```

The admission webhook validates that parameters referenced by `instance` are present in `spec.parameters` at install time. Addon authors should document which parameters are required for instance key construction.

When `instance` is absent, the addon is single-instance: the Addon CR name equals the addon name, and installing a second instance with different parameters updates the existing CR rather than creating a new one.

## _version.cue Fields

```cue
// modules/aws-s3/v1/_version.cue

// apiVersion is the user-facing API line identifier.
apiVersion: "v1"

// enabled is evaluated by CueX at reconcile time with injected cluster context.
// Defaults to true if absent.
enabled: context.isAWSCluster     // references derived values from _context.cue

// minKubeVelaVersion enforces a minimum version constraint (semver >=).
// Line surfaces as Disabled if constraint is not met.
minKubeVelaVersion: "v1.11.0"

// source specifies where the controller fetches the compiled definitions and
// auxiliary resources for this API line. Required for DefKit modules; optional
// for CUE modules whose files are inline in the addon source tree.
// Accepts an OCI registry reference or a Git repository.
// Both forms accept an exact tag or a semver range.
source: {
    oci: {
        ref:     "ghcr.io/myorg/aws-s3-module"
        version: ">=1.5.0 <2.0.0"   // semver range — resolved to highest matching tag at reconcile
    }
}
// Exact pin (OCI shorthand — equivalent to version: "v1.5.2"):
// source: { oci: "ghcr.io/myorg/aws-s3-module:v1.5.2" }
//
// Git with semver range:
// source: {
//     git: {
//         url:     "https://github.com/myorg/aws-s3-module.git"
//         version: "~1.5.0"   // patch-compatible range: >=1.5.0 <1.6.0
//     }
// }
```

Each API line declares its own `source`, allowing v1 and v2 to ship from independent artifacts with decoupled release cycles. If `source` is absent, the controller expects the definition `.cue` files to be present inline in the addon source tree alongside `_version.cue`.

When a semver range is specified, the addon controller resolves it against the registry at each reconcile cycle and fetches the highest matching version. The resolved concrete tag is recorded in `AddonModuleLineStatus.resolvedSourceVersion` — see the API Changes section. This means a module author can publish patch releases independently and addons that declare a compatible range will pick them up automatically, without an addon rollout.

## _context.cue Evaluation

`_context.cue` files are evaluated by CueX, enabling dynamic context derived from cluster state:

```cue
// modules/aws-s3/_context.cue
import "github.com/kubevela/pkg/cue/cuex/providers/kube"

_clusterConfig: kube.#Get & {
    resource: {
        apiVersion: "v1"
        kind:       "ConfigMap"
        name:       "cluster-config"
        namespace:  "vela-system"
    }
}

context: {
    isAWSCluster:  _clusterConfig.value.data.provider == "aws"
    clusterRegion: _clusterConfig.value.data.region
}
```

User-defined values are placed inside a `context:` struct, unifying with the controller-injected `context` fields (e.g. `context.clusterVersion`, `context.velaVersion`). Addon parameters are passed as top-level `parameter.*` — consistent with how parameters work in ComponentDefinition templates. Private helpers (prefixed `_`) are used for intermediate CueX calls and are not exported into `context`.

Two `_context.cue` files may be present per module: one at module level, one at version level. Version-level values take precedence on conflict.

If `_context.cue` evaluation fails (network unreachable, RBAC denied, resource not found), the controller aborts the reconciliation cycle and requeues with exponential backoff. After 3 consecutive failures the Addon CR transitions to `Failed`.

## Context Schema

The controller injects the following base context before evaluating any `_context.cue`, `_module.cue`, or `_version.cue`. User-defined fields from `_context.cue` are unified into the same `context` struct:

```cue
// Top-level — addon parameters, consistent with ComponentDefinition template convention
parameter: {}    // from Addon CR spec.parameters

// Controller-injected cluster and addon metadata
context: {
    addonName:    "aws-s3"
    addonVersion: { version: "v1.2.0", major: 1, minor: 2, patch: 0 }
    apiVersion:   "v1"           // version-level context only
    clusterVersion: {
        version: "v1.29.0", major: 1, minor: 29, gitVersion: "v1.29.0"
    }
    velaVersion: { version: "v1.11.0", major: 1, minor: 11, patch: 0 }
    addonPhase:  "installing"    // installing | upgrading | running | failed

    // User-defined — merged from _context.cue evaluation
    isAWSCluster:  true
    clusterRegion: "us-east-1"
}
```

Merging is performed via CUE unification. The controller injects concrete values for all built-in fields before evaluating `_context.cue` — CUE's unification semantics make overriding them structurally impossible: attempting to set `context.addonName: "something-else"` when `context.addonName: "aws-s3"` is already concrete produces `_|_` and the controller surfaces a validation error. The Addon CR transitions to `Failed`.

For additional protection, the controller wraps the base context in a `close({})` struct, which prevents `_context.cue` from introducing fields with names that collide with the reserved set even if their types would otherwise be compatible. This means the following reserved field names cannot appear in the user-defined `context:` block: `addonName`, `addonVersion`, `apiVersion`, `clusterVersion`, `velaVersion`, `addonPhase`.

## API Line Deprecation and Removal

### Deprecation Detection

On each addon upgrade (version bump in the `Addon` CR):

1. Scan `_version.cue` files in the new addon version to determine the set of API lines present
2. Diff against the previous installed version's line set (recorded in Addon CR status)
3. Any line present in the previous version but absent from the new version is marked deprecated

Deprecated definitions remain fully functional — Applications pinned to them continue to work. Deprecation surfaces as a warning condition on the Addon CR.

### Removal

A deprecated definition is eligible for removal only when:

1. Its API line is absent from the current addon version
2. No active Application references that definition in its current spec


```
Addon aws-s3: api line v1 is deprecated but removal is blocked:
  - application "data-processing" (namespace: production) references aws-s3-v1-bucket
  - application "batch-jobs" (namespace: staging) references aws-s3-v1-bucket
```

Once all blocking Applications migrate, the definition is removed on the next reconcile cycle automatically. There is no time-based grace period.

## API Line Contract Validation

When a definition with `spec.apiVersion` set is updated — whether by the addon controller, `kubectl apply`, or any other means — the admission webhook can validate that the change does not break the parameter contract established for that API line.

The check fires when the existing definition has `spec.apiVersion` set and the incoming update preserves the same `spec.apiVersion` value. Cross-line updates (changing `spec.apiVersion`) are never checked — introducing a new API line is the intended mechanism for breaking changes. 

### Policy Flag

Contract validation is controlled by a cluster-level feature flag set on the KubeVela controller:

```
--api-line-contract-policy=disabled|warn|reject
```

| Mode | Behaviour | Recommended for |
|---|---|---|
| `disabled` | No check performed | Default; teams exploring the API versioning model |
| `warn` | Breaking changes are detected and surfaced as a `BreakingChangeDetected` condition on the definition, but the apply proceeds | Teams who want visibility while iterating |
| `reject` | Breaking changes cause the webhook to hard-reject the update | Teams who have committed to the API line model and want strict enforcement |

The default is `disabled` so that teams adopting `spec.apiVersion` incrementally are not immediately blocked. The graduation path is `disabled` → `warn` → `reject` as confidence grows.

### Breaking vs Non-Breaking Changes

These rules align with the [Kubernetes API compatibility guidelines](https://github.com/kubernetes/community/blob/master/contributors/devel/sig-architecture/api_changes.md), adapted for CUE `parameter:` blocks.

| Change | Classification | Reason |
|---|---|---|
| Field removed | **Breaking** | Existing Applications referencing the field break |
| Optional → mandatory (`field?` → `field`) | **Breaking** | Applications that omitted the field now fail validation |
| New mandatory field added (no `?`, no default) | **Breaking** | Existing Applications do not supply the field |
| Type narrowed (`string \| int` → `string`) | **Breaking** | Applications passing the removed type break |
| Disjunction value removed (`"a" \| "b"` → `"a"`) | **Breaking** | Applications using the removed value break |
| Constraint tightened (`string` → `string & =~"^v[0-9]+"`) | **Breaking** | Existing values that don't match the new constraint break |
| Default removed from previously-defaulted field | **Breaking** | Applications relying on the default now get no value |
| Default value changed (`*"a"` → `*"b"`) | **Breaking** | Silent behaviour change for Applications that rely on the default — treated as breaking even though existing values remain valid |
| New optional field added | Non-breaking | Existing Applications simply don't supply it |
| Type widened (`string` → `string \| int`) | Non-breaking | Existing values remain valid |
| Disjunction value added (`"a"` → `"a" \| "b"`) | Non-breaking | More permissive than before |
| Constraint loosened (`string & =~"^v[0-9]+"` → `string`) | Non-breaking | Previously valid values remain valid; more values now accepted |
| Default added to previously undefaulted field | Non-breaking | More permissive than before |
| Field description or label changed | Non-breaking | No schema effect |

The webhook compares the CUE AST of the `parameter:` block from the existing definition against the incoming one. Field presence, optionality markers (`?`), type constraints, and default values are compared field-by-field, producing a specific message for each violation.

### Error Message (reject mode)

```
definition "aws-s3-v1-bucket" (apiVersion: v1): breaking parameter change detected
  - field "encryptionKey" changed from optional to mandatory
  - field "region" removed
If this change is intentional, introduce a new API line (v2) instead of modifying v1.
To override for this apply, set annotation definition.oam.dev/allow-breaking-change: "true".
```

### Per-Apply Bypass Annotation

```yaml
annotations:
  definition.oam.dev/allow-breaking-change: "true"
```

Bypasses the check for that specific apply operation regardless of the cluster-wide policy. It is not persisted — it must be re-supplied on each apply that would otherwise be rejected. The expected long-term path for genuine breaking changes is a new API line, not repeated use of this annotation.

### Implementation Location

New validation steps in the existing X-Definition validating webhook handlers (`pkg/webhook/core.oam.dev/v1beta1/`):

1. **`spec.apiVersion` format** (`CREATE` and `UPDATE`): if `spec.apiVersion` is non-empty, validate it matches `^v\d+(alpha\d+|beta\d+)?$`. Rejection is unconditional — no bypass annotation.
2. **`metadata.name` convention** (`CREATE`): each field independently enforces its portion of the naming convention — catching manual applies that omit the required prefix:
   - `spec.module` set → `metadata.name` must start with `{module}-`
   - `spec.apiVersion` set → `metadata.name` must start with `*-{apiVersion}-` (i.e. the api version segment follows the module prefix)
   - Both set → `metadata.name` must start with `{module}-{apiVersion}-`
   The addon controller always produces correctly-named definitions; this check is a safety net for user mistakes. On rejection the error message computes and surfaces the expected name. Mutating webhooks cannot change `metadata.name`, so rejection with a suggestion is the only available mechanism.
3. **Parameter contract check** (`UPDATE`): when the existing object has `spec.apiVersion` set and the incoming update preserves the same value, compare the CUE `parameter:` block from `spec.schematic.cue` against the prior definition using the CUE Go API. Behaviour controlled by `--api-line-contract-policy`.

## Legacy → Module Transition

When an addon author adds `_version.cue` to an existing addon, the controller installs new module-named definitions alongside legacy-named ones, marks the legacy-named definitions as deprecated, and removes them subject to the standard blocked-removal checks.

```bash
# Find deprecated definitions with no references, deprecated for more than 7 days
vela def list-deprecated --addon aws-s3 --no-references --older-than 7d
```

## Controller Detection Logic

The controller determines which semantics to apply by source tree inspection — no explicit spec field is needed:

```
if both definitions/ and modules/ present:
  → set Addon CR phase to Failed; abort

scan for _version.cue files:
  → found: apply module lifecycle semantics
  → not found in definitions/: apply legacy install; emit deprecation warning

for each _version.cue found:
  → locate nearest _module.cue in ancestor chain
  → if absent: skip with warning
  → if present: apply module lifecycle for this (module, apiVersion) pair
```

## API Changes

### New Fields on Definition CRDs

See "Definition Identity" section above. `spec.module` and `spec.apiVersion` are additive optional fields on `ComponentDefinitionSpec`, `TraitDefinitionSpec`, `WorkflowStepDefinitionSpec`, and `PolicyDefinitionSpec`.

### Extended `type` Field on Application Component

The `type` field on `ApplicationComponent` is extended to accept 1-, 2-, or 3-segment slash-separated values. No new fields are added to the struct — module scoping is expressed entirely within `type`. The parser determines the form from segment count and whether the first segment matches `^v\d+(alpha\d+|beta\d+)?$`.

### Label and Annotation Constants

```go
const (
    LabelDefinitionModule              = "definition.oam.dev/module"
    LabelDefinitionAPIVersion          = "definition.oam.dev/api-version"
    LabelDefinitionName                = "definition.oam.dev/name"
    AnnotationDefinitionFullName       = "definition.oam.dev/full-name"
    AnnotationDefinitionDeprecated     = "definition.oam.dev/deprecated"
    AnnotationDefinitionDeprecatedAt   = "definition.oam.dev/deprecated-at"
    AnnotationDefinitionNoReferencesSince = "definition.oam.dev/no-references-since"
    AnnotationDefinitionDisabled       = "definition.oam.dev/disabled"
)
```

### AddonModuleStatus Types

```go
type AddonModuleStatus struct {
    Module string                 `json:"module"`
    Lines  []AddonModuleLineStatus `json:"lines,omitempty"`
}

type AddonModuleLineStatus struct {
    APIVersion            string `json:"apiVersion"`
    Enabled               bool   `json:"enabled"`
    Deprecated            bool   `json:"deprecated"`
    Disabled              bool   `json:"disabled"`
    // ResolvedSourceVersion is the concrete tag the controller resolved the
    // _version.cue source.version constraint to on the last reconcile.
    // Empty when source is inline (no registry reference).
    ResolvedSourceVersion string `json:"resolvedSourceVersion,omitempty"`
    Message               string `json:"message,omitempty"`
}
```

## Built-in `vela` Module

KubeVela ships its own built-in definitions — the `addon` component type, built-in workflow step types, and other core capability definitions — as a reserved module named `vela`. These definitions follow the same module identity model and versioning convention as addon-delivered modules.

The `vela` module is distinguished from addon-delivered modules in one way: it is bundled with the KubeVela controller image and installed during controller startup, not by the addon controller. It does not have a registry entry and cannot be managed via an `Addon` CR.

Built-in definition naming follows the same convention:

| Module | Definition | API Version | Installed Name |
|---|---|---|---|
| `vela` | `addon` | `v1` | `vela-v1-addon` |
| `vela` | `apply-once` | `v1` | `vela-v1-apply-once` |
| `vela` | `webservice` | `v1` | `vela-v1-webservice` |

Applications reference built-in definitions using the same type reference syntax — either fully qualified (`type: addon/v1`, `module: vela`) or unqualified (`type: addon`) when no ambiguity exists with addon-delivered definitions of the same name.

The `vela` module name is reserved — addon authors may not ship a module named `vela`. The admission webhook rejects `Addon` CRs or addon source trees with `module: "vela"` set in `_module.cue`.

Built-in API lines are versioned independently of the KubeVela release version. A KubeVela v2.1 release may ship `vela-v1-addon` unchanged while introducing `vela-v2-webservice`. The deprecation and removal lifecycle for built-in API lines follows the same rules as addon-delivered lines (no references before removal), but removal requires a KubeVela controller upgrade rather than an addon upgrade.

## Module Developer Workflow

Modules are independently versioned artifacts. The `vela module` CLI subcommand covers the full lifecycle from local development through registry publication.

### Versioning Hierarchy

Three independent versioning axes operate at different cadences:

| Level | Mechanism | Author | Cadence |
|---|---|---|---|
| **Definition** | `spec.version` / DefinitionRevision (e.g. `-v3`) | addon controller (automatic) | Every definition update |
| **Module** | OCI/git tag from `_module.cue version` | module author | API patch/minor releases, independent of addon |
| **Addon** | Addon semver (`Addon CR spec.version`) | platform team | Infrastructure changes, composition overhauls |

This separation means a module author can publish a new definition patch (`aws-s3-module:v1.5.3`) and every addon that declares `version: "~1.5.0"` in its `_version.cue source` picks it up on the next reconcile — no addon rollout required. Larger changes that need infrastructure updates (new Crossplane provider, updated CRDs) flow through an addon version bump instead.

### CLI Commands

**Development iteration — deploy directly to the current cluster:**

```bash
# Apply a module's definitions and auxiliary resources directly to the cluster
# Reads from a local path; no packaging or publishing involved
vela module deploy ./aws-s3

# Deploy a specific API line only
vela module deploy ./aws-s3 --line v1

# Deploy without applying auxiliary resources (definitions only)
vela module deploy ./aws-s3 --no-auxiliary
```

`vela module deploy` is the inner development loop. It applies definitions and auxiliary resources to the current `kubectl` context, following the same installation ordering as the addon controller (Application health gate, auxiliary ready gate, definitions last), but without creating an Addon CR or touching a registry.

**Validation:**

```bash
# Validate CUE syntax, _module.cue and _version.cue fields, and naming conventions
vela module validate ./aws-s3

# Check for breaking parameter changes against the previously published version
# (reads version from _module.cue, fetches prior tag from the registry)
vela module validate ./aws-s3 --check-breaking --registry ghcr.io/myorg

# Check against an explicit version
vela module validate ./aws-s3 --check-breaking --against ghcr.io/myorg/aws-s3-module:v1.4.0
```

`--check-breaking` runs the same contract check as the admission webhook (see API Line Contract Validation) locally before publish, comparing `parameter:` blocks field-by-field across all API lines.

**Publishing to a registry:**

```bash
# Publish — version tag read from _module.cue
vela module publish ./aws-s3 --registry ghcr.io/myorg

# Override the version tag (e.g. for release candidates)
vela module publish ./aws-s3 --registry ghcr.io/myorg --version v1.5.3-rc1

# Publish to a git remote (creates a tag and pushes)
vela module publish ./aws-s3 --git https://github.com/myorg/aws-s3-module.git

# Dry-run — show what would be published without pushing
vela module publish ./aws-s3 --registry ghcr.io/myorg --dry-run
```

`vela module publish` packages the module directory — `_module.cue`, all `_version.cue` files and their sibling definition files, and `auxiliary/` directories — as an OCI artifact and pushes it to the registry under the version tag from `_module.cue`. The tag must be a valid semver string.

**Inspecting installed modules:**

```bash
# List all modules installed in the current cluster
vela module list

# Show resolved source versions for all lines
vela module list --show-versions

# Show details for a specific module including resolved source version per line
vela module describe aws-s3
```

### Addon as Module Consumer

An addon can reference a published module in `_version.cue` with a semver range. When it does, the addon directory contains no inline definition files for that line — they are fetched from the published module artifact at reconcile time:

```
my-platform-addon/
  metadata.yaml
  resources/              # Crossplane operator, provider configs
  modules/
    aws-s3/
      _module.cue         # module: "aws-s3", type: "cue" — no version here (addon doesn't own it)
      v1/
        _version.cue      # source points to published aws-s3-module artifact
      v2/
        _version.cue
```

```cue
// modules/aws-s3/v1/_version.cue in the addon
apiVersion: "v1"
enabled:    context.isAWSCluster
source: {
    oci: {
        ref:     "ghcr.io/myorg/aws-s3-module"
        version: "~1.5.0"   // auto-update within 1.5.x; a jump to 1.6.x requires updating this file
    }
}
```

The addon `_module.cue` omits `version` — version is the module artifact's concern, not the addon's. The addon controls *which range* it tracks in `_version.cue source`; the module author controls what is published within that range.

Alternatively, addons can continue to inline definition files directly alongside `_version.cue` without publishing to a registry — the `source` block is optional. Publishing as a separate module artifact is a choice, not a requirement.

The semver range governs non-breaking updates only. Breaking API changes always require a new API line (`v2/`) regardless of the range — the range cannot cross a breaking boundary because breaking changes mandate a new `apiVersion`.

### Testing (Future Enhancement)

Testing support for modules and addons is intentionally deferred. The intended commands are:

```bash
# Run tests against a module
vela module test ./aws-s3

# Run tests against an addon
vela addon test ./my-platform-addon
```

Testing at this level is non-trivial: meaningful tests require a live cluster (to verify auxiliary resources apply cleanly, definitions render correctly, and the full installation ordering holds), a way to declare expected outcomes for parameterised CUE templates, and integration with the API line contract validation checks. The tooling and conventions for this are left as a future enhancement once the core module and addon lifecycle is stable.

In the interim, `vela module deploy` against a development cluster combined with `vela module validate --check-breaking` covers the most critical pre-publish checks.

## Non-Goals

- Introducing a new CRD or controller outside the addon system
- Auto-upgrading addons without operator involvement
- Trait binding to module API lines (future work)
- Mandating `modules/` as the directory name — module discovery is by `_version.cue` presence

## Cross-KEP References

- **KEP-2.13** — Declarative addon lifecycle; Addon CR reconciliation; addon-of-addons composition
- **KEP-2.1** — Definition CRD specs that gain `spec.module` and `spec.apiVersion` fields