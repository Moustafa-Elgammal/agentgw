# agentgw: contributor guide

Works for people and for coding agents. Claude Code reads it through `CLAUDE.md`.

Go gateway between a player's AI agent and a game backend. See `README.md` and `docs/`.

## Docker first

Always run toolchain commands inside containers. Do not install Go, golangci-lint or
govulncheck on the host, and do not run `go` or `make` directly on the host.

```bash
docker compose run --rm dev make test      # go test -race -count=1 ./...
docker compose run --rm dev make vet fmt
docker compose run --rm dev go test -race -count=1 -run TestName ./internal/policy/...   # one test or package
docker compose run --rm dev make all       # fmt vet test build (binaries land in bin/)
docker compose run --rm dev make lint      # golangci-lint
docker compose run --rm dev make vuln      # govulncheck
docker compose run --rm dev bash           # shell in the toolchain container
docker compose run --rm dev go run ./cmd/gwctl validate examples/city/manifest.json

docker compose up --build                  # gateway (dev API on 127.0.0.1:8080) + fake city backend
docker compose down
```

Shortcuts: `make docker-test`, `docker-vet`, `docker-fmt`, `docker-lint`, `docker-vuln`,
`docker-shell`, `up`, `down` (these call the compose commands above).

Images (see `Dockerfile`): `dev` (golang + gcc for -race + govulncheck + golangci-lint),
`gateway` and `city-backend` (distroless, non-root, static binaries). Source is mounted into
`dev`; module and build caches live in named volumes. Published ports bind to 127.0.0.1 only.
The signing secret comes from `AGENTGW_SIGNING_SECRET` (default `dev-secret`, dev only).

Go 1.26 (see `go.mod`; the `toolchain` line pins the patch release CI uses. Bump it when `make vuln` or CI reports a standard library fix). Module path: `github.com/Moustafa-Elgammal/agentgw`.

Tests that need other services (Postgres, Redis in M2) get their own compose services; never
require a host install.

## Architecture in one minute

Call pipeline in `internal/gateway`: lookup -> Admit (rate limit, counts every attempt) ->
validate args -> confirmation gate (tools with `confirm`) -> Reserve (cooldown, daily caps) -> connector call (signed) -> Settle
(keep or release) -> audit. Expected failures are `core.Result{OK:false, Code, Message}`,
never Go errors. Transport failures are Go errors so callers fail closed.

Packages: `core` (types), `manifest`, `policy`, `confirm`, `connector`, `audit`, `gateway`, `httpapi`
(dev only, trusts `X-Dev-Player`, loopback only), `pkg/agentsig` (public signing library).
Details: `docs/ARCHITECTURE.md`, protocol: `docs/PROTOCOL.md`, security: `docs/SECURITY-MODEL.md`.

`make fmt` only checks (fails if `gofmt -l` lists files); it does not rewrite. Fix with
`gofmt -w` inside the container. The `/check` slash command (format, vet, test in Docker) lives in `.claude/commands/` for Claude Code users.
The gateway is exercised end to end through the MCP endpoint (`/mcp`, `internal/mcpserver`) or the
dev HTTP API (`internal/httpapi`) against `examples/city/`. Both trust `X-Dev-Player`. The MCP
transport is stateless on purpose: identity is resolved per request, never carried by a session.

## Rules for changes

1. Behavior changes need tests. Policy and gateway tests are table-driven; keep them so.
2. Never move rate limiting after validation. Invalid attempts must count.
3. A failed backend call must release its reservation and must not start a cooldown.
4. Policy store errors fail closed.
5. Audit stores a hash of arguments, never the arguments.
6. Dependencies are kept minimal (today: `go-sdk`, `goccy/go-yaml`, `go-redis`, `pgx`, `coder/websocket`). Adding one needs a reason in
   the PR and an entry in `docs/DECISIONS.md`, and ask first.
7. Compatibility changes (manifest format, signed request, policy semantics) get an entry in
   `docs/DECISIONS.md` first.
