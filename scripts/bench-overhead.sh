#!/usr/bin/env bash
# Measure what the gateway adds to a call: the same signed request is sent straight to the fake
# city backend ("direct") and through the gateway, with the same workers and rate, so the
# difference is the gateway's own work (policy, validation, signing, audit, Redis and Postgres).
#
#   scripts/bench-overhead.sh                       # about 20 minutes: 3 repetitions
#   DURATION=10 MAX_DURATION=5 WARMUP=2 REPS=1 scripts/bench-overhead.sh   # quick check
#   python3 scripts/bench-report.py overhead bench-results/<run>            # write the site data
#
# Phases per repetition, each run direct then through the gateway, for a read tool
# (get_city_state) and a capped action tool (sell_resource):
#   paced  RATE calls per second for DURATION seconds (latency at a fixed, comfortable load)
#   max    as fast as the workers go for MAX_DURATION seconds (throughput ceiling)
# The first WARMUP seconds of every run are thrown away. Needs Docker and nothing else on the host.
# Results go to bench-results/<timestamp>/ (git-ignored): meta.json, runs.jsonl, summary.txt.
set -euo pipefail
cd "$(dirname "$0")/.."

DURATION="${DURATION:-60}"
MAX_DURATION="${MAX_DURATION:-20}"
WARMUP="${WARMUP:-5}"
REPS="${REPS:-3}"
RATE="${RATE:-100}"
CONCURRENCY="${CONCURRENCY:-32}"
PLAYERS="${PLAYERS:-20000}"
PROJECT="agentgw-bench"
OUT="bench-results/$(date +%Y%m%d-%H%M%S)"
mkdir -p "$OUT"
DC=(docker compose -p "$PROJECT" -f docker-compose.yml -f scripts/bench.compose.yml)

say() { printf '==> %s\n' "$*"; }
cleanup() { "${DC[@]}" down -v --remove-orphans >/dev/null 2>&1 || true; }
trap cleanup EXIT

say "starting the stack (project $PROJECT)"
"${DC[@]}" up -d --build gateway >/dev/null
for _ in $(seq 1 60); do curl -sf http://127.0.0.1:9091/readyz >/dev/null 2>&1 && break; sleep 2; done
curl -sf http://127.0.0.1:9091/readyz >/dev/null || { echo "gateway not ready"; exit 1; }

docker info --format '{"cpus":{{.NCPU}},"mem_bytes":{{.MemTotal}},"docker":"{{.ServerVersion}}","os":"{{.OperatingSystem}}","arch":"{{.Architecture}}"' > "$OUT/meta.part"
printf '%s,"reps":%s,"duration_s":%s,"max_duration_s":%s,"warmup_s":%s,"rate":%s,"concurrency":%s,"players":%s}\n' \
  "$(cat "$OUT/meta.part")" "$REPS" "$DURATION" "$MAX_DURATION" "$WARMUP" "$RATE" "$CONCURRENCY" "$PLAYERS" > "$OUT/meta.json"
rm -f "$OUT/meta.part"

: > "$OUT/runs.jsonl"
# run <rep> <phase> <tool> <mode> <rate> <seconds>
run() {
  local rep=$1 phase=$2 tool=$3 mode=$4 rate=$5 secs=$6 target out line
  if [ "$mode" = direct ]; then target=(--direct-url http://city-backend:9090 --direct-key-id dev-1)
  else target=(--url http://gateway:8080); fi
  say "rep $rep $phase $tool $mode (${secs}s + ${WARMUP}s warmup)"
  out=$("${DC[@]}" run --rm -T load "${target[@]}" --mix "$tool=1" --rate "$rate" --duration "${secs}s" \
        --warmup "${WARMUP}s" --concurrency "$CONCURRENCY" --players "$PLAYERS" \
        --max-error-rate 0.001 --max-p99 0 --json 2>&1) || { echo "$out" | tail -20; echo "load run failed"; exit 1; }
  line=$(printf '%s\n' "$out" | grep '^JSON ' | sed 's/^JSON //')
  [ -n "$line" ] || { echo "$out" | tail -20; echo "no JSON line"; exit 1; }
  printf '{"rep":%s,"phase":"%s","tool":"%s","mode":"%s","r":%s}\n' "$rep" "$phase" "$tool" "$mode" "$line" >> "$OUT/runs.jsonl"
  printf '    %s\n' "$(printf '%s' "$line" | python3 -c 'import sys,json; r=json.load(sys.stdin); print("rps %.0f p50 %.2f p95 %.2f p99 %.2f ms, errors %d, denied %d" % (r["rps"], r["p50_ms"], r["p95_ms"], r["p99_ms"], r["errors"], r["denied"]))')"
}

for rep in $(seq 1 "$REPS"); do
  for tool in get_city_state sell_resource; do
    for mode in direct gateway; do run "$rep" paced "$tool" "$mode" "$RATE" "$DURATION"; done
  done
  for tool in get_city_state sell_resource; do
    for mode in direct gateway; do run "$rep" max "$tool" "$mode" 0 "$MAX_DURATION"; done
  done
done

say "summary"
python3 scripts/bench-report.py overhead "$OUT" --out "$OUT/overhead.json" | tee "$OUT/summary.txt"
say "results in $OUT; to publish on the site: cp $OUT/overhead.json site/src/data/overhead.json"
