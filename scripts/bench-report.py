#!/usr/bin/env python3
"""Turn one soak run (scripts/soak.sh) into site/src/data/soak.json for the landing page.

    python3 scripts/bench-report.py soak-results/<run> [--out site/src/data/soak.json]

Reads load.txt (the load tool's report) and samples.csv (resource samples). Stdlib only. The
numbers are copied, never rounded up: the page shows exactly what the run measured.
"""
import csv, json, platform, re, sys
from datetime import datetime, timezone
from pathlib import Path

def dur_ms(s):
    m = re.fullmatch(r"([\d.]+)(µs|us|ms|s)", s)
    v, u = float(m.group(1)), m.group(2)
    return v * {"µs": 0.001, "us": 0.001, "ms": 1.0, "s": 1000.0}[u]

def main():
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
        "env": f"{platform.system()} {platform.machine()}, Docker Compose, one gateway, Redis, Postgres and a fake backend on the same machine",
    }
    out.parent.mkdir(parents=True, exist_ok=True)
    out.write_text(json.dumps(data, indent=1) + "\n")
    print(f"wrote {out}: {data['calls']} calls, p99 {data['latency_ms']['p99']} ms, {data['errors']} errors")

main()
