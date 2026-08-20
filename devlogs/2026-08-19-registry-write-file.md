# registry.#WriteFile: writing a file back to an addon registry

Date: 2026-08-19
Branch: feature/source-definitions
Repos touched: kubevela/ only

## Why

`registry.#ReadFile` (added in 94fc38127) lets a template pull a file out of a
registry the platform configured, without the template carrying a URL and a
token. The same argument applies in the other direction: a workflow step that
needs to commit a rendered manifest, a version bump or a generated config back
to a repo should name a registry, not carry push credentials of its own.

This is **for workflow steps only**. It is deliberately not reachable from a
SourceDefinition, for reasons recorded under "Capability split" below.

Registries being an addon concept rather than a general-purpose one is
acknowledged and out of scope here. We build on `addon.Registry` as it stands.

## Decisions taken

| Decision | Choice | Note |
| --- | --- | --- |
| Backends in v1 | GitHub, Gitee, GitLab | Helm and OSS refused by name |
| Write credential | the registry's existing token | see "Trade-off accepted" |
| Reachable from sources | no, enforced by the compiler split | see below |
| Behaviour when content is unchanged | no commit, `changed: false` | see "Idempotency" |

## What exists today

| Piece | Where | State |
| --- | --- | --- |
| `AsyncReader` | `pkg/addon/source.go:237` | read-only: `ListAddonMeta`, `ReadFile`, `RelativePath` |
| `Registry.BuildReader` | `pkg/addon/source.go:395` | builds a reader per backend, passes the token |
| git readers | `reader_github.go`, `reader_gitee.go`, `reader_gitlab.go` | each holds a client that can already write |
| `FileReader` iface + DI | `pkg/cue/cuex/providers/registry/registry.go` | impl registered in `cmd/core/app/bootstrap.go` |
| `registry.Package` | registered with `WorkloadCompiler` only | **not** in the workflow compiler |

The last row is the surprise: `registry.#ReadFile` is not callable from a
workflow step today. Registering it is part of this work.

## Capability split

Two compilers, two package variants, one Go implementation.

```
pkg/cue/cuex/providers/registry/
  registry.go        FileReader, FileWriter, ReadFile(), WriteFile()
  registry.cue       #ReadFile                -> ReadOnlyPackage
  registry_rw.cue    #ReadFile + #WriteFile   -> Package
```

| Compiler | File | Registers | Templates can call |
| --- | --- | --- | --- |
| `WorkloadCompiler` | `pkg/cue/cuex/compiler.go:44` | `registry.ReadOnlyPackage` | `#ReadFile` |
| workflow steps | `pkg/workflow/providers/compiler.go:53` | `registry.Package` | `#ReadFile`, `#WriteFile` |

A SourceDefinition cannot call `#WriteFile` because the symbol is not in the
package it compiles against. This is enforcement, not documentation.

Why sources must not reach it: `sourceResolver.resolve`
(`pkg/cue/definition/template.go:1114`) re-runs on cache expiry and on every
reconcile with a cold cache, serves a stale value when a refresh fails
(`OnStaleFailure: useStale`, so a failed write would report `Resolved`), and is
reached from the render path at `pkg/appfile/validate.go:165`, which `vela
dry-run` also walks. A write there would fire on a timer, retry on reconcile,
fail silently, and commit during a dry run.

## Backend work

`AsyncWriter`, a second narrow interface alongside `AsyncReader`:

```go
// AsyncWriter writes a single file back to a registry.
type AsyncWriter interface {
	// WriteFile writes content at path. It returns the resulting commit sha,
	// and false when the file already held exactly this content.
	WriteFile(path, content, message string) (commit string, changed bool, err error)
}
```

`Registry.BuildWriter(opts ...ReaderOption) (AsyncWriter, error)` mirrors
`BuildReader`, reusing `WithRef` for the target branch.

| Backend | Client already held | Write call | Notes |
| --- | --- | --- | --- |
| Git (GitHub) | `*github.Client`, go-github v32 | `Repositories.CreateFile` / `UpdateFile` | update needs the current blob sha |
| Gitee | hand-rolled `*Client` over net/http | `PUT/POST /repos/{owner}/{repo}/contents/{path}` | hand-rolled, matching the existing reader style |
| GitLab | `*gitlab.Client`, gitlab.com/gitlab-org/api/client-go | `RepositoryFiles.CreateFile` / `UpdateFile` | create vs update is a distinct call, no sha |
| OSS | resty, no token passed (`source.go:398`) | refused | credential is dropped on the floor today |
| Helm | n/a | refused | a chart repo has no file-write concept |

