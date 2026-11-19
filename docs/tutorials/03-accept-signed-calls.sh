#!/usr/bin/env bash
# Runs the commands of 03-accept-signed-calls.md. Run from the repository root.
# Needs only Docker and curl. Exits nonzero on any surprise.
set -euo pipefail

DOC=docs/tutorials/03-accept-signed-calls.md

step() { printf '\n== %s\n' "$*"; }
fail() { printf 'FAIL: %s\n' "$*" >&2; exit 1; }
expect() { case "$2" in *"$3"*) printf 'ok: %s\n' "$1" ;; *) fail "$1: wanted [$3] in [$2]" ;; esac; }
PROJECTS=()
cleanup() {
  local p
  for p in ${PROJECTS[@]+"${PROJECTS[@]}"}; do
    docker compose -p "$p" -f "examples/pet-backend-${p#agentgw-t03-}/compose.yml" down -v >/dev/null 2>&1 || true
  done
}
trap cleanup EXIT

step "1. The vector in the tutorial and in the protocol equals examples/signing/vector.json"
sig=$(sed -n 's/.*"signature": "\([0-9a-f]*\)".*/\1/p' examples/signing/vector.json)
[ -n "$sig" ] || fail "no signature in vector.json"
for f in "$DOC" docs/PROTOCOL.md; do
  grep -q "$sig" "$f" || fail "$f does not contain the vector signature"
  echo "ok: $f carries the vector"
done

step "2. Every excerpt in the tutorial is quoted from its source file"
# An excerpt is the fenced block that follows a `<!-- from: path -->` line. Each of its lines
# must appear (ignoring leading whitespace) in that file.
n=0
while IFS=$'\t' read -r file line; do
  grep -qF -- "$line" "$file" || fail "line not found in $file: $line"
  n=$((n + 1))
done < <(awk '
  /^<!-- from: .* -->$/ { file = $3; want = 1; next }
  want && /^```/ { if (inblock) { inblock = 0; want = 0 } else { inblock = 1 } ; next }
  inblock { l = $0; sub(/^[ \t]+/, "", l); if (l != "") print file "\t" l }' "$DOC")
[ "$n" -gt 20 ] || fail "found only $n excerpt lines; the extraction is broken"
echo "ok: $n excerpt lines match"

step "3. The four language examples pass their tests"
for lang in go python typescript csharp; do
  out=$(docker compose run --rm "sig-$lang" 2>&1) || { echo "$out" | tail -30; fail "sig-$lang failed"; }
  echo "ok: sig-$lang"
done

backend_checks() { # backend_checks <go|python> <gateway port> <backend port>
  local impl=$1 gwp=$2 pp=$3 proj="agentgw-t03-$1"
  PROJECTS+=("$proj")
  export GW_PORT=$gwp PET_PORT=$pp
  step "4. ($impl) gateway in front of the pet shop"
  docker compose -p "$proj" -f "examples/pet-backend-$impl/compose.yml" up --build -d >/dev/null 2>&1
  local gw=http://127.0.0.1:$gwp be=http://127.0.0.1:$pp
  for _ in $(seq 1 60); do curl -fs "$gw/healthz" >/dev/null 2>&1 && break; sleep 1; done
  curl -fs "$gw/healthz" >/dev/null || fail "$impl: gateway did not come up"
  for _ in $(seq 1 60); do curl -s -o /dev/null "$be/api/v1/shop" 2>/dev/null && break; sleep 1; done
  curl -s -o /dev/null "$be/api/v1/shop" || fail "$impl: backend did not come up"

  local out code
  out=$(curl -s -X POST -H 'X-Dev-Player: p1' -d '{"arguments":{"pet":"rex","food":"fish","amount":2}}' "$gw/v1/tools/feed_pet/call")
  expect "$impl: a signed call reaches the backend" "$out" '"coins":96'

  step "5. ($impl) forged requests are refused"
  code=$(curl -s -o /dev/null -w '%{http_code}' -X POST -d '{"pet":"rex","food":"fish","amount":9}' "$be/api/v1/pets/feed")
  [ "$code" = 401 ] || fail "$impl: unsigned request got $code"
  echo "ok: $impl unsigned -> 401"
  code=$(curl -s -o /dev/null -w '%{http_code}' -X POST -H 'X-AG-Player: p1' -d '{"pet":"rex","food":"fish","amount":9}' "$be/api/v1/pets/feed")
  [ "$code" = 401 ] || fail "$impl: header-only request got $code"
  echo "ok: $impl header only -> 401"

  step "6. ($impl) a replayed signed request runs once"
  local body='{"pet":"rex","food":"fish","amount":1}' headers
  headers=$(docker run --rm -v "$PWD/examples/signing/python:/sig:ro" -v "$PWD/docs/tutorials:/t:ro" \
    python:3.13-slim python -B /t/sign-request.py POST /api/v1/pets/feed p1 "replay-$impl" "$body")
  local args=() h
  while IFS= read -r h; do args+=(-H "$h"); done <<< "$headers"
  local first second
  first=$(curl -si -X POST "${args[@]}" -d "$body" "$be/api/v1/pets/feed")
  second=$(curl -si -X POST "${args[@]}" -d "$body" "$be/api/v1/pets/feed")
  expect "$impl: first call succeeded" "$first" '"coins":94'
  expect "$impl: the repeat has the same body" "$second" '"coins":94'
  expect "$impl: the repeat is marked" "$(printf '%s' "$second" | tr -d '\r' | tr 'A-Z' 'a-z')" 'x-idempotent-replay: true'
  case "$(printf '%s' "$first" | tr 'A-Z' 'a-z')" in *x-idempotent-replay*) fail "$impl: the first call was marked as a replay" ;; esac
  out=$(curl -s -X POST -H 'X-Dev-Player: p1' -d '{}' "$gw/v1/tools/get_shop/call")
  expect "$impl: coins dropped once, not twice" "$out" '"coins":94'
  # The same signature on a different body is refused.
  code=$(curl -s -o /dev/null -w '%{http_code}' -X POST "${args[@]}" -d '{"pet":"rex","food":"fish","amount":5}' "$be/api/v1/pets/feed")
  [ "$code" = 401 ] || fail "$impl: tampered body got $code"
  echo "ok: $impl tampered body -> 401"
  docker compose -p "$proj" -f "examples/pet-backend-$impl/compose.yml" down -v >/dev/null 2>&1
}

backend_checks go 18103 19103
backend_checks python 18104 19104

printf '\nPASSED: 03-accept-signed-calls\n'
