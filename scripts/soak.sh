#!/usr/bin/env bash
# Soak test: run the load tool against the compose gateway (Redis and Postgres included) for a
# long time and sample resource use every INTERVAL seconds, so a leak or a slow drift shows up.
#
#   DURATION=1h RATE=100 PLAYERS=2000 scripts/soak.sh
#   DURATION=2m scripts/soak.sh            # quick check of the script itself
#
# Needs Docker and nothing else on the host. Results go to soak-results/<timestamp>/ (git-ignored):
# load.txt (the load tool's report), samples.csv, summary.txt. Exits nonzero when the load tool
# breaches a threshold or memory grows past MAX_GROWTH_PCT between the first and last quarter.
set -euo pipefail
cd "$(dirname "$0")/.."

DURATION="${DURATION:-1h}"
RATE="${RATE:-100}"
CONCURRENCY="${CONCURRENCY:-32}"
PLAYERS="${PLAYERS:-2000}"
INTERVAL="${INTERVAL:-30}"
MAX_P99="${MAX_P99:-500ms}"
MAX_ERROR_RATE="${MAX_ERROR_RATE:-0.001}"
MAX_GROWTH_PCT="${MAX_GROWTH_PCT:-30}"
PROJECT="agentgw-soak"
OUT="soak-results/$(date +%Y%m%d-%H%M%S)"
mkdir -p "$OUT"
DC=(docker compose -p "$PROJECT")
# AUDIT_QUEUE=1 runs the gateway with the batching audit writer (--audit-queue) and, at the end,
# checks that every call left an audit row and that nothing was dropped.
AUDIT_QUEUE="${AUDIT_QUEUE:-}"
[ -n "$AUDIT_QUEUE" ] && DC=(docker compose -p "$PROJECT" -f docker-compose.yml -f scripts/soak.audit-queue.yml)

say() { printf '==> %s\n' "$*"; }

cleanup() {
  [ -n "${SAMPLER:-}" ] && kill "$SAMPLER" 2>/dev/null || true
  "${DC[@]}" down -v --remove-orphans >/dev/null 2>&1 || true
}
trap cleanup EXIT

say "starting the stack (project $PROJECT)"
"${DC[@]}" up -d --build gateway >/dev/null
for _ in $(seq 1 60); do
  if "${DC[@]}" exec -T gateway /gateway --version >/dev/null 2>&1 || \
     curl -sf http://127.0.0.1:9091/readyz >/dev/null 2>&1; then break; fi
  sleep 2
done
curl -sf http://127.0.0.1:9091/readyz >/dev/null || { echo "gateway not ready"; exit 1; }

mem_mb() { # container name -> MiB in use
  docker stats --no-stream --format '{{.MemUsage}}' "$1" 2>/dev/null | awk '{
    v=$1; u=v; gsub(/[0-9.]/,"",u); n=v+0;
    if (u=="KiB") n/=1024; else if (u=="GiB") n*=1024; else if (u=="B") n/=1048576;
    printf "%.1f", n }'
}
cid() { "${DC[@]}" ps -q "$1"; }

