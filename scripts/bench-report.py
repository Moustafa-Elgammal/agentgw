#!/usr/bin/env python3
"""Turn one soak run (scripts/soak.sh) into site/src/data/soak.json for the landing page.

    python3 scripts/bench-report.py soak-results/<run> [--out site/src/data/soak.json]

Reads load.txt (the load tool's report) and samples.csv (resource samples). Stdlib only. The
numbers are copied, never rounded up: the page shows exactly what the run measured.

    python3 scripts/bench-report.py overhead bench-results/<run> [--out site/src/data/overhead.json]

The overhead form reads runs.jsonl and meta.json from scripts/bench-overhead.sh, takes the median
over the repetitions of each measure on its own, and writes what the gateway adds (through the
gateway minus direct) at p50, p95 and p99.
"""
import csv, json, platform, re, sys
from datetime import datetime, timezone
from pathlib import Path

def dur_ms(s):
    m = re.fullmatch(r"([\d.]+)(µs|us|ms|s)", s)
    v, u = float(m.group(1)), m.group(2)
    return v * {"µs": 0.001, "us": 0.001, "ms": 1.0, "s": 1000.0}[u]

def main():
    if sys.argv[1:2] == ["overhead"]:
        return overhead()
    args = [a for a in sys.argv[1:] if not a.startswith("--")]
    out = Path("site/src/data/soak.json")
    if "--out" in sys.argv:
        out = Path(sys.argv[sys.argv.index("--out") + 1])
        args = [a for a in args if a != str(out)]
    if len(args) != 1:
        sys.exit(__doc__)
    run = Path(args[0])
    load = (run / "load.txt").read_text()
    calls = re.search(r"calls\s+(\d+) in ([\dhms.µ]+) \(([\d.]+)/s\)", load)
    ok = int(re.search(r"^ok\s+(\d+)", load, re.M).group(1))
    errs = int(re.search(r"^errors\s+(\d+)", load, re.M).group(1))
    lat = re.search(r"p50 (\S+)\s+p95 (\S+)\s+p99 (\S+)\s+max (\S+)", load)
    den = re.search(r"^denials\s+(\d+)", load, re.M)
    codes = {m.group(1): int(m.group(2)) for m in re.finditer(r"^  (\w+)\s+(\d+)$", load, re.M)}

    rows = list(csv.DictReader((run / "samples.csv").open()))
    rows = [r for r in rows if r["gateway_mib"]]
    step = max(1, len(rows) // 120)
    pick = rows[::step]
    if pick[-1] is not rows[-1]:
        pick.append(rows[-1])
    series = {
        "t_min": [round(float(r["t_sec"]) / 60, 2) for r in pick],
        "gateway_mib": [float(r["gateway_mib"]) for r in pick],
        "redis_keys": [int(float(r["redis_keys"])) for r in pick],
        "pg_conns": [int(float(r["pg_conns"])) for r in pick],
        "gateway_cpu_pct": [float(r["gateway_cpu_pct"]) for r in pick],
    }
    data = {
        "run": run.name,
        "generated": datetime.now(timezone.utc).strftime("%Y-%m-%d"),
        "duration": calls.group(2),
        "duration_min": round(float(rows[-1]["t_sec"]) / 60, 1),
        "calls": int(calls.group(1)),
        "calls_per_sec": float(calls.group(3)),
        "ok": ok,
        "denied": int(den.group(1)) if den else 0,
        "denial_codes": codes,
        "errors": errs,
        "latency_ms": {k: round(dur_ms(lat.group(i)), 2) for k, i in (("p50", 1), ("p95", 2), ("p99", 3), ("max", 4))},
        "ready_failures": sum(1 for r in rows if r["ready"] != "200"),
        "samples": len(rows),
        "series": series,
        "audit_queue": (run / "audit.txt").exists(),
        "env": f"{platform.system()} {platform.machine()}, Docker Compose, one gateway, Redis, Postgres and a fake backend on the same machine",
    }
    out.parent.mkdir(parents=True, exist_ok=True)
    out.write_text(json.dumps(data, indent=1) + "\n")
    print(f"wrote {out}: {data['calls']} calls, p99 {data['latency_ms']['p99']} ms, {data['errors']} errors")


def overhead():
    import statistics
    rest = sys.argv[2:]
    out = Path("site/src/data/overhead.json")
    if "--out" in rest:
        i = rest.index("--out")
        out = Path(rest[i + 1])
        rest = rest[:i] + rest[i + 2:]
    if len(rest) != 1:
        sys.exit(__doc__)
    run = Path(rest[0])
    meta = json.loads((run / "meta.json").read_text())
    runs = [json.loads(l) for l in (run / "runs.jsonl").read_text().splitlines() if l.strip()]
    groups = {}
    for r in runs:
        groups.setdefault((r["phase"], r["tool"], r["mode"]), []).append(r["r"])
    def med(rs, k):
        return round(statistics.median(x[k] for x in rs), 3)
    def stat(rs):
        return {
            "p50_ms": med(rs, "p50_ms"), "p95_ms": med(rs, "p95_ms"), "p99_ms": med(rs, "p99_ms"),
            "max_ms": med(rs, "max_ms"), "rps": round(statistics.median(x["rps"] for x in rs), 1),
            "calls": sum(x["calls"] for x in rs), "errors": sum(x["errors"] for x in rs),
            "denied": sum(x["denied"] for x in rs), "reps": len(rs),
        }
    tools = {"get_city_state": "read", "sell_resource": "action"}
    data = {
        "generated": datetime.now(timezone.utc).strftime("%Y-%m-%d"),
        "env": f"{meta['os']} ({meta['arch']}), Docker {meta['docker']} with {meta['cpus']} CPUs and {round(meta['mem_bytes'] / 2**30)} GiB, one gateway, Redis, Postgres and a fake backend on the same machine",
        "reps": meta["reps"], "duration_s": meta["duration_s"], "max_duration_s": meta["max_duration_s"],
        "warmup_s": meta["warmup_s"], "rate": meta["rate"], "concurrency": meta["concurrency"], "players": meta["players"],
        "paced": {}, "max": {},
    }
    for phase in ("paced", "max"):
        for tool, kind in tools.items():
            d, g = groups.get((phase, tool, "direct")), groups.get((phase, tool, "gateway"))
            if not d or not g:
                sys.exit(f"missing runs for {phase} {tool}")
            ds, gs = stat(d), stat(g)
            data[phase][kind] = {
                "tool": tool, "direct": ds, "gateway": gs,
                "added_ms": {k: round(gs[f"{k}_ms"] - ds[f"{k}_ms"], 3) for k in ("p50", "p95", "p99")},
                "throughput_ratio": round(gs["rps"] / ds["rps"], 3) if ds["rps"] else None,
            }
    out.parent.mkdir(parents=True, exist_ok=True)
    out.write_text(json.dumps(data, indent=1) + "\n")
    for phase in ("paced", "max"):
        for kind, v in data[phase].items():
            a = v["added_ms"]
            print(f"{phase:5} {kind:6} direct p50 {v['direct']['p50_ms']:.2f} p99 {v['direct']['p99_ms']:.2f} ms {v['direct']['rps']:.0f}/s | gateway p50 {v['gateway']['p50_ms']:.2f} p99 {v['gateway']['p99_ms']:.2f} ms {v['gateway']['rps']:.0f}/s | added p50 {a['p50']:+.2f} p95 {a['p95']:+.2f} p99 {a['p99']:+.2f} ms")
    bad = [(k, v) for ph in ("paced", "max") for k, v in data[ph].items() if v["direct"]["errors"] or v["gateway"]["errors"]]
    if bad:
        print("WARNING: some runs had errors, read the numbers with care")
    print(f"wrote {out}")


main()
