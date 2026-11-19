# Tutorial 5: Ask the player before big actions

Verified against agentgw v0.1.0 (see the [changelog](../../CHANGELOG.md)). About 15 minutes.
The script [`05-ask-before-big-actions.sh`](05-ask-before-big-actions.sh) runs every command below
against a throwaway Docker project, and CI runs that script.

Some actions should never run on an agent's say-so: spending real money, deleting something,
anything the player cannot undo. You mark those tools `confirm: true`. The gateway then holds the
call until the player approves **that exact call** in your game's own UI. You will watch a call
get held, approve one, deny another, and see what the agent sees each time.

## Prerequisites

- Docker with Compose v2, and the repository checked out.
- [Tutorial 4](04-link-a-player.md). This one uses the same OAuth stack and the same helpers. The
  steps below repeat what is needed.
- Stop any other agentgw stack first (this one uses `127.0.0.1:8080` and `127.0.0.1:9091`).

## 1. Mark a tool, in the manifest

The example manifest already has one. This is `build_structure` from
`examples/city/manifest.json`:

```json manifest
{
  "version": 1,
  "game": "city-demo",
  "tools": [
    {"name": "build_structure", "description": "Build a structure on a free tile of the 5x5 grid.",
     "tier": "medium", "confirm": true, "caps": {"per_day_calls": 20},
     "http": {"method": "POST", "url": "/api/v1/build"},
     "params": {
       "kind": {"type": "string", "enum": ["house", "farm", "well", "power_plant", "sawmill"]},
       "x": {"type": "integer", "min": 0, "max": 4},
       "y": {"type": "integer", "min": 0, "max": 4}}}
  ]
}
```

`"confirm": true` is the whole change. A read tool cannot require confirmation (the validator
refuses it), because reading changes nothing.

## 2. Start the stack and link an agent

```bash
dc() { docker compose -f docker-compose.yml -f examples/tutorials/compose.oauth.yml "$@"; }
dc up -d --build gateway
dc build --quiet dev
dc run --rm -T dev go build -o bin/ ./examples/studiocall ./examples/oauthclient
studio() { dc run --rm -T dev bin/studiocall "$@"; }
oauth()  { dc run --rm -T dev bin/oauthclient "$@"; }
mcp() {
  curl -s -H "Authorization: Bearer $ACCESS" -H 'Content-Type: application/json' \
    -H 'Accept: application/json, text/event-stream' -H 'MCP-Protocol-Version: 2025-06-18' \
    -d "$1" http://127.0.0.1:8080/mcp
  echo
}

TICKET=$(studio --player alice --expect 201 POST /v1/studio/link-tickets 2>/dev/null \
  | sed 's/.*"ticket":"\([^"]*\)".*/\1/')
ACCESS=$(oauth --ticket "$TICKET" --scope operator | sed 's/.*"access_token":"\([^"]*\)".*/\1/')
```

`alice` has linked an agent as an operator. (Confirmations apply to operators. A coach cannot
call action tools at all.)

## 3. The agent asks to build. The call is held

```bash
BUILD='{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"build_structure","arguments":{"kind":"farm","x":1,"y":1}}}'
mcp "$BUILD"
```

```json
{"jsonrpc":"2.0","id":2,"result":{"content":[{"type":"text","text":"{\"code\":\"confirmation_required\",...}"}],
 "structuredContent":{"code":"confirmation_required",
  "data":{"confirmation_id":"f774c1fe6c0115a32838ec376c4efa90","expires_at":"2026-11-19T12:10:47.368418Z"},
  "message":"build_structure needs the player's approval. Ask the player to approve confirmation f774c1fe... in the game, then repeat this exact call. It expires at 12:10 UTC.",
  "ok":false},"isError":true}}
```

(Shortened.) Nothing reached your backend. The message is written for the agent: it says what is
needed, who has to do it, and what to do next. A good agent tells the player "I need your OK to
build a farm" and waits.

## 4. The game's backend sees what is waiting

The player opens your game. Your backend asks the gateway for alice's pending confirmations. The
call is signed with the studio key, so only your backend can make it:

```bash
studio --player alice --expect 200 GET /v1/studio/confirmations
```

```text
HTTP 200
{"confirmations":[{"id":"f774c1fe6c0115a32838ec376c4efa90","tool":"build_structure","state":"pending","created_at":"2026-11-19T12:00:47.368418Z","expires_at":"2026-11-19T12:10:47.368418Z","args_sha256":"ce5a514223128f47ca13cbb355bab4ae1d0c670e0c9d8ac28ac053512489026b"}]}
```

