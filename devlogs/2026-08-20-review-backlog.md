# Review backlog: KEP-2.16 source definitions

Open items from the `/code-review` pass on 2026-08-20, over the 13 commits since
the last upstream merge (`1525c32cc..68bbaa021`, 83 files, +4284/-1929). Nothing
here is fixed yet. The two mediums were each traced end to end and confirmed
reachable; the lows are read-and-believed unless noted.

Ordered by what would hurt most if shipped.

## 1. A whole-source read never triggers auto-update (medium, CONFIRMED)

`pkg/sources/source_resolver.go:204`

For `$(source.foo)` the reference is `Path=["foo"]`, so
`strings.Join(ref.Path[1:], ".")` is `""`. `lookupMapPath(values, "")` splits
`""` on `.` and gets `[""]` - one empty segment, not zero - then looks up the
key `""`, which is absent, and returns false. `recordConsumedValue` never runs.

The value still substitutes, so it looks like it works. But `ConsumedFields`
stays empty, and `resolvedSourceHashes` (`dispatcher.go:362`) skips any binding
with no consumed fields, so no `source.oam.dev/resolved-hash` is stamped. The
source can change forever and the workload is never re-dispatched: `autoUpdate`
is silently a no-op for that binding. Nothing appears under
`status.sources[].consumedValues` either.

Verified that `$(source.cfg)` is a supported form, not a hypothetical: it parses
to `Path=["cfg"]` and evaluates to the whole object. No example in the repo uses
the bare form, so this is latent rather than something the demo would have hit.

Fix sketch: treat the empty path as "the whole source" rather than as a lookup.
`lookupMapPath` returning `(data, true)` for `""` is the smallest change, but
check what a `""` property path then does to `+sensitive` redaction, which
matches on the recorded path, and to the `Reads` attribution.

Test first: a component reading `$(source.x)` whole, assert a resolved-hash is
stamped and that changing the source re-dispatches.

## 2. Chained source properties are mutated in place on a caller-owned map (medium, CONFIRMED)

`pkg/sources/source_resolver.go:598`, `resolveSourceNode` at `:115`

`resolveSourceNode` assigns into the map it walks (`val[k] = resolved`), and
`sourceInputsFromContext` (`:496`) does `in.Bindings = v` straight off the
process context with no copy. So resolving a chained source rewrites the shared
binding map in place.

The asymmetry is the tell: `ResolveSourceExpressions` deliberately JSON
round-trips the top-level params to get a private copy, and the bindings get no
such protection.

Reachability, each link checked:

- `baseGenerateComponent` (`pkg/appfile/appfile.go:665-678`) renders the
  component and then every trait against one `pCtx`, so they share one
  `Bindings` map.
- The component's render rewrites `Bindings["A"]`, replacing `$(source.B.x)`
  with the literal it resolved to.
- The trait's pass then resolves A from properties that no longer contain an
  expression, so B is never resolved during that pass and is absent from its
  statuses.
- `PushData` replaces rather than merges (confirmed in workflow
  `v0.7.3-0.20260724135823`), so the component's record of B is overwritten.
- `resolvedSourceHashes` and `recordComponentSourceReads` both read that final
  map, so B loses its hash and its consumer attribution.

Fix sketch: deep-copy the binding properties before resolving them, the same way
the top-level params already are. Consider whether the statuses push should
merge rather than replace, which would make the whole class of ordering bug
harmless rather than just this instance.

Test first: a component with a trait, both reading a chained source, asserting
the chained source survives in the final statuses.

## 3. `asNotFound` discards the underlying error (low)

`pkg/addon/notfound.go:45`

`fmt.Errorf("reading %q: %w", path, ErrFileNotFound)` wraps only the sentinel,
so the backend error is dropped. A GitHub 404 meaning "repository not found"
becomes indistinguishable from "file not found" in logs, and
`errors.As(err, &github.ErrorResponse{})` stops matching. No caller in
`pkg/addon` does that today, so this is a diagnostics loss rather than a break.
Wrapping both costs nothing. Mine, from the registry soft-fail commit.

## 4. Doc comments orphaned by the package moves (low)

The `sourceexpr` -> `propexpr` and `pkg/cue/definition` -> `pkg/sources` renames
separated several comments from the functions they document. `pkg/cue/render` is
worst affected because it is a brand-new exported package, so these are its
public docs:

- `render.go:50` `Template` has no doc at all.
- `render.go:65` `UserErrors` carries a block naming two other functions
  (`resolveSourceExpressions`, then `extractUserErrors`).
