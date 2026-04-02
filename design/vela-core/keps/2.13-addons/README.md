# KEP-2.13: Declarative Addon Lifecycle

**Status:** Draft
**Parent:** [vNext Roadmap](../README.md)

Addons are the **delivery and distribution mechanism** for versioned X-Definition APIs. The module identity model, API line versioning, and definition naming convention that make those APIs stable and manageable are covered in [KEP-2.20](../2.20-module-versioning/README.md). This KEP covers the declarative addon lifecycle that powers that delivery: continuous reconciliation of the `Addon` CR, drift correction, and addon-of-addons composition.

Together, KEP-2.13 and KEP-2.20 form the complete declarative addon delivery model — KEP-2.20 defines the versioned API contract, KEP-2.13 defines how that contract is reliably delivered and kept in sync with the cluster.

## Problem

KubeVela addons today are installed imperatively via `vela addon enable` and managed one version at a time:

- **Imperative and one-shot**: `vela addon enable` is a single operation, not a continuously reconciled loop. Drift from manual changes to addon-installed resources is not detected or healed.
- **No GitOps support**: There is no CR that represents "addon X at version Y should be installed". Platform teams cannot declare addon state in git and have a controller drive the cluster toward it.
- **No context-aware installation**: Addon definitions cannot gate themselves on cluster capabilities without custom wrapper tooling.
- **Monolithic addon disable**: `vela addon disable` removes all installed definitions immediately, with no mechanism to defer removal until Applications have migrated.
- **No composition model**: Installing multiple related addons with version pinning requires manual coordination.
- **No versioned API delivery**: Without continuous reconciliation, the X-Definition versioning model in KEP-2.20 cannot be reliably enforced — drift correction and deprecation lifecycle management require a continuously reconciled delivery layer.

## Goals

- Establish the `Addon` CR as the declarative delivery unit for versioned X-Definition APIs
- Extend the `Addon` CR to be continuously reconciled — a GitOps-compatible declaration of desired addon state
- Enable drift correction — re-apply definitions removed out of band, preserving the API contract
- Enable context-aware line installation via CueX-evaluated `enabled` in `_version.cue` (see KEP-2.20)
- Enable addon composition via the OAM Application model with a new `addon` component type
- Maintain full backwards compatibility with existing addons

## Declarative Addon CR

The `Addon` CR (which already exists in KubeVela) becomes the primary declarative unit for addon management. Rather than being a status-only record created by `vela addon enable`, the `Addon` CR becomes the desired-state declaration that the addon controller continuously reconciles.

`Addon` is a **cluster-scoped** CRD — it represents a platform-wide capability, not a namespaced resource. The Application that the addon controller creates to manage the addon's resources is installed into `vela-system`.

```yaml
apiVersion: core.oam.dev/v1beta1
kind: Addon
metadata:
  name: aws-s3
spec:
  version: v1.2.0
  registry: my-registry
  parameters:
    region: us-east-1
    enableV2: true
  clusters:
    - local
    - cluster1
  overrideDefinitions: false
  skipVersionCheck: false
```

`vela addon enable aws-s3 --version v1.2.0` now creates or patches an `Addon` CR rather than directly invoking installation logic. The CLI and GitOps workflows are interchangeable — both operate on the same CR.

Reconciliation is suspended by setting the label `controller.core.oam.dev/pause: "true"` on the `Addon` CR — consistent with how Application reconciliation is paused.

## Reconciliation Semantics

The addon controller reconciles continuously — on every change to the `Addon` CR and at a configurable periodic interval (default: 5 minutes). On each cycle:

1. Resolve the addon source from `spec.registry` and `spec.version`
2. Scan the addon source tree for `_module.cue` files under `modules/` and `_version.cue` in subdirectories
3. Evaluate `_context.cue` files via CueX, merging results into the context available to `_module.cue` and `_version.cue` (see KEP-2.20)
4. If `modules/` is present: invoke module lifecycle semantics (see KEP-2.20)
5. If only `definitions/` is present: invoke legacy install with deprecation warning
6. Create or update the owned Application CR for top-level `resources/` content
7. Wait for owned Application to reach Healthy status
8. Apply per-API-line `auxiliary/` resources via server-side apply
9. Wait for auxiliary resources to reach Ready status
10. Apply definitions via server-side apply — definitions are the go-live gate
11. Update Addon CR status with per-module and per-line conditions

Definitions are applied last (step 10) so that no API surface is visible to Application authors until the full stack beneath it — infrastructure and compositions — is operational. See KEP-2.20 (Two-Tier Resource Model) for rationale.

