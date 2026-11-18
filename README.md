# agentgw

An open-source gateway that lets a player's own AI agent act safely inside a game.

A game studio describes its game actions in a manifest. The gateway turns them into
[MCP](https://modelcontextprotocol.io) tools, applies safety policy (tiers, rate limits,
daily caps, cooldowns, kill switches), calls the studio's backend with a signed request,
and records every attempt in an audit log. It works with any backend language, because
the contract is a manifest plus a signed HTTP request, not a library.

**Status: M0 (foundations).** The core pipeline works and is tested. The MCP transport,
OAuth and persistence arrive in later milestones. See [docs/ROADMAP.md](docs/ROADMAP.md).

## Two modes for players

- **Coach**: read-only. State, prices, advice.
- **Operator**: limited actions on the player's behalf, inside caps the studio controls.

## What works today

| Piece | Where |
|---|---|
| Manifest loader and validator (strict, reports all problems at once) | `internal/manifest` |
| Policy: rate limit counted before validation, daily caps with reserve/settle, cooldowns, disabled sensitive tier | `internal/policy` |
| Request signing (HMAC-SHA256, timestamp, idempotency key), usable by Go backends | `pkg/agentsig` |
| REST connector (level 0) with path, query and JSON body mapping | `internal/connector` |
| Audit events with hashed arguments | `internal/audit` |
| Call pipeline: lookup, admit, validate, reserve, call, settle, audit | `internal/gateway` |
| Dev HTTP API around the pipeline | `internal/httpapi`, `cmd/gateway` |
| Manifest CLI | `cmd/gwctl` |

Not yet: MCP transport (M1), OAuth and player linking (M3), Redis/Postgres stores (M2),
relay for client-authoritative games (M5). The dev HTTP API takes the player from a header
and must stay on loopback.

## Try it

Everything runs in containers. You only need Docker with Compose v2.

```bash
docker compose run --rm dev make test        # tests with the race detector
docker compose up --build                    # gateway + fake city backend

curl -s -X POST -H 'X-Dev-Player: p1' \
  -d '{"arguments":{"resource":"food","quantity":50}}' \
  http://127.0.0.1:8080/v1/tools/sell_resource/call

docker compose down
```

Other commands: `docker compose run --rm dev make vet fmt lint vuln`, or the shortcuts
`make docker-test`, `make docker-lint`. Expected failures come back as results with a
machine-readable `code` and a hint, for example `invalid_arguments`, `rate_limited`,
`daily_amount_cap`, `cooldown`. The agent can read them and correct itself.

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
