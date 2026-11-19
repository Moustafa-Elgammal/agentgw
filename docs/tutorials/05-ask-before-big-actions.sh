#!/usr/bin/env bash
# Verifies tutorial 05 (docs/tutorials/05-ask-before-big-actions.md). Run from the repository root:
#   bash docs/tutorials/05-ask-before-big-actions.sh
# Needs only Docker (Compose v2) and curl. It starts its own stack and removes it on exit.
set -euo pipefail

step() { printf '\n==> %s\n' "$*"; }
fail() { printf 'FAIL: %s\n' "$*" >&2; exit 1; }
expect() { # expect "text" "needle" : the text must contain the needle
  case "$1" in *"$2"*) printf 'ok: found %s\n' "$2" ;; *) fail "expected to find: $2"$'\n'"got: $1" ;; esac
}

# --- the commands below are the ones the tutorial shows ---------------------------------

export TUTORIAL_PORT=18105 TUTORIAL_OPS_PORT=19105  # keeps the script off the ports of a demo you already run
PORT=$TUTORIAL_PORT OPS=$TUTORIAL_OPS_PORT
dc() { docker compose --progress quiet -p agentgw-t05 -f docker-compose.yml -f examples/tutorials/compose.ports.yml -f examples/tutorials/compose.oauth.yml "$@"; }
trap 'dc down -v --remove-orphans >/dev/null 2>&1 || true' EXIT

step "1. Start the gateway in OAuth mode and build the helpers"
dc down -v --remove-orphans >/dev/null 2>&1 || true
dc up -d --build gateway
for _ in $(seq 1 60); do curl -sf http://127.0.0.1:$OPS/readyz >/dev/null && break; sleep 1; done
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
# The id of the first pending confirmation in a list answer.
first_id() { sed 's/^{"confirmations":\[{"id":"\([^"]*\)".*/\1/'; }

step "2. Link alice's agent as an operator"
TICKET=$(studio --player alice --expect 201 POST /v1/studio/link-tickets 2>/dev/null | sed 's/.*"ticket":"\([^"]*\)".*/\1/')
ACCESS=$(oauth --ticket "$TICKET" --scope operator | sed 's/.*"access_token":"\([^"]*\)".*/\1/')
[ -n "$ACCESS" ] || fail "no access token"
BUILD='{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"build_structure","arguments":{"kind":"farm","x":1,"y":1}}}'

step "3. The agent asks to build. The gateway holds the call"
RES=$(mcp "$BUILD"); echo "$RES"
expect "$RES" 'confirmation_required'
expect "$RES" '"isError":true'

step "4. The game's backend asks the gateway what is waiting for alice"
PENDING=$(studio --player alice --expect 200 GET /v1/studio/confirmations); echo "$PENDING"
expect "$PENDING" '"tool":"build_structure"'
expect "$PENDING" '"state":"pending"'
ID=$(echo "$PENDING" | first_id)
[ -n "$ID" ] || fail "no confirmation id"

step "5. Another player cannot see or decide alice's confirmation"
expect "$(studio --player bob --expect 200 GET /v1/studio/confirmations)" '"confirmations":[]'
studio --player bob --expect 404 POST "/v1/studio/confirmations/$ID/approve"

step "6. alice taps Approve in the game. The backend calls the gateway"
studio --player alice --expect 204 POST "/v1/studio/confirmations/$ID/approve"

step "7. The agent repeats the exact call and it goes through"
RES=$(mcp "$BUILD"); echo "$RES"
expect "$RES" '"ok\":true'
expect "$RES" 'buildings'

step "8. The approval is used up: the same call asks again"
RES=$(mcp "$BUILD"); echo "$RES"
expect "$RES" 'confirmation_required'

step "9. This time alice taps Deny"
ID=$(studio --player alice --expect 200 GET /v1/studio/confirmations | first_id)
[ -n "$ID" ] || fail "no second confirmation id"
studio --player alice --expect 204 POST "/v1/studio/confirmations/$ID/deny"
studio --player alice --expect 204 POST "/v1/studio/confirmations/$ID/deny"   # denying twice is fine
studio --player alice --expect 409 POST "/v1/studio/confirmations/$ID/approve" # but it cannot be flipped
RES=$(mcp "$BUILD"); echo "$RES"
expect "$RES" 'confirmation_denied'

step "10. A different call is a new question, not covered by the denial"
OTHER='{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"build_structure","arguments":{"kind":"well","x":2,"y":2}}}'
RES=$(mcp "$OTHER"); echo "$RES"
expect "$RES" 'confirmation_required'

printf '\nPASS: tutorial 05\n'
