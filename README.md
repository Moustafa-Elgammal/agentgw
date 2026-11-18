# agentgw

An open-source gateway that lets a player's own AI agent act safely inside a game.

A game studio describes its game actions in a manifest. The gateway turns them into
[MCP](https://modelcontextprotocol.io) tools, applies safety policy (tiers, rate limits,
daily caps, cooldowns, kill switches), calls the studio's backend with a signed request,
and records every attempt in an audit log. It works with any backend language, because
the contract is a manifest plus a signed HTTP request, not a library.

**Status: M0 to M2 done, M3 nearly done.** The core pipeline, MCP transport, Redis and Postgres
stores and player confirmations work and are tested. Players link their agent with OAuth (M3, see
below). OIDC login at consent is the one open M3 item. See [docs/ROADMAP.md](docs/ROADMAP.md).

## Two modes for players

- **Coach**: read-only. State, prices, advice.
- **Operator**: limited actions on the player's behalf, inside caps the studio controls.

## What works today

| Piece | Where |
|---|---|
| Manifest loader and validator, JSON or YAML (strict, reports all problems at once) | `internal/manifest` |
| Policy: rate limit counted before validation, daily caps with reserve/settle, cooldowns, disabled sensitive tier | `internal/policy` |
| Request signing (HMAC-SHA256, timestamp, idempotency key), usable by Go backends | `pkg/agentsig` |
| REST connector (level 0) with path, query and JSON body mapping | `internal/connector` |
| Audit events with hashed arguments | `internal/audit` |
| Call pipeline: lookup, admit, validate, reserve, call, settle, audit | `internal/gateway` |
| Dev HTTP API around the pipeline | `internal/httpapi`, `cmd/gateway` |
| Player confirmations for tools marked `confirm` (approve or deny, single use, memory or Postgres) | `internal/confirm`, `internal/confirm/pgstore` |
| Policy store in Redis (shared across replicas, fails closed) and kill switches | `internal/policy/redisstore`, `internal/policy` |
| MCP endpoint: tools from the manifest, one server per authenticated request, sensitive tools hidden | `internal/mcpserver` |
| OAuth 2.1 resource server and embedded authorization server (PKCE, DCR, link tickets, consent, grants, revocation) | `internal/auth` |
| Manifest CLI | `cmd/gwctl` |

Not yet: OIDC login at consent, relay for client-authoritative games (M5). Set `--postgres-url` to
keep clients, grants and approvals across restarts.
With `--dev-auth` (the compose demo) the dev HTTP API and the MCP endpoint take the player from a
header and must stay on loopback.

## OAuth flow

Without `--dev-auth`, `/mcp` needs a bearer token. An MCP client discovers everything itself:

1. `GET /.well-known/oauth-protected-resource/mcp`, then `/.well-known/oauth-authorization-server`.
2. `POST /register` (dynamic client registration), then `GET /authorize` with PKCE S256 and `resource=<public-url>/mcp`.
3. The player is on the studio's site, logged in. The studio backend calls
   `POST /v1/studio/link-tickets` signed with `agentsig` (player in `X-AG-Player`) and shows the
   player a short code. The player types it on the consent page and picks **coach** or **operator**.
4. The client exchanges the code at `POST /token`, then refreshes with rotation.

The game's own UI uses signed studio endpoints for the player's side: list and revoke linked
agents (`/v1/studio/grants`) and approve or deny a pending confirmation
(`/v1/studio/confirmations`). See `docs/PROTOCOL.md`.

Flags: `--public-url` (issuer, default `http://127.0.0.1:8080`), `--studio-key-id` (secret is
`AGENTGW_SIGNING_SECRET`), `--register-per-hour`, `--max-clients`, `--client-ip-header`. The scripted end to end test is `test/conformance/auth_test.go`.
Grants live in memory for now, so restarting the gateway makes players link again.

## Try it

Everything runs in containers. You only need Docker with Compose v2.

```bash
docker compose run --rm dev make test        # tests with the race detector
docker compose up --build                    # gateway + Redis + Postgres + fake city backend

curl -s -X POST -H 'X-Dev-Player: p1' \
  -d '{"arguments":{"resource":"food","quantity":50}}' \
  http://127.0.0.1:8080/v1/tools/sell_resource/call

docker compose down
```

Other commands: `docker compose run --rm dev make vet fmt lint vuln`, or the shortcuts
`make docker-test`, `make docker-lint`. Expected failures come back as results with a
machine-readable `code` and a hint, for example `invalid_arguments`, `rate_limited`,
`daily_amount_cap`, `cooldown`, `switched_off`. The agent can read them and correct itself.

## Design rules

1. The agent plays by the same rules as a human. No side doors around game logic.
2. Every attempt counts against rate limits, valid or not.
3. Caps reserve first and settle after, so concurrent calls cannot exceed them.
4. Policy outage means action tiers fail closed.
5. Backend results are untrusted data, never instructions.
6. The audit log stores hashes of arguments, not game data.

## Docs

- [Architecture](docs/ARCHITECTURE.md)
- [Manifest](docs/MANIFEST.md)
- [Signed request protocol](docs/PROTOCOL.md)
- [Security model](docs/SECURITY-MODEL.md)
- [Roadmap](docs/ROADMAP.md)
- [Open decisions](docs/DECISIONS.md)

## License

Apache-2.0. See [LICENSE](LICENSE) and [NOTICE](NOTICE). Contributions are accepted under
the Developer Certificate of Origin; see [CONTRIBUTING.md](CONTRIBUTING.md).