| Event | Controller action |
|---|---|
| Addon CR created | Evaluate context; create owned Application; once healthy, apply auxiliary resources; once ready, apply definitions |
| Addon CR updated | Re-evaluate context; update owned Application; once healthy, update auxiliary; once auxiliary ready, update definitions |
| Addon CR updated with new `spec.version` | Re-evaluate context; upgrade owned Application; once healthy, update auxiliary; once auxiliary ready, update definitions |
| Addon CR deleted | Finalizer runs: remove definitions, delete auxiliary resources, delete owned Application |
| Periodic reconcile | Same as update — re-evaluate and re-apply all three tiers in order; server-side apply is idempotent |

The controller uses a finalizer (`addon.oam.dev/cleanup`) to ensure ordered cleanup on deletion.

## Addon-of-Addons Composition

Multiple addons can be composed into a capability set using the OAM Application model with a new built-in `addon` component type:

```yaml
apiVersion: core.oam.dev/v1beta1
kind: Application
metadata:
  name: data-platform
  namespace: vela-system
spec:
  components:
    - name: aws-s3           # name defaults to component name "aws-s3"
      type: addon
      properties:
        version: ">=1.2.0"   # semver constraint — resolved to highest matching version
        autoUpdate: true      # upgrade automatically when a new matching version appears
        parameters:
          region: us-east-1

    - name: postgres
      type: addon
      properties:
        version: "~2.1.0"    # patch-compatible range: >=2.1.0 <2.2.0
        include:
          resources: false    # install module definitions only; skip Application resources

    - name: aws-s3-v1
      type: addon
      properties:
        name: aws-s3
        version: v1.0.0        # exact pin — autoUpdate has no effect here
        parameters:
          region: us-east-1
```

The `addon` component type causes the Application controller to create or update `Addon` CRs for each component. Dependency ordering between addon components follows the existing OAM resource dependency model.

### Addon CR Naming

The Addon CR name is determined as follows:

1. If the addon declares `instance` in `_module.cue`: `{addon-name}-{instance-value}` (see KEP-2.20). The instance value is evaluated from `spec.parameters` — the addon author defines which parameters form the unique key. Application authors simply supply the required parameters; the naming is the addon's concern, not theirs.

2. Otherwise (single-instance addon): the Addon CR is named after the addon — `{name}` from `AddonComponentProperties`. Installing two components with the same `name` updates the same Addon CR.

**Multi-instance example** — an addon with `instance: parameter.tenant` in `_module.cue`:

```yaml
components:
  - name: s3-team-a
    type: addon
    properties:
      name: aws-s3
      parameters:
        tenant: team-a     # → Addon CR: "aws-s3-team-a"

  - name: s3-team-b
    type: addon
    properties:
      name: aws-s3
      parameters:
        tenant: team-b     # → Addon CR: "aws-s3-team-b"
```

The OAM component name (`s3-team-a`, `s3-team-b`) is only used within the Application for dependency ordering. The Addon CR name is derived entirely from the addon's `instance` expression and the supplied parameters.

## API Changes

### Extended Addon CR Spec

```go
type AddonSpec struct {
    // Version is always an exact semver tag (e.g. "v1.2.0").
    // When created from an addon component with a semver constraint, the
    // controller resolves the constraint and writes the resolved exact version
    // here. The constraint and autoUpdate flag live on the addon component
    // properties, not on the Addon CR.
    Version             string                 `json:"version,omitempty"`
    Registry            string                 `json:"registry,omitempty"`
    Parameters          map[string]interface{} `json:"parameters,omitempty"`
    Clusters            []string               `json:"clusters,omitempty"`
    OverrideDefinitions bool                   `json:"overrideDefinitions,omitempty"`
    SkipVersionCheck    bool                   `json:"skipVersionCheck,omitempty"`
}
```

### `addon` Component Type

The `addon` component type is a built-in `ComponentDefinition` shipped with KubeVela core. It renders an `Addon` CR from the component properties. The application controller treats it identically to any other component type; the addon controller reconciles the resulting `Addon` CR through its normal loop.

