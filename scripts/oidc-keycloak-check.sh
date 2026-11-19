#!/usr/bin/env bash
# Checks the gateway's OIDC sign-in against Keycloak (a second provider after Dex in tutorial 8).
# Run from the repository root: bash scripts/oidc-keycloak-check.sh
# Needs Docker (Compose v2), curl and openssl. It starts its own stack and removes it on exit.
set -euo pipefail

step() { printf '\n==> %s\n' "$*"; }
fail() { printf 'FAIL: %s\n' "$*" >&2; exit 1; }
expect() { case "$1" in *"$2"*) printf 'ok: found %s\n' "$2" ;; *) fail "expected to find: $2"$'\n'"got: ${1:0:400}" ;; esac; }
header() { tr -d '\r' | awk -F': ' -v n="$1" 'tolower($1)==tolower(n){sub(/^[^:]*: /,""); print; exit}'; }

export KC_GW_PORT=18109 KC_OPS_PORT=19109
GW=http://127.0.0.1:$KC_GW_PORT
KC=http://127.0.0.1:15557/realms/agentgw
dc() { docker compose --progress quiet -p agentgw-kc -f docker-compose.yml -f examples/oidc-keycloak/compose.yml "$@"; }
JAR=$(mktemp)
trap 'dc down -v --remove-orphans >/dev/null 2>&1 || true; rm -f "$JAR"' EXIT

step "1. Start Keycloak (imports the realm) and the gateway"
dc down -v --remove-orphans >/dev/null 2>&1 || true
dc up -d --build keycloak gateway
for _ in $(seq 1 180); do curl -sf http://127.0.0.1:$KC_OPS_PORT/readyz >/dev/null && break; sleep 2; done
curl -s http://127.0.0.1:$KC_OPS_PORT/readyz | grep -q ready || { dc logs --tail 40 gateway keycloak; fail "gateway is not ready"; }
LOG=$(dc logs gateway 2>&1 | grep 'oidc sign-in enabled' | tail -n 1)
expect "$LOG" "\"issuer\":\"$KC\""

step "2. An agent registers and starts an authorization request (PKCE)"
RESOURCE=$(curl -s $GW/.well-known/oauth-protected-resource/mcp | sed 's/.*"resource":"\([^"]*\)".*/\1/')
CLIENT_ID=$(curl -s -X POST $GW/register -H 'Content-Type: application/json' \
  -d '{"client_name":"keycloak check","redirect_uris":["http://127.0.0.1/callback"]}' | sed 's/.*"client_id":"\([^"]*\)".*/\1/')
[ -n "$CLIENT_ID" ] || fail "no client id"
VERIFIER=$(openssl rand -base64 48 | tr '+/' '-_' | tr -d '=\n' | cut -c1-64)
CHALLENGE=$(printf %s "$VERIFIER" | openssl dgst -sha256 -binary | openssl base64 -A | tr '+/' '-_' | tr -d '=')
RES_ENC=$(printf %s "$RESOURCE" | sed 's#:#%3A#g; s#/#%2F#g')
QS="client_id=$CLIENT_ID&redirect_uri=http%3A%2F%2F127.0.0.1%2Fcallback&response_type=code&code_challenge=$CHALLENGE&code_challenge_method=S256&state=kc&resource=$RES_ENC"

step "3. The gateway sends the player to Keycloak"
START=$(curl -s -i -c "$JAR" -b "$JAR" -X POST $GW/authorize/oidc -d "$QS&scope_choice=coach")
LOC=$(echo "$START" | header location)
expect "$LOC" "$KC/protocol/openid-connect/auth?"
expect "$LOC" 'code_challenge_method=S256'

step "4. Keycloak shows its login form"
R=$(curl -s -i -c "$JAR" -b "$JAR" "$LOC")
expect "$R" 'name="username"'
FORM=$(echo "$R" | tr -d '\r' | grep -o 'action="[^"]*"' | head -n 1 | sed 's/action="//; s/"$//; s/&amp;/\&/g')
[ -n "$FORM" ] || fail "no login form action"

step "5. A wrong password is refused"
BAD=$(curl -s -i -c "$JAR" -b "$JAR" -X POST "$FORM" -d 'username=player&password=wrong')
echo "$BAD" | head -n 1
[ -z "$(echo "$BAD" | header location)" ] || fail "a wrong password must not redirect"
# The failed attempt returns a fresh form with a new action URL.
FORM=$(echo "$BAD" | tr -d '\r' | grep -o 'action="[^"]*"' | head -n 1 | sed 's/action="//; s/"$//; s/&amp;/\&/g')

step "6. The right password sends the browser back to the gateway with a code"
GOOD=$(curl -s -i -c "$JAR" -b "$JAR" -X POST "$FORM" -d 'username=player&password=password')
CALLBACK=$(echo "$GOOD" | header location)
expect "$CALLBACK" "$GW/authorize/oidc/callback?"

step "7. The gateway checks the ID token and sends the agent back with its own code"
DONE=$(curl -s -i -c "$JAR" -b "$JAR" "$CALLBACK")
BACK=$(echo "$DONE" | header location)
expect "$BACK" "http://127.0.0.1/callback?"
expect "$BACK" 'state=kc'
CODE=$(echo "$BACK" | sed 's/.*[?&]code=\([^&]*\).*/\1/')

step "8. The agent trades its code for a token, and the grant belongs to the Keycloak user"
TOKENS=$(curl -s -X POST $GW/token -d "grant_type=authorization_code&code=$CODE&client_id=$CLIENT_ID&redirect_uri=http%3A%2F%2F127.0.0.1%2Fcallback&code_verifier=$VERIFIER&resource=$RES_ENC")
expect "$TOKENS" '"scope":"coach"'
dc build --quiet dev
dc run --rm -T dev go build -o bin/ ./examples/studiocall
GRANTS=$(dc run --rm -T dev bin/studiocall --url http://keycloak:8080 --player player --expect 200 GET /v1/studio/grants)
expect "$GRANTS" '"client_name":"keycloak check"'

printf '\nPASS: keycloak\n'
