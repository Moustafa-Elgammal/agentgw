# agentgw

[![ci](https://github.com/Moustafa-Elgammal/agentgw/actions/workflows/ci.yml/badge.svg)](https://github.com/Moustafa-Elgammal/agentgw/actions/workflows/ci.yml)
[![Go Reference](https://pkg.go.dev/badge/github.com/Moustafa-Elgammal/agentgw.svg)](https://pkg.go.dev/github.com/Moustafa-Elgammal/agentgw)
[![License](https://img.shields.io/badge/license-Apache--2.0-blue.svg)](LICENSE)

**An open-source gateway that lets a player's own AI agent act safely inside a game.**

A game studio describes its game actions in a manifest. The gateway turns them into
[MCP](https://modelcontextprotocol.io) tools, applies safety policy (tiers, rate limits,
daily caps, cooldowns, kill switches, player confirmations), calls the studio's backend with a
signed request, and records every attempt in an audit log. It works with any backend language,
because the contract is a manifest plus a signed HTTP request, not a library. Games whose logic
runs in the client can use a WebSocket relay instead.

## Two modes for players

- **Coach**: read-only. State, prices, advice.
- **Operator**: limited actions on the player's behalf, inside caps the studio controls.

A studio can start a pilot read only for everyone with `--max-scope coach`, then raise it to
operator later.

## Quickstart

You only need Docker with Compose v2. Nothing is installed on your machine.

```bash
git clone https://github.com/Moustafa-Elgammal/agentgw && cd agentgw
docker compose up --build        # gateway, Redis, Postgres and a fake city backend on 127.0.0.1:8080

# in another terminal: let a player "sell" some food through the gateway
curl -s -X POST -H 'X-Dev-Player: p1' \
  -d '{"arguments":{"resource":"food","quantity":50}}' \
  http://127.0.0.1:8080/v1/tools/sell_resource/call

docker compose down
```

An expected failure comes back as a result with a machine-readable `code` and a hint, for
example `invalid_arguments`, `rate_limited`, `daily_amount_cap`, `cooldown` or `switched_off`.
The agent can read it and correct itself. The demo runs with `--dev-auth`, which trusts a
header and must stay on loopback. Real deployments use OAuth (see the docs).

Run the tests: `docker compose run --rm dev make test`. To connect Claude Desktop, see
[docs/CLAUDE-DESKTOP.md](docs/CLAUDE-DESKTOP.md).

## Status and compatibility

Version `0.x`. The pipeline, MCP endpoint, OAuth 2.1, Redis and Postgres stores, confirmations,
manifest upload and rollback, activity feed, relay, metrics, and a Helm chart are built and
tested. Open items: OIDC login at consent, a hosted offering, and the docs site and guides
(see [docs/ROADMAP.md](docs/ROADMAP.md)).

While the version is `0.x`, the manifest format, the signed request and some protocol details
may still change. Every such change is recorded in [docs/DECISIONS.md](docs/DECISIONS.md) and
the [CHANGELOG](CHANGELOG.md). Pin a version if you build on it.

## Features

- A manifest, written in YAML (JSON also works), becomes MCP tools. Strict validation reports every problem at once.
- Policy: rate limits counted before validation, daily caps with reserve and settle, cooldowns,
  tiers, kill switches. Policy outages fail closed.
- Signed backend calls (HMAC-SHA256, timestamp, idempotency key). Go backends can import
  [`pkg/agentsig`](pkg/agentsig); other languages follow [docs/PROTOCOL.md](docs/PROTOCOL.md).
- Player confirmations for sensitive tools, linked through an embedded OAuth 2.1 server.
- Versioned manifests with upload, rollback and hot swap, no restart. Key rotation.
- Activity feed for players and studios. Audit stores hashes of arguments, never the arguments.
- Relay for client-authoritative games, across several gateway replicas.
- Metrics, health checks, an SSRF-safe connector, a Helm chart and a runbook.

The full component list is in [docs/FEATURES.md](docs/FEATURES.md).

## Design rules

1. The agent plays by the same rules as a human. No side doors around game logic.
2. Every attempt counts against rate limits, valid or not.
3. Caps reserve first and settle after, so concurrent calls cannot exceed them.
4. Policy outage means action tiers fail closed.
5. Backend results are untrusted data, never instructions.
6. The audit log stores hashes of arguments, not game data.

## Docs

- [Overview](docs/get-started/overview.md), [quickstart](docs/get-started/quickstart.md) and [connect an agent](docs/get-started/connect-an-agent.md)
- Guides: [learning path](docs/guides/index.md), [best practices](docs/guides/best-practices.md), [cookbook](docs/guides/cookbook.md), [troubleshooting](docs/guides/troubleshooting.md), [FAQ](docs/guides/faq.md)
- Concepts: [how a call flows](docs/concepts/pipeline.md), [tiers and policy](docs/concepts/tiers-and-policy.md), [players, grants and confirmations](docs/concepts/players-grants-confirmations.md)
- [Architecture](docs/ARCHITECTURE.md) and [features](docs/FEATURES.md)
- [Manifest](docs/MANIFEST.md)
- [Signed request protocol](docs/PROTOCOL.md)
- [Security model](docs/SECURITY-MODEL.md)
- [Runbook](docs/RUNBOOK.md) and [releasing](docs/RELEASING.md)
- [Roadmap](docs/ROADMAP.md), [decisions](docs/DECISIONS.md), [open source plan](docs/OPEN-SOURCE-PLAN.md)

## Contributing and security

Small, focused pull requests are easiest to review. Every commit is signed off (DCO). Read
[CONTRIBUTING.md](CONTRIBUTING.md) and [AGENTS.md](AGENTS.md). Report vulnerabilities privately,
see [SECURITY.md](SECURITY.md).

This is an independent project. It contains no code, data or API details from any employer or
real game. The example game is fictional.

## License

Apache-2.0. See [LICENSE](LICENSE) and [NOTICE](NOTICE). Contributions are accepted under
the Developer Certificate of Origin.
