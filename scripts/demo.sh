#!/usr/bin/env bash
# A paced terminal demo for recording (about 60 seconds). It types each command, runs it for real
# against the compose demo, and shows the gateway enforcing its policy.
#
#   scripts/demo.sh              # paced, for recording (see docs/DEMO.md)
#   DEMO_FAST=1 scripts/demo.sh  # no pauses, to check that the script still works
#
# Needs Docker with Compose v2 and curl. Uses 127.0.0.1:8080, so stop anything else on that port.
set -euo pipefail
cd "$(dirname "$0")/.."

GW=http://127.0.0.1:8080
FAST="${DEMO_FAST:-}"
DC=(docker compose -p agentgw-demo)
trap '"${DC[@]}" down -v >/dev/null 2>&1 || true' EXIT

pause() { [ -n "$FAST" ] || sleep "$1"; }
say()   { printf '\n\033[1;36m# %s\033[0m\n' "$*"; pause 1.5; }
type_() { # print a command one character at a time, then run it
  printf '\033[1;32m$\033[0m '
  if [ -z "$FAST" ]; then
    for ((i = 0; i < ${#1}; i++)); do printf '%s' "${1:i:1}"; sleep 0.02; done
  else printf '%s' "$1"; fi
  printf '\n'; pause 0.5
  eval "$1"; printf '\n'; pause 2.5
}

"${DC[@]}" up --build -d >/dev/null 2>&1
for _ in $(seq 1 90); do curl -fs "$GW/healthz" >/dev/null 2>&1 && break; sleep 1; done
curl -fs "$GW/healthz" >/dev/null || { echo "gateway did not come up" >&2; exit 1; }
clear 2>/dev/null || true

say "agentgw: a player's AI agent plays a game, and the studio keeps control"
say "The studio described its game actions in a manifest. Each one is a tool."
type_ "curl -s $GW/v1/tools | python3 -c 'import sys,json; [print(t[\"name\"]) for t in json.load(sys.stdin)[\"tools\"]]'"

say "The agent reads the player's state."
type_ "curl -s -X POST -H 'X-Dev-Player: p1' -d '{}' $GW/v1/tools/get_city_state/call"

say "It sells 50 food. The gateway signs the request to the backend."
type_ "curl -s -X POST -H 'X-Dev-Player: p1' -d '{\"arguments\":{\"resource\":\"food\",\"quantity\":50}}' $GW/v1/tools/sell_resource/call"

say "It asks for far too much. The daily cap stops it, and says what is left."
type_ "curl -s -X POST -H 'X-Dev-Player: p1' -d '{\"arguments\":{\"resource\":\"water\",\"quantity\":400}}' $GW/v1/tools/sell_resource/call"

say "Repeating an action too soon hits the cooldown. The agent can read the code and wait."
type_ "curl -s -X POST -H 'X-Dev-Player: p1' -d '{}' $GW/v1/tools/collect_taxes/call >/dev/null; curl -s -X POST -H 'X-Dev-Player: p1' -d '{}' $GW/v1/tools/collect_taxes/call"

say "Every attempt, allowed or refused, is in the audit log."
say "Try it: git clone https://github.com/Moustafa-Elgammal/agentgw"
pause 3
