# Features and components

New here? Start with the [overview](get-started/overview.md), the [quickstart](get-started/quickstart.md) and [how a call flows](concepts/pipeline.md).

What is built today and where it lives in the code. The roadmap is in [ROADMAP.md](ROADMAP.md).

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
| Studio kill switch API: switch a tool, a player or the game off and on, audited (admin-signed) | `internal/switchapi` |
| MCP endpoint: tools from the manifest, one server per authenticated request, sensitive tools hidden | `internal/mcpserver` |
| OAuth 2.1 resource server and embedded authorization server (PKCE, DCR, link tickets, consent, grants, revocation) | `internal/auth` |
| OIDC sign-in at consent: discovery, PKCE, nonce, signature check with go-oidc, sealed session cookie, claim to player mapping | `internal/auth/oidc`, `cmd/gateway/oidc.go` (confidential or public client with `--oidc-public-client`) |
| Versioned manifests: upload, list, roll back, hot swap, replica sync (admin-signed, memory or Postgres) | `internal/manifeststore`, `internal/manifeststore/pgstore` |
| Studio key rotation: key sets, key files, reload on SIGHUP | `internal/studiosig` |
| Optional batched audit writes: bounded queue, one `COPY` per batch, drop-and-count overflow, drained on shutdown (`--audit-queue`) | `internal/audit` (`Batcher`), `internal/audit/pgstore` |
| Activity feed: what a player's agents did, player and admin views, retention (memory or Postgres) | `internal/activity`, `internal/audit/pgstore` |
| Manifest CLI | `cmd/gwctl` |

Optional sign in with the studio's own OIDC identity provider at the consent page (`--oidc-issuer`; see [the design](design/oidc-consent.md) and the [runbook](RUNBOOK.md#sign-in-with-your-identity-provider-oidc)). Set `--postgres-url` to
keep clients, grants and approvals across restarts.
With `--dev-auth` (the compose demo) the dev HTTP API and the MCP endpoint take the player from a
header and must stay on loopback.

Pilot control: `--max-scope coach` makes the whole gateway read only whatever players granted, and `--max-body` caps request bodies at the front door.

Also built: the relay for client-authoritative games (`internal/relay`, protocol 1.1 with limits, deadlines and backpressure, see [PROTOCOL.md](PROTOCOL.md); a C# reference client for Unity in `examples/unity-relay-client`), metrics and health on a separate ops listener (`internal/obs`), an SSRF-safe connector, a Helm chart, a runbook and a load tool.

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
(`/v1/studio/confirmations`). The studio's own tooling manages the manifest through
`/v1/studio/manifest`, signed with a separate admin key. See `docs/PROTOCOL.md`.

Flags: `--public-url` (issuer, default `http://127.0.0.1:8080`), `--studio-key-id` (secret is
`AGENTGW_SIGNING_SECRET`), `--studio-keys-file` and `--admin-keys-file` (extra keys for rotation,
reloaded on SIGHUP), `--admin-key-id` (secret is `AGENTGW_ADMIN_SECRET`), `--manifest-poll`
(how often replicas pick up a manifest change, default 15s), `--register-per-hour`, `--max-clients`, `--client-ip-header`, `--ops-addr` (loopback `/healthz`, `/readyz`, `/metrics`, default `127.0.0.1:9090`). The scripted end to end test is `test/conformance/auth_test.go`.
Grants live in memory for now, so restarting the gateway makes players link again.