Refusals name the type: `registry %q is a %s registry, which does not support
writes`.

Path handling follows each reader exactly, including GitLab's
`GetProjectPath()+"/"+path`, so a write lands where the matching read would find
it.

## Provider surface

```cue
// #WriteFile writes a single file back to a named addon registry.
#WriteFile: {
	#do:       "write-file"
	#provider: "registry"

	$params: {
		// +usage=Name of a registry configured in this cluster, as listed by `vela registry ls`
		registry: string
		// +usage=Path of the file within the registry, relative to the registry's own root path
		path: string
		// +usage=The file contents to write, verbatim
		content: string
		// +usage=Branch to commit on. Defaults to whatever the registry's URL pinned.
		ref?: string
		// +usage=Commit message. Defaults to a generated one naming the path.
		message?: string
	}

	$returns?: {
		// +usage=Sha of the commit made. Empty when nothing changed.
		commit: string
		// +usage=False when the file already held exactly this content
		changed: bool
		...
	}
	...
}
```

Worked example, a step definition:

```cue
import "vela/registry"

"write-manifest": {
	type: "workflow-step"
	annotations: {}
	description: "Commit a rendered manifest back to a registry"
}
template: {
	write: registry.#WriteFile & {
		$params: {
			registry: parameter.registry
			path:     parameter.path
			content:  parameter.content
			message:  parameter.message
		}
	}

	parameter: {
		registry: string
		path:     string
		content:  string
		message:  *"vela: update file" | string
	}
}
```

Go side mirrors `ReadFile` exactly: `WriteFileVars`, `WriteFileResult`,
`WriteFileParams`, `WriteFileReturns`, and a `FileWriter` interface resolved
through `pkg/registry` DI, with the implementation registered in
`cmd/core/app/bootstrap.go` next to `addonRegistryFileReader`. Same cycle-break,
same testability.

## Idempotency

A workflow step is ordered and runs once on success, but its CUE re-evaluates on
each reconcile until the step completes, and failure, suspend and resume all
re-enter it. So `WriteFile` reads the current blob first and returns
`changed: false` with no commit when the content already matches. This kills the
retry-double-commit case and stops a repo filling with empty commits, and on
GitHub the same read supplies the sha the update needs anyway.

## Errors

Errors are returned, never swallowed, matching `ReadFile`'s reasoning: a missing
registry, an unwritable backend, an auth failure or a rejected push should fail
the step loudly. Messages carry the registry, the path and the ref.

## Test plan, in build order

Each step is a commit, tests first.

| # | Step | Tests |
| --- | --- | --- |
| 1 | `AsyncWriter` iface + `BuildWriter` dispatch | table test: each backend type returns a writer or the right refusal; OSS and Helm refused by name |
| 2 | GitHub writer | httptest server: create, update with sha, unchanged no-ops, auth failure surfaces |
| 3 | GitLab writer | httptest: create vs update paths, ref honoured, project path prefixing |
| 4 | Gitee writer | httptest: create, update, unchanged |
| 5 | `WriteFile` provider fn | fake `FileWriter`: required-field errors, ref/message defaulting, error wrapping, missing-writer message |
| 6 | package split (`ReadOnlyPackage` vs `Package`) | compile a template calling `#WriteFile` against `WorkloadCompiler` and assert it fails; against the workflow compiler and assert it resolves |
| 7 | compiler registration | assert `registry` is in the workflow compiler's package set |
| 8 | bootstrap wiring | `addonRegistryFileWriter` resolves a registry and delegates |

Step 6 is the one that matters. It is the regression guard on the capability
boundary, and it is the test that fails if someone later "tidies up" by
registering one package in both compilers.

e2e at the checkpoint: a real step against a scratch repo, asserting the commit
lands and a re-run of the same step commits nothing.

## Trade-off accepted

Writes use the registry's existing token. That means any workflow step naming a
registry can commit with the platform's credential, using the controller's
identity rather than the author's, and most existing registry tokens were issued
for read. This is a deliberate v1 choice for symmetry with `ReadFile`. If it
needs tightening later, the shape is an optional write-scoped credential on
`Registry`, with a registry lacking one being read-only. `BuildWriter` is the
single place that would change.

## Not doing

- OSS writes. Needs credentials plumbed through `BuildReader`/`BuildWriter`
  first, and OSS has no commit semantics, so `commit` would be meaningless.
- Deletes, renames, multi-file commits. One file, one commit, until there is a
  case for more.
- Decoupling registries from addons. Noted as the real fix, out of scope here.
