#!/usr/bin/env bash
# Verifies tutorial 08 (docs/tutorials/08-sign-in-with-your-identity-provider.md). Run from the
# repository root:
#   bash docs/tutorials/08-sign-in-with-your-identity-provider.sh
# Needs only Docker (Compose v2), curl and openssl. It starts its own stack and removes it on exit.
set -euo pipefail

step() { printf '\n==> %s\n' "$*"; }
fail() { printf 'FAIL: %s\n' "$*" >&2; exit 1; }
expect() { # expect "text" "needle" : the text must contain the needle
  case "$1" in *"$2"*) printf 'ok: found %s\n' "$2" ;; *) fail "expected to find: $2"$'\n'"got: $1" ;; esac
}
header() { # header NAME: print the value of a response header from "curl -i" output on stdin
  tr -d '\r' | awk -F': ' -v n="$1" 'tolower($1)==tolower(n){sub(/^[^:]*: /,""); print; exit}'
}

# --- the commands below are the ones the tutorial shows ---------------------------------

export TUTORIAL_PORT=18108 TUTORIAL_OPS_PORT=19108  # keeps the script off the ports of a demo you already run
PORT=$TUTORIAL_PORT OPS=$TUTORIAL_OPS_PORT
GW=http://127.0.0.1:$PORT            # the address players and agents use (--public-url)
DEX=http://127.0.0.1:15556/dex       # the identity provider's issuer
dc() { docker compose --progress quiet -p agentgw-t08 -f docker-compose.yml -f examples/tutorials/compose.oidc.yml "$@"; }
JAR=$(mktemp)
trap 'dc down -v --remove-orphans >/dev/null 2>&1 || true; rm -f "$JAR"' EXIT

step "1. Start the identity provider (Dex) and the gateway with OIDC sign-in"
dc down -v --remove-orphans >/dev/null 2>&1 || true
dc up -d --build dex gateway
for _ in $(seq 1 90); do curl -sf http://127.0.0.1:$OPS/readyz >/dev/null && break; sleep 1; done
curl -s http://127.0.0.1:$OPS/readyz | tee /dev/stderr | grep ready >/dev/null || fail "gateway is not ready"
LOG=$(dc logs gateway 2>&1 | grep 'oidc sign-in enabled' | tail -n 1 | sed 's/^[^|]*| *//')
echo "$LOG"
expect "$LOG" '"issuer":"http://127.0.0.1:15556/dex"'
expect "$LOG" '"player_claim":"name"'
case "$LOG" in *dev-oidc-client-secret*|*dev-only-session-secret*) fail "a secret was logged" ;; esac

step "2. An agent registers and starts an authorization request (PKCE)"
RESOURCE=$(curl -s $GW/.well-known/oauth-protected-resource/mcp | sed 's/.*"resource":"\([^"]*\)".*/\1/')
CLIENT_ID=$(curl -s -X POST $GW/register -H 'Content-Type: application/json' \
  -d '{"client_name":"oidc tutorial","redirect_uris":["http://127.0.0.1/callback"]}' | sed 's/.*"client_id":"\([^"]*\)".*/\1/')
[ -n "$CLIENT_ID" ] || fail "no client id"
VERIFIER=$(openssl rand -base64 48 | tr '+/' '-_' | tr -d '=\n' | cut -c1-64)
CHALLENGE=$(printf %s "$VERIFIER" | openssl dgst -sha256 -binary | openssl base64 -A | tr '+/' '-_' | tr -d '=')
RES_ENC=$(printf %s "$RESOURCE" | sed 's#:#%3A#g; s#/#%2F#g')
QS="client_id=$CLIENT_ID&redirect_uri=http%3A%2F%2F127.0.0.1%2Fcallback&response_type=code&code_challenge=$CHALLENGE&code_challenge_method=S256&state=tutorial&resource=$RES_ENC"

step "3. The consent page now offers a sign-in with the game account"
PAGE=$(curl -s "$GW/authorize?$QS")
expect "$PAGE" 'Sign in with your game account'
expect "$PAGE" 'formaction="/authorize/oidc"'

step "4. The player presses the button: the gateway sends them to the identity provider"
START=$(curl -s -i -c "$JAR" -b "$JAR" -X POST $GW/authorize/oidc -d "$QS&scope_choice=coach")
echo "$START" | head -n 1
LOC=$(echo "$START" | header location)
echo "${LOC:0:120}..."
expect "$LOC" "$DEX/auth?"
expect "$LOC" 'code_challenge_method=S256'
expect "$LOC" 'nonce='
expect "$LOC" 'state='
expect "$START" 'agw_oidc='

