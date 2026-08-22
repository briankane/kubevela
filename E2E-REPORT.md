# KubeVela e2e suite: where the time goes, and what is likely to flake

Measured on `feature/source-definitions`, 2026-08-22, against a local k3d cluster
with the controller run from source.

## How this was measured, and what it cannot tell you

Ginkgo's own JSON report (`-ginkgo.json-report`), not scraped console output, so
the per-spec durations and states are the runner's own numbers.

Two full runs, each with `-ginkgo.flake-attempts=2`, which retries a failed spec
once. A spec that fails then passes is flaky by construction and is reported as
such.

**Two runs is thin evidence for flakiness.** It finds gross flakiness and
nothing subtle. A spec that fails one run in twenty will not appear here. Treat
the flakiness section as "what we caught", not "what exists", and treat the
static risk factors as the more useful half.

The runs did **not** set `KUBEVELA_E2E_AUTH=1`, so 17 `Helmchart Auth` specs
were skipped. CI (`makefiles/e2e.mk`) does set it, so CI runs more than this and
`helmchart_test.go`'s share of CI time is higher than reported below.

## Headline

| | |
| --- | --- |
| Specs measured | 197 (199 passed, 17 skipped) |
| Total | 2658s (44.3 min) |
| Mean / median | 13.5s / 6.0s |
| p90 / max | 41.6s / 185.4s |
| Specs under 1s | 20 (10%) |
| Specs over 30s | 26 (13%), and **59% of all runtime** |

The distribution is the story: half the specs finish in six seconds, and an
eighth of them account for three-fifths of the wall clock.

## Where the time goes

### By file

| Seconds | Share | Specs | File |
| ---: | ---: | ---: | --- |
| 1041.6 | 39.2% | 63 | `helmchart_test.go` |
| 526.0 | 19.8% | 11 | `app_autoupdate_test.go` |
| 318.2 | 12.0% | 9 | `postdispatch_trait_test.go` |
| 271.2 | 10.2% | 15 | `application_test.go` |
| 191.9 | 7.2% | 17 | `source_definition_test.go` |
| 80.1 | 3.0% | 4 | `resource_policy_test.go` |
| 74.4 | 2.8% | 9 | `definition_revision_test.go` |
| 45.4 | 1.7% | 23 | `source_expression_test.go` |
| 36.9 | 1.4% | 18 | `definition_output_validation_test.go` |
| 23.7 | 0.9% | 10 | `definition_test.go` |

`app_autoupdate_test.go` is the outlier: 11 specs costing a fifth of the suite,
48s each on average.

### The ten slowest specs

| Seconds | Spec |
| ---: | --- |
| 185.4 | PostDispatch Trait tests / Test PostDispatch status for trait, component and application |
| 86.3 | Application AutoUpdate / Enabled / specified exact trait version available |
| 85.7 | Application AutoUpdate / Enabled / new trait version created after app creation |
| 81.2 | Application AutoUpdate / Disabled / specified trait version is available |
| 78.7 | Application AutoUpdate / Enabled / specified exact component version available |
| 77.9 | Application AutoUpdate / Enabled / new component version after app creation |
| 77.0 | Application AutoUpdate / Disabled / specified component version unavailable |
| 63.0 | SourceDefinition e2e / keeps a component auto-updating when its trait reads a different source |
| 58.1 | Helmchart valuesFrom / Required missing valuesFrom source fails the workflow |
| 58.1 | Helmchart Edge Cases / Namespace does not exist and createNamespace=false |

## Three different causes, needing three different fixes

Slow specs are not all slow for the same reason, and the distinction decides who
can fix them.

### 1. Unconditional sleep — fixable in the test

`app_autoupdate_test.go` holds `reconcileSleepTime = 70 * time.Second` and
`sleepTime = 5 * time.Second`, called at 6 and 16 sites respectively:

```
reconcileSleepTime (70s) x6  = 420s
sleepTime          (5s) x16  =  80s
                               ----
                               500s  (8.3 min, ~19% of the suite)
```

Measured total for the file is 526s, so **roughly 95% of that file's runtime is
sleeping**, not asserting.

This is the worst kind of slow, because it is also flaky in both directions: it
always pays the full 70s even when the system converges in 5, and it fails
outright when the system needs 71. `Eventually` with the same timeout budget
would be faster in the common case and more reliable in the bad one.

**This is the single best speed fix available.** ~8 minutes, in one file, with no
product change.

### 2. Product convergence — not a test problem

The 185.4s PostDispatch spec has a 390s timeout budget across four `Eventually`
assertions and used 185s of it. It is not misconfigured; it is waiting for the
system.

More interesting, five `helmchart` specs took 57.8-58.1s each. That uniformity is
not noise. They all call `deployPodinfoExpectWorkflowFailure`, which polls up to
180s for `Workflow.Phase == "failed"` — and the workflow reliably takes ~58
seconds to declare itself failed.

