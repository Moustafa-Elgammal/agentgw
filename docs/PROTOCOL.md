# Signed request protocol (v1)

The gateway signs every request it sends to a backend. Backends verify it. The scheme is
a few lines in any language; Go backends can import `pkg/agentsig`.

## Headers

| Header | Meaning |
|---|---|
| `X-AG-Key-Id` | Which shared secret signed the request (supports rotation) |
| `X-AG-Timestamp` | Unix seconds when the request was signed |
| `X-AG-Player` | The player the call is for, taken from the player's grant |
| `X-AG-Idempotency-Key` | Unique per call. A repeated key must return the same result |
| `X-AG-Signature` | Lowercase hex HMAC-SHA256 |

## Signed string

Fields joined by a single `\n`:

```text
v1
<timestamp>
<key id>
<HTTP METHOD upper-case>
<path and query, exactly as sent>
<player id>
<idempotency key>
<hex sha256 of the request body>
```

`signature = hex(HMAC_SHA256(secret, signed string))`. An empty body hashes the empty string.

## Verifier rules

1. All five headers must be present.
2. The timestamp must be within 5 minutes of the verifier's clock (blocks replays).
3. The key id must map to a known secret.
4. Compare signatures in constant time.
5. Treat `X-AG-Player` as the authenticated player only after the signature checks out.
6. Store idempotency keys for at least the skew window and return the stored result on repeats.

## Responses

- `2xx`: success. The body is passed to the agent as data (JSON if valid).
- `4xx`: expected rejection. The body text (truncated to 500 characters) becomes the
  message with code `backend_rejected`. Use a short code plus a hint, for example
  `not_enough_coins: need 60, have 20`.
- `5xx` or timeout: the gateway reports `backend_unavailable` and releases its reservation.
  Backend details are not shown to the agent.

## Test vector

One fixed request every implementation must reproduce. The same data is in
`examples/signing/vector.json`, which the Go, Python, TypeScript and C# examples in
`examples/signing/` check, and a Go test fails if the file drifts from `pkg/agentsig`.

```text
key id           dev-1
secret           dev-secret
timestamp        1767268800
method           POST
path and query   /api/v1/pets/feed?src=agent
player           player-7
idempotency key  idem-0001
body             {"pet":"rex","food":"fish","amount":2}
body sha256      2bf28910270739367847a8e121091f3b9851196ff3e2330ef0776071153ab377
signature        a706e2b8923961ede4fc119424fe81125533df9ca469cdd5ec86663eefbba6de
```

The signed string for it, with `\n` as a real newline:

```text
v1
1767268800
dev-1
POST
/api/v1/pets/feed?src=agent
player-7
idem-0001
2bf28910270739367847a8e121091f3b9851196ff3e2330ef0776071153ab377
```

`pkg/agentsig/agentsig_test.go` has more cases (skew, tampering, unknown key). The tutorial
[Accept signed calls](tutorials/03-accept-signed-calls.md) shows a verifier in four languages.

## Studio requests: replay rule

Every studio-signed endpoint (link tickets, grants, confirmations) goes through one
verifier. Beyond the signature and the 5 minute timestamp window, the gateway accepts each
`(X-AG-Key-Id, X-AG-Idempotency-Key)` pair once. A second request with the same pair inside
the window gets `401 {"error":"invalid_client"}` (link tickets: `401 invalid_client`, "this
signed request was already used"), whatever its method, path or body. The pair is shared
across all studio endpoints, so a request captured against one endpoint cannot be replayed
against another.

The rule applies to every method, including read only `GET` requests. A replayed read would
return the captured data to whoever replays it.

What studios must do: send a fresh, unique idempotency key (a random UUID) with every
request, **including every retry**. A retry that reuses the key of an earlier attempt is
refused even when the earlier attempt failed, because the gateway cannot tell a retry from a
replay. Re-signing with a new key is always safe: approve, deny and revoke are idempotent, so
a retry with a fresh key is harmless. If the gateway answers `503` because its replay cache
is full, back off and retry with a fresh key.

The gateway keeps each accepted pair for twice the skew window (10 minutes), because a
timestamp up to 5 minutes in the future is accepted. The cache holds at most 100000 pairs
and only grows from requests whose signature verified. At the cap the gateway fails closed
with `503 {"error":"temporarily_unavailable"}` and never forgets a pair early. The cache is
per process: with several gateway replicas, replay protection is per replica until the cache
moves to a shared store.

## Studio grant endpoints

Players have no gateway login. The studio's backend knows the player and calls these
endpoints, signed exactly like the gateway's own calls (same five headers, signed with the
studio key). The player is the signed `X-AG-Player` header and the tenant is the gateway's
configured tenant, so a player can only ever see and revoke their own grants. A missing,
bad, stale or tampered signature gives `401 {"error":"invalid_client"}`. A store failure
gives `503 {"error":"temporarily_unavailable"}` and nothing is reported as revoked. Every
response carries `Cache-Control: no-store`.

