# KEP-2.25 design 05: servers

Phases 5 and 6 of the [KEP](../README.md): the wire contract, the server SDK, mutual TLS, what the controller creates for a server Package, the `Describe` check, rolling out a new version, calls from vela-core, and what changes for Packages written the old way. Phase 5 is everything over HTTP; phase 6 adds gRPC.

Figures are from the spike in `kubevela/pkg` (`cue/cuex/serverspike`): mutual TLS on loopback, an Apple M2 Max under load, so the ratios matter more than the absolute times. Phase 5 re-measures in a cluster.

## What exists today

| | Today | Problem |
| --- | --- | --- |
| Server SDK | `externalserver.NewServer(path, fns)`: go-restful, `POST <path>/<fn>`, self-signed TLS | every error is a 500 with text; no `Describe`; no health endpoint |
| Client | `ExternalProviderFn`: `POST <endpoint>/<fn>` with `$params` as JSON | a result must be a JSON object; the status code is ignored; TLS is never verified; no size limit |
| Connections | `http.Transport`'s default: two idle connections per host | under load, almost every other call opens a new TLS connection |
| Authentication | `provider.header`, sent on every call | written in the Package in plain text |
| gRPC | declared in the API | "protocol grpc not supported yet" |

The last row of the spike's benchmark is today's client:

| Client, 96 callers at once | µs per call | New connections per call | Allocated per call |
| --- | --- | --- | --- |
| today's (2 idle per host) | 122 | 0.48 | 72.7 KB |
| HTTP/1.1, 64 idle per host | 14 | 0.006 | 9.6 KB |
| HTTP/2, one connection | 35 to 41 | 0.001 | 12.0 KB |
| gRPC | 16 to 18 | | 10.1 to 10.5 KB |

With a 64 KiB result, gRPC takes 73 to 82 µs against pooled HTTP/1.1's 103 to 106 µs, and allocates 229 KB against 319 KB. Parameters and results are JSON on both, since the CUE is the contract, so gRPC's gain is framing only.

## Transports

Both, served by one SDK from the same registered functions. `cuex.yaml`'s `transport` picks one:

| | `http` (default) | `grpc` |
| --- | --- | --- |
| Suits | most packages; anything curl should reach | packages returning large results (about 25% faster at 64 KiB) |
| Service | ClusterIP | headless |
| Balancing | per connection by kube-proxy; the SDK closes connections older than 2 minutes so pools reach new pods | the client's `round_robin` over the headless Service's addresses; the SDK sets `MaxConnectionAge` to 2 minutes |
| Health | `GET /cuex/healthz` | `grpc.health.v1` |
| Phase | 5 | 6 |

The balancing row matters for replicas and the HPA: kube-proxy balances connections, not calls. A gRPC client keeps one connection, so without client-side balancing a second replica gets no traffic at all. A pooled HTTP client keeps each connection on the pod it first reached, so the SDK's maximum connection age is what lets a scale-up take load. The spike checks it: calls over 300 ms against a 50 ms age re-dialled about once per 50 ms.

## The wire contract

### HTTP

| Request | Answer |
| --- | --- |
| `POST /cuex/call/<function>`, body `$params` as JSON | 200 with `$returns` as any JSON value, or an error |
| `GET /cuex/describe` | the description below |
| `GET /cuex/healthz` | 200 once the server can take calls |

| Header | Carries |
| --- | --- |
| `X-Cuex-Timeout` | the time left before the render's deadline, as a Go duration; the SDK puts it on the call's context |
| `traceparent`, `tracestate` | the render's trace, as today |
| `X-Vela-Encoded-Context` | the render's propagated context, as today |

An error is a non-2xx status with a JSON body:

```json
{"code": "InvalidArgument", "message": "repo must be owner/name"}
```

### gRPC

```proto
syntax = "proto3";
package cuex.v1;

service Provider {
  rpc Describe(DescribeRequest) returns (Description);
  rpc Call(CallRequest) returns (CallResponse);
}

message CallRequest  { string function = 1; bytes params = 2; }   // params: JSON
message CallResponse { bytes returns = 1; }                       // returns: JSON
message DescribeRequest {}
message Description {
  string path = 1;
  string version = 2;
  map<string, string> files = 3;   // name to CUE source
  repeated string functions = 4;
  string sdk = 5;
}
```

