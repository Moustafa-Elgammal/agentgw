# Changelog

All notable changes are listed here. The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/)
and the project follows [Semantic Versioning](https://semver.org). While the version is `0.x`,
the manifest format, the signed request and some protocol details may change in a minor release.
Each such change has an entry in [docs/DECISIONS.md](docs/DECISIONS.md).

## [Unreleased]

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
