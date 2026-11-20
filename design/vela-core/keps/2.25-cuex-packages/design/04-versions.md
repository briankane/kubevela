# KEP-2.25 design 04: versions

Phase 4 of the [KEP](../README.md): import paths, ranges, the compatibility check that keeps a range safe to follow, maturity, deprecation and retiring a major. Resolution itself is in [design 02](02-registries.md#resolving-a-version).

## Lines

A major version is a line, and a line is a `Package`:

| | Path | Package | Moves by |
| --- | --- | --- | --- |
| `naming` 1.x | `acme.com/naming@v1` | `naming-v1` | minor and patch releases, in place |
| `naming` 2.x | `acme.com/naming@v2` | `naming-v2` | the same, beside v1 |

The major is always written, v0 and v1 included, and only ever comes from the path: `cuex.yaml`'s `version` must agree with it, and `vet` refuses a 2.x release published as `@v1`. A definition imports a line, never a release:

```cue
import (
	"acme.com/naming@v2"        // imports as naming
	old "acme.com/naming@v1"    // two lines at once need an alias
)
```

Bare paths stay for built-ins and Packages written the old way.

## Ranges

A Package's `version` is a range in Helm's syntax (Masterminds semver), since packages are charts: `~2.1`, `^2.1`, `2.x`, `>=2.1.0 <2.5.0`, or an exact `2.1.3`. It must stay within the path's major; `>=2.0.0` on `@v2` is read as `>=2.0.0 <3.0.0`.

**v0 minors are breaking.** Within `@v0`, a range follows patches only unless it says more: `^0.3` and `~0.3` both mean `>=0.3.0 <0.4.0`. A wider range such as `>=0.3.0 <0.6.0` must be written out, and accepts the breakage it asks for. The compatibility check below applies to v0 patches.

Pre-releases match only a range that names one, as [design 02](02-registries.md#resolving-a-version) says.

## The compatibility check

A range is a promise that every release it can resolve to is compatible with the one before it. The controller checks that promise rather than trusting it.

**Rules**, applied to every definition the package exports, function or not:

| Change between releases | Compatible |
| --- | --- |
| a field added to `$params`, optional or with a default | yes |
| a required field added to `$params` | no |
| a `$params` field removed | no |
| an optional `$params` field becoming required without a default | no |
| a default added to a `$params` field | yes |
| a `$params` field widened (`<=63` to `<=253`, `string` to `string \| int`) | yes |
| a `$params` field narrowed | no |
| `$returns` narrowed (`string` to `"x" \| "y"`) | yes |
| `$returns` widened or changed in type | no |
| a definition or function removed | no |
| a definition added | yes |
| maturity lowered (stable to beta) | no |

A non-function definition (a library's `#Labels`) follows the `$params` rules, since templates unify their values with it: it must still accept everything it did.

**The comparison** is [KEP-2.27](../../2.27-cue-schema-compatibility/README.md)'s, with every definition compared as `Accepts`, each function's `$returns` as `Produces`, and `#do` and `#provider` ignored. The rules above are what its findings table gives for those directions.

An `Undecidable` finding, such as a changed `time.Format` layout with no example to show it breaks, counts as incompatible with the reason "cannot verify". The author can publish it with `--allow-breaking`, and operators can pin its digest.

**Where it runs:**

| Where | Against | On failure |
| --- | --- | --- |
| `vela cuex package vet --against <ref>` | any earlier release | an error naming the definition and field |
| `vela cuex package push` | the highest earlier release in the same line in the target registry | refused, unless `--allow-breaking`, which records `cue.oam.dev/breaking: "true"` in the chart |
| the controller, on resolution | the digest the Package serves now | the update is held: the Package keeps serving, with `Resolved` `False`, reason `Incompatible`, naming the definition, field and both versions |

- **A pinned digest skips the check.** Pinning is an explicit choice of exactly what runs.
- **A release marked breaking** is held by the controller like any other failure, so `--allow-breaking` stops nobody being upgraded by accident.
- **Moving past a held release** is a pin, or a range that excludes it. A release fixing the break resolves normally.

What the check cannot see is behaviour: a function returning a different value of the same type passes. That stays the author's promise, as in every semver ecosystem.

## Maturity

`cuex.yaml`'s `maturity: alpha | beta | stable`, carried per release and shown in status. Importing an alpha or beta line warns at admission, in `vet` and in `vela def test`, and never blocks. Within a line, maturity may only rise.

## Deprecation

A line is deprecated by a release's `cuex.yaml` (`deprecated: "use acme.com/naming@v3"`) or by the Package (`spec.deprecated`), which lets a platform deprecate a line it doesn't publish. Status carries `Deprecated` with the message.

| Who | Sees |
| --- | --- |
| a definition newly importing the line | an admission warning with the message |
| a definition already importing it | nothing at admission; `vet` and `vela def test` warn |
| `vela cuex package list` and `show` | the line marked deprecated, with its importers |
| Applications | nothing: deprecation is for authors, not for users of their definitions |

"Newly" compares the definition's imports before and after the update, so editing an old definition does not repeat the warning.

## Retiring a line

1. **Deprecate it**, naming the replacement.
2. **Find its importers**: `vela cuex package show acme.com/naming@v1` lists the definitions whose templates import it, across every namespace that can see it.
3. **Migrate them**: `vela cuex package diff acme.com/naming@v1 acme.com/naming@v2` prints the differences between the lines with the same rules, so each break is a line to fix.
4. **Delete the Package.** Admission warns with any importers still left, and their renders fail with "package not found".

## Multi-cluster

Spokes are out of scope, as the KEP says. Whatever renders on a spoke later must use the versions the hub resolved, so resolution stays on the hub.