Deadlines are gRPC's own, the trace and context travel in metadata under the same names, and an error is a status with the code below.

### Errors

| Code | Means | HTTP | gRPC | The render |
| --- | --- | --- | --- | --- |
| `InvalidArgument` | the parameters are wrong; the same call fails again | 400 | `InvalidArgument` | fails with `<path>.<#do>: <message>` |
| `Unavailable` | the server, or something it depends on, can't answer now | 503 | `Unavailable` | fails retryably: a workflow step backs off, a source keeps its cached value |
| `Internal` | anything else | 500 | `Internal` | fails with the message |

A connection failure, a deadline and a response over the size limit are `Unavailable`, `Unavailable` and `Internal`. The client never retries a call itself: a function may write, and only its caller knows whether repeating it is safe.

A server not built on the SDK has no `code` in its body, so the status decides: 4xx is `InvalidArgument` except 429, which with 502, 503 and 504 is `Unavailable`, anything else is `Internal`, and the body becomes the message. A 2xx body that is not JSON is `Internal`.

### Limits

`$params` and `$returns` are each limited to 4 MiB, gRPC's default message size, on both transports. The spike checks both sides refuse a 5 MB result.

## Describe

`Describe` returns the package's own files, not a digest of them, so the controller canonicalises both sides with one formatter. A server built with another CUE version can't fail the check because its formatter lays things out differently.

The canonical form, per file:

1. parse, with comments;
2. drop every comment;
3. clear every position in the syntax tree, so the formatter lays the file out itself;
4. format, simplified.

The forms are sorted, so file names and order don't count, and hashed with SHA-256, each prefixed by its length.

Step 3 is the spike's finding. CUE's formatter keeps the author's line breaks, so "formatted, comments removed" alone gives `{s: string, sep: string}` and the same struct over three lines different digests.

| Change | Same digest |
| --- | --- |
| comments added or removed | yes |
| indentation, alignment, a struct on one line or several | yes |
| a file renamed, or the files listed in another order | yes |
| a bound changed (`<=63` to `<=253`) | no |
| a `#do` renamed | no |
| a field made required | no |
| `a: b: 1` written as `a: {b: 1}` | no: a different spelling is a different build |

The controller also compares `path`, `version` and `functions` (the `#do` values) with the package's own. What the server cannot call travels beside the files and is not part of the digest; see [Credentials](#credentials-and-what-a-server-can-call).

## The Go SDK

A module of its own, `github.com/kubevela/pkg/cue/cuex/server`, depending on CUE's parser and gRPC only, so a server does not pull in `kubevela/pkg`. The legacy `externalserver` package stays as it is for servers written the old way.

```go
package main

import (
	"context"
	"embed"
	"fmt"
	"strings"

	"github.com/kubevela/pkg/cue/cuex/server"
)

//go:embed cuex.yaml *.cue
var pkg embed.FS

type prParams struct {
	Repo   string `json:"repo"`
	Number int    `json:"number"`
}

type prState struct {
	State     string `json:"state"`
	Mergeable bool   `json:"mergeable"`
}

func pullRequest(ctx context.Context, p prParams) (prState, error) {
	if !strings.Contains(p.Repo, "/") {
		return prState{}, server.Invalid(fmt.Errorf("repo must be owner/name, got %q", p.Repo))
	}
	...
}

func main() {
	server.Main(pkg, server.Func("pullRequest", pullRequest))
}
```

| Function | Does |
| --- | --- |
| `server.Func[P, R](name, fn func(context.Context, P) (R, error))` | registers a function: JSON into `P`, `R` back as JSON; a `P` that doesn't decode is `InvalidArgument` |
| `server.Invalid(err)`, `server.Unavailable(err)` | mark an error's code; any other error is `Internal` |
| `server.Main(fsys, fns...)` | reads `cuex.yaml` and the CUE from `fsys`, checks the functions, serves the transport `cuex.yaml` names, and exits on SIGTERM after draining |

**At startup** the SDK refuses a function list that differs from the CUE's `#do` values, naming both sides (`missing [split], not in the CUE [splt]`), as the WebAssembly host does. It writes the reason to `/dev/termination-log`, so the Package's condition can quote it rather than pointing at a crash loop.

