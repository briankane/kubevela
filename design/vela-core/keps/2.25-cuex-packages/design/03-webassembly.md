# KEP-2.25 design 03: WebAssembly

Phase 3 of the [KEP](../README.md): the module contract, the host, limits, toolchains and the authoring commands. Numbers are the prototype's (Apple M-series); phase 3 re-measures them on the CI's Linux runners, and the defaults below are confirmed or changed by that benchmark.

## The module contract

A WebAssembly package is the layout from [design 01](01-api.md#package-layout): `cuex.yaml`, its `*.cue`, and `module.wasm`. **The CUE files are the contract**, as for libraries and servers. The module carries functions only.

| A module | |
| --- | --- |
| exports | one function per `#do` in the package's CUE, named as the `#do` value |
| takes | `$params` as JSON, through Extism's input |
| returns | `$returns` as JSON, through Extism's output, and 0 |
| fails | with a message through Extism's error, and non-zero; the render fails with `<path>.<#do>: <message>` |
| imports | Extism's kernel, WASI preview 1 (for the toolchains' runtimes, with nothing behind it) and `cuex.log` |

The host reads the `#do` values from the CUE and refuses a module missing an export, or exporting a function no `#do` names (a typo on either side). `build` records the Extism ABI version in `cuex.yaml` (`abi`) and the chart's annotations; the host lists the ABIs it supports and refuses others with `Loaded=False`.

## The Go SDK

```go
package main

import "github.com/kubevela/pkg/cue/cuex/wasm/guest"

type labelParams struct {
	Name      string `json:"name"`
	MaxLength int    `json:"maxLength,omitempty"`
}

func dnsLabel(p labelParams) (string, error) {
	guest.Log("shortening %q", p.Name)
	return shorten(p.Name, p.MaxLength), nil
}

//go:wasmexport dnsLabel
func dnsLabelExport() int32 { return guest.Run(dnsLabel) }

func main() {}
```

| Function | Does |
| --- | --- |
| `guest.Run[P, R](fn func(P) (R, error)) int32` | decodes the input into `P`, calls `fn`, encodes `R` or sets the error |
| `guest.Log(format string, args ...any)` | writes a line through the `cuex.log` host function |

`guest.Template` and the `cuex_template` export from the prototype are gone. The SDK is a module of its own, `github.com/kubevela/pkg/cue/cuex/wasm/guest`, depending on Extism's Go PDK only, so a module does not pull in `kubevela/pkg`.

## The host

Extism's Go SDK over wazero, kept behind `kubevela/pkg`'s `cue/cuex/wasm`.

- **Nothing granted**: no allowed hosts or paths, no environment, fake clocks and a fixed random seed. WASI is enabled because both toolchains' runtimes call it, with nothing behind it.
- **One host function**, `cuex.log`, which only takes output. It writes to the controller log at verbosity 4 with the package, function and render's owner, limited to 20 lines a second per module (the rest counted and dropped). Under `vela cuex package test` and `render` it goes to stderr unlimited.
- **The program name** is the package's name, given as `argv[0]`, since Go's runtime and klog read it.
- **A trap, a deadline or the memory limit retires the instance**; the call fails, and the next call gets a new one. With Extism, a refused host call (an attempted HTTP request) fails the call without a trap.

## Instances

| Toolchain | Mode | Why | One call |
| --- | --- | --- | --- |
| TinyGo (`-scheduler=none`) | fresh instance per call | starts in tens of microseconds, so a result depends on its parameters alone | 130 to 250 µs |
| Go | instances reused | starts in milliseconds | 52 to 96 µs |

The mode comes from the toolchain, recorded in the package's metadata; nothing sets it per Package. With reuse, globals survive between calls and keeping that harmless is the author's job: `vet` warns on package-level variables a function assigns, and the Package's status shows `instances: reuse`.

Instances start on first call, not on load, up to `instances` running at once; a call beyond that waits for one within its deadline. A module replaced by a new digest drains: calls already running finish on the old instances, new calls go to the new module, and the old one closes when idle.