- `render.go:104` `Properties` has its doc duplicated verbatim.
- `render.go:42` vs `:82` document `MaxAnnotationValueLen` contradictorily: "the
  budget for everything recorded on one object" against "caps a single recorded
  value". The code matches the first, so the second is stale, and it sits next
  to `MaxPropertyValueLen`, which really is the per-value one.
- `source_sensitive.go:106` has an orphaned `WorkflowClient` comment that
  migrated in from `pkg/appfile`, which left `Appfile.WorkflowClient`
  undocumented on the way out. Same file: `RedactValue` (`:174`) is documented
  as `maskedPath`, exported `MaskedPath` (`:206`) has no doc, and `:62`'s
  `findTopLevelStruct` comment is orphaned above `collectSensitivePaths`.
- `validation_sources.go:652` - inserting `listElementAt` split `kindAt` from
  its doc, so `listElementAt` opens with a sentence about `kindAt`'s return
  values and `kindAt` is undocumented. Mine, from the array-parameter fix.
- `source_resolver.go:212` and `:496` each have their doc comment duplicated
  verbatim immediately above themselves.

## 5. Import grouping (low)

`github.com/oam-dev/kubevela/pkg/sources` was added to the first (stdlib) import
block in eight files: `application/apply.go`, `application/generator.go`,
`appfile/appfile.go`, `appfile/parser.go`, `appfile/validate.go`,
`validation_expressions.go`, `sourcedefinition/validator.go`,
`docgen/cluster.go`. It sorts alphabetically so `goimports` accepts it, but
every other file in the repo separates the groups.

## Checked and fine

Recorded so a later pass does not re-derive them:

- `listElementAt`'s disjunction decomposition is correct and keeps a closed list
  closed. All three callers of `lookup` guard on `ok`, so returning a zero
  `cue.Value` rather than the old non-existent-but-valid one is safe.
- `SourceEngine.Resolve` returns `SourceResult` by value, so reading
  `res.Statuses` before the error check is deliberate, not a nil deref.
- `collectSensitivePaths`'s `append(prefix, name)` looks like the classic
  slice-aliasing bug but is safe: every path is `strings.Join`ed eagerly and no
  slice is retained.
- `identityContext` cannot hit its bare-vs-indexed clobber, because
  `cachekey/infer.go` skips the plain-selector pass for `Indexed` fields.
- The OSS/Gitee/GitHub/GitLab status checks are correct and their imports are
  present.

---

# Feature review: smells, APIs, unoptimised code

A second pass over the whole feature (~7,500 non-test LOC across `pkg/sources`,
`pkg/definition/{propexpr,celexpr,cachekey}`, `pkg/cue/render`, the registry
provider, the SourceDefinition controller and the two webhook validators),
looking for what the correctness review was not looking for. Measurements are
from benchmarks run on this machine, quoted with the benchmark that produced
them so they can be re-run.

## P1. Every expression rebuilds the CEL environment and recompiles itself

`pkg/definition/celexpr/celexpr.go:251` (`Eval`), `:419` (`DynEnv`)

`Eval` calls `env.Compile(expr)` and `env.Program(ast)` on every evaluation, and
every entry point calls `DynEnv()` fresh: `expressionReferences`
(`source_resolver.go:222`), `celEvalProperty` (`:246`), `ValidateTree`
(`tree.go:44`), `EvalTree` (`tree.go:96`).

Measured, one expression, `$(source.cfg.host)`:

| Step | ns/op |
| --- | ---: |
| `DynEnv()` | 19,819 |
| `env.Compile` + `env.Program` | 38,347 |
| `prg.Eval` (the actual work) | **644** |
| End to end, as written today | 83,253 |

So **99.2% of the cost is rebuilding two things that never vary**. The
environment is a constant, and the compiled program is a pure function of the
expression text. This runs per expression, per property, per component, per
trait, per reconcile.

Fix: build the env once (`sync.Once`, `cel.Env` is safe for concurrent use), and
put a bounded, keyed cache in front of Compile+Program. The expression text is
the whole key for `DynEnv`-based evaluation because the env is fixed. The typed
admission path (`EnvForContext`) varies with the schemas, so key that on the
schema set or leave it alone - admission is not the hot path.

Worth doing before any of the correctness fixes, because it is the change most
likely to be blocked by them later.

## P2. Sources resolve strictly sequentially

`pkg/sources/source_resolver.go:565` (`resolve`)

There is no concurrency anywhere in the resolver - the only `sync` reference in
the whole package is the LRU's mutex. Bindings are resolved one at a time in
dependency order.

For an Application whose sources are ConfigMap reads this is irrelevant. For one
reading three registries or three HTTP endpoints, the latencies add up rather
than overlapping, and this happens twice: once at admission (`ValidateComponents`
resolves for real) and once per render.