**From the environment**, all set by the controller: `CUEX_PORT` (default 9443) and `CUEX_TLS_DIR` (default `/var/run/cuex/tls`, holding `tls.crt`, `tls.key` and `ca.crt`). The certificate is re-read when the mounted Secret changes, so a rotation needs no restart. `CUEX_INSECURE=true` serves plain HTTP without client certificates, for `vela cuex package test` and local runs only; the controller never sets it.

**`vela cuex package init --runtime server`** scaffolds this `main.go`, a Go test per function, the Dockerfile on a pinned distroless base (which the project may edit), and `transport: http`.

## Mutual TLS

One mechanism covers both encryption and authentication, replacing a per-Package token.

| Secret | Namespace | Holds | Lifetime |
| --- | --- | --- | --- |
| `cuex-package-ca` | vela-system | the CA's key and certificate | 10 years |
| `cuex-package-client` | vela-system | vela-core's client certificate, CN `vela-core` | 90 days, renewed at 60 |
| `<server>-tls` | the Package's | the server's certificate for its Service names, and `ca.crt` | 90 days, renewed at 60 |

- **The leader issues and renews** every certificate. Every replica reads the client certificate from its informer and uses the new one for new connections; open ones finish on the old.
- **The server accepts only vela-core.** The SDK requires a client certificate chaining to `ca.crt` with CN `vela-core`. No team can obtain one: the CA's key and the client key never leave vela-system.
- **The client accepts only that Package's server.** It verifies the server's certificate against the CA, for the Service name the Package's status names. A team that can read its own `<server>-tls` holds a certificate for its own server's name only, so it can't stand in for another Package's server.
- **Rotating the CA** adds the new CA beside the old in every `ca.crt` and in the client's pool, reissues every leaf from the new CA, then drops the old. Each step waits for the one before to reach every server.

The spike covers the refusals on both transports: no client certificate, a client certificate from another CA, a client other than `vela-core`, another Package's server name, and a server certificate from another CA. It also covers rotation: new connections get the new certificate while pooled ones keep working.

cert-manager would do the issuing, but it's an optional dependency KubeVela can't assume, and certificates for its own servers are the controller's to manage. A cluster that has it changes nothing here.

## Credentials, and what a server can call

A server usually calls something on its own key: GitHub, a cloud API, a database. The key belongs to the Package, not the package: one image, installed twice with two keys, is two Packages.

```yaml
spec:
  server:
    secrets: [{name: github-app}]   # in the Package's namespace, mounted read-only
```

- **Mounted, never passed.** Each Secret is mounted at `/var/run/cuex/secrets/<name>/`. vela-core never reads it and no call carries it. The permission check on the server spec covers it: whoever may create Deployments in the namespace may already mount its Secrets.
- **Different keys, different Packages.** A team that wants its own key installs its own copy, which imports as `local/` in its namespace; the platform's copy, on the platform's key, stays the bare path. Nothing chooses a key per call.
- **A key worth guarding is restricted.** `spec.restrictions` says which namespaces may call its functions, all or by group or function, as a definition's says where it may be used, with a required role or RBAC grant on top where a namespace is too coarse ([design 06](06-selection.md#restricted-functions)). So the platform's `githubwrite` can be callable from team-a for issues and not for administration. `$requires` below says what the key can do; restrictions say who may use it.

A function may say what its server's key needs, in its CUE, opaque to KubeVela. A package generated from an API that publishes its permissions takes them from there, as the GitHub sample does:

```cue
// Get a repository
#ReposGet: {
	#do:       "repos/get"
	$groups: ["contents:read"]
	$requires: {contents: "read"}
	$params: {owner: string, repo: string}
	$returns: {status: int, body: #FullRepository}
}
```

The server knows what its key was granted, so `Describe` reports what it cannot call, beside the files and outside the digest:

```json
{"availability": "known",
 "unavailable": [{"function": "repos/delete", "requires": {"administration": "write"}, "granted": {"administration": "read"}}],
 "unknown": []}
```

`availability` is `unknown` when the server cannot tell, as with a key that does not list its own grants; every function is then reported as unknown, not as available. A function with no `$requires` is listed under `unknown` even when the key is known, since there is nothing to compare.

