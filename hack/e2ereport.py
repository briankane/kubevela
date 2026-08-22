import json, sys, collections
runs = sys.argv[1:]
# name -> list of (duration_seconds, state, attempts)
data = collections.defaultdict(list)
fileof = {}
for path in runs:
    report = json.load(open(path))
    for suite in report:
        for spec in suite.get("SpecReports", []):
            texts = spec.get("ContainerHierarchyTexts") or []
            leaf = spec.get("LeafNodeText") or ""
            name = " / ".join([t for t in texts if t] + ([leaf] if leaf else []))
            if not name:
                continue
            state = spec.get("State", "")
            if state == "skipped":
                continue
            dur = spec.get("RunTime", 0) / 1e9
            attempts = spec.get("NumAttempts", 1)
            data[name].append((dur, state, attempts))
            loc = (spec.get("LeafNodeLocation") or {}).get("FileName", "")
            if loc:
                fileof[name] = loc.split("/")[-1]

print("SPECS MEASURED: %d across %d run(s)\n" % (len(data), len(runs)))
tot = sum(d[0] for v in data.values() for d in v) / max(len(runs), 1)
print("Total runtime of measured specs: %.0fs (%.1f min)\n" % (tot, tot / 60))

print("=" * 78)
print("SLOWEST SPECS (mean over runs)")
print("=" * 78)
rows = sorted(((sum(d[0] for d in v) / len(v), name) for name, v in data.items()), reverse=True)
cum = 0
for i, (mean, name) in enumerate(rows[:20], 1):
    cum += mean
    print("%2d. %6.1fs  %5.1f%% cum   %s" % (i, mean, 100.0 * cum / tot, name[:88]))
    print("               %s" % fileof.get(name, "?"))

print()
print("=" * 78)
print("BY FILE")
print("=" * 78)
byfile = collections.defaultdict(lambda: [0.0, 0])
for name, v in data.items():
    f = fileof.get(name, "?")
    byfile[f][0] += sum(d[0] for d in v) / len(v)
    byfile[f][1] += 1
for f, (secs, n) in sorted(byfile.items(), key=lambda kv: -kv[1][0])[:14]:
    print("  %7.1fs  %5.1f%%  %3d specs  %s" % (secs, 100.0 * secs / tot, n, f))

print()
print("=" * 78)
print("FLAKINESS")
print("=" * 78)
retried = [(n, v) for n, v in data.items() if any(d[2] > 1 for d in v)]
if retried:
    for n, v in retried:
        print("  RETRIED: %s" % n[:80])
        for dur, state, att in v:
            print("           %d attempt(s), final %s, %.1fs" % (att, state, dur))
else:
    print("  No spec needed a retry: nothing failed-then-passed within a run.")
if len(runs) > 1:
    print()
    incons = [(n, v) for n, v in data.items() if len({d[1] for d in v}) > 1]
    if incons:
        for n, v in incons:
            print("  INCONSISTENT ACROSS RUNS: %s" % n[:78])
            print("           states: %s" % ", ".join(d[1] for d in v))
    else:
        print("  Every spec reached the same state in every run.")
    print()
    print("  Widest duration spread (max/min across runs, specs over 5s):")
    spread = []
    for n, v in data.items():
        ds = [d[0] for d in v]
        if len(ds) > 1 and min(ds) > 5:
            spread.append((max(ds) / min(ds), min(ds), max(ds), n))
    for ratio, lo, hi, n in sorted(spread, reverse=True)[:10]:
        print("    %4.2fx  %6.1fs -> %6.1fs  %s" % (ratio, lo, hi, n[:60]))
fails = [(n, v) for n, v in data.items() if any(d[1] not in ("passed",) for d in v)]
print()
print("  Specs not passing: %d" % len(fails))
for n, v in fails[:10]:
    print("    %s  [%s]" % (n[:70], ", ".join(d[1] for d in v)))