echo "t_sec,gateway_mib,gateway_cpu_pct,redis_mib,redis_keys,postgres_mib,pg_conns,ready,pool_total,pool_max,goroutines" > "$OUT/samples.csv"
sampler() {
  local start; start=$(date +%s)
  while true; do
    local now t gw cpu rd keys pg conns ready pm ptot pmax gor
    now=$(date +%s); t=$((now - start))
    gw=$(mem_mb "$(cid gateway)")
    cpu=$(docker stats --no-stream --format '{{.CPUPerc}}' "$(cid gateway)" 2>/dev/null | tr -d '%')
    rd=$(mem_mb "$(cid redis)")
    keys=$("${DC[@]}" exec -T redis redis-cli dbsize 2>/dev/null | tr -d '\r' || echo 0)
    pg=$(mem_mb "$(cid postgres)")
    # Only client sessions, and not this probe itself: Postgres always shows its own background
    # processes (about five) in pg_stat_activity, which are not the gateway's connections.
    conns=$("${DC[@]}" exec -T postgres psql -U agentgw -d agentgw -tAc "select count(*) from pg_stat_activity where backend_type = 'client backend' and pid <> pg_backend_pid()" 2>/dev/null | tr -d '\r' || echo 0)
    pm=$(curl -s http://127.0.0.1:9091/metrics 2>/dev/null || true)
    ptot=$(printf '%s\n' "$pm" | awk '$1=="agentgw_pgx_pool_total_conns"{print $2}')
    pmax=$(printf '%s\n' "$pm" | awk '$1=="agentgw_pgx_pool_max_conns"{print $2}')
    gor=$(printf '%s\n' "$pm" | awk '$1=="go_goroutines"{print $2}')
    ready=$(curl -s -o /dev/null -w '%{http_code}' http://127.0.0.1:9091/readyz || echo 000)
    echo "$t,$gw,$cpu,$rd,$keys,$pg,$conns,$ready,$ptot,$pmax,$gor" >> "$OUT/samples.csv"
    sleep "$INTERVAL"
  done
}
sampler &
SAMPLER=$!

say "load: $DURATION at $RATE/s, $CONCURRENCY workers, $PLAYERS players"
set +e
"${DC[@]}" run --rm load --url http://gateway:8080 --duration "$DURATION" --rate "$RATE" \
  --concurrency "$CONCURRENCY" --players "$PLAYERS" --max-error-rate "$MAX_ERROR_RATE" \
  --max-p99 "$MAX_P99" 2>&1 | tee "$OUT/load.txt"
LOAD_RC=${PIPESTATUS[0]}
set -e
kill "$SAMPLER" 2>/dev/null || true; SAMPLER=""

say "metrics at the end"
curl -s http://127.0.0.1:9091/metrics > "$OUT/metrics.txt" || true
AUDIT_RC=0
if [ -n "$AUDIT_QUEUE" ]; then
  sleep 3 # let the last batch flush
  rows=$("${DC[@]}" exec -T postgres psql -U agentgw -d agentgw -tAc "select count(*) from audit_events" | tr -d '\r ')
  dropped=$(awk '/^audit_dropped_total/{n+=$2} END{print n+0}' "$OUT/metrics.txt")
  depth=$(awk '$1=="audit_queue_depth"{print $2}' "$OUT/metrics.txt")
  {
    echo "audit rows in Postgres: $rows; dropped: $dropped; queue depth at the end: ${depth:-?}"
    grep -E '^audit_(flush_duration_seconds_(sum|count)|batch_size_(sum|count)|flush_errors_total)' "$OUT/metrics.txt" || true
  } | tee "$OUT/audit.txt"
  [ "$dropped" = "0" ] || { echo "FAIL: audit events were dropped"; AUDIT_RC=1; }
fi
"${DC[@]}" logs --no-color gateway 2>&1 | grep -c '"level":"ERROR"' > "$OUT/gateway-error-lines.txt" || true

python3 - "$OUT" "$MAX_GROWTH_PCT" <<'PY' | tee "$OUT/summary.txt"
import csv, sys, statistics as st
out, limit = sys.argv[1], float(sys.argv[2])
rows = list(csv.DictReader(open(f"{out}/samples.csv")))
rows = [r for r in rows if r["gateway_mib"]]
def col(n): return [float(r[n]) for r in rows if r[n] not in ("", None)]
print(f"samples: {len(rows)} over {rows[-1]['t_sec']} s" if rows else "no samples")
bad = False
if len(rows) >= 8:
    q = len(rows) // 4
    for name in ("gateway_mib", "redis_mib", "redis_keys", "pg_conns"):
        v = col(name)
        a, b = st.mean(v[:q]), st.mean(v[-q:])
        g = (b - a) / a * 100 if a else 0
        print(f"{name:14} first quarter {a:9.1f}  last quarter {b:9.1f}  change {g:+6.1f}%  max {max(v):.1f}")
        if name == "gateway_mib" and g > limit:
            print(f"  FAIL: {name} grew more than {limit:.0f}%"); bad = True
    # Postgres connections: a pool fills up to its cap in the first minutes and then stays level,
    # so growth since the start is expected. A leak is a count above the cap, or one that is still
    # climbing in the second half of the run.
    conns = col("pg_conns")
    caps = [float(r["pool_max"]) for r in rows if r.get("pool_max")]
    cap = max(caps) if caps else None
    third, fourth = st.mean(conns[2 * q:3 * q]), st.mean(conns[-q:])
    if cap is not None:
        print(f"pg client conns: peak {max(conns):.0f} against a pool cap of {cap:.0f}; third quarter {third:.1f}, last quarter {fourth:.1f}")
        if max(conns) > cap:
            print("  FAIL: more Postgres client connections than the pool may open"); bad = True
    else:
        print(f"pg client conns: peak {max(conns):.0f}; third quarter {third:.1f}, last quarter {fourth:.1f} (pool cap not exposed by this gateway build)")
    if fourth - third > 0.5:
        print("  FAIL: Postgres connections still growing in the second half"); bad = True
    gor = [float(r["goroutines"]) for r in rows if r.get("goroutines")]
    if len(gor) >= 8:
        g3, g4 = st.mean(gor[2 * q:3 * q]), st.mean(gor[-q:])
        print(f"goroutines: third quarter {g3:.0f}, last quarter {g4:.0f}, max {max(gor):.0f}")
        if g4 > g3 * 1.2 + 10:
            print("  FAIL: goroutines still growing in the second half"); bad = True
    notready = [r for r in rows if r["ready"] != "200"]
    print(f"readyz not 200 in {len(notready)} of {len(rows)} samples")
    bad = bad or bool(notready)
else:
    print("too few samples for a trend (run longer than a few minutes)")
sys.exit(1 if bad else 0)
PY
TREND_RC=${PIPESTATUS[0]}
errs=$(cat "$OUT/gateway-error-lines.txt" 2>/dev/null || echo 0)
say "gateway ERROR log lines: $errs; load exit $LOAD_RC; trend exit $TREND_RC; results in $OUT"
[ "$LOAD_RC" -eq 0 ] && [ "$TREND_RC" -eq 0 ] && [ "$AUDIT_RC" -eq 0 ]