| Where | Shows |
| --- | --- |
| Package status | each function with `requires` and `available: true`, `false` or `unknown`, with the reason |
| `vela cuex package show`, VelaUX | the same, so an author sees what a Package's key allows before writing against it |
| Definition admission, `vela def vet` | a warning naming each function the template refers to that its Package marks unavailable |
| A call to one | fails without reaching the server: `github-v1 cannot call repos/delete: its key lacks administration: write` |

The controller asks again on every rollout and every `--cuex-server-describe-interval` (default 5 minutes), since a key's grants change without the Package changing. Availability never affects Ready: a Package whose key allows nothing is still the package it says it is.

## What the controller creates

The Package controller deploys nothing itself. For each server Package it writes one Application in the Package's namespace, owned by the Package, named `cuex-<package>` (for example `cuex-github-v1`) and labelled `cue.oam.dev/package`, and KubeVela deploys it like any other. The Package controller keeps what only it can do: the certificates, the `Describe` check, the switch and the permission check.

```yaml
apiVersion: core.oam.dev/v1beta1
kind: Application
metadata:
  name: cuex-github-v1
  namespace: team-a
  ownerReferences: [{kind: Package, name: github-v1, controller: true}]
spec:
  components:
    - name: github-v1-4f2c9a1e        # one per live digest: two during a rollout
      type: cuex-server
      properties:
        image: ghcr.io/acme/github@sha256:4f2c...
        transport: http
        port: 9443
        tlsSecret: github-v1-4f2c9a1e-tls
        replicas: 2
        resources: {...}
        serviceAccountName: github
        secrets: [github-app]
        pullSecrets: [...]
```

| Who | Owns | Reverts |
| --- | --- | --- |
| the Package controller | the Application's spec, and the `<component>-tls` Secret it issues | any edit to the Application, by server-side apply; admission refuses changes to it by anyone else |
| the Application controller | what the component renders | drift on the Deployment and the rest, image included, as it does for every Application |

### The `cuex-server` definition

KubeVela ships a `cuex-server` ComponentDefinition in vela-system. It is the platform's to edit, like any shipped definition, so pod security, sidecars, node placement or the NetworkPolicy are shaped there rather than by fields on the Package. `spec.server`'s fields are its parameters.

As shipped, it renders:

| Object | When | Notes |
| --- | --- | --- |
| Deployment | always | see below |
| Service | always | ClusterIP for HTTP, headless for gRPC; port 9443 |
| NetworkPolicy | always | ingress from vela-core's pods only, on the server's port; egress open |
| PodDisruptionBudget | more than one replica | `maxUnavailable: 1` |
| HorizontalPodAutoscaler | `autoscaling` set | the Deployment's replicas are then left to it |

The pod:

| | |
| --- | --- |
| Image | `cuex.yaml`'s `server.image`, pinned by digest |
| Pull secrets | `spec.pullSecrets`, plus the registry's own Secret when the Package is in vela-system and that Secret is a docker config |
| Security | Pod Security "restricted": non-root, read-only root filesystem with an emptyDir at `/tmp`, every capability dropped, `RuntimeDefault` seccomp |
| Service account | `spec.server.serviceAccountName`, with its token mounted; with none, the namespace's `default` with `automountServiceAccountToken: false`. Admission checks by SubjectAccessReview that whoever creates or changes the server spec may create Deployments in the namespace, so the Package grants no identity its author could not already run as |
| Secrets | `spec.server.secrets`, each read-only at `/var/run/cuex/secrets/<name>/` |
| Resources | `spec.server.resources`; by default requests of 50m CPU and 64Mi, a 256Mi memory limit |
| Probes | readiness and liveness on `/cuex/healthz` or `grpc.health.v1` |
| Spread | across nodes, when more than one replica |
| Termination | `FallbackToLogsOnError`, so the SDK's startup refusal reaches the Package |

Two rules keep it trustworthy:

- **`cuex-server` is vela-system's name.** Admission refuses a ComponentDefinition of that name in any other namespace, so a team's Application can't resolve the type to a definition of its own.
- **It imports no Packages**, only built-ins, so rendering a server never waits on a server. A definition that imports one leaves every server Package `ServerReady` `False`, reason `InvalidServerDefinition`.

