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

## Planned

OAuth 2.1 resource server with audience binding per game and per-grant revocation, SSRF-safe dialer for connector URLs, tenant isolation with row-level security,
envelope encryption for signing secrets, circuit breaker per backend.

## Reporting

See [SECURITY.md](../SECURITY.md).
