#!/usr/bin/env bash
# Verifies tutorial 10 (docs/tutorials/10-connect-a-unity-game-through-the-relay.md). Run from the
# repository root:
#   bash docs/tutorials/10-connect-a-unity-game-through-the-relay.sh
# Needs only Docker (Compose v2) and curl. It starts its own stack and
# removes it on exit. The "game" is the C# console sample in examples/unity-relay-client: it uses
# the same RelayClient a Unity game would, without the editor.
set -euo pipefail

step() { printf '\n==> %s\n' "$*"; }
fail() { printf 'FAIL: %s\n' "$*" >&2; exit 1; }
expect() { # expect "text" "needle" : the text must contain the needle
  case "$1" in *"$2"*) printf 'ok: found %s\n' "$2" ;; *) fail "expected to find: $2"$'\n'"got: $1" ;; esac
}
wait_for_log() { # wait_for_log CONTAINER TEXT: wait up to 40 s for TEXT in the container's output
  for _ in $(seq 1 40); do docker logs "$1" 2>&1 | grep -q "$2" && return 0; sleep 1; done
  return 1
}

# --- the commands below are the ones the tutorial shows ---------------------------------

export TUTORIAL_PORT=18110 TUTORIAL_OPS_PORT=19110  # keeps the script off the ports of a demo you already run
PORT=$TUTORIAL_PORT OPS=$TUTORIAL_OPS_PORT
OUT="$PWD/.relay-tutorial-out"
dc() { docker compose --progress quiet -p agentgw-t10 -f docker-compose.yml -f examples/tutorials/compose.ports.yml -f examples/tutorials/compose.oauth.yml -f examples/tutorials/compose.relay.yml "$@"; }
trap 'dc down -v --remove-orphans >/dev/null 2>&1 || true; rm -rf "$OUT"' EXIT

step "1. Start the gateway with the relay turned on"
dc down -v --remove-orphans >/dev/null 2>&1 || true
dc up -d --build gateway
for _ in $(seq 1 60); do curl -sf http://127.0.0.1:$OPS/readyz >/dev/null && break; sleep 1; done
curl -s http://127.0.0.1:$OPS/readyz | tee /dev/stderr | grep ready >/dev/null || fail "gateway is not ready"
MANIFEST=$(cat examples/tutorials/relay-game/manifest.yaml)
expect "$MANIFEST" 'backend: relay'

step "2. Build the helper programs and the console game"
dc build --quiet dev
dc run --rm -T dev go build -o bin/ ./examples/studiocall ./examples/oauthclient
studio() { dc run --rm -T dev bin/studiocall "$@"; }
oauth() { dc run --rm -T dev bin/oauthclient "$@"; }
mkdir -p "$OUT"
dc run --rm -T -v "$OUT:/out" --entrypoint sh unity-client-sample -c 'cp -r /s /tmp/s && cd /tmp/s/sample && dotnet publish -c Release -o /out >/dev/null'
[ -f "$OUT/ConsoleGame.dll" ] || fail "the console game did not build"

step "3. Before any game connects, a call fails with client_offline"
LT=$(studio --player alice --expect 201 POST /v1/studio/link-tickets 2>/dev/null | sed 's/.*"ticket":"\([^"]*\)".*/\1/')
ACCESS=$(oauth --ticket "$LT" --scope operator | sed 's/.*"access_token":"\([^"]*\)".*/\1/')
[ -n "$ACCESS" ] || fail "no access token"
mcp() { # mcp TOOL: call a tool as alice's agent
  curl -s -H "Authorization: Bearer $ACCESS" -H 'Content-Type: application/json' \
    -H 'Accept: application/json, text/event-stream' -H 'MCP-Protocol-Version: 2025-06-18' \
    -d "{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"tools/call\",\"params\":{\"name\":\"$1\",\"arguments\":{}}}" http://127.0.0.1:$PORT/mcp
  echo
}
OFFLINE=$(mcp get_state); echo "$OFFLINE"
expect "$OFFLINE" 'client_offline'

step "4. Your backend mints a relay ticket for alice and the game connects with it"
TICKET=$(studio --player alice --expect 201 POST /v1/studio/relay/tickets | sed 's/.*"ticket":"\([^"]*\)".*/\1/')
[ -n "$TICKET" ] || fail "no relay ticket"
echo "ticket: ${TICKET:0:12}... (60 seconds, one player)"
game() { # game TICKET: start the console game as a background container and print its id
  dc run -d -T -v "$OUT:/out" -e RELAY_URL=ws://gateway:8080/v1/relay/connect -e RELAY_TICKET="$1" --entrypoint dotnet unity-client-sample /out/ConsoleGame.dll
}
GAME=$(game "$TICKET")
wait_for_log "$GAME" "connected:" || fail "the game did not connect"
docker logs "$GAME" 2>&1 | grep 'connected:' | tee /dev/stderr | grep -q "gateway protocol 1.1, up to 8 calls at once, 10000 ms per call" || fail "unexpected hello"
curl -s http://127.0.0.1:$OPS/metrics | grep '^relay_connections ' | tee /dev/stderr | grep -q ' 1$' || fail "relay_connections is not 1"

step "5. The agent's call reaches the game and the game's answer comes back"
OUTP=$(mcp get_state); echo "$OUTP"; expect "$OUTP" 'wood'
OUTP=$(mcp build_farm); echo "$OUTP"; expect "$OUTP" 'farms'
OUTP=$(mcp build_farm); echo "$OUTP"; expect "$OUTP" 'farms'

step "6. The game says no: the third farm needs wood it does not have"
OUTP=$(mcp build_farm); echo "$OUTP"
expect "$OUTP" 'not_enough_wood'
expect "$OUTP" 'a farm costs 5 wood'

step "7. A second connection for the same player replaces the first (close code 4000)"
TICKET2=$(studio --player alice --expect 201 POST /v1/studio/relay/tickets | sed 's/.*"ticket":"\([^"]*\)".*/\1/')
GAME2=$(game "$TICKET2")
wait_for_log "$GAME2" "connected:" || fail "the second game did not connect"
wait_for_log "$GAME" "closed with code 4000" || fail "the first game was not closed with 4000"
docker logs "$GAME" 2>&1 | grep -E 'closed with code 4000|reconnecting in'
docker kill "$GAME" >/dev/null   # a replaced client must not fight back: stop it
curl -s http://127.0.0.1:$OPS/metrics | grep '^relay_socket_closes_total' | tee /dev/stderr | grep -q 'reason="replaced"' || fail "the takeover was not counted"
OUTP=$(mcp get_state); echo "$OUTP"
expect "$OUTP" '"wood\":10'   # the second game has fresh state, so the call went to it

step "8. The game leaves with a bye, and the next call fails with client_offline"
docker kill --signal INT "$GAME2" >/dev/null
for _ in $(seq 1 20); do [ "$(docker inspect -f '{{.State.Running}}' "$GAME2")" = false ] && break; sleep 1; done
docker logs "$GAME2" 2>&1 | tail -n 3
OUTP=$(mcp get_state); echo "$OUTP"
expect "$OUTP" 'client_offline'
curl -s http://127.0.0.1:$OPS/metrics | grep -E '^relay_socket_closes_total' | tee /dev/stderr | grep -q 'reason="bye"' || fail "the bye was not counted"

printf '\nPASS: tutorial 10\n'