The Application is applied with the controller's account, which is why the permission check above stays. Where KubeVela's user impersonation is on, the Application could carry the author's identity instead; that is a later option, not something this relies on.

**No quota of its own.** A server runs in the team's namespace, so its pods count against that namespace's ResourceQuota like any other workload. WebAssembly needs caps because its modules run inside vela-core; a server doesn't.

### Where an image may come from

The registry boundary covers the package (its `cuex.yaml` and CUE), but `server.image` names an image anywhere. A registry Config therefore says which image repositories its packages may run:

```yaml
spec:
  properties:
    type: helm
    url: https://charts.acme.com
    images: [ghcr.io/acme/]     # prefixes; an OCI registry defaults to its own repository
```

A server package whose image is outside them is refused with `ServerReady` `False`, reason `ImageNotAllowed`. Phase 7 verifies image signatures with the registry's keys, as it does packages.

## Ready

A new component becomes the one that serves after these steps:

| Step | Waits for | Condition while waiting | On failure |
| --- | --- | --- | --- |
| 1. Image allowed | | | `ServerReady=False`, reason `ImageNotAllowed` |
| 2. Healthy | the component's health in the Application's status, within 10 minutes | `ServerReady=False`, reason `Starting` | `ServerReady=False` with the pod's termination message or event |
| 3. `Describe` matches | one call through the Service | | `ServerReady=False`, reason `TemplateMismatch`, naming both digests and the first file and functions that differ |
| 4. Switch | | | |

One `Describe` call is enough: the image is pinned by digest and drift is reverted, so every pod of a component runs the same build. A Package's first version is Ready after step 4.

## Rolling out a new version

A new digest, from a range resolving forward or a changed pin, adds a second component to the Application beside the running one:

```
running  github-v1-4f2c9a1e  ─────────────────────────────────────────▶ deleted
                                                         ▲ drain (2 minutes)
new                          github-v1-9a1e0b2c  starting │ Describe │ switch ──▶ serving
status   Ready, Progressing: 1.2.0 sha256:9a1e…          │          │ Ready, 1.2.0
```

- **The switch is one status write**: `version`, `digest`, the CUE and `endpoint` together. Each replica switches when its informer sees it, so a render never pairs the new CUE with the old server.
- **The old one drains** for `--cuex-server-drain` (default 2 minutes), enough for calls in flight and for replicas still on the old status, then its component is removed and the Application deletes what it rendered.
- **A failed rollout leaves the old one serving**, with `Progressing=False`, reason `RolloutFailed`, and the step's message. The failed component stays for debugging until the Package changes again.
- **A change to `spec.server` alone** (replicas, resources, account) applies in place: the build is the same, so there's nothing to check.

## Calls from vela-core

| | |
| --- | --- |
| Pool | HTTP/1.1, 64 idle connections per server, 90-second idle timeout; one gRPC connection per server, `round_robin` over `dns:///` |
| Deadline | the render's own, capped by `spec.server.timeout` (default 30s, at most the `--cuex-server-max-timeout` flag, default 5 minutes) |
| Size | 4 MiB each way |
| Retries | none |
| Concurrency | calls run alongside each other as external calls do today, when a template asks with `@concurrency` |
| Errors | `<path>.<#do>: <message>`, with the code deciding retryability |

**Metrics** per Package and function: calls, errors by code, latency, bytes each way, and connections opened. **Events** on the Package for each rollout step, failed `Describe` and certificate renewal.

## Packages written the old way

