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
internal/manifest  strict JSON manifest loader and validator
internal/policy    Policy interface + in-memory implementation
internal/connector Connector interface + level 0 REST connector
internal/audit     Auditor interface, memory and slog implementations
internal/gateway   the pipeline
internal/httpapi   dev-only JSON front door
pkg/agentsig       request signing and verification (importable by Go backends)
examples/city      example manifest and a fake backend
```

Planned packages (see ROADMAP): `mcpserver` (MCP transport), `auth` and `authserver`
(OAuth 2.1), `store` (Postgres, Redis), `relay` (WebSocket relay for client-authoritative
games), `admin` (studio and player APIs), `obs` (metrics and tracing).

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

A Redis-backed `Policy` replaces the in-memory one in M2 without touching the pipeline.