The dependency graph is already computed for chaining, so independent bindings
are identifiable. Fix would be to resolve each dependency level concurrently.
Note this interacts with P1 of the correctness list: sharing a mutable bindings
map across goroutines would turn a latent ordering bug into a data race, so the
copy fix has to land first.

## P3. The public API has no consumer outside its own package

`pkg/sources/source_engine.go`

`NewSourceEngine` has exactly one non-test caller: `ResolveSourceExpressions`,
at `source_resolver.go:84`, in the same package. Nothing outside `pkg/sources`
can hold a `SourceEngine`, so by construction every method on it -
`Resolve`, `Check`, `TypeOf`, `Reads` - has zero external consumers. `Reads` has
no non-test caller at all.

This was built deliberately, to be introduced to other controllers, so it is not
dead code by accident. But it means the API's ergonomics have never been tested
by a real caller, which is exactly the thing an API design is supposed to be
validated by. Either wire one real consumer through it (the admission validator
is the obvious candidate - it does the same work by hand), or mark it explicitly
as provisional so nobody treats it as settled.

Related: there are now two entry points for the same job - the
`ResolveSourceExpressions` convenience function and the engine. Four call sites
use the former, none use the latter. Two doors onto one room tend to drift.

## P4. In-place mutation of caller-owned trees, twice

`pkg/definition/celexpr/tree.go:107` (`evalNode`),
`pkg/sources/source_resolver.go:115` (`resolveSourceNode`)

Both walk a tree and assign back into it (`t[k] = out`). Neither documents that
it mutates, and both are reachable from exported functions.

This is not theoretical: it silently invalidated a benchmark written during this
review. `EvalTree` over `{"v": "$(source.cfg.host)"}` rewrote the input to
`{"v": "a"}` on the first iteration, so every subsequent iteration measured a
no-op and the numbers looked 4x better than they were. If it can do that to a
benchmark it can do it to a caller.

It is also the root of correctness item 2 above. Worth fixing as one change:
copy on entry, or document loudly and rename to something that says so.

## P5. `source_resolver.go` is doing too many jobs

1,105 lines covering: expression walking, reference extraction, dependency
ordering and cycle detection, cache policy resolution, cache key assembly,
template compilation, status recording, sensitive-path redaction, consumed-value
hashing and annotation metadata. `ApplySourceCacheMetadata` (67 lines) and
`resolveCachePolicy` (82 lines) have nothing to do with expression resolution.

Longest functions across the feature:

| Lines | Function |
| ---: | --- |
| 185 | `ValidateSources` (`validation_sources.go:68`) |
| 146 | `validateExpressionTargetTypes` (`validation_expressions.go:122`) |
| 143 | `sourceResolver.resolve` (`source_resolver.go:565`) |
| 110 | `cachekey.Infer` (`infer.go:75`) |
| 82 | `resolveCachePolicy` (`source_resolver.go:708`) |

`ValidateSources` in particular is a single 185-line orchestrator that collects
references from five surfaces, dedupes, loads definitions, and runs four
different checks. It reads as a pipeline written inline.

## P6. A mutex that implies a guarantee it does not give

`pkg/sources/source_cache_lru.go:65`

`lruSourceCacheStore` carries its own `sync.Mutex`, but `NewLRUSourceCacheStore`
returns a **new** store per call (once per appfile, `appfile.go:911`) while every
instance points at the same package-level `sharedSourceLRU`. So N stores hold N
different mutexes over one shared cache.

Checked before reporting: `k8s.io/utils/lru.Cache` has its own `sync.RWMutex` and
locks internally, so **there is no data race** - the per-instance mutex is simply
redundant. The problem is that it reads as though it is what makes the shared
cache safe, and it is not. Anyone swapping the LRU implementation for a
non-locking one would find that out the hard way.

Worth noting the compound operations are not atomic regardless: `Read` does
load, then delegate, then store, so two concurrent reconciles can both miss and
both fetch. That is a benign thundering herd, not a correctness problem, but it
is the kind of thing a reader assumes the mutex prevents.

## P7. Admission recompiles definition parameter blocks per request

`validation_sources.go:777` (`loadTargetParameter`)

Memoised within a single validation call (`targetParams` map in
`validateExpressionTargetTypes`), but nothing survives the request. Every
admission of every Application re-fetches and re-CUE-compiles the parameter block
of every definition it references.

The client get is cheap (controller-runtime cache), the CUE compile is not.
Bounded by distinct definitions per Application, so this is a real but modest
cost - listed last because it is the least of these and the fix (a process-level
cache keyed on definition name plus resourceVersion) carries invalidation risk
that the others do not.