A Package with `spec.provider` points at a server the platform runs itself, so it gets the client fixes and keeps its own authentication. TLS and the non-2xx error land in phase 1 ([design 07](07-phase-1.md#the-kubevelapkg-fixes)); the rest with this phase:

| | Before | After |
| --- | --- | --- |
| Result | a JSON object, decoded into Go maps first, so every number reaches CUE as a float (`int` refuses it, and `200` prints as `2E+2`) and `null` as nothing at all | any JSON value, built into CUE from the bytes: integers stay `int`, `null` stays `null` |
| A non-2xx status | ignored; the body is parsed as a result | an error, coded by status, with the body as the message |
| TLS | never verified | verified against the system roots; `spec.provider.insecureSkipVerify: true` opts out, with a warning |
| `header` | sent | still sent, the only authentication such a server has |
| Connections | 2 idle per host | the same pool as server Packages |
| Size | unlimited | 4 MiB each way |
| `grpc` | refused | still refused: a legacy server speaks only the old HTTP contract |

A server written with `externalserver` and its self-signed certificate needs `insecureSkipVerify` or a real certificate after the upgrade, as the KEP's table of breaking changes says.

## Offline tools

Servers never target `workload` or `default`, so `vela dry-run` of components and policies never reaches one. What remains is workflow steps and sources: `vela def render` of a step or source definition, VelaUX previews of them, and `vela def test`, where a `test.#PackageCall` mock is the usual answer anyway.

Offline, each server package's functions are registered as stubs. A stub checks `$params` against the function's CUE, leaves `$returns` as its declared type, and records the call. The render then reports each unresolved call and the output paths that are still not concrete because of one, instead of failing on the first:

```
unresolved: acme.com/github@v1 #PullRequest at steps.check.pr ($returns: {state: "open" | "closed" | "merged", mergeable: bool})
  not concrete because of it: steps.check.outputs.state
```

## Testing a server project

`vela cuex package test` builds the image, runs it in a container with `CUEX_INSECURE=true` on a local port, and points the `*_test.cue` cases at it, so `test.#PackageCall` exercises the real server. `go test` runs the functions as plain Go first. The e2e suite runs a server Package in k3d through a rollout from one version to the next, with calls running throughout, and checks none fails.

## A sample: the whole GitHub API

A server package wrapping GitHub's REST API was generated from GitHub's OpenAPI description and its published fine-grained permissions, and run on a cluster with today's controller, through an Application of the `cuex-server` definition written as the Package controller would write it.

| | |
| --- | --- |
| Operations | 1,281: 643 needing read access in `github`, 638 needing write or admin in `githubwrite`, split by GitHub's own access level (`POST /markdown` only reads); 277 without published permissions split by method |
| Typing | parameters and results fully typed from 1,016 schemas: 539 KB and 467 KB of CUE, each loading and building in 55 to 75 ms; a template calling one function compiles in 15 ms once the package is built |
| One image, two packages | `--package` picks which functions, CUE and `Describe` a replica serves |
| Deployed | the Application rendered the Deployment, Service and NetworkPolicy and reported healthy; a pod in another namespace could not reach the server, vela-core could |
| Called | a workflow step importing `example.com/github` read `kubevela/kubevela` and wrote its name, stars and licence into a ConfigMap |

What it found:

- **Today's client loses types**, as the table above says: `status: int` fails and `200` arrives as `2E+2`, so the sample types every result number as `number` and defaults nullable fields to `null`, which in turn breaks OpenAPI schema generation for a step reading them. Building `$returns` from the bytes removes both workarounds.
- **Strict enums in results break renders** when the API adds a value; GitHub's own examples already use values outside its enums. A generated package should leave result enums open and keep strictness for parameters.
- **GitHub's permissions are natural groups.** The sample emits each function's permissions as `$groups`, at their level (`issues:write`), and a Role per permission and level in `deploy/access-roles.yaml` (137), each granting `call` on its level and those below, in both packages. With `githubwrite` restricted to grants, binding `github-issues-write` gives a definition's writer what a fine-grained token with `issues: write` would: `issues/create`, but not `issues/add-assignees`, which also needs `pull_requests: write`. The level is in the name because seven write functions also need another permission at read. A function GitHub publishes no permission for (102 write) has no groups and needs a grant on the package or the function.
- **A rate limit is not a bad request**: a 429, or a 403 with no requests remaining, is `Unavailable`, so the step backs off.
- **The CUE outgrows a client-side apply**: a 600 KB legacy Package exceeds the last-applied annotation, so it needs `kubectl apply --server-side`. A Package from a registry carries no CUE in its spec, so this is a legacy-only limit.

## Measured in phase 5

- Call latency in a cluster, pod to pod, against WebAssembly and a built-in, at one replica and at three.
- Calls spread across replicas within a minute of a scale-up, on both transports.
- A rollout with renders running throughout: no failed call, and no render pairing new CUE with the old server.
- The cost of certificate renewal: handshakes per minute across every replica once the client certificate is renewed.
