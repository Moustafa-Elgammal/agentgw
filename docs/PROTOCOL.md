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
