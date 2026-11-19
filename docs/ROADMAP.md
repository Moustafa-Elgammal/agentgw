# Roadmap

Technical milestones only. Each one ends with something demonstrable. Dates are not set: this is a
solo, spare-time project. Milestone numbers are kept as they were, so references in
[DECISIONS.md](DECISIONS.md) and the code still point at the right place. Releasing, repository
settings, documentation and launch tasks are not milestones: they are in
[LAUNCH-CHECKLIST.md](LAUNCH-CHECKLIST.md) and [RELEASING.md](RELEASING.md).

| Milestone | Deliverables | Exit criterion | Status |
|---|---|---|---|
| M0 Foundations | Core types, manifest validator, in-memory policy, signing, REST connector, audit, pipeline, dev API | An end-to-end call through a signed backend with caps, cooldown and audit | Done |
| M1 MCP transport | Official Go SDK, runtime tool registration from the manifest, streamable HTTP, YAML manifests, conformance suite | An MCP client lists and calls tools through the gateway | Done |
| M2 Policy engine | Redis-backed policy, concurrency tests, confirmations in Postgres, kill switches | Concurrent reserves never exceed a cap; Redis-down behavior matches the spec | Done |
| M3 Auth | Protected resource metadata, token validation, authorization server with link tickets and consent, grants and revocation, OIDC login at consent | A scripted PKCE client completes the flow; wrong-audience and revoked tokens are rejected | Done. OIDC is tested against a fake provider, Dex (tutorial 8) and Keycloak (`scripts/oidc-keycloak-check.sh`), not yet against a hosted provider |
| M4 Studio and player APIs | Manifest upload and rollback, key rotation, grant list and revoke, activity feed | A studio onboards with a manifest and no backend code | Done |
| M5 Relay | WebSocket hub, signed tickets, multi-replica routing over Redis, wire protocol v1.1, C# reference client | A sample game is played by an agent through the relay | Done, except running the C# client inside Unity (tutorial 10 walks the flow with the console sample) |
| M6 Hardening | Metrics and ops listener, SSRF-safe connector and injection tests, Helm chart, Grafana dashboard, alerts, runbook, load and soak tools | Pilot-ready in Coach mode | Done. A 1 hour soak: 359,763 calls at 99.9 per second, p99 6.5 ms, 1 failure, flat memory, Redis keys and Postgres connections |
| M10 Pilot controls | Studio-signed kill switch API, `--max-scope` coach cap, request body limits, server timeouts, key rotation without restart | A pilot runs in Coach mode with a kill switch the studio can reach, then Operator mode by raising one flag | Done |
| M12 Audit and integration hardening | Audit batching writer proven in a 15 minute soak (done), audit table partitioning with a retention job ([design](design/audit-partitioning.md)), OIDC against Keycloak and a public-client mode (both done), the C# relay client run in Unity and packaged for the Unity Package Manager | A pilot can run for weeks without Postgres bloat, with a common identity provider, and a Unity developer can install the client from a package | In progress. The audit batching soak is done (15 minutes, 89,900 calls, 0 dropped, flat memory; see the runbook). Partitioning is a proposal |