## Limits

Per Package, bounded by the namespace:

```yaml
spec:
  wasm:                 # all optional
    memory: 32Mi        # per instance
    timeout: 2s         # per call, within the render's own deadline
    instances: 4        # running at once
```

| | TinyGo default | Go default | Namespace cap (default) |
| --- | --- | --- | --- |
| `memory` | 32Mi | 128Mi | 256Mi per Package |
| `timeout` | 2s | 2s | 10s |
| `instances` | 4 | 4 | 16 |
| modules loaded | | | 20 per namespace |
| memory, all modules | | | 1Gi per namespace (`memory` × `instances`, summed) |
| package size | | | 16Mi |

A Package above a cap is refused at admission, and `Loaded=False` if the cap is lowered under it. Caps are controller flags (`--cuex-wasm-max-memory` and so on), overridden per namespace by annotations only the platform can set, each named for the cap it replaces (`wasm-max-memory` is the namespace's total across modules):

```yaml
apiVersion: v1
kind: Namespace
metadata:
  name: team-a
  annotations:
    cue.oam.dev/wasm-max-modules: "40"
    cue.oam.dev/wasm-max-memory: 2Gi
```

The memory figures bound what wazero may allocate, not what the controller holds: a module's compiled code is held once per replica (about 8 MiB for TinyGo, 60 MiB for Go with Kubernetes types), and counts toward the namespace's total at its module size.

## Cache

```
<cache>/modules/<digest>/module.wasm     # fetched, by package digest
<cache>/compiled/                        # wazero's compilation cache
```

The compilation cache is wazero's own, keyed by module and wazero version, so a controller upgrade that changes wazero recompiles rather than loading stale code. Both are on the controller's cache volume (an emptyDir by default, [design 02](02-registries.md#libraries-kept-in-cluster)). From the disk cache, a TinyGo module loads in 7 to 10 ms and a Go one in 38 ms.

## Toolchains

| Toolchain | Builder image | Command |
| --- | --- | --- |
| `tinygo` | `tinygo/tinygo:0.44.0@sha256:…` | `tinygo build -target=wasip1 -buildmode=c-shared -no-debug -opt=2 -scheduler=none -o module.wasm` |
| `go` | `golang:1.27.1@sha256:…` | `GOOS=wasip1 GOARCH=wasm go build -buildmode=c-shared -trimpath -o module.wasm` |

- **Pinned in the CLI**, by digest, and bumped with KubeVela releases, so a given `vela` and source build the same digest anywhere.
- **Recorded**: `build` writes the image it used into the chart's annotations (`cue.oam.dev/toolchain-image`), so a package says what built it.
- **Overridable**: `--toolchain-image` builds with another image, and the package records that one.
- **Run in any docker-compatible runtime**, as the calling user, with the module cache and build cache mounted, and any `replace` directives pointing outside the project mounted read-only.

A new language is a row here plus a guest SDK; Extism's PDKs cover most.

## Authoring commands

| Command | Runs |
| --- | --- |
| `vela cuex package build` | the toolchain build, then writes `Chart.yaml` and the annotations |
| `vela cuex package vet` | the controller's check from [design 02](02-registries.md#fetching-and-checking), plus the exports matching the CUE's `#do` values both ways, the ABI, and the reuse warning |
| `vela cuex package test` | `go test` in the builder image, `build`, then the `*_test.cue` files with `vela def test`'s framework (`test.#PackageCall`) against the built module, logs on stderr |
| `vela cuex package render <file>` | a CUE file against the local package, as the controller would compile it |

## Measured in phase 3

- Call latency and load time per toolchain on Linux amd64 and arm64, fresh and reused, to confirm the defaults above.
- Memory per loaded module, idle and under `instances` concurrent calls.
- A render importing a WebAssembly package against the same render with the function as a built-in, as the overhead a team pays.
