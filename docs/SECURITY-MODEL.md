# Security model

The gateway sits between an AI agent and a game economy. The main threats are untrusted
text steering the agent, a backend reached where it should not be, one studio touching
another's data, and runaway agents.

## Tiers

| Tier | Examples | Controls | v1 |
|---|---|---|---|
| read | state, inventory, prices | rate limit | enabled |
| safe | collect, repair, plant | rate limit, cooldown | enabled |
| medium | build, buy, sell | rate limit, cooldown, daily caps | enabled |
| sensitive | gifts, paid currency, deletion | human confirmation | disabled |

## Rules

1. **Same rules as a player.** The backend stays authoritative for energy, cooldowns and
   the economy. The agent never gets a side door.
2. **Every attempt counts.** Rate limits are charged before validation.
3. **Reserve, then settle.** Caps are claimed before the call and released if it fails.
4. **Fail closed.** If the policy store is unavailable, calls are refused.
5. **Static tool descriptions.** They come from the uploaded manifest, never from runtime data.
6. **Untrusted results.** Backend output and player-written text are data, size-limited
   and never copied into instructions.
7. **Signed requests.** The backend only trusts `X-AG-Player` after verifying the signature.
8. **Hashed audit.** Events store a SHA-256 of the arguments, not the arguments.

## Implemented in M0

Rules 1 to 4, 7 and 8 are implemented and tested. Rules 5 and 6 need the MCP layer (M1).

## Kill switches (M2)

A `policy.Switch` turns off everything matching a tenant, tool and player (empty fields are
wildcards). It is checked first in `Admit` and again in `Reserve`, answers `switched_off`, and
is not charged to the rate limit. A switch blocks every tier it matches, reads included. If the
switch store errors, `Admit` fails closed. Denials are audited like any other.

## OAuth and grants (M3)

The gateway is an OAuth 2.1 resource server with an embedded authorization server (decisions in
`docs/DECISIONS.md`, "Auth (M3)"). Without `--dev-auth`, `/mcp` accepts only bearer tokens and the
header-trusting dev API is not mounted.

- Tokens are opaque and stored as SHA-256. Access tokens last 15 minutes, refresh tokens rotate, and
  replaying a rotated refresh token revokes the whole grant.
- PKCE S256 is mandatory. Codes are single use, last 60 seconds, and are bound to client,
  redirect URI and challenge. A failed exchange still burns the code.
- Audience binding: `resource` must equal the gateway's MCP URL at authorize and token time, and
  the resource server rejects any token whose grant has another resource or tenant.
- Revocation is per grant and immediate, because every request looks the token up.
- Redirect URIs: https, or http on literal loopback (any port). Errors before the client and
  redirect URI are verified are shown to the player and never redirected. Later errors redirect
  with `state` and `iss` (RFC 9207, mix-up defence).
- Consent page: the client name is HTML escaped, the page sets `X-Frame-Options: DENY`,
  `frame-ancestors 'none'`, a CSP with no scripts, `form-action` limited to the page and the
  client's redirect origin, `no-store` and `no-referrer`. A missing or unknown decision is a denial.
- Link tickets (`XXXX-XXXX`, 40 bits, 5 minutes, single use) are minted by the studio backend
  with a signed request (`agentsig`, replays refused). Guessing is limited to 10 attempts a minute
  per address, and every failure looks the same.
- Studio-signed requests (link tickets, grants, confirmations) share one verifier
  (`internal/studiosig`). It checks the signature, requires a player, and accepts each
  (key id, idempotency key) pair once for twice the skew window, on every method and every
  endpoint. A pair is recorded only after the signature verified, so an attacker without the
  key cannot fill or poison the cache. The cache is capped; at the cap it fails closed (503)
  and never evicts early. A captured approve, deny or revoke cannot be replayed.
- Known limits: the authorization server state is in memory (lost on restart), the ticket
  limiter is per process, and dynamic client registration is unauthenticated (rate limited and
  capped, see below, but the cap is per process).

## Manifest uploads (M4)

- Manifest upload, list and activate are signed with a separate admin key, never the player-call
  key, and go through their own replay checked verifier. Without `AGENTGW_ADMIN_SECRET` they are not mounted.
- Uploads are validated before they are stored. Rollback validates again. A failure changes nothing.
- Every version records the signed actor and a hash, so a change to what agents can call is attributable.
- Studio and admin keys rotate without downtime: the verifier accepts a set of keys, loaded from
  key files and reloaded on SIGHUP. A failed reload keeps the keys in use. Secrets and ids are
  never logged. The replay cache survives a rotation.

## Activity feed (M4)

- The player feed is signed with the studio key and filtered to the signed player and the gateway
  tenant on the server. Query parameters cannot change either. The admin feed needs the admin
  key, so a leaked studio key cannot read other players' activity.
- Rows hold the argument hash, never the arguments, and no tenant. Replays are refused and
  responses are `no-store`.
- Events are deleted after `--audit-retention`. The in memory store holds only the newest 10000.

## Planned

SSRF-safe dialer for connector URLs, tenant isolation with row-level security,
envelope encryption for signing secrets, circuit breaker per backend.

## Reporting

See [SECURITY.md](../SECURITY.md).

## Public OAuth endpoint hardening (M3)

- `POST /register` is limited to 10 registrations per hour per address (every attempt counts),
  and the process registers at most 10000 clients. The cap is process local: it resets on restart
  and is not shared between replicas. Redirect URIs must be unique and at most 2048 bytes, and
  `client_name` must be printable text.
- **Client address behind a proxy.** The ticket limiter and the registration limiter key on the
  connection's address by default, so behind a reverse proxy every player shares one budget. The
  `ClientIPHeader` setting makes them use the first value of a header such as `X-Forwarded-For`.
  Set it ONLY when the proxy overwrites that header on every request and clients cannot reach the
  gateway directly. Otherwise any client can choose its own address and evade the limits entirely.
  A missing or unparsable header falls back to the connection address.
- CORS (`Access-Control-Allow-Origin: *`, no credentials) exists only on the discovery metadata,
  `/register` and `/token`. `/authorize`, `/v1/studio/*` and `/mcp` send no CORS headers.