**That is a product observation, not a test one: an operator who applies a broken
Application waits about a minute to be told it is broken.** Five specs paying it
costs the suite ~290s (11%), but the latency itself is worth a look on its own
terms.

### 3. Infrastructure

- **The suite is serial.** `makefiles/e2e.mk` runs `ginkgo -v ./test/e2e-test`
  with no `-p`. Given most of the runtime is waiting rather than computing,
  parallelism is the largest untested lever here. Untested deliberately — see
  "Not verified" below.
- **~12 distinct public images** are pulled (busybox, nginx at 7 tags,
  crccheck/hello-world, alpine:3.18). Warm locally, cold in CI.
- **Four of them are moving tags** — `busybox`, `nginx`, `nginx:latest`,
  `crccheck/hello-world` all resolve to `:latest`, so the suite's inputs can
  change without a commit.

## Flakiness

### What was actually observed

**Across both instrumented runs: zero failures, zero retries.** No spec failed
and then passed. On this machine, on this cluster, the suite is stable.

One flake was seen earlier in the same session, outside the instrumented runs:

> `Helmchart valuesFrom / Adoption of an existing vanilla Helm release`
> failed with `dial tcp: lookup stefanprodan.github.io: no such host`,
> and passed on its own 19 seconds later.

That is the shape to expect: not a race in the product, a dependency on the
public internet.

### Static risk factors, in rough order of concern

**1. Public internet in three files.** `helmchart_test.go` references
`stefanprodan.github.io` 8 times; `definition_test.go` and
`definition_revision_test.go` reach `github.com` / `kubevela.io`. This is the
only flake actually observed all session. A chart-repo mirror, or a
pre-pulled fixture, removes an entire class of failure.

**2. Moving image tags.** Four images have no pinned tag. A `nginx:latest` that
changes upstream changes what the suite tests, with no commit and no warning.

**3. Docker Hub rate limits.** ~12 distinct images on a cold cache. Never seen
locally because the cache is warm; a classic CI-only failure.

**4. `Ordered` containers mask causes.** Five in `helmchart_test.go`. A failure
early in an `Ordered` block skips the rest, so one flaky spec reports as several
failures and the real cause is whichever ran first. Worth knowing when reading a
CI failure, even though it is not itself a flake.

**5. Shared-namespace coupling.** Isolation is good overall — every file
randomises its namespace, and `postdispatch_trait_test.go` randomises the *names*
of the TraitDefinitions it puts in `vela-system`. But definitions do land in a
shared namespace, and Ginkgo randomises top-level containers, so a leak would
land on a different neighbour each run and present as an unrelated spec failing.

**6. Short timeouts under load.** The shortest budgets are in
`requiredparam_validation_test.go` (3s) and `application_test.go` /
`definition_test.go` (5s). Fine on an idle laptop; these go first on a loaded CI
runner.

## Recommendations, by value over effort

1. **Replace the sleeps in `app_autoupdate_test.go` with `Eventually`.** ~8
   minutes off the suite, one file, no product change, and it removes a
   both-directions flake.
2. **Stop depending on the public internet for charts.** The only flake actually
   observed. Mirror the podinfo chart or vendor a fixture.
3. **Pin the four moving image tags.** Cheap, and stops the suite's inputs
   drifting without a commit.
4. **Try `ginkgo -p`.** Most of the suite is waiting, so parallelism is the
   biggest lever. Needs verifying, not assuming — see below.
5. **Look at the ~58s workflow-failure latency** as a product question rather
   than a test one.

## Not verified

- **Parallel execution was not attempted.** The suite looks parallel-safe on
  inspection — namespaces and definition names are randomised, and Ginkgo keeps
  `Ordered` containers on one node — but "looks safe" is not "is safe", and
  claiming a speedup without running it would be a guess.
- **`KUBEVELA_E2E_AUTH=1` was not set**, so 17 auth specs are unmeasured.
- **Flakiness evidence is two runs.** Anything rarer than roughly one-in-two will
  not show up here.

## Reproducing

```bash
export KUBEBUILDER_ASSETS="$HOME/Library/Application Support/io.kubebuilder.envtest/k8s/1.31.0-darwin-arm64"

go test ./test/e2e-test/ -timeout 120m -count=1 \
  -ginkgo.json-report=/tmp/e2e.json \
  -ginkgo.flake-attempts=2
```

Then analyse with `hack/e2ereport.py` (in this branch), which takes one or more
JSON reports and prints the slowest specs, the per-file breakdown, retries, and
cross-run duration spread:

```bash
python3 hack/e2ereport.py /tmp/e2e-run1.json /tmp/e2e-run2.json
```
