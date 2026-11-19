#!/usr/bin/env bash
# End to end: the C# console game (examples/unity-relay-client/sample) plays through the REAL
# gateway. An agent calls a tool over MCP, the gateway sends it down the game's WebSocket, the
# game answers. Run from the repository root:
#   bash examples/unity-relay-client/e2e.sh
# Needs Docker (Compose v2) and curl. Starts its own stack and removes it on exit.
set -euo pipefail

step() { printf '\n==> %s\n' "$*"; }
fail() { printf 'FAIL: %s\n' "$*" >&2; exit 1; }
expect() { case "$1" in *"$2"*) printf 'ok: found %s\n' "$2" ;; *) fail "expected to find: $2"$'\n'"got: $1" ;; esac; }

export TUTORIAL_PORT=18110 TUTORIAL_OPS_PORT=19110
PORT=$TUTORIAL_PORT OPS=$TUTORIAL_OPS_PORT
OUT="$PWD/.unity-out"
dc() { docker compose --progress quiet -p agentgw-unity -f docker-compose.yml -f examples/tutorials/compose.ports.yml -f examples/tutorials/compose.oauth.yml -f examples/unity-relay-client/e2e/compose.yml "$@"; }
trap 'dc down -v --remove-orphans >/dev/null 2>&1 || true; rm -rf "$OUT"' EXIT

step "1. Start the gateway with the relay on"
dc down -v --remove-orphans >/dev/null 2>&1 || true
dc up -d --build gateway
for _ in $(seq 1 60); do curl -sf http://127.0.0.1:$OPS/readyz >/dev/null && break; sleep 1; done
curl -s http://127.0.0.1:$OPS/readyz | grep -q ready || fail "gateway is not ready"

step "2. Build the helper programs and publish the console game"
dc build --quiet dev
dc run --rm -T dev go build -o bin/ ./examples/studiocall ./examples/oauthclient
studio() { dc run --rm -T dev bin/studiocall "$@"; }
oauth() { dc run --rm -T dev bin/oauthclient "$@"; }
mkdir -p "$OUT"
dc run --rm -T -v "$OUT:/out" --entrypoint sh unity-client-sample -c 'cp -r /s /tmp/s && cd /tmp/s/sample && dotnet publish -c Release -o /out >/dev/null'
[ -f "$OUT/ConsoleGame.dll" ] || fail "the console game did not build"

step "3. The studio backend mints a relay ticket for alice and the game connects with it"
TICKET=$(studio --player alice --expect 201 POST /v1/studio/relay/tickets | sed 's/.*"ticket":"\([^"]*\)".*/\1/')
[ -n "$TICKET" ] || fail "no relay ticket"
GAME=$(dc run -d -T -v "$OUT:/out" -e RELAY_URL=ws://gateway:8080/v1/relay/connect -e RELAY_TICKET="$TICKET" --entrypoint dotnet unity-client-sample /out/ConsoleGame.dll)
for _ in $(seq 1 30); do docker logs "$GAME" 2>&1 | grep -q "connected:" && break; sleep 1; done
docker logs "$GAME" 2>&1 | tee /dev/stderr | grep -q "connected: gateway protocol 1.1" || fail "the game did not connect"
curl -s http://127.0.0.1:$OPS/metrics | grep '^relay_connections ' | tee /dev/stderr | grep -q ' 1$' || fail "relay_connections is not 1"

step "4. alice links an agent as operator and calls the game's tools"
LT=$(studio --player alice --expect 201 POST /v1/studio/link-tickets 2>/dev/null | sed 's/.*"ticket":"\([^"]*\)".*/\1/')
ACCESS=$(oauth --ticket "$LT" --scope operator | sed 's/.*"access_token":"\([^"]*\)".*/\1/')
mcp() {
  curl -s -H "Authorization: Bearer $ACCESS" -H 'Content-Type: application/json' \
    -H 'Accept: application/json, text/event-stream' -H 'MCP-Protocol-Version: 2025-06-18' \
    -d "{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"tools/call\",\"params\":{\"name\":\"$1\",\"arguments\":{}}}" http://127.0.0.1:$PORT/mcp
  echo
}
OUTP=$(mcp get_state); echo "$OUTP"; expect "$OUTP" 'wood'
OUTP=$(mcp build_farm); echo "$OUTP"; expect "$OUTP" 'farms'
OUTP=$(mcp build_farm); echo "$OUTP"; expect "$OUTP" 'farms'

step "5. The game says no: the third farm needs wood it does not have"
OUTP=$(mcp build_farm); echo "$OUTP"
expect "$OUTP" 'not_enough_wood'

step "6. The game leaves with a bye (docker stop sends SIGTERM, the sample says goodbye on ctrl-c)"
docker kill --signal INT "$GAME" >/dev/null
for _ in $(seq 1 20); do [ "$(docker inspect -f '{{.State.Running}}' "$GAME")" = false ] && break; sleep 1; done
docker logs "$GAME" 2>&1 | tail -3
OUTP=$(mcp get_state); echo "$OUTP"
expect "$OUTP" 'client_offline'
curl -s http://127.0.0.1:$OPS/metrics | grep -E '^relay_(calls_total|socket_closes_total)' | tee /dev/stderr | grep -q 'reason="bye"' || fail "the bye was not counted"

printf '\nPASS: unity relay client against the real gateway\n'
