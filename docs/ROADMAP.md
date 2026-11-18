# Roadmap

Each milestone ends with something demonstrable. Dates are not set: this is a solo,
spare-time project.

| Milestone | Deliverables | Exit criterion | Status |
|---|---|---|---|
| M0 Foundations | Repo, CI, core types, manifest validator, policy (in memory), signing, REST connector, audit, pipeline, dev API | End-to-end call through a signed backend with caps, cooldown and audit | Done |
| M1 MCP transport | Official Go SDK, runtime tool registration from the manifest, streamable HTTP, YAML manifest, conformance suite v0 | An MCP client lists and calls tools through the gateway | Done |
| M2 Policy engine | Redis-backed Policy, concurrency tests, confirmations in Postgres, kill switches | Concurrent reserves never exceed a cap; Redis-down behavior matches the spec | Next |
| M3 Auth | Protected resource metadata, token validation, authorization server with link tickets and consent, grants and revocation | A scripted PKCE client completes the flow; wrong-audience and revoked tokens are rejected | |
| M4 Studio and player APIs | Manifest upload and rollback, key rotation, grant list and revoke, activity feed | A studio onboards with a manifest and no backend code | |
| M5 Relay | WebSocket hub, multi-replica routing, wire protocol for a Unity package | A sample game is played by an agent through the relay | |
| M6 Hardening | Load and soak tests, SSRF and injection tests, dashboards, runbooks, Helm chart | Pilot-ready in Coach mode, then Operator mode behind a flag | |

## M1 first task

Pin the official `github.com/modelcontextprotocol/go-sdk` version and prove two things in a
spike: registering tools from a runtime schema (not Go structs), and serving a different
tool set per request. Everything else in M1 builds on those two.