`GET /v1/studio/grants` lists the player's active grants, oldest first. `client_name` is
empty if the client is gone.

```json
{"grants":[{"id":"9f2c...","client_id":"c1","client_name":"Claude on the laptop","scope":"operator","resource":"https://gw.example/mcp","created_at":"2026-01-02T03:04:05Z"}]}
```

`DELETE /v1/studio/grants/{id}` revokes one grant and every token of it, effective on the
next token lookup. It returns `204`. A grant that does not exist, is already revoked or
belongs to another player gives the same `404 {"error":"not_found"}`, so ids cannot be probed.

`DELETE /v1/studio/grants` revokes all of the player's grants (a "log out all agents"
button) and returns `{"revoked":2}`. Repeating it returns `{"revoked":0}`.

## Studio confirmation endpoints

Tools marked `confirm: true` need the player's approval for each call. In OAuth mode the
player approves in the game's own UI, and the studio backend calls these endpoints signed
exactly like the grant endpoints (same five headers, studio key, player in `X-AG-Player`).
Only a studio-signed request can approve or deny. The agent's bearer token never reaches
these routes. The tenant is the gateway's configured tenant, so a player only ever sees and
decides their own confirmations. A bad, missing, stale or tampered signature gives
`401 {"error":"invalid_client"}`. A store failure gives
`503 {"error":"temporarily_unavailable"}` and nothing is reported as approved. Every
response carries `Cache-Control: no-store`. Request bodies are limited to 64 KiB.

`GET /v1/studio/confirmations` lists the player's live pending confirmations, oldest
first. The list is `[]` when there are none.

```json
{"confirmations":[{"id":"3f1a...","tool":"build","state":"pending","created_at":"2026-01-02T03:04:05Z","expires_at":"2026-01-02T03:14:05Z","args_sha256":"9b7e..."}]}
```

`args_sha256` is not meant for display. The gateway stores only a hash of the arguments,
never the arguments. If the studio needs to match a pending confirmation to something it
knows (for example "build a farm at 2,3"), it can compute the same digest: the SHA-256,
hex encoded, of the JSON encoding of the validated arguments with keys sorted (`argsDigest`
in `internal/gateway/gateway.go`). Show the player the studio's own description of the action.

`POST /v1/studio/confirmations/{id}/approve` and `POST /v1/studio/confirmations/{id}/deny`
return `204`. They are idempotent: approving an approved confirmation or denying a denied
one is also `204`. A confirmation that does not exist, has expired or belongs to another
player gives the same `404 {"error":"not_found"}`, so ids cannot be probed. A confirmation
that cannot make that move (for example approving a denied one) gives
`409 {"error":"wrong_state"}`.

A replayed approve or deny is refused by the studio replay rule above, so a captured request
cannot move a confirmation again. An approval stays bound to one exact tool call and expires with
the confirmation after 10 minutes.

## Studio manifest endpoints

A studio uploads and rolls back its tool manifest with these endpoints, so onboarding needs no
backend code and no restart. They are signed exactly like the other studio calls (same five
headers, same replay rule) but with the **admin key** (`--admin-key-id`, secret
`AGENTGW_ADMIN_SECRET`), never the key that signs player calls. `X-AG-Player` names the acting
admin (for example a CI job or a person) and is recorded as the version's `actor`. Without
`AGENTGW_ADMIN_SECRET` the endpoints are not mounted. Bodies up to 1 MiB are accepted
(`413 {"error":"too_large"}` above that). A bad signature gives `401 {"error":"invalid_client"}`
and a store failure gives `503 {"error":"temporarily_unavailable"}`.

`PUT /v1/studio/manifest` validates the body, stores it as a new version, activates it and
serves it from the next request on. The body is JSON, or YAML when `Content-Type` is
`application/yaml`. It returns `201` with the version:

```json
{"id":3,"sha256":"ab12...","actor":"ci","created_at":"2026-01-02T03:04:05Z","tools":12,"active":true}
```

An invalid manifest gives `422 {"error":"invalid_manifest","detail":"..."}` and changes nothing.

`GET /v1/studio/manifest` returns `{"version":{...},"manifest":{...}}` for the active version,
or `404 not_found` if none.

`GET /v1/studio/manifest/versions` returns `{"versions":[...]}`, newest first, with `active` set
on one.

`POST /v1/studio/manifest/versions/{id}/activate` makes an existing version active again (a
rollback) and returns it. The stored document is validated again; if it no longer passes the
current rules the answer is `422` and nothing changes. An unknown id is `404 not_found`.