step "5. The identity provider shows its own login page (here: Dex's password form)"
while :; do
  R=$(curl -s -i -c "$JAR" -b "$JAR" "$LOC")
  NEXT=$(echo "$R" | header location)
  [ -n "$NEXT" ] || break
  case "$NEXT" in /*) LOC=http://127.0.0.1:15556$NEXT ;; *) LOC=$NEXT ;; esac
done
expect "$R" 'name="login"'
expect "$R" 'name="password"'
FORM=$(echo "$R" | tr -d '\r' | grep -o 'action="[^"]*"' | head -n 1 | sed 's/action="//; s/"$//; s/&amp;/\&/g')
echo "login form posts to: $FORM"

step "6. A wrong password is refused by the identity provider, nothing reaches the gateway"
BAD=$(curl -s -i -c "$JAR" -b "$JAR" -X POST "http://127.0.0.1:15556$FORM" -d 'login=player@example.com&password=wrong')
echo "$BAD" | head -n 1
expect "$BAD" '401'
[ -z "$(echo "$BAD" | header location)" ] || fail "a wrong password must not redirect"

step "7. The right password sends the browser back to the gateway with a code"
GOOD=$(curl -s -i -c "$JAR" -b "$JAR" -X POST "http://127.0.0.1:15556$FORM" -d 'login=player@example.com&password=password')
echo "$GOOD" | head -n 1
CALLBACK=$(echo "$GOOD" | header location)
echo "${CALLBACK:0:110}..."
expect "$CALLBACK" "$GW/authorize/oidc/callback?code="

step "8. The gateway checks the ID token and sends the agent back with its own code"
DONE=$(curl -s -i -c "$JAR" -b "$JAR" "$CALLBACK")
echo "$DONE" | head -n 1
BACK=$(echo "$DONE" | header location)
echo "${BACK:0:110}..."
expect "$BACK" "http://127.0.0.1/callback?"
expect "$BACK" 'state=tutorial'
CODE=$(echo "$BACK" | sed 's/.*[?&]code=\([^&]*\).*/\1/')
[ -n "$CODE" ] && [ "$CODE" != "$BACK" ] || fail "no authorization code in the redirect"

step "9. Replaying the same callback does nothing"
REPLAY=$(curl -s -i -c "$JAR" -b "$JAR" "$CALLBACK")
echo "$REPLAY" | head -n 1
expect "$REPLAY" '400'
[ -z "$(echo "$REPLAY" | header location)" ] || fail "a replayed callback must not redirect"

step "10. A callback with no sign-in session of its own is refused too"
NOSESSION=$(curl -s -i "$GW/authorize/oidc/callback?code=abc&state=xyz")
echo "$NOSESSION" | head -n 1
expect "$NOSESSION" '400'

step "11. The agent trades its code for a token and calls the gateway"
TOKENS=$(curl -s -X POST $GW/token -d "grant_type=authorization_code&code=$CODE&client_id=$CLIENT_ID&redirect_uri=http%3A%2F%2F127.0.0.1%2Fcallback&code_verifier=$VERIFIER&resource=$RES_ENC")
echo "${TOKENS:0:120}..."
expect "$TOKENS" '"scope":"coach"'
ACCESS=$(echo "$TOKENS" | sed 's/.*"access_token":"\([^"]*\)".*/\1/')
TOOLS=$(curl -s -H "Authorization: Bearer $ACCESS" -H 'Content-Type: application/json' \
  -H 'Accept: application/json, text/event-stream' -H 'MCP-Protocol-Version: 2025-06-18' \
  -d '{"jsonrpc":"2.0","id":1,"method":"tools/list"}' $GW/mcp | grep -o '"name":"[a-z_]*"')
echo "$TOOLS"
expect "$TOOLS" '"name":"get_city_state"'

step "12. The grant belongs to the player the identity provider vouched for"
dc build --quiet dev
dc run --rm -T dev go build -o bin/ ./examples/studiocall
GRANTS=$(dc run --rm -T dev bin/studiocall --url http://dex:8080 --player player --expect 200 GET /v1/studio/grants)
echo "$GRANTS"
expect "$GRANTS" '"scope":"coach"'
expect "$GRANTS" '"client_name":"oidc tutorial"'
OTHER=$(dc run --rm -T dev bin/studiocall --url http://dex:8080 --player alice --expect 200 GET /v1/studio/grants)
case "$OTHER" in *'"client_name":"oidc tutorial"'*) fail "another player sees this grant" ;; *) echo "ok: alice has no such grant" ;; esac

step "13. Refusing at the identity provider ends the sign-in with access_denied"
VERIFIER2=$(openssl rand -base64 48 | tr '+/' '-_' | tr -d '=\n' | cut -c1-64)
CHALLENGE2=$(printf %s "$VERIFIER2" | openssl dgst -sha256 -binary | openssl base64 -A | tr '+/' '-_' | tr -d '=')
QS2="client_id=$CLIENT_ID&redirect_uri=http%3A%2F%2F127.0.0.1%2Fcallback&response_type=code&code_challenge=$CHALLENGE2&code_challenge_method=S256&state=second&resource=$RES_ENC"
JAR2=$(mktemp); trap 'dc down -v --remove-orphans >/dev/null 2>&1 || true; rm -f "$JAR" "$JAR2"' EXIT
START2=$(curl -s -i -c "$JAR2" -b "$JAR2" -X POST $GW/authorize/oidc -d "$QS2&scope_choice=coach")
STATE2=$(echo "$START2" | header location | sed 's/.*[?&]state=\([^&]*\).*/\1/')
DENIED=$(curl -s -i -c "$JAR2" -b "$JAR2" "$GW/authorize/oidc/callback?error=access_denied&state=$STATE2")
echo "$DENIED" | head -n 1
DLOC=$(echo "$DENIED" | header location)
echo "$DLOC"
expect "$DLOC" 'error=access_denied'
expect "$DLOC" 'state=second'

printf '\nPASS: tutorial 08\n'
