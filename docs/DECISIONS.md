# Decisions and open questions

## Decided

| Decision | Choice | Notes |
|---|---|---|
| License | Apache-2.0 | Patent grant, easy for studios to adopt |
| Contribution terms | DCO sign-off | No CLA |
| Language | Go | Single static binary, good concurrency |
| M0 dependencies | Standard library only | Keeps the core auditable |
| Manifest format in M0 | JSON | YAML authoring format comes with M1 |
| Cap unit in M0 | Units of an integer argument | Value-based caps are open |
| Test environment | A separate fictional city simulation | Lives outside this repo |
| MCP SDK | `github.com/modelcontextprotocol/go-sdk` v1.8.0 | First dependency. Official SDK; raises the Go floor to 1.25. Pulled in by the M1 spike |

## Open

- [ ] Authorization server: embed a minimal one in Go, or use an external one (Keycloak, Ory Hydra, Zitadel)?
- [ ] Hosting: hosted multi-tenant service, self-hosted only, or both from the start?
- [ ] Value-based caps: add a backend dry-run call so the gateway can cap coins, or keep caps on calls and numeric arguments?
- [ ] Audit: hashes only, or redacted arguments as an opt-in?
- [ ] Relay routing across replicas: Redis pub/sub or a broker such as NATS?
- [ ] Player identity at consent: link tickets only, or also OIDC against the studio's identity provider?
- [ ] Which Go SDK release to pin, and how to track MCP spec revisions after 2026-07-28?
- [ ] Open core: what, if anything, stays out of the open-source repository?

## M1 spike findings (go-sdk v1.8.0)

- Tools register from a raw JSON schema: `Server.AddTool(&mcp.Tool{InputSchema: json.RawMessage(...)}, handler)`. No Go structs needed. The schema must be a JSON object with `type: "object"`, otherwise `AddTool` panics, so the adapter must validate compiled schemas at manifest load time.
- The non-generic `AddTool` does not validate arguments against the schema. The gateway's own validation (`internal/gateway/validate.go`) stays the single authority, which also keeps "invalid attempts count against the rate limit" intact.
- Per-player tool sets work with `mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server, nil)`: the factory sees each request's headers and returns a server with that player's tools. With the default stateful mode the factory runs when a session is created, so identity is fixed per session. Principal must come from the validated token, not from a header, once M3 lands.
- A call to a tool that was not registered for that player is rejected by the SDK.
- Test teardown: close client sessions before `httptest.Server.Close`, or open SSE streams block it.
