# Decisions and open questions

## Decided

| Decision | Choice | Notes |
|---|---|---|
| License | Apache-2.0 | Patent grant, easy for studios to adopt |
| Contribution terms | DCO sign-off | No CLA |
| Language | Go | Single static binary, good concurrency |
| M0 dependencies | Standard library only | Keeps the core auditable |
| Manifest format | JSON wire format, YAML authoring format (M1) | Both load through `manifest.LoadFile` by extension |
| Cap unit in M0 | Units of an integer argument | Value-based caps are open |
| Test environment | A separate fictional city simulation | Lives outside this repo |
| YAML library | `github.com/goccy/go-yaml` v1.19.2 | Strict mode rejects unknown fields and duplicate keys, matching the JSON loader. `yaml.v3` is archived. `sigs.k8s.io/yaml` was rejected for weaker error positions. YAML is authoring only: it compiles to the same `Manifest` as JSON |
| Redis client | `github.com/redis/go-redis/v9` v9.23.0 | Shared policy store. Every decision is one Lua script, so Admit, Reserve and Settle are atomic across replicas. Single node or primary/replica only: scripts touch the global switch set, so Redis Cluster is out of scope |
| Postgres driver | `github.com/jackc/pgx/v5` v5.13.0 | Confirmation store. Claims are one atomic UPDATE and a partial unique index keeps one live confirmation per call. Schema is created by `Migrate` (idempotent), no migration framework yet |
| Policy store contract | `internal/policy/policytest` | Every store must pass the same suite. Memory and Redis both do |
| Dev admin API | `GET/POST /v1/admin/switches` behind `X-Dev-Admin: true` | Studio side of kill switches until M4. Header trust means loopback only |
| Kill switches | `policy.Switch` with wildcard tenant, tool, player | Checked first in Admit and again in Reserve. Not charged to the rate limit. Block every tier they match |
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

## Auth (M3)

Decided with the user: the gateway is an OAuth 2.1 resource server and embeds a minimal
authorization server. A player proves who they are at consent with link tickets, and also with
OIDC against the studio's identity provider.

- **Opaque tokens, stored hashed.** Tokens are 256 bit random strings, kept as SHA-256 in the
  store. No JWT library, and revocation is immediate because validation is a lookup. Access tokens
  live 15 minutes. Refresh tokens live 30 days, rotate on every use, and reuse of a rotated token
  revokes the whole grant.
- **PKCE S256 required.** No implicit or password flow. Redirect URIs match exactly, except that
  loopback `http://127.0.0.1` and `http://[::1]` clients may vary the port (RFC 8252). Clients
  register themselves (RFC 7591) as public clients.
- **Audience binding.** `resource` (RFC 8707) is required at authorize and token. A token is bound to
  one resource, the game's MCP URL, and the gateway rejects a token whose audience is not its own.
- **Scopes.** `coach` allows read tools. `operator` allows read and action tools. The player picks
  one at consent. tools/list shows only what the scope allows and the pipeline enforces it
  (`scope_insufficient`), so a coach grant cannot call an action tool by guessing its name.
- **Grants.** One active grant per (player, client, resource). Consenting again updates its scope.
  Revoking it kills every token of the grant. Players list and revoke their grants.
- **Link tickets.** The studio's backend mints a single-use ticket (5 minutes) for a logged-in
  player through a request signed like any other studio call (`agentsig`), and sends the player to
  the consent page with it. The ticket carries the player id.
- **OIDC.** Added after the ticket flow, needs a dependency, decided when built.
- **Dev auth.** `X-Dev-Player` and `X-Dev-Admin` stay behind an explicit `--dev-auth` flag. Without
  it, requests need a valid bearer token.
- **Tenants.** One tenant per gateway process in M3 (`--tenant`). Per-tenant tokens and keys come with M4.

### Auth store on Postgres (`internal/auth/pgstore`)

- Tables `oauth_clients`, `oauth_tickets`, `oauth_codes`, `oauth_grants`, `oauth_access_tokens`,
  `oauth_refresh_tokens`. Only hashes are stored. `Migrate` is idempotent.
- Single use tickets and codes are one conditional `UPDATE ... RETURNING`. One active grant per
  (tenant, player, client, resource) is a partial unique index on `revoked_at IS NULL`, and
  `UpsertGrant` is `INSERT ... ON CONFLICT DO UPDATE`, so a revoked grant never returns.
- Revocation sets `revoked_at`. Token lookups join the grant, so it is effective on the next call.
- `RotateRefresh` locks the old token row (`FOR UPDATE`) in a transaction. One caller rotates,
  every other caller sees `rotated` and gets `ErrReuse`, and the grant is revoked in that commit.
- `Prune` deletes expired rows and grants revoked over 30 days ago. Rotated refresh tokens stay
  until they expire so reuse is still detected. The caller schedules it; no goroutines.

## Confirmations (M2)

A tool marked `confirm: true` needs the player's approval for each call. The agent cannot approve
for itself: approval comes from the player through an authenticated channel (the dev HTTP API
now, the player API in M4).

Flow, in pipeline order: Admit, validate, **confirmation gate**, Reserve, backend call, Settle.

1. First call: no approval exists. The gateway records a pending confirmation for
   (tenant, player, tool, SHA-256 of the validated arguments) and answers `confirmation_required`
   with `data.confirmation_id`. Repeating the same call while it is pending returns the same id.
