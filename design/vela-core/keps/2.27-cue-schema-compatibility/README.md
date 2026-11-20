# KEP-2.27: CUE Schema Compatibility

**Status:** Proposed
**Parent:** [vNext Roadmap](../README.md)
**Used by:** [KEP-2.20](../2.20-module-versioning/README.md) (the API line parameter check), [KEP-2.25](../2.25-cuex-packages/README.md) (package ranges)

One API in `kubevela/pkg` that compares two CUE schemas and reports, field by field, whether the newer can replace the older. It decides nothing: callers choose which parts of a schema to compare, in which direction, how strict to be, and what to do with the report.

## Problem

Several things in KubeVela are versioned against a CUE schema and promise compatibility within a line:

| Where | Schema | Promise |
| --- | --- | --- |
| KEP-2.20 API lines | a definition's `parameter` | a line never breaks its users |
| KEP-2.25 packages | each function's `$params` and `$returns`, a library's definitions | a range only resolves compatible releases |
| Addon upgrades | `parameter.cue` | an upgrade keeps existing parameters valid |
| ConfigTemplates (KEP-2.18) | `parameter` | existing Configs stay valid |
| Workflow step definitions | `parameter` | existing workflows keep working |

Each would otherwise write its own check. KEP-2.20 already plans one, "a static AST-level comparison" of `parameter` blocks. CUE's own `Subsume` looks like the answer and is not: on its own it passes a removed optional field, rejects an added default, misses optional fields, and fails on bounds inside disjunctions (see [What CUE gives](#what-cue-gives)).

## Goals

- A single, tested answer to "can this schema replace that one", shared by every caller.
- Findings precise enough to act on: the path, what changed, both sides.
- Strictness and rules set by the caller, so one API serves a hard gate and a warning.
- An explicit `Undecidable` finding when the check cannot decide, never a silent pass.

## Non-goals

- **Behaviour.** A function returning a different value of the same type, or a template rendering differently, is out of reach of a schema comparison.
- **Policy.** Whether a finding blocks, warns or is ignored is the caller's (2.20's `disabled|warn|reject`, 2.25's held update).
- **Migration.** Rewriting values from one schema to another.
- **Real values.** The check compares schemas only; validating stored values against a new schema is the caller's.

## Directions

Compatibility depends on which way values flow, so every comparison has a direction:

| Direction | Means | For |
| --- | --- | --- |
| `Accepts` | the newer accepts every value the older accepted | inputs: `parameter`, `$params`, a library's types that templates unify with |
| `Produces` | the newer produces only values the older could | outputs: `$returns`, an exported schema others read |

The same change reads oppositely in each: widening a field is compatible for `Accepts` and breaking for `Produces`.

## Findings

The test for `Breaking`: some value valid against the older schema fails against the newer (`Accepts`), or a consumer of the newer's output can receive something the older never gave it (`Produces`). A change that passes that test but changes what renders is a `Warning`. One that changes neither is `Info`.

### Accepts

| Kind | Example | Severity |
| --- | --- | --- |
| `AddedRequired` | `prefix: string` added | Breaking |
| `NowRequired` | `maxLength?: int` to `maxLength: int` | Breaking |
| `Removed`, closed struct | `maxLength` gone from a definition | Breaking: values still setting it are rejected |
| `Removed`, open struct | `maxLength` gone from `{name: string, ...}` | Info: the struct accepts it anyway |
| `Narrowed` | `<=63` to `<=32`; an enum value removed; a list length capped | Breaking |
| `TypeChanged` | `string` to `int` | Breaking |
| `Closed` | `{...}` to closed | Breaking |
| `DefinitionRemoved` | `#Labels` gone | Breaking |
| `AddedOptional`, open struct | `prefix?: string` added to `{name: string, ...}` | Warning: a value already using the name may not fit |
| `DefaultChanged` | `*1` to `*3`; a default removed with the field still optional | Warning: values still validate, but everyone relying on the default renders differently |
| `AttributeChanged` | `+immutable` added | Warning: values stay valid, updates changing the field are refused |
| `AddedOptional`, closed struct | `prefix?: string` | Info |
| `AddedWithDefault` | `prefix: *"" \| string` | Info |
| `Widened` | `<=63` to `<=253`; an enum value added | Info |
| `Opened` | closed to `{...}` | Info |
| `NowOptional` | `name: string` to `name?: string` | Info |

