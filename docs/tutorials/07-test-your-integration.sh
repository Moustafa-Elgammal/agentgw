#!/usr/bin/env bash
# Verifies tutorial 07 (docs/tutorials/07-test-your-integration.md). Run from the repository root:
#   bash docs/tutorials/07-test-your-integration.sh
# Needs only Docker (Compose v2). It starts its own stack and removes it on exit.
set -euo pipefail

step() { printf '\n==> %s\n' "$*"; }
fail() { printf 'FAIL: %s\n' "$*" >&2; exit 1; }
expect() { # expect "text" "needle" : the text must contain the needle
  case "$1" in *"$2"*) printf 'ok: found %s\n' "$2" ;; *) fail "expected to find: $2"$'\n'"got: $1" ;; esac
}

# --- the commands below are the ones the tutorial shows ---------------------------------

export TUTORIAL_PORT=18107 TUTORIAL_OPS_PORT=19107  # keeps the script off the ports of a demo you already run
PORT=$TUTORIAL_PORT OPS=$TUTORIAL_OPS_PORT
dc() { docker compose --progress quiet -p agentgw-t07 -f docker-compose.yml -f examples/tutorials/compose.ports.yml "$@"; }
trap 'dc down -v --remove-orphans >/dev/null 2>&1 || true' EXIT

step "1. Start the demo gateway (dev auth) and the demo backend"
dc down -v --remove-orphans >/dev/null 2>&1 || true
dc up -d --build gateway
for _ in $(seq 1 60); do curl -sf http://127.0.0.1:$OPS/readyz >/dev/null && break; sleep 1; done
dc build --quiet dev

step "2. Check the gateway from outside"
dc run --rm -T \
  -e AGENTGW_CONFORMANCE_URL=http://gateway:8080/mcp \
  -e AGENTGW_CONFORMANCE_PLAYER=tutorial-7 \
  dev go test -count=1 -v -run 'TestExternalGateway$' ./test/conformance | tee /dev/stderr | grep -- '--- PASS: TestExternalGateway ' >/dev/null \
  || fail "the gateway checks did not pass"

step "3. Check the backend from outside"
dc run --rm -T \
  -e AGENTGW_CONFORMANCE_BACKEND_URL=http://city-backend:9090 \
  -e AGENTGW_CONFORMANCE_BACKEND_KEY_ID=dev-1 \
  -e AGENTGW_CONFORMANCE_BACKEND_SECRET=dev-secret \
  -e AGENTGW_CONFORMANCE_BACKEND_READ_PATH=/api/v1/city \
  dev go test -count=1 -v -run 'TestExternalBackend$' ./test/conformance | tee /dev/stderr | grep -- '--- PASS: TestExternalBackend ' >/dev/null \
  || fail "the backend checks did not pass"

step "4. A wrong secret fails the first check, with a hint"
if OUT=$(dc run --rm -T \
  -e AGENTGW_CONFORMANCE_BACKEND_URL=http://city-backend:9090 \
  -e AGENTGW_CONFORMANCE_BACKEND_SECRET=a-secret-the-backend-does-not-know \
  -e AGENTGW_CONFORMANCE_BACKEND_READ_PATH=/api/v1/city \
  dev go test -count=1 -run 'TestExternalBackend$' ./test/conformance 2>&1); then
  fail "the run with a wrong secret must fail"
fi
echo "$OUT"
expect "$OUT" 'wrong key, secret or path?'

step "5. A manifest that is not the one the gateway serves fails the tool list check"
if OUT=$(dc run --rm -T \
  -e AGENTGW_CONFORMANCE_URL=http://gateway:8080/mcp \
  -e AGENTGW_CONFORMANCE_PLAYER=tutorial-7b \
  -e AGENTGW_CONFORMANCE_MANIFEST=../../examples/tutorials/city-lite.json \
  dev go test -count=1 -run 'TestExternalGateway$' ./test/conformance 2>&1); then
  fail "the run with the wrong manifest must fail"
fi
echo "$OUT"
expect "$OUT" 'unexpected tool'

step "6. The gateway's own suite, for anyone changing the gateway"
dc run --rm -T dev go test -race -count=1 ./test/conformance | tail -n 3

step "7. A 15 second load smoke test"
dc run --rm -T load --url http://gateway:8080 --duration 15s --rate 100 \
  --max-error-rate 0.01 --max-p99 500ms | tee /dev/stderr | grep p99 >/dev/null || fail "the load run printed no latency"

printf '\nPASS: tutorial 07\n'
