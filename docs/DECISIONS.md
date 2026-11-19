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
- [x] Relay routing across replicas: Redis pub/sub and presence keys (decided for M5; revisit NATS if pilot scale needs it).
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

## Public endpoint hardening (M3)

Three weaknesses in the authorization server's public endpoints, fixed together.

- **Registration is bounded.** `POST /register` is limited per client address (default 10 per
  hour, `ServerConfig.RegisterPerHour`, 429 with `Retry-After` and an RFC 7591 style error body).
  Every attempt counts, valid or not, as with the ticket limiter. The server also refuses to
  register more than `MaxClients` clients (default 10000, answered 503). The `auth.Store`
  interface does not grow a count method, because another store implementation exists, so the
  cap is counted in the `Server`. **Limitation:** the count is process local. It starts at zero
  after a restart, even if the store already holds clients, and each replica counts alone. It
  bounds growth per process, it is not a global quota. A store backed count can replace it when
  the interface next changes. Registration is also stricter: duplicate redirect URIs and URIs
  over 2048 bytes are rejected, and `client_name` must be printable text (no control, format or
  bidi override characters, valid UTF-8). The name is shown on the consent page, so this limits
  spoofing of what the player reads.
- **One client address helper.** The ticket limiter and the registration limiter both key on
  `Server.clientAddr`. By default that is the host of `RemoteAddr`. `ServerConfig.ClientIPHeader`
  (for example `X-Forwarded-For` or `X-Real-IP`) makes it the first comma separated value of that
  header. A missing or unparsable value falls back to `RemoteAddr`. The header is trusted
  blindly, so it must be set only behind a proxy that overwrites it. Otherwise any client picks
  its own address and evades the limits.
- **CORS is narrow.** `Access-Control-Allow-Origin: *`, never `Allow-Credentials`, and OPTIONS
  preflights on: `GET /.well-known/oauth-authorization-server`, `GET
  /.well-known/oauth-protected-resource` (and `/mcp`), `POST /register`, `POST /token`. Methods
  and headers are exactly what each needs (`Content-Type` for the two POSTs, none for metadata;
  `Authorization` is not needed anywhere), preflight cache 10 minutes. `/register` and `/token`
  expose `Retry-After`. `*` is acceptable there because the metadata is public, registered
  clients are public clients whose codes are protected by PKCE, and these endpoints use no
  cookies or other ambient credentials, so a page from any origin gains nothing it could not do
  with curl. It is not acceptable on `/authorize` (a consent page for a player, framing and
  cross origin reads are what we defend against), on `/v1/studio/*` (signed studio calls), or on
  the MCP endpoint (bearer tokens for an agent, and a browser page must not be able to drive it
  unprompted). Those get no CORS headers, and tests prove it.

## One verifier for studio-signed requests, with replay protection

**Problem.** Ticket minting, the grants endpoints and the confirmations endpoints each read
the body and called `agentsig.Verify`. Only ticket minting refused replays, with its own map.
A captured signed `deny` replayed inside the 5 minute window could flip an approved
confirmation, and a captured `DELETE /v1/studio/grants` repeated.

**Decision.** `internal/studiosig.Verifier` is the only place studio signatures are checked.
It reads at most 64 KiB, verifies the signature, requires a player, and accepts each
(key id, idempotency key) pair once. `auth.ServerConfig.Verifier` and
`confirm.StudioConfig.Verifier` take a shared instance, so a request captured against one
endpoint cannot be replayed against another. When nil, each builds its own from the key id and
secret, which only protects that handler. `cmd/gateway` should pass one instance to both.

**GET is replay checked too.** A replayed read leaks nothing new to the studio, but the
response goes to whoever replays it, so an exempt GET would let a captured request disclose a
player's grants or pending confirmations again. One rule for every method is also simpler to
document and to test. The cost is that a studio retrying a failed GET with the same key is
refused. Studios must send a fresh idempotency key with every request including retries
(PROTOCOL.md). That is cheap because the studio endpoints are idempotent, so retrying with a
new key is safe.

**Cache.** Pairs are recorded only after the signature verified, so unsigned traffic cannot
fill, evict or poison the cache. Each entry lives for twice the skew window from insertion,
because a timestamp up to one skew in the future is accepted (the old ticket map used one skew
and could forget a future dated request while its timestamp was still valid). Entries sit in a
FIFO queue and are dropped as they expire. The hard cap is 100000 entries. At the cap, after
dropping expired entries, new requests fail closed with `503`. Evicting the oldest entry
instead would let an attacker with a stream of valid signed requests push a captured one out
and replay it, so it is not done. Concurrent identical requests are serialised by one mutex:
exactly one wins.