8. Sign off commits (`git commit -s`). No third-party or proprietary material anywhere (no real game, company or customer data).
9. Anything that needs a service or tool gets a Dockerfile target or compose service. Never ask
   the user to install it on the host.

## Current focus: M5 and most of M6 built, M7 next (see `docs/ROADMAP.md`)

M6: `internal/obs` (metrics, `--ops-addr`), SSRF-safe connector (`--backend-allow-private`), `deploy/` (Helm, Grafana, alerts), `docs/RUNBOOK.md`, `test/load`. Check deploy files with `make deploy-check`.

M5: `internal/relay` (Hub, signed tickets, `Handler`, `MintHandler`, `Bus` with Memory and `redisbus`, contract suite `bustest`), `backend: relay` tools via `connector.Mux`; enabled by `AGENTGW_RELAY_SECRET`. Decisions in `docs/DECISIONS.md`.

M4 so far: versioned manifests (`internal/manifeststore`, memory plus `pgstore`), hot swap in
`gateway.Service.SetManifest`, and admin-signed `/v1/studio/manifest` endpoints (separate admin key,
`AGENTGW_ADMIN_SECRET`). Key rotation for studio and admin keys is done (key files, SIGHUP). The activity feed is done (`internal/activity`, `internal/audit` Store and `pgstore`, `--audit-retention`): a player feed on the studio key and an admin feed on the admin key. M4 exit criterion met. The M3 notes below still apply.

M0 to M2 are done. Pipeline and signing; MCP endpoint (`internal/mcpserver`, stateless streamable
HTTP); JSON or YAML manifests; conformance suite v0 (`test/conformance`); kill switches
(`policy.Switch`); Redis policy store (`internal/policy/redisstore`, `--redis-addr`); player
confirmations for tools with `confirm: true` (`internal/confirm`, Postgres store `pgstore`,
`--postgres-url`; flow in `docs/DECISIONS.md`). New policy or confirmation stores must pass
`policytest.Run` or `confirmtest.Run`. The `dev` service starts `redis` and `postgres` and sets
`AGENTGW_TEST_REDIS_ADDR` and `AGENTGW_TEST_POSTGRES_URL`; those tests skip without them, CI runs
them with service containers.

M3 progress: the auth store (`internal/auth`, memory plus `internal/auth/pgstore` for Postgres),
bearer validation with audience binding (`ResourceServer`), the embedded authorization server
(`server.go`, `authorize.go`, `token.go`: DCR, PKCE S256, consent page, link tickets, rotating
refresh), and the studio-signed grant list and revoke endpoints (`studio.go`, `StudioHandler`) are
built and tested, with the scripted PKCE conformance test in `test/conformance/auth_test.go`.
`cmd/gateway` uses OAuth unless `--dev-auth` is set; compose keeps `--dev-auth` for the demo.

`cmd/gateway` shares one Postgres pool (`--postgres-url`) between the confirmation and auth stores,
prunes expired OAuth rows hourly, and mounts `StudioHandler`.

Studio-signed confirmation endpoints (`confirm.StudioHandler`) are the player's approve and deny path
in OAuth mode. Still open: OIDC login at consent (library choice pending), a shared replay cache across
replicas (studio-signed requests are replay checked per process by `internal/studiosig`, one
verifier shared by every studio endpoint). Registration limits, trusted client
address header and narrow CORS are done (`--register-per-hour`, `--max-clients`,
`--client-ip-header`; limiters are per process until M6). MCP result
polish is done: results carry `structuredContent` and a permissive `outputSchema`, and a cancelled
call stops the backend request (see `docs/DECISIONS.md`).

## Testing against a game

Use `examples/city/` (fake REST backend that verifies signatures, started by `docker compose up`)
or the separate `../tinycity-mock` MCP server (`docker compose up -d mock` from the workspace root).
Do not point the gateway at any real game.