### Produces

| Kind | Example | Severity |
| --- | --- | --- |
| `Widened` | `"x" \| "y"` to `string` | Breaking: consumers get values they never handled |
| `TypeChanged` | `string` to `int` | Breaking |
| `NowOptional`, `Removed` (was always present) | `id: string` to `id?: string`, or gone | Breaking: consumers reading it find nothing |
| `Removed` (was optional) | `region?: string` gone | Warning: consumers had to handle absence, but likely rely on it |
| `DefaultChanged` | | Warning |
| `Narrowed`, `Added`, `NowRequired` | | Info |

### Everywhere

| Kind | Severity |
| --- | --- |
| `Undecidable`: the check cannot compare the two (see [what stays undecidable](#what-stays-undecidable)) | Warning, with the reason; a gate that must fail closed sets `Severity(Undecidable, Breaking)` |
| a rename | reported as `Removed` plus an add; there is no rename detection |
| comments, `+usage` and other documentation | ignored |

`Strict` raises `DefaultChanged` and `AttributeChanged` to Breaking. The check is schema only: it never reads real values, so a narrowing no existing value happens to hit is still Breaking, and the answer is a new line or an override.

**Examples.** A `Narrowed` finding carries, where one is found, a value the older schema accepts and the newer rejects: `parameter.host narrowed, e.g. "svc-", when kind == "b"`. Every example is checked against both schemas with CUE itself before it is reported. A candidate the older schema rejects means a constraint the comparison does not model, and the finding becomes `Undecidable` rather than a break the example cannot show. Examples come from the regex search, from integer bounds, from rune and item counts, and from a pool of likely values of the field's kind (addresses, URLs, durations, times in the layouts involved, the validators' own arguments).

### Conditional fields

A schema with `if` guards is compared branch by branch, as a tree:

1. **One AST pass per side** splits each struct into a level: its unconditional fields (with every branch below them removed), its branches, each a level of its own, and the sub-structs that have branches inside.
2. **The unconditional schema is compared once.**
3. **Each level's branches are compared on their own.** For every combination of a group of related conditions, a small schema is built from the branch bodies and, at each enclosing level, only the fields they and their conditions touch, closed over what those refer to. It is compiled in a fresh context, so branches share nothing and run in parallel.
4. **It recurses** into each assumed body's own branches and into sub-structs, under the conditions so far. A finding carries the whole chain:

```cue
parameter: {
	scale: *false | bool
	if scale == true {
		auto: *false | bool
		if auto == true {
			max: int
			min: int               // added in the newer release
		}
	}
}
// parameter.min: AddedRequired, when scale == true && auto == true
```

- **Related conditions are compared together, others apart.** Two conditions at a level share a group only when one reads or declares a field the other declares, so feature flags guarding their own fields cost one comparison each.
- **Within a group, sets of conditions are compared smallest first.** A set is tried only when every set one smaller was reached, and a set whose older side conflicts is never extended, since every superset conflicts too. Twelve `kind == "..."` branches over a shared field cost 78 comparisons (twelve singles, then sixty-six pairs that all conflict), not 4,096. Related conditions that do not exclude each other still grow as 2ⁿ, so the walk stops at a budget (`MaxCombinations`, 256). Every single condition and pair is checked first, and the sets past the budget are one `Undecidable` finding naming the size reached. An existence guard that is unreachable only because no assumed branch declares its field yet is extended anyway, since a larger set may declare it.
- **Each finding is reported once**, under the fewest conditions it needs.

### Assertions

A branch assumed taken is constrained on both sides, so that a branch no value can reach on one side is skipped rather than compared:

| Condition | Asserted as | A side where it cannot hold |
| --- | --- | --- |
| `x == "lit"` | `x: "lit"` | its enum excludes the value: no value reaches the branch |
| `x != _\|_` | `x: _`, which makes an optional `x` required and keeps its type | `x` is declared neither at the level nor in an assumed branch: no value sets it |

Without the second, a release adding `foo?: string` and `if foo != _|_ { bar: string }` reports `bar` as a new required field, though no older value could set `foo`.

### Loops

| Loop | Compared as |
| --- | --- |
| over a concrete value (`for k, v in {web: 80, api: 8080}`) | the fields it expands to, like any other |
| over a field (`for p in ports`) | a branch keyed by its clause, with its variables bound to the element's schema as each side declares it, and computed labels (`"\(p.name)Probe"`) named from their source, so the generated fields line up |

```cue
parameter: {
	ports: [...{name: string, port: int}]
	for p in ports {
		"\(p.name)Probe"?: {path: string, scheme: string}   // scheme added
	}
}
// parameter.<"\(p.name)Probe">.scheme: AddedRequired, when for p in ports
```

### Branching options

```go
type Branching struct {
	Enabled         bool // compare branch by branch; off, any conditional field is Undecidable
	MaxDepth        int  // nested levels followed; deeper is Undecidable; 0 follows all
	MaxGroup        int  // related conditions compared together; a larger group is Undecidable; 0 for no limit
	MaxCombinations int  // sets of related conditions compared, smallest first; the rest are Undecidable; 0 for no limit
	AssertEquality  bool // assert x == lit on both sides
	AssertExistence bool // assert x != _|_ as x being set
	Loops           bool // compare loops over fields as branches; off, they are Undecidable
}

var DefaultBranching = Branching{Enabled: true, MaxCombinations: 256, AssertEquality: true, AssertExistence: true, Loops: true}
```

A gate that must stay cheap turns branching off and treats the `Undecidable` finding as it chooses; a publish-time check follows everything.

Measured on the prototype (Apple M-series, one CPU), a parameter block of nine plain fields and n flags each guarding three fields:

| Conditions | Whole schema per branch | Tree, isolated |
| --- | --- | --- |
| 0 | about 1 ms | about 1 ms |
| 4 | 20 to 29 ms, 8.8 MB | 4 to 6 ms, 2.0 MB |
| 8 | 68 to 113 ms, 22.5 MB; every combination rather than groups: seconds and 1.5 GB | 7 to 17 ms, 3.5 MB |
| 16 | | 22 to 23 ms, 6.4 MB |

About a millisecond for the baseline and a millisecond per branch. Prototyped with about 290 cases across the plain comparison, branches, nested branches, sub-structs, loops over fields and over concrete values, existence guards with and without assertion, each branching option, every builtin validator, string constraints, time layouts, schema validators, lists, pattern constraints, integer bounds, top-level definitions, imports and aliases, comprehensions of several clauses and computed labels. Comparing lists, pattern constraints and validators allocates about half as much again as a check that skipped them (1.3 MB with no conditions, 6.7 MB with eight), most of it the pattern constraints and list elements a component's parameters already have. One CUE pitfall surfaced on the way: rebuilding a value from its raw syntax loses references such as a loop variable, so values are rebuilt from their evaluated syntax, and a rebuild that fails is never a pass.

### Validators

CUE subsumes no validator, not even one with itself: an unchanged `strings.MinRunes(3)` is not subsumed by `strings.MinRunes(3)`. A value is therefore split into its conjuncts by operator (`Expr`), and each is compared for what it means. CUE has no user-defined validators; every one is a builtin returning a bool, so the set is closed for each CUE version and each is modelled:

| Validators | Compared |
| --- | --- |
| `strings.MinRunes`, `strings.MaxRunes` | as patterns (`^(?s:.){n,}$`) with the field's other string constraints, so `=~"^[a-z]{5,}$"` implies `MinRunes(5)`; past Go's repetition limit (1000), by count alone |
| `list.MinItems`, `list.MaxItems`, `struct.MinFields`, `struct.MaxFields` | by count, against the older value's own shape too: `[string, string]` is within `MaxItems(3)`, required fields count towards `MinFields` |
| `math.MultipleOf` | by division, as rationals: `MultipleOf(4)` implies `MultipleOf(2)`, and every integer is a multiple of `0.5` |
| `strings.HasPrefix`, `HasSuffix`, `Contains`, `ContainsAny` | as the patterns they are (`HasPrefix("v")` is `=~"^v"`, its argument quoted), so they compare with `=~` and `!~` |
| `net.IP` and the address kinds, `net.URL`, `net.AbsURL`, `time.Time`, `time.Duration`, `list.UniqueItems`, `json.Valid` and the other predicates | through an implication table (`IPv4` implies `IP`, `LinkLocalMulticastIP` implies `MulticastIP`, `AbsURL` implies `URL`), itself tested against CUE on every candidate value; otherwise by example |
| `time.Format` | equal when the layouts are, `time.Time` being `RFC3339Nano`; otherwise by example, formatting reference times in the older layout |
| `matchN` | by what its count makes of its schemas: any (`>=1`), all (the count of schemas), none (`0`) or one (`1`), one being any when no two schemas overlap; schemas compared with the check itself |
| `matchIf` | with the same condition, each branch must cover; otherwise by example |
| `json.Validate`, `yaml.Validate`, `yaml.ValidatePartial` | by their schemas, with the check itself |
| `list.Contains`, `list.IsSorted`, `list.MatchN` and the rest | equal when their arguments are, compared as syntax with optional fields included; otherwise by example |

An added validator narrows unless the older value already implies it (an `int` meets `matchN(>=1, [int, string])`); a removed one widens.

### String constraints

Every string constraint on a field, `=~`, `!~`, the string validators and rune counts, is compiled into Go's own NFA (`regexp/syntax`), wrapped as `=~` applies it (unanchored). For each newer constraint, a breadth-first search runs the older constraints and that one together, looking for a string every older constraint allows (an `=~` matching, a `!~` not) and the newer one refuses. Go's regex syntax is regular, so the answer is exact, and the shortest such string is the finding's example.

- **The alphabet** is split at every rune where an instruction's ranges start or end, at the word characters and at newline, and at every case of a folded rune (`(?i)k` takes the Kelvin sign), so one rune per interval stands for all of it.
- **Line and word assertions** (`\b`, `\B`, `(?m)^`, `(?m)$`) hold or not by the runes either side, so the search carries the previous rune's class and evaluates each assertion when it steps.
- **The search is bounded** at 20,000 product states; past it the change is `Undecidable`.

| Older | Newer | Result |
| --- | --- | --- |
| `=~"^[a-c]+$"` | `=~"^[a-z]+$"` | compatible |
| `=~"^[a-c]+$"` | `=~"^(a\|b\|c)+$"` | compatible: the same language |
| `=~"^[a-z]+$" & !~"^x"` | `=~"^[a-wyz]"` | compatible: only both older constraints together cover it |
| `!~"^ab"` | `!~"^a"` | `Narrowed`, e.g. `"a"` |
| `=~"\\bab"` | `=~"\\ba"` | compatible |
| `=~"^[kK]$"` | `=~"(?i)^k$"` | compatible; the reverse is `Narrowed`, e.g. `"K"` |
| `=~"^\\p{Greek}+$"` | `=~"^\\pL+$"` | `Narrowed`, e.g. `"͵"`: the Greek script has symbols too |
| `=~"^a.b"` | `strings.HasPrefix("a.b")` | `Narrowed`, e.g. `"aab"` |
| `string` | `=~"^[a-c]+$"` | `Narrowed`, e.g. `""` |

### Lists, patterns and integers

- **Lists** compare position by position: each older element against the newer one at that position or its rest, the newer needing no more items than the older took, and the older's rest against the newer's. Validators on the list itself compare as above.
- **Pattern constraints** compare through a field they would take. For each older pattern, a sample label is found by the string search (the label's language, minus every declared name), filled in on both sides and compared as a field. A newer schema with no pattern taking it has `Removed` the pattern. A field removed from the newer schema but taken by one of its patterns compares against that pattern.
- **Integers** bounded only by `<`, `<=`, `>` and `>=` compare as intervals, each bound rounded to the integers it allows, since CUE subsumes neither `int & >0` by `int & >=1` nor the reverse.

### What stays undecidable

| Case | Why |
| --- | --- |
| a changed `time.Format` layout, a `matchIf` with a changed condition, or two predicates the table does not relate, when no example is found | the languages are not compared exactly; RFC 3339 and RFC 3339 with nanoseconds parse alike, so no example exists |
| `matchN` exactly one of overlapping schemas, changed, when no example is found | whether a value matches two schemas at once depends on how they overlap |
| `matchN` with any other count, changed | |
| a pattern past 20,000 product states, or a rune count past 1000 beside a pattern | the bound |
| a pattern constraint whose label is not a string pattern (`[#Name]: T`) | no sample label |
| an example the older schema rejects | a constraint the comparison does not model |
| a branch that cannot be built | never a pass |
| branches when branching is off, deeper than `MaxDepth`, or past `MaxCombinations` | the options |

### Comprehensions and computed labels

- **Several clauses** (`for p in ports if p.expose { ... }`, `if a if b { ... }`) are rewritten as nested comprehensions of one clause each, which is how CUE defines them, and compared by the recursion above. A `let` clause becomes a `let` in the struct it scopes over.
- **Computed labels** (`(key)?: int`, `"\(prefix)Name": string`) are evaluated by CUE when what they read is concrete, and compared as the fields they produce. When they read a field, a loop variable or a `let` computed from either, they are named from their source, as in loops, so both sides line up.
- **Each side keeps its own imports and top-level declarations.** A branch's schema takes the definitions and `let`s it refers to, transitively, and the imports they use, only those, since CUE refuses an unused import or `let`. So a renamed alias (`s.MinRunes` to `strings.MinRunes`) compares equal, and `#Port: int & <=65535` narrowed is a finding on every field using it.
- **A branch that cannot be built** is `Undecidable`, never skipped. Only a conflict (no value reaches the branch) skips it.

## What each consumer blocks on

| Consumer | Blocks on | Surfaces |
| --- | --- | --- |
| KEP-2.25, a range resolving a release | Breaking, and Undecidable (it sets `Severity(Undecidable, Breaking)`) | Warning as an event |
| KEP-2.20 `reject` | Breaking | Warning as a condition |
| KEP-2.20 `warn` | nothing | Warning and above as `BreakingChangeDetected` |

## API

```go
// package github.com/kubevela/pkg/cue/compat

// Check compares older and newer, which must be built in the same
// cue.Context.
func Check(older, newer cue.Value, opts ...Option) Report

// CheckSource compiles both in one context and compares the value at each
// selected path. Defaults are read from the source as written.
func CheckSource(older, newer []byte, opts ...Option) (Report, error)

type Report struct{ Findings []Finding }

func (r Report) Compatible() bool         // no finding at Breaking
func (r Report) At(s Severity) []Finding  // findings at s or above
func (r Report) Err() error               // nil, or every breaking finding

type Finding struct {
	Path     string   // "$params.maxLength", "#DNSLabel.$returns"
	Kind     Kind
	Severity Severity // Info, Warning, Breaking
	Older    string   // the older value, formatted
	Newer    string
	When     []string // the conditions the finding holds under, empty if always
	Example  string   // for Narrowed: a value the older takes and the newer refuses, checked with CUE
	Reason   string   // for Undecidable: what could not be compared
}
```

| Option | Default | Does |
| --- | --- | --- |
| `Direction(d)` | `Accepts` | the direction for the whole schema |
| `Path(glob, d)` | | a direction for paths matching `glob`, e.g. `Path("#*.$returns", Produces)`; the most specific glob wins |
| `Ignore(globs...)` | none | paths left out of the comparison (`#*.#do`, comments, attributes) |
| `Only(globs...)` | everything | compare only these paths |
| `Strictness(s)` | `Standard` | `Lenient` reports only `Breaking`; `Standard` as the tables; `Strict` raises `DefaultChanged` and `AttributeChanged` to Breaking |
| `Severity(kind, s)` | the table | overrides one kind, e.g. `Severity(Undecidable, Warning)` |
| `Definitions(bool)` | true | whether `#` definitions are compared, or only regular fields |
| `Branching(b)` | `DefaultBranching` | how conditional fields and loops are compared; see below |

**What KEP-2.25 passes:**

```go
compat.CheckSource(old, new,
	compat.Path("#*", compat.Accepts),
	compat.Path("#*.$returns", compat.Produces),
	compat.Ignore("#*.#do", "#*.#provider"),
	compat.Severity(compat.Undecidable, compat.Breaking),
)
```

**What KEP-2.20 passes:**

```go
compat.CheckSource(old, new, compat.Only("parameter"), compat.Strictness(compat.Lenient))
```

## What CUE gives

Prototyped against CUE v0.14.1 (kubevela/pkg, `cue/cuex/compatspike`). The comparison walks both schemas field by field and uses `Subsume(..., cue.Schema())` only for leaf values, because:

| CUE behaviour | Consequence for a plain `Subsume` | What the walk does |
| --- | --- | --- |
| lookups skip optional fields | `$returns?` and every optional parameter go unchecked | iterates with `cue.Optional(true)` |
| a closed struct subsumes one with an extra optional field | a removed optional parameter passes | compares fields by name, both ways |
| a default makes a field regular | an added default reads as narrowing | strips default marks from the source before comparing types, reads requiredness from the original |
| bounds inside a disjunction are not subsumed (`int & <=63` by `63 \| int & <=63`) | compatible changes reported as breaking | compares disjunct by disjunct |
| a value read out of a definition and one built alone do not subsume even when equal | false breaks | builds both sides the same way |
| values from two contexts never subsume | everything breaks | `CheckSource` compiles both in one; `Check` requires it |
| no validator subsumes another, even an identical one | every unchanged validator breaks | compares validators by what they mean |
| `int & >0` and `int & >=1` do not subsume each other | false breaks | compares integer intervals |
| printing a value leaves out optional fields (`{a?: int}` prints as `{}`) | changed validator arguments look equal | compares arguments as syntax with optional fields |
| the syntax of a value using an import is a whole file, imports and all | disjunctions with a validator in them are not split | unwraps the file and rebuilds each part with its imports |
| only a `[string]` pattern can be looked up; `[=~"^x"]` cannot | regex-label patterns go unchecked | reads patterns from the syntax and compares a sample label |
| the formatter writes `< -1` as `<-1`, the arrow token, so its own output no longer parses (`cue fmt` included) | a schema with a negative upper bound cannot be rebuilt | brackets the operand when parsing: `<(-1)` |
| a conflict between integer bounds reads "incompatible integer bounds", not "conflicting values" | an unreachable branch is reported as one that cannot be built | counts it as a conflict |

Each row is a test, so a CUE upgrade that changes one shows up as a failing case rather than a changed verdict.

## Test plan

- A corpus of change pairs per kind and direction, with the expected findings, as the acceptance test; each consumer adds its real cases (the shipped definitions' history for 2.20, packages for 2.25).
- A differential test: concrete values the older schema accepts (from defaults, examples and generated samples) must be accepted by a newer one reported compatible under `Accepts`.
- The CUE-behaviour rows above as their own tests, run on every CUE bump.
- The predicate implication table checked against CUE: no candidate value the stronger predicate takes is refused by the weaker.
- Every example a finding carries is re-checked against both schemas; one the newer accepts fails the test, as the comparison disagreeing with CUE.
- Branches compared in parallel, run under the race detector.

## Phases

| # | What |
| --- | --- |
| 1 | `Check`, `CheckSource`, directions, the findings table, `Path`, `Ignore`, `Only`; structs, scalars, disjunctions, defaults |
| 2 | Lists, pattern constraints, references to other definitions, `Strictness` and attributes, conditional fields and loops branch by branch with `Branching` |
| 3 | Adoption: KEP-2.20's parameter check and KEP-2.25's range check move onto it |

## Open questions

1. **Imports**: a schema using a type from an imported package compares that type as resolved; should a change in the import itself be reported against the importer?
2. **More assertions**: `x != lit`, `x > n` and conjunctions would prune more unreachable branches; worth it, or rare in parameter blocks?
3. **Open structs**: a definition's `parameter` is a regular field, so whether removing a field from it is `Breaking` (closed) or `Info` (open) depends on how KubeVela validates parameters; the prototype treats every struct as closed.