**Limits.** The cache is per process. Several replicas each accept a captured request once.
A shared store is the fix and needs its own entry when the gateway is scaled out.

## Manifest upload and rollback (M4)

**Decision.** `internal/manifeststore` keeps every uploaded manifest version (Postgres with
`--postgres-url`, otherwise memory) and one active pointer per tenant. Studio-signed endpoints
upload, list and activate versions (PROTOCOL.md).

- **Separate admin key.** Manifest calls are signed with `--admin-key-id` and
  `AGENTGW_ADMIN_SECRET`, not the player-call key. Whoever holds it decides which tools agents
  can call, so a leak of the player key must not reach it. `X-AG-Player` is the acting admin and
  is stored on the version. The endpoints are off when the secret is unset.
- **The store is the source of truth after the first upload.** `--manifest` only seeds a tenant
  that has no active version (actor `bootstrap`). Restarting with a different file does not
  override uploaded versions.
- **Rollback re-validates.** Activating an old version parses it with the current validator, so
  a version the rules no longer accept is refused instead of served.
- **Poll sync.** Each replica compares the active version id to what it serves every
  `--manifest-poll` and swaps the tool set atomically (`Service.SetManifest`). A call already
  running finishes with the tools it started with. No push channel until the relay (M5).
- **Not per tenant yet.** One tenant per process (`--tenant`), as in M3.
- **Versions are kept forever.** No pruning in M4.

## Key rotation for studio and admin keys

**Decision.** `studiosig.Verifier` holds a set of keys (id to secret), replaced atomically by
`SetKeys`. `--studio-keys-file` and `--admin-keys-file` name JSON files `{"id":"secret"}` that
are added to `--studio-key-id` and `--admin-key-id`. SIGHUP reloads them. A file that fails to
load (bad JSON, no keys, empty id or secret) is logged and the keys in use stay, so a typo never
locks a studio out. Secrets and ids are never logged.

**Rotation procedure.** Add the new key to the file and send SIGHUP. Move the studio to it.
Remove the old key and send SIGHUP again. The replay cache is keyed by key id and idempotency
key and survives `SetKeys`, so rotating never reopens a replay.

**Not covered.** The gateway's own signing key toward the backend (`--key-id`, one secret) is
unchanged. Backends already pick the secret by `X-AG-Key-Id`, so they can accept two keys while
the operator restarts the gateway with the new one. Per-tenant key sets come with multi tenancy.

## Activity feed

**Decision.** The audit log becomes readable through `audit.Store` (Query, Prune), kept by
`audit.Ring` (in memory, newest 10000, dev only) or `internal/audit/pgstore` (with
`--postgres-url`). The gateway writes to the structured log and to the store. Two studio-signed
endpoints (`internal/activity`) serve it: a player feed on the studio key and an admin feed on the
admin key (`docs/PROTOCOL.md`).

- **Hash only.** The feed returns what the audit log holds: tool, tier, outcome, code, time and the
  hash of the arguments. Arguments were never stored (rule 5), so there is nothing to leak.
- **Own events only.** The player feed forces the signed player and the gateway tenant onto the
  query. A parameter cannot widen it. Only the admin feed can name another player.
- **Paging by id.** Newest first, `before` is the id cursor, and the handler fetches one extra row to
  know whether `next_before` is needed. Ids order events within one store.
- **Retention.** `--audit-retention` (default 720h, `0` is forever), pruned hourly.
- **A failed audit write never fails a call.** `Record` has no error return; the Postgres store
  reports failures through `OnError`, which the gateway logs.
- **Not yet.** No export, no streaming and no aggregate counts. They can come with the M6
  dashboards. With several replicas on Postgres ids are global; on the in memory Ring they are
  per process.

## Relay (M5)

For games whose logic runs in the client (Unity and similar), the backend has no endpoint the
gateway can call. The game opens an outbound WebSocket to the gateway instead, and tool calls go
down that socket.

- **Manifest: `backend` per tool.** `"backend": "relay"` sends the call over the player's socket.
  The default, `"http"`, is unchanged. Relay tools take no `http` binding (rejected if present), so
  one manifest can mix both. A compatibility addition: old manifests parse as before.
