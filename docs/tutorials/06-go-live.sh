#!/usr/bin/env bash
# Verifies tutorial 06 (docs/tutorials/06-go-live.md). Run from the repository root:
#   bash docs/tutorials/06-go-live.sh
# Needs only Docker (Compose v2) and curl. It starts its own stack and removes it on exit.
set -euo pipefail

step() { printf '\n==> %s\n' "$*"; }
fail() { printf 'FAIL: %s\n' "$*" >&2; exit 1; }
expect() { # expect "text" "needle" : the text must contain the needle
  case "$1" in *"$2"*) printf 'ok: found %s\n' "$2" ;; *) fail "expected to find: $2"$'\n'"got: $1" ;; esac
}
refuse() { # refuse "text" "needle" : the text must NOT contain the needle
  case "$1" in *"$2"*) fail "did not expect to find: $2"$'\n'"got: $1" ;; *) printf 'ok: no %s\n' "$2" ;; esac
}

# --- the commands below are the ones the tutorial shows ---------------------------------

export TUTORIAL_KEYS_DIR="$PWD/.tutorial-keys"
export TUTORIAL_PORT=18106 TUTORIAL_OPS_PORT=19106  # keeps the script off the ports of a demo you already run
PORT=$TUTORIAL_PORT OPS=$TUTORIAL_OPS_PORT
dc() {
  docker compose --progress quiet -p agentgw-t06 -f docker-compose.yml -f examples/tutorials/compose.ports.yml \
    -f examples/tutorials/compose.oauth.yml -f examples/tutorials/compose.golive.yml "$@"
}
trap 'dc down -v --remove-orphans >/dev/null 2>&1 || true; rm -rf "$TUTORIAL_KEYS_DIR"' EXIT

step "1. A key file for the studio key, then start the gateway with Postgres, Redis and the admin key"
dc down -v --remove-orphans >/dev/null 2>&1 || true
mkdir -p "$TUTORIAL_KEYS_DIR"
printf '{"studio-a":"golive-demo-studio-a-secret-0001"}\n' > "$TUTORIAL_KEYS_DIR/studio-keys.json"
chmod 600 "$TUTORIAL_KEYS_DIR/studio-keys.json"
dc up -d --build gateway
for _ in $(seq 1 60); do curl -sf http://127.0.0.1:$OPS/readyz >/dev/null && break; sleep 1; done
dc build --quiet dev
dc run --rm -T dev go build -o bin/ ./examples/studiocall ./examples/oauthclient
studio_a() { dc run --rm -T dev bin/studiocall --key-id studio-a --secret-env STUDIO_A_SECRET "$@"; }
studio_b() { dc run --rm -T dev bin/studiocall --key-id studio-b --secret-env STUDIO_B_SECRET "$@"; }
admin() { dc run --rm -T dev bin/studiocall --key-id admin-1 --secret-env AGENTGW_ADMIN_SECRET "$@"; }
oauth() { dc run --rm -T dev bin/oauthclient "$@"; }
mcp() {
  curl -s -H "Authorization: Bearer $ACCESS" -H 'Content-Type: application/json' \
    -H 'Accept: application/json, text/event-stream' -H 'MCP-Protocol-Version: 2025-06-18' \
    -d "$1" http://127.0.0.1:$PORT/mcp
  echo
}
tool_names() { grep -o '"name":"[a-z_]*"' | tr '\n' ' '; echo; }
LIST='{"jsonrpc":"2.0","id":1,"method":"tools/list"}'

step "2. Health, readiness and metrics on the ops port"
curl -s http://127.0.0.1:$OPS/healthz; echo
curl -s http://127.0.0.1:$OPS/readyz; echo
curl -s http://127.0.0.1:$OPS/metrics | grep -E '^agentgw_build_info' | head -n 1

step "3. The studio signs with studio-a, the key from the file"
OUT=$(studio_a --player alice --expect 201 POST /v1/studio/link-tickets); echo "$OUT"
TICKET=$(echo "$OUT" | sed 's/.*"ticket":"\([^"]*\)".*/\1/')
ACCESS=$(oauth --ticket "$TICKET" --scope operator | sed 's/.*"access_token":"\([^"]*\)".*/\1/')
[ -n "$ACCESS" ] || fail "no access token"
TOOLS=$(mcp "$LIST" | tool_names); echo "$TOOLS"
expect "$TOOLS" sell_resource

step "4. Upload a smaller manifest with the admin key. It is live at once"
admin --player ci --expect 201 --body-file examples/tutorials/city-lite.json PUT /v1/studio/manifest
TOOLS=$(mcp "$LIST" | tool_names); echo "$TOOLS"
refuse "$TOOLS" sell_resource
expect "$TOOLS" get_city_state

step "5. List the versions and roll back to the first one"
VERSIONS=$(admin --player ci --expect 200 GET /v1/studio/manifest/versions); echo "$VERSIONS"
expect "$VERSIONS" '"actor":"bootstrap"'
expect "$VERSIONS" '"actor":"ci"'
FIRST=$(echo "$VERSIONS" | grep -o '"id":[0-9]*,"sha256":"[0-9a-f]*","actor":"bootstrap"' | sed 's/"id":\([0-9]*\),.*/\1/')
[ -n "$FIRST" ] || fail "no bootstrap version"
admin --player ci --expect 200 POST "/v1/studio/manifest/versions/$FIRST/activate"
TOOLS=$(mcp "$LIST" | tool_names); echo "$TOOLS"
expect "$TOOLS" sell_resource

