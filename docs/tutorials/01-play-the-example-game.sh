#!/usr/bin/env bash
# Runs the commands of 01-play-the-example-game.md against a throwaway compose project.
# Run from the repository root. Needs only Docker and curl. Exits nonzero on any surprise.
set -euo pipefail

P=agentgw-t01
GW=http://127.0.0.1:18101
NET=${P}_default
DC=(docker compose -p "$P" -f docker-compose.yml -f docs/tutorials/t01.compose.yml)

step() { printf '\n== %s\n' "$*"; }
fail() { printf 'FAIL: %s\n' "$*" >&2; exit 1; }
expect() { # expect "<what>" "<haystack>" "<needle>"
  case "$2" in *"$3"*) printf 'ok: %s\n' "$1" ;; *) fail "$1: wanted [$3] in [$2]" ;; esac
}
cleanup() { "${DC[@]}" logs gateway > "${TMPDIR:-/tmp}/$P-gateway.log" 2>&1 || true; "${DC[@]}" down -v >/dev/null 2>&1 || true; }
trap cleanup EXIT

step "1. Start the demo"
"${DC[@]}" up --build -d >/dev/null
for _ in $(seq 1 60); do curl -fs "$GW/healthz" >/dev/null 2>&1 && break; sleep 1; done
curl -fs "$GW/healthz" >/dev/null || fail "gateway did not come up"
call() { curl -s -X POST -H 'X-Dev-Player: p1' -d "$2" "$GW/v1/tools/$1/call"; }

step "2. Look at the state, then sell some food"
out=$(call get_city_state '{}');                                          expect "city state" "$out" '"coins":500'
out=$(call sell_resource '{"arguments":{"resource":"food","quantity":50}}'); expect "sold food" "$out" '"earned":100'

step "3. Hit the daily cap (300 units a day, 50 used)"
out=$(call sell_resource '{"arguments":{"resource":"water","quantity":400}}')
expect "cap code" "$out" '"code":"daily_amount_cap"'
expect "the message says what is left" "$out" '250 left today'

step "4. Hit the cooldown"
out=$(call collect_taxes '{}');  expect "first collect" "$out" '"collected":25'
out=$(call collect_taxes '{}');  expect "second collect" "$out" '"code":"cooldown"'

step "5. Connect through gwbridge (stdio) and list the tools"
"${DC[@]}" build bridge >/dev/null
reply=$({
  printf '%s\n' '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"tutorial","version":"1"}}}'
  printf '%s\n' '{"jsonrpc":"2.0","method":"notifications/initialized"}'
  printf '%s\n' '{"jsonrpc":"2.0","id":2,"method":"tools/list"}'
  sleep 3
} | docker run -i --rm --network "$NET" agentgw-bridge:latest \
      --url http://gateway:8080/mcp --header "X-Dev-Player: desktop")
expect "bridge lists get_city_state" "$reply" 'get_city_state'
expect "bridge lists sell_resource" "$reply" 'sell_resource'

step "6. Call a tool through the bridge"
reply=$({
  printf '%s\n' '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"tutorial","version":"1"}}}'
  printf '%s\n' '{"jsonrpc":"2.0","method":"notifications/initialized"}'
  printf '%s\n' '{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"get_city_state","arguments":{}}}'
  sleep 3
} | docker run -i --rm --network "$NET" agentgw-bridge:latest \
      --url http://gateway:8080/mcp --header "X-Dev-Player: desktop")
expect "bridge call result has coins" "$reply" 'coins'

step "7. Read the audit trail"
log=$("${DC[@]}" logs gateway | grep agent_call || true)
expect "audit has the cap denial" "$log" '"code":"daily_amount_cap"'
expect "audit has the cooldown denial" "$log" '"code":"cooldown"'
expect "audit has the bridge player" "$log" '"player":"desktop"'
case "$log" in *'"quantity"'*) fail "audit leaked arguments" ;; *) echo "ok: audit holds hashes, not arguments" ;; esac

printf '\nPASSED: 01-play-the-example-game\n'