- **Only the pipeline's last step changes.** Rate limit, validation, confirmation, caps, settle and
  audit run the same for relay tools. `connector.Mux` picks the connector by backend. A relay tool
  with no relay configured is a transport error, so it fails closed and releases its reservation.
- **Library: `github.com/coder/websocket` v1.8.15.** Context aware, no transitive dependencies,
  maintained. Chosen with the user over gorilla/websocket and a hand-rolled framer.
- **Connecting: stateless signed tickets.** The studio's backend calls the studio-signed
  `POST /v1/studio/relay/tickets` (the signed `X-AG-Player` names the player) and hands the
  returned ticket to the game client. The client connects to `GET /v1/relay/connect` with the
  ticket (`Authorization: Bearer`, or `?ticket=` where headers are not possible). A ticket is
  HMAC signed with the relay secret, carries tenant, player and expiry (60 s), and is checked
  before the upgrade. It needs no shared store, so any replica accepts it. A ticket can be reused
  until it expires; it only opens a socket for that one player, and the newer socket replaces the
  older one. The game client never holds the studio key.
- **One socket per player.** A new connection closes the previous one (code 4000). Calls pending
  on the replaced socket fail.
- **Routing across replicas: Redis presence keys and pub/sub.** A replica claims
  `tenant/player` with a TTL it refreshes while the socket lives. A call looks up the owner; if
  the owner is another replica it publishes the call to that replica's inbox, and the result comes
  back to the caller's inbox. Delivery is at most once. A lost message surfaces as a timeout,
  which fails closed. The `Bus` interface keeps NATS possible later.
- **Offline player.** No owner is an expected result `client_offline`, not a Go error. A timeout
  or bus failure is a Go error, so the pipeline releases the reservation.
- **Wire protocol v1** is JSON text frames: `hello`, `call` (gateway to client), `result` (client
  to gateway). Results are validated no more than the HTTP connector's are: the game is the
  authority, the gateway still limits what may be asked.

## Observability (M6)

- **Own registry, no dependency.** `internal/obs` writes the Prometheus text format (0.0.4) itself:
  counters, gauges and fixed-bucket histograms. The client library would add a dependency tree for
  a few hundred lines of code. Revisit if OpenTelemetry metrics are needed.
- **Cardinality is capped.** One metric keeps at most 1000 label sets; more fold into a series
  whose label values are all `other`. The `tool` label uses the manifest name or `unknown`, so an
  agent cannot mint series by inventing tool names. HTTP metrics use the mux pattern (the route
  table), never the raw path; no match is `unmatched`.
- **Metrics.** `gateway_calls_total{tool,outcome}` (outcome is `ok` or the denial code, so a failed
  backend shows as `backend_unavailable` and a cut-off call as `outcome_unknown`),
  `gateway_call_duration_seconds{tool}`, `relay_connections`, `relay_calls_total{result}` (`ok`,
  `denied`, `offline`, `error`), `http_requests_total{route,code}`, `agentgw_build_info{version}`.
  The pipeline is unchanged: `gateway.WithObserver` and `relay.WithObserver` are optional hooks
  called once per call.
- **Separate ops listener.** `--ops-addr` (default `127.0.0.1:9090`, empty disables) serves
  `/healthz` (200 while the process serves), `/readyz` (200 only when the manifest is loaded and
  Redis and Postgres answer, when configured; otherwise 503 with the failing checks; each check
  times out after 2 s) and `/metrics`. It is never on the public listener. In compose it is
  published on loopback only.

## Backend hardening (M6)

- **Default deny for private addresses.** `connector.NewHTTP` builds a client whose dialer
  refuses loopback, private, link-local, metadata, multicast, unspecified, CGNAT and special
  purpose ranges, checked in the dialer's `Control` hook on the resolved IP (no rebinding gap).
  `connector.AllowPrivate()` and the flag `--backend-allow-private` opt out. The compose demo
  sets the flag because its backend is on a private network. A compatibility change for
  operators: a deployment whose backend is on a private address must now set the flag.
- **Redirects are never followed**, even with the flag. A signed request is meant for the
  configured host only. A 3xx becomes a backend error. No proxy from the environment.
- **Path values.** Empty, `.` and `..` placeholder values are refused as `invalid_arguments`.
- **Identifiers in headers.** Player and call ids must be printable ASCII, at most 128 bytes,
  or the connector returns `invalid_principal` before signing.
- **Validation.** Trailing data after the arguments object is refused, string arguments are
  limited to 4096 bytes, and integer bounds are compared as int64 (float64 rounded values above
  2^53 onto the bound).