```go
type AddonComponentProperties struct {
    // Name is the addon name. Defaults to the component name if omitted.
    Name       string                 `json:"name,omitempty"`
    Registry   string                 `json:"registry,omitempty"`
    // Version accepts an exact version ("v1.2.0") or a semver constraint
    // (">=2.0.0", "~8.0.0", "^1.0.0"). Constraints are resolved against the
    // registry at reconcile time to the highest matching version.
    Version    string                 `json:"version,omitempty"`
    // AutoUpdate enables periodic version checking within the semver constraint.
    // Has no effect when Version is an exact version tag.
    // When true, the reconciler checks the registry at each periodic reconcile
    // interval and upgrades the Addon CR's spec.version to the highest available
    // version still satisfying the constraint. The upgrade follows normal
    // Addon CR update semantics — definitions are re-applied, owned Application
    // is upgraded, and Addon CR status reflects the new installed version.
    AutoUpdate bool                   `json:"autoUpdate,omitempty"`
    Parameters map[string]interface{} `json:"parameters,omitempty"`
    Clusters   []string               `json:"clusters,omitempty"`
    // Include controls which addon asset categories are installed.
    // All categories default to true. Set a category to false to skip it.
    // Useful when a team only wants module definitions installed without
    // the accompanying Application resources (e.g. Crossplane compositions).
    Include    *AddonInclude          `json:"include,omitempty"`
}

type AddonInclude struct {
    Definitions     *bool `json:"definitions,omitempty"`     // default: true
    ConfigTemplates *bool `json:"configTemplates,omitempty"` // default: true
    Views           *bool `json:"views,omitempty"`           // default: true
    // Resources controls installation of top-level resources/ — the owned Application
    // that installs addon infrastructure (operators, CRDs). Skipping this means the
    // addon's owned Application is not created or updated.
    Resources       *bool `json:"resources,omitempty"`       // default: true
    // Auxiliary controls installation of per-API-line auxiliary/ resources (e.g.
    // Crossplane Compositions, KRO ResourceGraphDefinitions). These are applied
    // after the owned Application is healthy. Skipping this installs only
    // definitions and top-level resources, without the line-specific compositions.
    Auxiliary       *bool `json:"auxiliary,omitempty"`       // default: true
}
```

#### Version Constraints and AutoUpdate

`spec.version` in the rendered `Addon` CR always stores the resolved exact version — the constraint is a property of the `addon` component, not the `Addon` CR. When `autoUpdate: false` (the default), the constraint is resolved once at component creation and written as an exact pin. When `autoUpdate: true`, every periodic reconcile cycle re-resolves the constraint against the registry; if a newer matching version is found, the Addon CR's `spec.version` is updated, triggering the normal upgrade path (re-apply definitions, update owned Application).

`autoUpdate` has no effect when `version` is an exact semver tag (no range operators). The addon controller validates this at admission and surfaces a warning condition if `autoUpdate: true` is set with a pinned version.

#### Default Name Convention

The `name` property defaults to the OAM component name (`context.name`) when omitted. This allows concise component declarations:

```yaml
components:
  - name: aws-s3        # addon name also becomes "aws-s3"
    type: addon
    properties:
      version: ">=1.2.0"
      autoUpdate: true
      parameters:
        region: us-east-1
```

Explicit `name` is required only when the component name differs from the addon name (e.g. multi-instance addons where the component name encodes a tenant or environment).

### Addon CR Status (lifecycle fields)

```go
type AddonStatus struct {
    Phase              AddonPhase             `json:"phase,omitempty"`
    ObservedGeneration int64                  `json:"observedGeneration,omitempty"`
    LastReconciledAt   *metav1.Time           `json:"lastReconciledAt,omitempty"`
    InstalledVersion   string                 `json:"installedVersion,omitempty"`
    InstalledRegistry  string                 `json:"installedRegistry,omitempty"`
    ApplicationName    string                 `json:"applicationName,omitempty"`
    ApplicationHealthy bool                   `json:"applicationHealthy,omitempty"`
    InstalledResources AddonInstalledResources `json:"installedResources,omitempty"`
    // Modules records per-module API line states — see KEP-2.20
    Modules            []AddonModuleStatus    `json:"modules,omitempty"`
}

type AddonPhase string
const (
    AddonPhaseInstalling AddonPhase = "installing"
    AddonPhaseUpgrading  AddonPhase = "upgrading"
    AddonPhaseRunning    AddonPhase = "running"
    AddonPhaseFailed     AddonPhase = "failed"
)

type AddonInstalledResources struct {
    Definitions     []AddonResourceRef `json:"definitions,omitempty"`
    VelaQLViews     []AddonResourceRef `json:"velaQLViews,omitempty"`
    ConfigTemplates []AddonResourceRef `json:"configTemplates,omitempty"`
}

type AddonResourceRef struct {
    Name              string `json:"name"`
    Kind              string `json:"kind"`
    Deprecated        bool   `json:"deprecated,omitempty"`
    DeprecatedAt      string `json:"deprecatedAt,omitempty"`
    NoReferencesSince string `json:"noReferencesSince,omitempty"`
}
```

