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

echo "t_sec,gateway_mib,gateway_cpu_pct,redis_mib,redis_keys,postgres_mib,pg_conns,ready" > "$OUT/samples.csv"
sampler() {
  local start; start=$(date +%s)
  while true; do
    local now t gw cpu rd keys pg conns ready
    now=$(date +%s); t=$((now - start))
    gw=$(mem_mb "$(cid gateway)")
    cpu=$(docker stats --no-stream --format '{{.CPUPerc}}' "$(cid gateway)" 2>/dev/null | tr -d '%')
    rd=$(mem_mb "$(cid redis)")
    keys=$("${DC[@]}" exec -T redis redis-cli dbsize 2>/dev/null | tr -d '\r' || echo 0)
    pg=$(mem_mb "$(cid postgres)")
    conns=$("${DC[@]}" exec -T postgres psql -U agentgw -d agentgw -tAc 'select count(*) from pg_stat_activity' 2>/dev/null | tr -d '\r' || echo 0)
    ready=$(curl -s -o /dev/null -w '%{http_code}' http://127.0.0.1:9091/readyz || echo 000)
    echo "$t,$gw,$cpu,$rd,$keys,$pg,$conns,$ready" >> "$OUT/samples.csv"
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
        if name in ("gateway_mib", "pg_conns") and g > limit:
            print(f"  FAIL: {name} grew more than {limit:.0f}%"); bad = True
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
[ "$LOAD_RC" -eq 0 ] && [ "$TREND_RC" -eq 0 ]
