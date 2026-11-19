# Architecture

agentgw is a stateless gateway. A player's AI agent connects to a per-game endpoint,
every call passes one pipeline, and approved calls reach the studio's backend as signed
HTTP requests. The gateway holds no game logic.

## Call pipeline

Implemented in `internal/gateway`. The order is deliberate.

| Step | What happens | Why this position |
|---|---|---|
| 1. Lookup | Find the tool in the manifest | Unknown tools are audited as `unknown_tool` |
| 2. Admit | Tier switch and per-minute rate limit | Counts the attempt before validation, so bad requests cannot dodge the limit |
| 3. Validate | Check arguments against the manifest params | Errors name the field and the fix so the agent can self-correct |
| 4. Reserve | Cooldown and daily caps, claimed atomically | Concurrent calls cannot both pass a cap that has room for one |
| 5. Call | Signed request to the backend | Transport errors are Go errors, so the caller fails closed |
| 6. Settle | Keep the claim on success, release it on failure | A failed call does not burn the daily cap or start a cooldown |
| 7. Audit | Record the outcome, including denials | The log is complete, not only successes |

Expected failures are results (`ok:false`, `code`, `message`), not protocol errors.

## Packages

```text
cmd/gateway        dev server (M0)
cmd/gwctl          manifest CLI
internal/core      shared types: Tool, Tier, Principal, Caps, Result
internal/manifest  strict JSON and YAML manifest loader and validator
internal/manifeststore  versioned manifests: Store, Manager (upload, rollback, bootstrap, sync), admin Handler, pgstore, storetest contract
internal/studiosig one verifier for studio and admin signed requests: key sets, rotation, replay cache
internal/policy    Policy and Switchboard interfaces, in-memory store
internal/policy/redisstore  shared Redis store (Lua scripts, atomic across replicas)
internal/policy/policytest  the contract every store must pass
internal/connector Connector interface, level 0 REST connector, Mux (picks the connector by tool backend)
internal/relay     WebSocket relay for client-authoritative games: Hub, tickets, handlers, Bus (Memory); redisbus and bustest below it
internal/audit     Auditor and Store interfaces, Ring (memory), slog, pgstore (Postgres)
internal/activity  activity feed endpoints: player feed (studio key) and admin feed (admin key)
internal/gateway   the pipeline (the tool set is swapped whole by SetManifest, safe during calls)
internal/confirm   player confirmations: Store interface, memory store, pgstore (Postgres), confirmtest contract
internal/auth/pgstore   auth.Store on Postgres (clients, tickets, codes, grants, token hashes); Migrate and Prune
internal/httpapi   dev-only JSON front door (also the player's approve and deny endpoints)
internal/mcpserver MCP front door (streamable HTTP, stateless, per-request principal)
pkg/agentsig       request signing and verification (importable by Go backends)
examples/city      example manifest and a fake backend
```

Planned packages (see ROADMAP): `obs` (metrics and tracing).

## Stdio bridge

`cmd/gwbridge` (package `internal/bridge`) lets stdio-only desktop MCP clients use the gateway.
It is an MCP server on stdio and an MCP client of the gateway's `/mcp`, with fixed extra headers
(a bearer token, or `X-Dev-Player` for `--dev-auth`). It answers `tools/list` and `tools/call`
straight from the upstream on every request, so a changed tool set shows up without a restart.
Results, including `isError` denials, pass through unchanged. A transport failure becomes a tool
error so the stdio session survives; a local cancel cancels the upstream call. It makes no
policy decisions and never logs to stdout.

## Integration levels

| Level | Studio effort | How |
|---|---|---|
| 0 | A manifest | Tools map onto existing REST endpoints (`http.method`, `http.url`) |
| 1 | One signed endpoint | Backend verifies the signature and handles tool calls, any language |
| 2 | Optional SDKs | Decorators and types on top of the same protocol |

## Interfaces that matter

```go
type Policy interface {
    Admit(ctx, Principal, Tool) (Decision, error)
    Reserve(ctx, Principal, Tool, amount int64) (Reservation, Decision, error)
    Settle(ctx, Reservation, ok bool) error
}

type Connector interface {
    Call(ctx, Principal, Tool, args map[string]any, idempotencyKey string) (Result, error)
}
```

The Redis-backed `Policy` replaces the in-memory one (`--redis-addr` or `AGENTGW_REDIS_ADDR`)
without touching the pipeline. If Redis is unreachable every call is refused with
`policy_unavailable` and audited as an error. Sensitive tools are refused without asking Redis.
