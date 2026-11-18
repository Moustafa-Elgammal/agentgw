# agentgw

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

Go 1.25+ (see `go.mod`). Module path: `github.com/Moustafa-Elgammal/agentgw`.

Tests that need other services (Postgres, Redis in M2) get their own compose services; never
require a host install.

## Architecture in one minute

Call pipeline in `internal/gateway`: lookup -> Admit (rate limit, counts every attempt) ->
validate args -> Reserve (cooldown, daily caps) -> connector call (signed) -> Settle
(keep or release) -> audit. Expected failures are `core.Result{OK:false, Code, Message}`,
never Go errors. Transport failures are Go errors so callers fail closed.

Packages: `core` (types), `manifest`, `policy`, `connector`, `audit`, `gateway`, `httpapi`
(dev only, trusts `X-Dev-Player`, loopback only), `pkg/agentsig` (public signing library).
Details: `docs/ARCHITECTURE.md`, protocol: `docs/PROTOCOL.md`, security: `docs/SECURITY-MODEL.md`.

`make fmt` only checks (fails if `gofmt -l` lists files); it does not rewrite. Fix with
`gofmt -w` inside the container. Slash commands `/check` and `/m1-spike` live in `.claude/commands/`.
The gateway is exercised end to end through the MCP endpoint (`/mcp`, `internal/mcpserver`) or the
dev HTTP API (`internal/httpapi`) against `examples/city/`. Both trust `X-Dev-Player`. The MCP
transport is stateless on purpose: identity is resolved per request, never carried by a session.

## Rules for changes

1. Behavior changes need tests. Policy and gateway tests are table-driven; keep them so.
2. Never move rate limiting after validation. Invalid attempts must count.
3. A failed backend call must release its reservation and must not start a cooldown.
4. Policy store errors fail closed.
5. Audit stores a hash of arguments, never the arguments.
6. Dependencies: M0 is standard library only. Adding one needs a reason in the PR and an
   entry in `docs/DECISIONS.md`.
7. Compatibility changes (manifest format, signed request, policy semantics) get an entry in
   `docs/DECISIONS.md` first.
8. Sign off commits (`git commit -s`). No real game or employer material anywhere.
9. Anything that needs a service or tool gets a Dockerfile target or compose service. Never ask
   the user to install it on the host.

## Current focus: M1 (see `docs/ROADMAP.md`)

Done: SDK spike (go-sdk v1.8.0, findings in `docs/DECISIONS.md`) and the `internal/mcpserver`
adapter calling `gateway.Service.Call`.

1. YAML manifest authoring format compiling to the same structure (decide the YAML library).
2. Conformance suite v0 under `test/conformance` driven by the official MCP client.
3. Remaining adapter work: MCP result mapping review, `structuredContent`, request cancellation.

## Testing against a game

Use `examples/city/` (fake REST backend that verifies signatures, started by `docker compose up`)
or the separate `../tinycity-mock` MCP server (`docker compose up -d mock` from the workspace root).
Do not point the gateway at any real game.
