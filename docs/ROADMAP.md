# Roadmap

Each milestone ends with something demonstrable. Dates are not set: this is a solo,
spare-time project.

| Milestone | Deliverables | Exit criterion | Status |
|---|---|---|---|
| M0 Foundations | Repo, CI, core types, manifest validator, policy (in memory), signing, REST connector, audit, pipeline, dev API | End-to-end call through a signed backend with caps, cooldown and audit | Done |
| M1 MCP transport | Official Go SDK, runtime tool registration from the manifest, streamable HTTP, YAML manifest, conformance suite v0 | An MCP client lists and calls tools through the gateway | Done |
| M2 Policy engine | Redis-backed Policy, concurrency tests, confirmations in Postgres, kill switches (switches, policy contract suite, Redis store, confirmations in memory and Postgres, dev admin endpoints) | Concurrent reserves never exceed a cap; Redis-down behavior matches the spec | Done |
| M3 Auth | Protected resource metadata, token validation, authorization server with link tickets and consent, grants and revocation | A scripted PKCE client completes the flow; wrong-audience and revoked tokens are rejected | Exit criterion met. Open: OIDC login at consent |
| M4 Studio and player APIs | Manifest upload and rollback, key rotation, grant list and revoke, activity feed | A studio onboards with a manifest and no backend code | Done. Manifest upload and rollback, key rotation, grant list and revoke, and the activity feed (player and admin) |
| M5 Relay | WebSocket hub, multi-replica routing, wire protocol for a Unity package | A sample game is played by an agent through the relay | Built: relay hub, tickets, Redis routing, `backend: relay`, a sample client. Hardening of the wire protocol for a Unity package is still open |
| M6 Hardening | Load and soak tests, SSRF and injection tests, dashboards, runbooks, Helm chart | Pilot-ready in Coach mode, then Operator mode behind a flag | Built: metrics and ops listener (`internal/obs`), SSRF-safe connector and injection tests, Helm chart, Grafana dashboard, alerts, runbook, load tool. Not done: a long soak run, a signed kill-switch endpoint, Operator mode flag |
| M7 Open source release | Pre-flight audit (independence, secrets, history, licenses), repository settings and protections, security and DCO automation, release pipeline (binaries, signed multi-arch images, SBOM, changelog), a README for strangers, decision on publishing `tinycity-mock` | The repository is public, `v0.1.0` is tagged and verifiable, and a stranger runs the demo in five minutes | Built, not yet published: audit (`docs/AUDIT-REPORT.md`), workflows, GoReleaser, signed images, SBOM, release and settings guides, README, `tinycity-mock` community files. Left to do: push, apply `docs/REPO-SETTINGS.md`, tag `v0.1.0` |
| M8 Project site and docs on GitHub Pages | Static site built from `docs/` in a container, landing page that shows the features, concepts and operate pages, generated reference (manifest, flags, errors, OpenAPI for the studio API), Pages deploy workflow, link and snippet checks | The site is live at the project's Pages URL and rebuilds on every merge | Planned. After M4 |
| M9 Developer guides | Seven getting-started tutorials, best practices, runnable examples in Go and Python, cookbook, troubleshooting by error code, guides verified by CI | A developer onboards a toy game by following the guides alone | Planned. After M8 skeleton |

The detailed plan for M7 to M9, including decisions still needed, is in
[OPEN-SOURCE-PLAN.md](OPEN-SOURCE-PLAN.md).

## M1 first task

Pin the official `github.com/modelcontextprotocol/go-sdk` version and prove two things in a
spike: registering tools from a runtime schema (not Go structs), and serving a different
tool set per request. Everything else in M1 builds on those two.