Other replicas pick up the new active version within `--manifest-poll` (default 15s).

## Studio kill switch endpoints

A studio turns a tool, a player or the whole game off during an incident, and back on after. The
requests are signed like the other studio calls (same five headers, same replay rule) with the
**admin key**, never the key that signs player calls. `X-AG-Player` names the acting admin and is
recorded in an audit event. Without an admin key the endpoints are not mounted. Bodies are at most
4 KiB (`413 {"error":"too_large"}`). A bad signature is `401 {"error":"invalid_client"}`, a store
failure is `503 {"error":"temporarily_unavailable"}`, and a bad body is
`400 {"error":"invalid_request","detail":"..."}`.

`GET /v1/studio/switches` lists the switches that are on, ordered by tenant, tool and player:

```json
{"switches":[{"tenant":"tinycity","tool":"build_structure"}]}
```

`PUT /v1/studio/switches` turns one on. The body is optional JSON:

```json
{"tool":"build_structure","player":"alice","reason":"duplicate items under investigation"}
```

`tool` is a tool name, `player` is 1 to 128 printable ASCII characters without spaces, and `reason`
is up to 200 characters. All are optional. With no `tool` and no `player` the whole game is
switched off. The tenant is always the gateway's own. The answer is
`200 {"switch":{"tenant":"tinycity","tool":"build_structure"},"on":true}`.

`DELETE /v1/studio/switches` turns one off with the same body and answers `"on":false`. Turning a
switch on or off twice is not an error.

While a switch matches, a call gets `switched_off` and the agent is told not to retry. A switch
blocks every tier it matches, reads included. Each change writes an audit event with tool
`admin.switch.on` or `admin.switch.off`, tier `admin`, and the admin as `player`. It shows in the
admin activity feed. The `reason` goes to the gateway log, not to the event.

## Relay (client-authoritative games)

Tools marked `"backend": "relay"` in the manifest are not sent to a REST backend. The game
client holds an outbound WebSocket to the gateway, and the gateway sends each call down it.

**1. Mint a ticket.** The studio's backend calls `POST /v1/studio/relay/tickets`, signed like
the other studio calls with the studio key. The signed `X-AG-Player` is the player the ticket is
for. It returns `201`:

```json
{"ticket":"eyJ0Ijoi...","expires_at":"2026-01-02T03:05:05Z"}
```

A ticket lives 60 seconds, is signed with the relay secret and names tenant and player.

**2. Connect.** The game client opens `GET /v1/relay/connect` as a WebSocket with
`Authorization: Bearer <ticket>` (or `?ticket=<ticket>` where a header cannot be set). A bad or
expired ticket gives `401 {"error":"invalid_ticket"}` before any upgrade. One socket per player:
a new connection closes the old one with code 4000.

**3. Frames.** JSON text frames, at most 1 MiB. Unknown `type` values and unknown fields are
ignored, so the protocol can grow.

| Direction | Frame |
|---|---|
| gateway to client | `{"type":"hello","protocol":1,"minor":1,"call_timeout_ms":10000,"ping_interval_ms":20000,"max_in_flight":8,"max_frame":1048576}` once after connect |
| client to gateway | `{"type":"hello","protocol":1,"minor":1,"client":"unity-relay 0.1","capabilities":[]}` optional reply |
| gateway to client | `{"type":"call","id":"c1","tool":"build_structure","args":{"kind":"farm"},"idem":"...","player":"alice","deadline_ms":9800}` |
| client to gateway | `{"type":"result","id":"c1","ok":true,"data":{...}}` |
| client to gateway | `{"type":"result","id":"c1","ok":false,"code":"insufficient_funds","message":"...","retry_after_ms":2000}` |
| client to gateway | `{"type":"bye","reason":"app quit"}` optional graceful close |

**Versions.** `protocol` is the major version (1) and `minor` the minor (1). Version 1.1 only adds
fields and the two optional client frames, so a v1 client, which reads `protocol`, ignores what it
does not know and never sends `hello` or `bye`, works unchanged. A client that declares a different
major version in its `hello` is closed with code 1002. The client `hello` is optional and only
informs: `client` (printable ASCII, at most 64 characters) appears in gateway debug logs, and
`capabilities` is reserved. Only the first `hello` counts.

**Calls.** The client answers each `call` with a `result` carrying the same `id`, before
`deadline_ms` has passed. `deadline_ms` is how long the gateway will still wait, counted from the
moment it sent the frame (a relative time, so client clocks do not matter). The default call
timeout is 10 s. After the deadline the gateway has given up, the agent sees `outcome_unknown`, and a late
result is dropped, so a client may stop work it has not started. A result for an unknown or expired id is
dropped. A failed `result` should carry a `code`; without one the agent sees `client_error`.
`retry_after_ms` (on a failed result only) says when a retry makes sense. The gateway clamps it to
one hour and adds "retry after N ms" to the message the agent reads.

