# KEP-2.25 design 01: API and compile ownership

Concrete shapes behind the [KEP](../README.md). Where a detail here is a proposal rather than an agreed decision, it says so.

## The Package source

The same shape for every registry kind, as addons are enabled by name and version from any registry:

```yaml
spec:
  source:
    registry: catalog          # a cuex-package-registry Config in vela-system
    package: labels            # the path under the registry's root
    version: "~1.4"            # a semver range or an exact version
    digest: sha256:9a1e...     # optional; pins, and must be what version resolves to
```

| Registry type | `package` is | `version` resolves against | `digest` is |
| --- | --- | --- | --- |
| `oci` | a repository under the registry URL | its tags | the manifest digest |
| `helm` | a chart in the repository index | the index's versions | the chart's digest in the index |
| `git`, `gitee`, `gitlab` | a directory under the registry's path | tags `<package>/vX.Y.Z`, or `vX.Y.Z` for the root, as Go modules tag a monorepo | a commit SHA |
| `oss` | a directory | nothing: OSS has no versions, so only `digest` (a content digest) is accepted | a content digest |

- `version` and `digest` together work like an image's `tag@digest`: the digest is what runs, and the controller refuses a digest that is not what the version resolves to. A pinned Package still names its release, for people and for maturity and deprecation.
- At least one of the two is required. A range without a digest is re-resolved on its registry's `resolveInterval` and on spec change, from phase 4; phase 2 accepts an exact version or a digest.

```go
// kubevela/pkg apis/cue/v1alpha1
type PackageSource struct {
	Registry string `json:"registry"`
	Package  string `json:"package"`
	// +optional
	Version string `json:"version,omitempty"`
	// +optional
	Digest string `json:"digest,omitempty"`
}
```

## cuex.yaml

| Field | Required | Meaning |
| --- | --- | --- |
| `path` | yes | the import path without the major (`acme.com/naming`); its last element is the CUE package name, the import identifier and the default Package name |
| `version` | yes | semver; the major goes into the import path |
| `runtime` | yes | `library`, `wasm` or `server` |
| `toolchain` | wasm, server | `tinygo` or `go` |
| `transport` | no | server only: `http` (default) or `grpc` |
| `maturity` | no | `alpha`, `beta` or `stable` (default) |
| `deprecated` | no | a message; the line is deprecated |
| `dependencies` | no | packages the CUE imports, as `path@vN` and a semver range (`acme.com/types@v1 >=1.3`); `vet` checks the list against the imports |
| `abi` | wasm | written by `build`: the Extism ABI the module was built against |
| `server.image` | server | written by `build`: the image, pinned by digest |
| `server.port` | no | server only, default 9443 |

Unknown fields are refused, so a typo is an error.

## Package layout

```
cuex.yaml
*.cue            # the package's CUE, one CUE package
module.wasm      # runtime wasm only
```

Git and OSS hold it as a directory. Helm repositories and OCI hold it as a Helm chart, as modules are published, with a `Chart.yaml` generated from `cuex.yaml`: see [design 02](02-registries.md#a-package-is-a-helm-chart).

## The registry ConfigTemplate

*Proposal*, in KEP-2.18's terms:

```cue
// cuex-package-registry
parameter: {
	type: "oci" | "helm" | "git" | "gitee" | "gitlab" | "oss"
	url:  string
	// git and gitlab: the directory packages live under; oss: the bucket
	path?:   string
	bucket?: string
	// gitlab: the repository name
	repo?: string
	// namespaces whose Packages may use this registry
	namespaces: *["vela-system"] | [...string]
	// how often ranges are re-resolved against this registry
	resolveInterval: *"10m" | string
	insecureSkipTLS: *false | bool
	// phase 7: refuse packages not signed by one of these
	verify?: {keys: [...{secretRef: string}]} | {keyless: {issuer: string, subject: string}}
	// credentials come from fromSecrets: username, token, password
}
```

Only Configs in vela-system are registries; a Config of this template elsewhere is refused at admission.

## Which definition a compile is for

About sixty places compile CUE through a CueX compiler, across `kubevela/pkg`, `kubevela` and `workflow`. A definition's identity is in hand at about ten of them, where definitions are loaded; elsewhere only a CUE string arrives. An `extends` chain compiles several definitions, possibly from different namespaces, in one pass. So the owner travels in the context, which every compile already takes:

```go
// kubevela/pkg cue/cuex
type Owner struct {
	Kind      string // ComponentDefinition, TraitDefinition, ...
	Namespace string
	Name      string
}

func WithOwner(ctx context.Context, owner Owner) context.Context
func OwnerFrom(ctx context.Context) (Owner, bool)
```

`CompileStringWithOptions` reads the owner to choose the packages a compile may import. No owner means vela-system packages only, so a call site that forgets fails safe: it sees less, never another namespace's packages.

### Where the owner is set

| Load point | Repo | Covers |
| --- | --- | --- |
| `appfile.LoadTemplate`, `LoadTemplateFromRevision`, `DryRunTemplateLoader` (`pkg/appfile/template.go`) | kubevela | component, trait and workload-scope policy renders, and admission validation of them; the engines take it from the `*Template` |
| `inherit.Level` gains `Namespace` and `Kind`; every `CompileFunc` wraps its context per level | kubevela | every `extends` chain: render, admission, schema |
| `renderPolicyCUETemplate` (`application_policies.go`) | kubevela | Application-scope policies |
| the source definitions map in `appfile.go`, which keeps each definition's namespace instead of only its template | kubevela | SourceDefinitions and their cache policy |
| the definition webhook handlers and `compileForValidation` callers | kubevela | admission of every definition kind |
| the definition controllers' `Capability*Definition` wrappers | kubevela | stored OpenAPI schemas |
| `WorkflowStepLoader.LoadTemplate` in both repos, through a loader that also returns the owner | kubevela, workflow | workflow steps, in vela-core and in the WorkflowRun controller |
| `Definition.FromCUEString`, once the metadata block is decoded | kubevela | `vela def` and the tools built on it |
| `gen_sdk`, docgen, the MCP server | kubevela | generated SDKs and docs |

The workflow `Loader` interface returns only a template string today and discards which namespace it found the definition in; it gains a variant returning the owner, and the custom task sets it before compiling. That is a change in the `workflow` repo, landing between `kubevela/pkg` and `kubevela`.

### No owner, by design

`vela cuex eval`, adopt and its topology rules, VelaQL views (stored or inline), Config scripts, addon `parameter.cue` schemas, and the `/cuex` compile endpoint compile CUE that belongs to no definition. They see vela-system packages only.

### Tested

Every load point above has a test asserting a compile it starts carries the owner, so a new load point that forgets fails a test rather than silently narrowing what a team's definitions can import.

## Health and status CUE

`healthPolicy`, `customStatus` and `status.details` compile on a bare CUE context, never through CueX, so they cannot import packages. Referring to a package identifier there fails with: `packages are not available in healthPolicy; compute the value in the template and read it from output`. Moving them onto a compiler can follow if there is demand; health runs on every status check, so it would need its own benchmark.

## Revisions

*Proposal.*

```yaml
# ApplicationRevision
spec:
  cuexPackages:
    - path: acme.com/naming@v2
      version: 2.1.3
      digest: sha256:4f2c...
      package: vela-system/naming-v2
```

One entry per package any render of the revision imported, across components, traits, policies and steps. It is a record for `vela live-diff`, audits and the follow-up that reproduces old revisions exactly; nothing re-renders from it in this KEP.