## Implementation Location

Implemented entirely in KubeVela core (`github.com/kubevela/kubevela`):

- `pkg/addon/` — addon source loading, module tree scanning
- `pkg/controller/addon/` — `AddonReconciler` extension with continuous reconciliation
- `pkg/webhook/core.oam.dev/v1beta1/application/` — `instance` parameter presence validation; `autoUpdate` + pinned version warning

## Implementation Constraints

### CueX Circular Dependency

The addon controller uses CueX to evaluate `_context.cue`, `_module.cue`, and `_version.cue` expressions. CueX itself is distributed as a KubeVela addon. This creates a potential circular dependency at startup:

```
cuex addon → installs CueX definitions → addon controller uses CueX → evaluates addon definitions → cuex addon
```

This cycle is real and has been encountered in implementation. It is resolved via **interface injection at startup** — the addon controller accepts a CueX evaluator interface that is wired at process startup rather than resolved through the addon system. The concrete CueX implementation is linked directly into the controller binary; the addon system installs the CueX *definitions* (WorkflowStepDefinitions etc.) but does not gate the controller's ability to evaluate CUE.

**Implication for implementers:** Do not attempt to load the CueX evaluator through the addon registry at runtime. The evaluator must be injected as a dependency at controller construction time. Any refactor that moves CueX evaluation behind a dynamic addon lookup will reintroduce the cycle.

## Security Considerations

- **RBAC for Addon CR creation**: Creating an `Addon` CR causes the controller to install arbitrary Definitions. RBAC should limit Addon CR creation to platform team service accounts.
- **Instance isolation**: Multi-instance Addon CRs carry the addon registry name as their `addon.oam.dev/name` label, keeping fleet-level queries (e.g. "all installs of aws-s3") accurate regardless of per-instance CR names.
- **`instance` CUE evaluation sandbox**: The `instance` expression in `_module.cue` is evaluated in the same CueX sandbox as `_context.cue`. It has no side effects and is bounded to string output — see KEP-2.20 for sandbox constraints.
- See KEP-2.20 for module-specific security considerations (definition name collision, remote defkit source trust boundary, CueX evaluation sandbox).

## Alternatives Considered

### Group / Module / Component Directory Layering

**Suggestion:** Introduce a three-level hierarchy within `modules/` — a `group` directory (e.g. `aws`), a `module` directory beneath it (e.g. `s3`), and component definitions beneath that (e.g. `bucket.cue`). Under this model, the full path would be `modules/aws/s3/v1/bucket.cue` and the reference syntax would require both a group and module qualifier.

**Decision: not adopted.** The current two-level structure (`modules/aws-s3/v1/bucket.cue`) is sufficient and introduces less cognitive overhead for the following reasons:

- **The addon can serve as the grouping structure.** Modules within an addon are naturally grouped by their shared delivery and lifecycle unit — the `Addon` CR itself. `aws-s3` and `aws-rds` are both modules within an `aws` addon, and that addon boundary already conveys the relationship. A formal `group` directory adds a concept without adding capability.
- **Grouping is already expressible via naming convention.** `aws-s3`, `aws-rds`, and `aws-eks` are self-evidently in the same `aws` family. Label selectors (`definition.oam.dev/module=aws-s3`) already support fleet-level queries by module. No directory nesting is required to answer "give me all AWS definitions".
- **The reference syntax stays simple.** `type: bucket/v1` with `module: aws-s3` maps directly to the two concepts users need to know: what definition, from which module. A group layer would require either a path-like `module: aws/s3` field value or separate `group:` and `module:` fields — both add surface area with no new capability.
- **The four-concept stack is already at the cognitive limit.** Users reason about: addon → module → API line → definition. Adding `group` as a formal concept between addon and module increases the stack to five levels. Discovery of "what concrete type do I use?" already requires navigating module names, API versions, and definition names; a group qualifier makes that traversal harder, not easier.

The naming convention (`{module}-{definition}-{apiVersion}`) already encodes the vendor/service relationship that groups would formalise. If a future use case emerges that naming and label selectors genuinely cannot serve, a group concept can be introduced then with concrete motivation.

## Cross-KEP References

- **KEP-2.20** — Module identity, API line versioning, definition naming convention, deprecation lifecycle
- **KEP-2.6** — KubeVela Operator installs and drift-corrects the addon controller deployment