**No retries, and what `idem` is.** The gateway sends each call once and never resends it, on the
same socket or after a reconnect. `idem` is a fresh unique value for every call, the same one a REST
backend would get in `X-AG-Idempotency-Key`. A well-behaved client still ignores a `call` whose `id`
it has already handled on this socket, which costs nothing. A call that was in flight when the
socket dropped has an unknown outcome: the agent is told `outcome_unknown` and decides what to do.
It is never replayed after a reconnect.

**Backpressure.** At most `max_in_flight` calls (default 8, `--relay-max-in-flight`) wait on one
player's socket at a time. A call over that is not sent: the agent gets the denied result
`client_busy` at once. A client that cannot take more work may answer a call with
`{"ok":false,"code":"client_busy","retry_after_ms":1000}` instead. A client may send at most
`--relay-max-frames-per-sec` frames per second (default 100, bursts of twice that); above it the
socket is closed with 1008 "too many frames".

**Closing.** To leave on purpose, answer the calls still running, then send `bye` and close. The
gateway closes with 1000, and any call still waiting on the socket fails with an unknown outcome.
The gateway pings every 20 s and closes a socket that stops answering.

| Close code | Who | Meaning |
|---|---|---|
| 1000 | gateway | normal close after the client's `bye` |
| 1001 | gateway | the client stopped answering pings |
| 1002 | gateway | the client's `hello` declared a major version other than 1 |
| 1008 | gateway | a frame that is not valid JSON, a binary frame, or too many frames per second |
| 1009 | gateway | a frame over 1 MiB |
| 4000 | gateway | the same player connected again (on this or another replica) and replaced this socket |

**Reconnecting.** A closed socket is not resumed: open a new one. Fetch a fresh ticket from your
backend every time, because tickets last 60 seconds and are checked before the upgrade. After 4000
do not reconnect at once, the player has another client open. For every other close or failure,
back off exponentially with jitter: wait a random time between 0 and `min(30 s, 1 s * 2^attempt)`,
reset the attempt counter after a connection has lasted a minute, and never retry faster than once a
second. While the socket is down, tool calls for this player answer `client_offline`.

## Activity feed

What a player's agents did, read back from the audit log. Two read only endpoints, signed like
the other studio calls (same five headers, same replay rule, `Cache-Control: no-store`).

`GET /v1/studio/activity` is the **player feed**. Sign it with the studio key. The signed
`X-AG-Player` is the player whose events are returned. A `player` or `tenant` query parameter is
ignored, so a player can only ever read their own events.

`GET /v1/studio/admin/activity` is the **admin feed**. Sign it with the admin key. It reads any
player of the gateway's tenant, accepts an extra `player` filter, and its rows name the player.
`X-AG-Player` is the acting admin and is not a filter.

Query parameters (all optional): `tool`, `outcome` (`ok`, `denied` or `error`), `since` (RFC 3339,
events at or after it), `limit` (default 50, at most 200, larger values are clamped) and `before`
(a cursor, only events with a smaller id). A malformed value gives
`400 {"error":"invalid_request"}`. A store failure gives `503 {"error":"temporarily_unavailable"}`.
A bad signature gives `401 {"error":"invalid_client"}`.

Events come newest first. When more remain, the response carries `next_before`: pass it as
`before` to get the next page. It is omitted on the last page.

```json
{"events":[{"id":42,"time":"2026-03-01T10:05:00Z","tool":"sell_resource","tier":"medium","outcome":"denied","code":"daily_amount_cap","args_sha256":"9f2c..."}],"next_before":41}
```

An admin row also has `"player":"alice"`. `outcome` and `code` are the audit event's: `ok`,
`denied` (with the denial code) or `error` (for example `backend_unavailable`). Rows carry the
SHA-256 of the arguments, never the arguments, so match an entry to a game action by the
timestamp and tool, or by the hash if you can compute it. Gateways keep events for
`--audit-retention` (default 30 days; `0` keeps them forever).

## Rotating studio keys

Studio and admin requests name their key in `X-AG-Key-Id`. The gateway accepts every key in its
current set, so rotate without downtime: add the new key (key file plus SIGHUP), switch your
backend to sign with it, then remove the old key. A request signed with a removed key gets
`401 {"error":"invalid_client"}`. The key from `--studio-key-id` and
`AGENTGW_SIGNING_SECRET` is retired the same way with `--studio-keys-only` (see the runbook).

The key on the gateway's own calls to your backend rotates in the other order: your backend must
accept the new `X-AG-Key-Id` before the gateway switches to it (`--backend-key-file` plus SIGHUP),
and you drop the old key afterwards.
