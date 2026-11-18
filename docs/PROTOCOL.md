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

Use `pkg/agentsig/agentsig_test.go` as the reference. A cross-language conformance suite
is planned for M1.

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
