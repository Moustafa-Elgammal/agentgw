# Changelog

All notable changes are listed here. The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/)
and the project follows [Semantic Versioning](https://semver.org). While the version is `0.x`,
the manifest format, the signed request and some protocol details may change in a minor release.
Each such change has an entry in [docs/DECISIONS.md](docs/DECISIONS.md).

## [Unreleased]

### Added

- Optional batched audit writes: `--audit-queue`, `--audit-batch-size`, `--audit-flush-interval` and `--audit-overflow`. Events go on a bounded queue and one writer sends them with a single `COPY`, so a call no longer waits for Postgres and commits fall sharply. Off by default (queue 0 keeps the synchronous write). Queued events are lost on a crash and written on a graceful stop. New metrics: `audit_queue_depth`, `audit_dropped_total{reason}`, `audit_batch_size`, `audit_flush_duration_seconds`, `audit_flush_errors_total`. See `docs/design/audit-write-path.md`.
- OIDC sign-in at the consent page, built behind an interface and off by default: `internal/auth/oidc` (flow, claim checks, sealed session cookie), `POST /authorize/oidc` and `GET /authorize/oidc/callback`. The ID token signature check and the flags wait for a dependency decision. See `docs/design/oidc-consent.md`.
- Relay protocol v1.1 (backward compatible): the hello announces limits, calls carry
  `deadline_ms`, results may carry `retry_after_ms`, clients may send `hello` and `bye`, at most
  `--relay-max-in-flight` calls wait per socket (`client_busy`), and a client sending more than
  `--relay-max-frames-per-sec` frames is closed. Metrics `relay_busy_total` and
  `relay_socket_closes_total`.
- `examples/unity-relay-client`, a dependency-free C# reference client with a console game and
  a Unity package layout.

- Rotate the primary keys without a restart: `--studio-keys-only` and `--admin-keys-only` retire the key from the environment, and `--backend-key-file` reloads the key that signs backend calls on SIGHUP. Reloads log the key ids added and removed and count in `agentgw_key_reloads_total{kind,result}`. The example backend accepts a key file (`AGENTGW_SIGNING_KEYS_FILE`).
- `--postgres-max-conns` and `--postgres-min-conns` for the shared Postgres pool, and the effective pool settings logged at start.
- Metrics for the Postgres and Redis pools (`agentgw_pgx_pool_*`, `agentgw_redis_pool_*`) and the Go runtime (`go_goroutines`, `go_memstats_*`, `go_gc_*`, `process_open_fds`), read at scrape time. New Grafana panels and the alerts `AgentgwPostgresPoolSaturated` and `AgentgwGoroutinesGrowing`.
- Studio kill switch API: `GET`, `PUT` and `DELETE /v1/studio/switches`, signed with the admin key.
  A tool, a player or the whole game can be switched off and on without a restart. Every change is
  an audit event (`admin.switch.on`, `admin.switch.off`).

- `--max-scope coach|operator` (`AGENTGW_MAX_SCOPE`, Helm `config.maxScope`): a studio can cap every
  grant at read only for a first pilot. The consent page offers only coach, tokens and refreshes
  report coach, and stored operator grants act as coach until the cap is raised.
- `--max-body`: request bodies are capped at the front door (1 MiB, 64 KiB for OAuth and studio
  endpoints) with a clean `413 {"error":"too_large"}`. Keep-alive connections close after 120 s idle.

### Fixed

- A relay client that never answered could hold slots for calls from other replicas forever; they
  now expire with the caller's deadline.

### Changed

- Examples, tutorials and docs now write manifests in YAML. JSON is still supported and stored.
  The Helm chart takes `manifestFile` (default `manifest.yaml`) so the seed manifest can be either.

## [0.1.0] - unreleased

First public release.

### Added

- Call pipeline: lookup, rate limit, validation, confirmation, reserve, signed backend call,
  settle, audit. Policy outages fail closed.
- Manifest (JSON or YAML) with strict validation, and the `gwctl` CLI.
- MCP endpoint (stateless streamable HTTP) and the `gwbridge` stdio bridge for desktop clients.
- OAuth 2.1 resource server with an embedded authorization server (PKCE, dynamic client
  registration, link tickets, consent, grants, rotating refresh tokens).
- Redis policy store with kill switches. Postgres stores for confirmations, auth, manifests
  and the audit feed.
- Studio APIs: grants, confirmations, manifest upload and rollback, activity feed, key rotation.
- Relay for client-authoritative games over WebSocket, with routing across replicas.
- Metrics, health and readiness on a separate ops listener. SSRF-safe connector.
- Helm chart, Grafana dashboard, Prometheus alerts, runbook, load tool.
- `pkg/agentsig`, the public request signing library.

[Unreleased]: https://github.com/Moustafa-Elgammal/agentgw/compare/v0.1.0...HEAD
[0.1.0]: https://github.com/Moustafa-Elgammal/agentgw/releases/tag/v0.1.0