Show the player **your own words** for the action, for example "Build a farm at 1,1 for 60
coins". Do not show text the agent wrote. The gateway stores only a hash of the arguments
(`args_sha256`), never the arguments. If you need to match a pending item to something you know,
compute the same hash. The rule is in [the protocol](../PROTOCOL.md#studio-confirmation-endpoints).

```bash
ID=$(studio --player alice --expect 200 GET /v1/studio/confirmations 2>/dev/null \
  | sed 's/^{"confirmations":\[{"id":"\([^"]*\)".*/\1/')
```

## 5. Another player cannot touch it

A confirmation belongs to one player. Another player's signed call does not see it and cannot
decide it:

```bash
studio --player bob --expect 200 GET /v1/studio/confirmations
studio --player bob --expect 404 POST /v1/studio/confirmations/$ID/approve
```

```text
HTTP 200
{"confirmations":[]}
HTTP 404
{"error":"not_found"}
```

The answer is the same for "no such id" and "not yours", so ids cannot be probed.

## 6. alice taps Approve

```bash
studio --player alice --expect 204 POST /v1/studio/confirmations/$ID/approve
```

```text
HTTP 204
```

## 7. The agent repeats the exact call and it goes through

```bash
mcp "$BUILD"
```

```json
{"jsonrpc":"2.0","id":2,"result":{"content":[{"type":"text","text":"{\"data\":{\"buildings\":1,\"coins\":440},\"ok\":true}"}],"structuredContent":{"data":{"buildings":1,"coins":440},"ok":true}}}
```

"Exact" means the same tool and the same arguments. Change `x` and it is a different call that
needs its own approval.

## 8. An approval is used once

```bash
mcp "$BUILD"
```

```text
... "code":"confirmation_required" ... a new confirmation_id ...
```

The first approval was spent by the call that ran. The same call asks again. An agent cannot
build ten farms on one tap.

If the backend call fails (the game is down, say), the gateway gives the approval back, so the
player is not asked twice for something that never happened. That rule is in the code
(`internal/confirm`) and in its store contract tests (`internal/confirm/confirmtest`). The script does
not make the backend fail, so it is not run here.

## 9. This time alice taps Deny

```bash
ID=$(studio --player alice --expect 200 GET /v1/studio/confirmations 2>/dev/null \
  | sed 's/^{"confirmations":\[{"id":"\([^"]*\)".*/\1/')
studio --player alice --expect 204 POST /v1/studio/confirmations/$ID/deny
studio --player alice --expect 204 POST /v1/studio/confirmations/$ID/deny
studio --player alice --expect 409 POST /v1/studio/confirmations/$ID/approve
```

```text
HTTP 204
HTTP 204
{"error":"wrong_state"}
HTTP 409
```

Deny is idempotent: the second one is fine. A denied confirmation cannot be flipped to approved.
If alice changes her mind, the agent asks again once the denial has expired. Now the agent
repeats the call:

```bash
mcp "$BUILD"
```

```json
{"jsonrpc":"2.0","id":2,"result":{"content":[{"type":"text","text":"{\"code\":\"confirmation_denied\",\"message\":\"the player denied this exact build_structure call. Do not repeat it. Ask the player what they want instead.\",\"ok\":false}"}],"structuredContent":{"code":"confirmation_denied",...,"isError":true}}
```

The message tells the agent not to retry and to talk to the player instead.

## 10. A different call is a new question

A denial covers the exact call only:

```bash
OTHER='{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"build_structure","arguments":{"kind":"well","x":2,"y":2}}}'
mcp "$OTHER"
```

```text
... "code":"confirmation_required" ... a new confirmation_id ...
```

## Expiry

A confirmation lives for 10 minutes (`confirm.TTL`, and `expires_at` in every answer). After
that, it no longer appears in the list, approving it returns `404 not_found`, and the same call
from the agent creates a fresh confirmation. The expiry window also bounds how long a denial
stands. The script does not wait ten minutes, so this part comes from the code and the protocol
document and is not run here.

## Clean up

```bash
dc down -v
```

## What you learned

- `confirm: true` holds every call of that tool until the player approves that exact call.
- The player approves in **your** UI. Your backend calls the gateway with a studio-signed request.
  The agent's token cannot reach those routes.
- An approval is single use, per player and per exact arguments. A denial stands for that call.
- The agent gets plain-language results (`confirmation_required`, `confirmation_denied`) it can act
  on.
- Confirmations are stored in Postgres (`--postgres-url`), so a restart does not lose them. With
  no Postgres the gateway keeps them in memory.

## Next

[Tutorial 6: Go live](06-go-live.md).

Reference: [studio API](../reference/studio-api.md), [error codes](../reference/errors.md) and
[players, grants and confirmations](../concepts/players-grants-confirmations.md).
