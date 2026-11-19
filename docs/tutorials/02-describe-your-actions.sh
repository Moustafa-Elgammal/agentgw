#!/usr/bin/env bash
# Runs the commands of 02-describe-your-actions.md. Run from the repository root.
# The manifest and the broken manifest are extracted from the tutorial itself, so the page
# cannot drift from what is tested. Needs only Docker and curl.
set -euo pipefail

P=agentgw-t02
GW=http://127.0.0.1:18102
DOC=docs/tutorials/02-describe-your-actions.md
WORK=.tutorial-tmp/t02
DC=(docker compose -p "$P" -f examples/pet-backend-go/compose.yml)
export GW_PORT=18102

step() { printf '\n== %s\n' "$*"; }
fail() { printf 'FAIL: %s\n' "$*" >&2; exit 1; }
expect() { case "$2" in *"$3"*) printf 'ok: %s\n' "$1" ;; *) fail "$1: wanted [$3] in [$2]" ;; esac; }
cleanup() { "${DC[@]}" down -v >/dev/null 2>&1 || true; rm -rf .tutorial-tmp/t02; }
trap cleanup EXIT
mkdir -p "$WORK"

# block <fence-info> <nth>: print the nth fenced block whose opening line is exactly ```<info>
block() { awk -v info="$1" -v n="$2" '
  $0 == "```" info { c++; if (c == n) { on = 1; next } }
  on && $0 == "```" { exit }
  on { print }' "$DOC"; }

step "1. Extract the manifests from the tutorial"
block "yaml manifest" 1 > "$WORK/pets.yaml"
block "yaml" 1 > "$WORK/broken.yaml"
block "yaml" 2 > "$WORK/typo.yaml"
[ -s "$WORK/pets.yaml" ] && [ -s "$WORK/broken.yaml" ] && [ -s "$WORK/typo.yaml" ] || fail "could not extract the manifests"
# The file in the repository carries a comment header the page leaves out.
grep -v '^#' examples/pet-shop/manifest.yaml | diff -u - "$WORK/pets.yaml" || fail "examples/pet-shop/manifest.yaml differs from the tutorial"
echo "ok: the tutorial manifest equals examples/pet-shop/manifest.yaml"

step "2. Validate the good manifest"
out=$(docker compose run --rm dev go run ./cmd/gwctl validate "$WORK/pets.yaml" 2>&1 | grep -v Container)
expect "validates" "$out" "ok: 4 tools"
out=$(docker compose run --rm dev go run ./cmd/gwctl tools "$WORK/pets.yaml" 2>&1 | grep -v Container)
expect "lists feed_pet as safe" "$out" "safe       feed_pet"

step "3. The broken manifest is rejected with every problem at once"
if out=$(docker compose run --rm dev go run ./cmd/gwctl validate "$WORK/broken.yaml" 2>&1); then fail "the broken manifest was accepted"; fi
for want in 'name must match' 'description is required' 'unknown tier "admin"' 'http.url must be a path starting with /' \
            'url placeholder {name} has no matching param' 'read tools cannot require confirmation'; do
  expect "reports: $want" "$out" "$want"
done

if out=$(docker compose run --rm dev go run ./cmd/gwctl validate "$WORK/typo.yaml" 2>&1); then fail "the typo manifest was accepted"; fi
expect "typo reports the line" "$out" 'unknown field "tyer"'
expect "typo shows the position" "$out" '[6:5]'

step "4. Run the gateway in front of the Go pet shop"
"${DC[@]}" up --build -d >/dev/null
for _ in $(seq 1 60); do curl -fs "$GW/healthz" >/dev/null 2>&1 && break; sleep 1; done
curl -fs "$GW/healthz" >/dev/null || fail "gateway did not come up"
call() { curl -s -X POST -H 'X-Dev-Player: p1' -d "$2" "$GW/v1/tools/$1/call"; }

out=$(call get_shop '{}');                                   expect "shop" "$out" '"coins":100'
out=$(call get_pet '{"arguments":{"name":"rex"}}');          expect "pet" "$out" '"hunger":80'
out=$(call feed_pet '{"arguments":{"pet":"rex","food":"kibble","amount":5}}')
expect "fed" "$out" '"hunger":30'; expect "paid" "$out" '"coins":95'

step "5. Push on the limits"
out=$(call feed_pet '{"arguments":{"pet":"rex","food":"pizza","amount":1}}')
expect "enum refused" "$out" '"code":"invalid_arguments"'; expect "enum message" "$out" 'choose one of [kibble fish]'
out=$(call feed_pet '{"arguments":{"pet":"rex","food":"kibble","amount":5}}'); expect "second feed" "$out" '"hunger":0'
out=$(call feed_pet '{"arguments":{"pet":"rex","food":"kibble","amount":1}}')
expect "daily cap" "$out" '"code":"daily_amount_cap"'; expect "cap message" "$out" '0 left today'
out=$(call get_pet '{"arguments":{"name":"zed"}}')
expect "backend refusal" "$out" '"code":"backend_rejected"'; expect "backend text" "$out" 'no_such_pet'

step "6. Adopt needs the player's approval"
out=$(call adopt_pet '{"arguments":{"name":"milo","species":"cat"}}')
expect "held" "$out" '"code":"confirmation_required"'
id=$(printf '%s' "$out" | sed -n 's/.*"confirmation_id":"\([0-9a-f]*\)".*/\1/p')
[ -n "$id" ] || fail "no confirmation id in: $out"
out=$(curl -s -X POST -H 'X-Dev-Player: p1' "$GW/v1/confirmations/$id/approve"); expect "approved" "$out" '"result":"approved"'
out=$(call adopt_pet '{"arguments":{"name":"milo","species":"cat"}}')
expect "adopted" "$out" '"adopted":"milo"'; expect "paid 50" "$out" '"coins":40'

printf '\nPASSED: 02-describe-your-actions\n'
