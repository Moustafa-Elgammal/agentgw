#!/usr/bin/env bash
# Verifies tutorial 04 (docs/tutorials/04-link-a-player.md). Run from the repository root:
#   bash docs/tutorials/04-link-a-player.sh
# Needs only Docker (Compose v2) and curl. It starts its own stack and removes it on exit.
set -euo pipefail

step() { printf '\n==> %s\n' "$*"; }
fail() { printf 'FAIL: %s\n' "$*" >&2; exit 1; }
expect() { # expect "text" "needle" : the text must contain the needle
  case "$1" in *"$2"*) printf 'ok: found %s\n' "$2" ;; *) fail "expected to find: $2"$'\n'"got: $1" ;; esac
}

# --- the commands below are the ones the tutorial shows ---------------------------------

export TUTORIAL_PORT=18104 TUTORIAL_OPS_PORT=19104  # keeps the script off the ports of a demo you already run
PORT=$TUTORIAL_PORT OPS=$TUTORIAL_OPS_PORT
dc() { docker compose --progress quiet -p agentgw-t04 -f docker-compose.yml -f examples/tutorials/compose.ports.yml -f examples/tutorials/compose.oauth.yml "$@"; }
trap 'dc down -v --remove-orphans >/dev/null 2>&1 || true' EXIT

step "1. Start the gateway in OAuth mode"
dc down -v --remove-orphans >/dev/null 2>&1 || true
dc up -d --build gateway
for _ in $(seq 1 60); do curl -sf http://127.0.0.1:$OPS/readyz >/dev/null && break; sleep 1; done
curl -s http://127.0.0.1:$OPS/readyz | tee /dev/stderr | grep ready >/dev/null || fail "gateway is not ready"

step "2. Build the two helper programs in the toolchain container"
dc build --quiet dev
dc run --rm -T dev go build -o bin/ ./examples/studiocall ./examples/oauthclient
studio() { dc run --rm -T dev bin/studiocall "$@"; }
oauth() { dc run --rm -T dev bin/oauthclient "$@"; }
mcp() {
  curl -s -H "Authorization: Bearer $ACCESS" -H 'Content-Type: application/json' \
    -H 'Accept: application/json, text/event-stream' -H 'MCP-Protocol-Version: 2025-06-18' \
    -d "$1" http://127.0.0.1:$PORT/mcp
  echo
}

step "3. An agent without a token is turned away and told where to link"
OUT=$(curl -s -i -X POST http://127.0.0.1:$PORT/mcp -d '{}')
echo "$OUT" | head -n 3
expect "$OUT" "401"
expect "$OUT" "resource_metadata="
curl -s http://127.0.0.1:$PORT/.well-known/oauth-protected-resource/mcp; echo

step "4. The studio backend mints a link ticket for alice"
OUT=$(studio --player alice --expect 201 POST /v1/studio/link-tickets); echo "$OUT"
TICKET=$(echo "$OUT" | sed 's/.*"ticket":"\([^"]*\)".*/\1/')
[ -n "$TICKET" ] || fail "no ticket"

step "5. The agent links with the coach scope"
COACH_TOKENS=$(oauth --ticket "$TICKET" --scope coach); echo "$COACH_TOKENS"
ACCESS=$(echo "$COACH_TOKENS" | sed 's/.*"access_token":"\([^"]*\)".*/\1/')
expect "$COACH_TOKENS" '"scope":"coach"'
COACH_ACCESS=$ACCESS

step "6. A coach sees one read only tool and cannot act"
TOOLS=$(mcp '{"jsonrpc":"2.0","id":1,"method":"tools/list"}' | grep -o '"name":"[a-z_]*"')
echo "$TOOLS"
expect "$TOOLS" '"name":"get_city_state"'
case "$TOOLS" in *sell_resource*) fail "a coach must not see sell_resource" ;; esac
RES=$(mcp '{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"sell_resource","arguments":{"resource":"food","quantity":5}}}')
echo "$RES"
expect "$RES" 'unknown tool'

step "7. The same player links again with the operator scope"
TICKET=$(studio --player alice --expect 201 POST /v1/studio/link-tickets 2>/dev/null | sed 's/.*"ticket":"\([^"]*\)".*/\1/')
OP_TOKENS=$(oauth --ticket "$TICKET" --scope operator); echo "$OP_TOKENS"
ACCESS=$(echo "$OP_TOKENS" | sed 's/.*"access_token":"\([^"]*\)".*/\1/')
OPERATOR_ACCESS=$ACCESS
TOOLS=$(mcp '{"jsonrpc":"2.0","id":1,"method":"tools/list"}' | grep -o '"name":"[a-z_]*"')
echo "$TOOLS"
expect "$TOOLS" '"name":"sell_resource"'
RES=$(mcp '{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"sell_resource","arguments":{"resource":"food","quantity":5}}}')
echo "$RES"
expect "$RES" '"ok\":true'

step "8. The game's UI lists alice's linked agents"
GRANTS=$(studio --player alice --expect 200 GET /v1/studio/grants); echo "$GRANTS"
expect "$GRANTS" '"scope":"coach"'
expect "$GRANTS" '"scope":"operator"'
COACH_GRANT=$(echo "$GRANTS" | sed 's/^{"grants":\[{"id":"\([^"]*\)".*/\1/')

step "9. alice revokes the coach grant. Its token stops working at once"
studio --player alice --expect 204 DELETE "/v1/studio/grants/$COACH_GRANT"
CODE=$(curl -s -o /dev/null -w '%{http_code}' -H "Authorization: Bearer $COACH_ACCESS" \
  -H 'Content-Type: application/json' -H 'Accept: application/json, text/event-stream' \
  -H 'MCP-Protocol-Version: 2025-06-18' -d '{"jsonrpc":"2.0","id":1,"method":"tools/list"}' http://127.0.0.1:$PORT/mcp)
echo "coach token: HTTP $CODE"
[ "$CODE" = 401 ] || fail "revoked coach token answered $CODE, want 401"
ACCESS=$OPERATOR_ACCESS
expect "$(mcp '{"jsonrpc":"2.0","id":1,"method":"tools/list"}')" 'sell_resource'

step "10. A log out everywhere button"
studio --player alice --expect 200 DELETE /v1/studio/grants
CODE=$(curl -s -o /dev/null -w '%{http_code}' -H "Authorization: Bearer $OPERATOR_ACCESS" \
  -H 'Content-Type: application/json' -H 'Accept: application/json, text/event-stream' \
  -H 'MCP-Protocol-Version: 2025-06-18' -d '{"jsonrpc":"2.0","id":1,"method":"tools/list"}' http://127.0.0.1:$PORT/mcp)
echo "operator token: HTTP $CODE"
[ "$CODE" = 401 ] || fail "revoked operator token answered $CODE, want 401"

printf '\nPASS: tutorial 04\n'