2. The player approves or denies. Only the owning player can.
3. The agent repeats the identical call. The gateway claims the approval (approved to claimed,
   atomic, single use), then runs Reserve and the backend call. On success the confirmation is
   consumed. If the call fails the claim returns to approved, so a backend outage does not cost
   the player a fresh approval. While a claim is executing, an identical call gets
   `confirmation_in_use`. A denial answers `confirmation_denied` until it expires, so a denied
   call cannot nag the player. An expired confirmation is simply replaced by a new pending one.
4. Arguments are hashed from the validated, key-sorted JSON, so whitespace and key order do not
   matter, and any changed value needs a new approval.

Rules: read tools cannot require confirmation (nothing to confirm). `confirm` is independent of
`tier`: the sensitive tier stays disabled in v1. A confirmation store error fails closed
(`confirmation_unavailable`). Expiry is 10 minutes from the request, for every state. Stores: in memory for tests
and single-process use, Postgres (pgx v5) for shared use. Both pass one contract suite.

## M1 spike findings (go-sdk v1.8.0)

- Tools register from a raw JSON schema: `Server.AddTool(&mcp.Tool{InputSchema: json.RawMessage(...)}, handler)`. No Go structs needed. The schema must be a JSON object with `type: "object"`, otherwise `AddTool` panics, so the adapter must validate compiled schemas at manifest load time.
- The non-generic `AddTool` does not validate arguments against the schema. The gateway's own validation (`internal/gateway/validate.go`) stays the single authority, which also keeps "invalid attempts count against the rate limit" intact.
- Per-player tool sets work with `mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server, nil)`: the factory sees each request's headers and returns a server with that player's tools. With the default stateful mode the factory runs when a session is created, so identity is fixed per session. Principal must come from the validated token, not from a header, once M3 lands.
- A call to a tool that was not registered for that player is rejected by the SDK.
- Test teardown: close client sessions before `httptest.Server.Close`, or open SSE streams block it.

## MCP results: structuredContent, outputSchema, cancellation

**structuredContent.** Every result carries the same body twice: a JSON text block (MCP says to
keep it) and `structuredContent` (`ok`, optional `code`, `message`, `data`). A test decodes both
with the official client and checks they are equal for success with data, for a denial with a
code and for a cap denial.

**outputSchema: declared, one permissive schema for all tools.** In go-sdk v1.8.0 the raw
`Server.AddTool` does not validate results ("Validating the result against the output schema is
the caller's responsibility"), and the Go client does not either, so the schema is
documentation for the Go ecosystem. Other clients may validate `structuredContent` against it.
A denial has no `data` and is returned with `isError`, so a schema that required `data` would
make strict clients reject every error. The schema therefore requires only `ok` (boolean), types
`code` and `message` as strings and leaves `data` unconstrained (backend data, untrusted). It
covers both shapes. Tightening it later (for example per tool `data` shapes) is a compatibility
change and needs an entry here first.

**Cancellation.** The context chain is: MCP handler ctx, `Service.Call`, `connector.HTTP.Call`
(`http.NewRequestWithContext`). Cancelling it aborts the backend request. The problem was
getting the signal in. `StreamableHTTPOptions.PropagateRequestCancellation` only applies to
requests using protocol version 2026-07-28 or later ("Requests using older protocol versions are
unaffected"), and the pinned SDK computes it as `usesNewProtocol && ...`. This server speaks the
older versions today, and in stateless mode the client's `notifications/cancelled` is a separate
POST that reaches a different, new server instance, so it cannot find the in-flight request.
A test showed the backend request kept running until its own timeout. We therefore leave the
option off (revisit when the client base moves to the new protocol) and the tool handler joins
its context with the HTTP request context (`joinCancel`). Stateless mode has one POST per call,
so the request context ends exactly when the client disconnects or gives up.

Bookkeeping after a cancel: the release of the reservation, the confirmation claim settle and
the audit event use a context detached from cancellation (values kept) with a 5 second
deadline. Otherwise a networked store (Redis, Postgres) would refuse the release on a cancelled
context and leak the cap, and the audit event would be lost. A failed or cancelled call never
starts a cooldown (unchanged rule). Exactly one audit event is written per call.

**What a cancel means for a call that reached the backend.** The outcome is ambiguous: the
backend may or may not have applied the action. The gateway cannot know. It releases the
reservation (so the daily cap is not burned by a call that may have done nothing) and answers
`outcome_unknown`: "the call was cut off before the game answered (cancelled or timed out). The
game may or may not have applied it. Check the game state before trying again." The audit event
is outcome `error`, code `outcome_unknown`. The same code is used when the connector's own
timeout fires mid-request. A refused connection stays `backend_unavailable` ("nothing was
changed") because nothing was sent. Releasing means the cap can be claimed again even if the
backend did apply the action, so the cap can be overshot by at most the amount of the cut off
calls. We accept that over locking the cap for an unknown outcome.

**What the idempotency key is for.** Each gateway attempt sends one fresh key
(`X-AG-Idempotency-Key`). It lets a backend de-duplicate transport level repeats of that same
attempt (a proxy or HTTP client retrying a request that already arrived). It does not make an
agent level retry safe, because a retry is a new call with a new key. Safe retry after
`outcome_unknown` is the agent's job: read the game state first. Letting the agent supply a
stable key so a backend can de-duplicate real retries is possible later, but it changes the
protocol and needs its own entry first.