## Site (M8)

- **Astro Starlight**, chosen with the user over MkDocs Material, Docusaurus and Hugo. Markdown
  stays the source, search, dark mode and a splash landing page come with it.
- **Everything lives in `site/` and runs in a container.** `docker compose run --rm site
  build|dev|check|check-external|lock`. Node is never installed on the host. Dependencies are pinned
  exactly in `site/package.json` with a lockfile, and never touch the Go module.
- **The site reads `docs/`, it does not fork it.** `site/scripts/sync-docs.mjs` copies `docs/**` and
  the root `CHANGELOG`, `CONTRIBUTING`, `SECURITY` and `CODE_OF_CONDUCT` into
  `site/src/content/docs/` (git-ignored) before every build. It takes the title from the first H1,
  adds an edit link, sets the sidebar order, and rewrites relative links to site routes or GitHub
  URLs. `OPEN-SOURCE-PLAN`, `REPO-SETTINGS` and `AUDIT-REPORT` are process documents and stay out of
  the site. The docs read the same on GitHub.
- **Pages not written yet get a placeholder and a warning**, so a build never fails because a
  planned page is missing. The expected list is in the sync script.
- **Only the landing page (`index.mdx`) and `404.md` are written by hand** under `site/`.
- **Base path `/agentgw/`**, site URL `https://moustafa-elgammal.github.io/agentgw/`, default
  github.io for now (a custom domain later needs a `CNAME` and a new base).
- **Gates.** `site check` fails on a build warning, a broken internal link
  (`starlight-links-validator`), a missing sitemap or `robots.txt`, or a URL that escapes the base.
  `go test ./test/docs` validates every `json manifest` or `yaml manifest` code block with the real
  validator. `site check-external` checks outside links, is not a gate, and skips itself offline.
  `.github/workflows/pages.yml` runs these on pull requests and deploys on pushes to `main`.
- **Not done:** spell check and Markdown lint, an accessibility audit, versioned docs per release,
  and the terminal recording on the landing page (a text placeholder marks the spot).

## Guides (M9)

- **Tutorial scripts are the CI test.** Every `docs/tutorials/NN-slug.md` has a `NN-slug.sh` with
  the commands of that guide. `docs/tutorials/run-all.sh` runs them in order against the compose
  stack, continues after a failure, and exits nonzero if any failed (`--only NN` runs one).
  `.github/workflows/guides.yml` runs it on pull requests that touch guides, tutorials or
  examples, and weekly. A guide that stops working fails the build.
- **Versions are stated.** Each tutorial has a `Verified against` line with the release or commit it
  ran against. A Go test (`test/docs`) fails if the line, the script, or the guide is missing, so
  the pair cannot drift apart. A tutorial not written yet is logged, not failed, so guides can land
  one at a time.
- **Troubleshooting is complete by test.** `docs/guides/troubleshooting.md` needs one heading per
  code in `core.Codes`. A new result code fails the test until the page explains it. The generated
  [result codes](reference/errors.md) page holds the meaning, the guide holds cause, check and fix.
- **Guides are not generated.** The best practices, cookbook and FAQ are hand written and checked
  for broken links, dashes and invalid manifest snippets. Facts in them must match the code and the
  other docs.
- **One sidebar section.** The site shows `docs/guides/` and `docs/tutorials/` together as
  "Guides", the learning path first, then the tutorials in numeric order, then the reference guides.
  The sources stay where they are and read the same on GitHub.

## YAML is the default authoring format

Studio developers write and review manifests by hand, so the examples, tutorials and docs use
YAML: one block per tool, small maps on one line, comments allowed, and a typo reports its line
and column. JSON stays fully supported and is still what the gateway stores and what the studio
API returns. Nothing about the loader or the wire format changed.

- The format follows the file extension (`.yaml` or `.yml` is YAML, anything else JSON) for
  `--manifest`, `gwctl` and the Helm chart (`manifestFile`, default `manifest.yaml`). An upload
  uses `Content-Type: application/yaml` or `application/json`.
- `examples/city/manifest.json` stays next to `manifest.yaml` for tools and tests that read JSON.
  A test requires every JSON and YAML pair under `examples/` to describe the same manifest.
- Manifest blocks in the docs are fenced as `yaml manifest` and checked by `test/docs`. A JSON
  block is allowed only where the page is about the JSON form, and a test lists those pages.