step "6. A bad manifest is refused and changes nothing"
printf '{"version":1,"tools":[]}' > "$TUTORIAL_KEYS_DIR/bad.json"
admin --player ci --expect 422 --body-file .tutorial-keys/bad.json PUT /v1/studio/manifest
expect "$(mcp "$LIST" | tool_names)" sell_resource

step "7. Rotate the studio key without a restart"
printf '{"studio-a":"golive-demo-studio-a-secret-0001","studio-b":"golive-demo-studio-b-secret-0002"}\n' > "$TUTORIAL_KEYS_DIR/studio-keys.json"
dc kill -s HUP gateway
sleep 1
dc logs gateway 2>&1 | grep 'studio keys loaded' | grep studio-keys.json | tail -n 1
studio_b --player alice --expect 200 GET /v1/studio/grants >/dev/null
echo "studio-b is accepted, and studio-a still is during the overlap"
studio_a --player alice --expect 200 GET /v1/studio/grants >/dev/null
printf '{"studio-b":"golive-demo-studio-b-secret-0002"}\n' > "$TUTORIAL_KEYS_DIR/studio-keys.json"
dc kill -s HUP gateway
sleep 1
echo "studio-a is retired:"
studio_a --player alice --expect 401 GET /v1/studio/grants
studio_b --player alice --expect 200 GET /v1/studio/grants >/dev/null
echo "a file that fails to parse is ignored and the current keys stay:"
printf 'not json' > "$TUTORIAL_KEYS_DIR/studio-keys.json"
dc kill -s HUP gateway
sleep 1
dc logs gateway 2>&1 | grep 'key reload failed' | tail -n 1 | cut -c1-200
studio_b --player alice --expect 200 GET /v1/studio/grants >/dev/null
echo "put the file back:"
printf '{"studio-b":"golive-demo-studio-b-secret-0002"}\n' > "$TUTORIAL_KEYS_DIR/studio-keys.json"
dc kill -s HUP gateway
sleep 1

step "8. The activity feed: what alice's agents did"
mcp '{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"get_city_state","arguments":{}}}' >/dev/null
mcp '{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"sell_resource","arguments":{"resource":"food","quantity":5000}}}' >/dev/null
FEED=$(studio_b --player alice --expect 200 GET '/v1/studio/activity?limit=5'); echo "$FEED"
expect "$FEED" '"tool":"get_city_state"'
expect "$FEED" '"code":"invalid_arguments"'
ADMIN_FEED=$(admin --player ci --expect 200 GET '/v1/studio/admin/activity?limit=3'); echo "$ADMIN_FEED"
expect "$ADMIN_FEED" '"events"'

step "9. Behind a proxy the limiter must see the real client"
CODES=$(for _ in $(seq 1 10); do
  curl -s -o /dev/null -w '%{http_code} ' -H 'X-Forwarded-For: 203.0.113.7' -H 'Content-Type: application/json' \
    -d '{"client_name":"x","redirect_uris":["http://127.0.0.1/cb"]}' http://127.0.0.1:$PORT/register
done); echo "$CODES"
[ "$CODES" = "201 201 201 201 201 201 201 201 201 201 " ] || fail "ten registrations from one address must all pass"
CODE=$(curl -s -o /dev/null -w '%{http_code}' -H 'X-Forwarded-For: 203.0.113.7' -H 'Content-Type: application/json' \
  -d '{"client_name":"x","redirect_uris":["http://127.0.0.1/cb"]}' http://127.0.0.1:$PORT/register)
echo "11th registration from 203.0.113.7: HTTP $CODE"
[ "$CODE" = 429 ] || fail "want 429, got $CODE"
CODE=$(curl -s -o /dev/null -w '%{http_code}' -H 'X-Forwarded-For: 203.0.113.8' -H 'Content-Type: application/json' \
  -d '{"client_name":"x","redirect_uris":["http://127.0.0.1/cb"]}' http://127.0.0.1:$PORT/register)
echo "first registration from 203.0.113.8: HTTP $CODE"
[ "$CODE" = 201 ] || fail "want 201, got $CODE"

step "10. Redis goes away: the gateway fails closed and says it is not ready"
dc stop redis
RES=$(mcp '{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"get_city_state","arguments":{}}}'); echo "$RES"
expect "$RES" 'policy_unavailable'
CODE=$(curl -s -o /dev/null -w '%{http_code}' http://127.0.0.1:$OPS/readyz); echo "readyz: HTTP $CODE"
[ "$CODE" = 503 ] || fail "readyz answered $CODE, want 503"
curl -s http://127.0.0.1:$OPS/readyz; echo
dc start redis
for _ in $(seq 1 30); do curl -sf http://127.0.0.1:$OPS/readyz >/dev/null && break; sleep 1; done
RES=$(mcp '{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"get_city_state","arguments":{}}}')
expect "$RES" '\"ok\":true'

step "11. Metrics now show the calls"
METRICS=$(curl -s http://127.0.0.1:$OPS/metrics | grep -E '^gateway_calls_total' | head -n 5); echo "$METRICS"
expect "$METRICS" 'gateway_calls_total'

printf '\nPASS: tutorial 06\n'
