# Changelog

All notable changes are listed here. The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/)
and the project follows [Semantic Versioning](https://semver.org). While the version is `0.x`,
the manifest format, the signed request and some protocol details may change in a minor release.
Each such change has an entry in [docs/DECISIONS.md](docs/DECISIONS.md).

## [Unreleased]

## [0.1.0] - unreleased

First public release. The date is set when the tag is cut.

### Gateway and policy

- Call pipeline: lookup, rate limit, validation, confirmation, reserve, signed backend call,
  settle, audit. Rate limits count every attempt, caps reserve before the call, a failed call
  releases its reservation, and policy outages fail closed.
- Manifest in YAML or JSON with strict validation that reports every problem at once, and the
  `gwctl` CLI. Tools per tier (read, safe, medium, sensitive), caps, cooldowns and `confirm`.
- MCP endpoint (stateless streamable HTTP) and the `gwbridge` stdio bridge for desktop clients.
- Redis policy store shared across replicas, with kill switches. Memory store for one process.
- `--max-scope coach|operator` caps every grant at read only for a first pilot.
- `--max-body` limits request bodies at the front door (`413 too_large`), and the server sets
  read, write and idle timeouts.
- SSRF-safe backend connector: no loopback, private or metadata addresses unless
  `--backend-allow-private`, no redirects.
- `pkg/agentsig`, the public request signing library, with a published test vector.

### Players and studios

- OAuth 2.1 resource server with an embedded authorization server: PKCE, dynamic client
  registration, link tickets, consent with coach or operator scope, rotating refresh tokens.
- Optional OIDC sign-in at consent (`--oidc-issuer`), verified with go-oidc and go-jose, tried
  against Dex.
- Player confirmations for tools marked `confirm`.
- Studio APIs signed with `agentsig`: grants, confirmations, activity feed (player and admin),
  manifest upload, list and rollback, kill switches (`/v1/studio/switches`), relay tickets.
- Rotate the studio, admin and backend signing keys without a restart (key files and SIGHUP,
  `--studio-keys-only`, `--admin-keys-only`, `--backend-key-file`).

### Relay for client-authoritative games

- WebSocket relay with signed tickets and routing across replicas through Redis.
- Protocol v1.1, backward compatible: limits in the hello, `deadline_ms`, `retry_after_ms`,
  `bye`, an in-flight cap (`client_busy`) and a frame rate limit.
- `examples/relayclient` (Go) and `examples/unity-relay-client` (C#, with a Unity package layout).

### Operations

- Metrics, `/healthz` and `/readyz` on a separate ops listener (`--ops-addr`), including pgx and
  Redis pool and Go runtime metrics.
- Helm chart, Grafana dashboard, Prometheus alert rules and a runbook.
- Optional batched audit writes (`--audit-queue`), off by default.
- Load tool and a repeatable soak script. A one hour run at 100 calls per second held p99 at
  6.5 ms with flat memory (see the runbook).
- Signed multi-arch images on GHCR, binaries for six platforms, SBOMs and build provenance.

### Docs and examples

- A project site with a landing page, concepts, generated reference (manifest, flags, error codes,
  audit events, OpenAPI for the studio API), and eight tutorials whose scripts CI runs.
- Best practices, a cookbook, troubleshooting by result code and an FAQ.
- Signing examples in Go, Python, TypeScript and C#, and two pet-shop backends.

### Known limits

- Version `0.x`: the manifest format, the signed request and some protocol details may change in
  a minor release. Every change has an entry in `docs/DECISIONS.md`.
- The Unity package has not been run inside Unity. The relay was tried with the C# reference
  client and a Go client.
- Audit retention prunes with an hourly `DELETE`. Partitioning is a follow-up.
- Kill switches live in Redis, so without `--redis-addr` they are per process.
- No hosted offering.

[Unreleased]: https://github.com/Moustafa-Elgammal/agentgw/compare/v0.1.0...HEAD
[0.1.0]: https://github.com/Moustafa-Elgammal/agentgw/releases/tag/v0.1.0
