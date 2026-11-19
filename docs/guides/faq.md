# FAQ

## The basics

### How is this different from letting the agent call my REST API?

The agent would hold credentials and act with no limits. With the gateway the agent holds a token
for one player, sees only the tools you list, and every call passes tiers, rate limits, caps,
cooldowns and optional player approval before your backend sees a signed request. Your backend
never has to trust an agent. See [how a call flows](../concepts/pipeline.md).

### Does my backend need new code?

For a REST game, no new endpoints: the manifest maps tools onto the endpoints you have. You must
verify the signature the gateway adds, so your backend knows the call came from the gateway and
which player it is for. Go backends can import `pkg/agentsig`. Other languages follow
[PROTOCOL.md](../PROTOCOL.md), which is a few lines of HMAC.

### What does the agent see?

A list of tools with names, descriptions and parameter types, taken from your manifest, and results
with a machine-readable `code` when something fails. Coach grants see read tools only.

### Which agents work?

Any MCP client that speaks streamable HTTP and OAuth 2.1 can connect to `/mcp`. Desktop clients that
only run local stdio servers use the `gwbridge` stdio bridge. See
[connect an agent](../get-started/connect-an-agent.md).

## Safety

### Can the agent bypass the gateway?

Not through the gateway's design. The agent only has a token that works at `/mcp`. Your backend
accepts a call only with a valid signature, a fresh timestamp and a one-time idempotency key, and
the signing key is not in the agent's hands. Keep your backend closed to unsigned callers, or this
stops being true.

### What if Redis is down?

Policy checks fail closed: every call, reads included, gets `policy_unavailable` and nothing reaches
your backend. See [tiers and policy](../concepts/tiers-and-policy.md).

### What if Postgres is down?

Calls to tools with `confirm: true` fail closed with `confirmation_unavailable`. OAuth token
checks fail, so MCP calls are refused, and studio endpoints answer 503. The [runbook](../RUNBOOK.md)
lists what each outage does.

### Can a player's chat text steer another player's agent?

Only if your backend echoes it in a tool result. Backend output is data, but a model may still
follow instructions in it. Do not return other players' free text. See
[best practices](best-practices.md).

### What stops a runaway agent?

Every attempt counts against a per-player rate limit, valid or not. Cooldowns and daily caps bound
what it can change. A player can revoke the agent at once. Your studio can remove a tool by
uploading a manifest without it.

### Is there a panic button?

Kill switches exist in the policy store, but the only HTTP surface today is the dev admin API. In
production, upload or roll back a manifest, or revoke grants. A signed kill-switch endpoint is not
built yet.

### What is in the audit log?

One event per attempt: time, tenant, player, tool, tier, outcome, code and a SHA-256 hash of the
arguments. The arguments themselves are never stored. See [audit events](../reference/audit-events.md).

### How long are events kept?

`--audit-retention` sets it, 30 days by default, and `0` keeps them forever. The gateway also logs
each event to stdout as JSON.

## Running it

### Is it multi-tenant?

A gateway process serves one tenant, set by `--tenant`. Tenant isolation with row-level security in
Postgres is planned, not built. Run one deployment per studio for now.

### Is there a hosted version?

No. You run it. Hosting is an open question in [DECISIONS.md](../DECISIONS.md).

### How do I run more than one replica?

Configure Redis and Postgres first. Without them each replica keeps its own limits and stores, and
a player could get the budget twice. The Helm chart in `deploy/helm/agentgw` runs several.

### What does it cost to run?

The gateway is one small static Go binary. It needs Redis and Postgres for production use. The
repository has load test tooling (`test/load`) so you can measure your own numbers.

### Can I change the rate limits?

Not by flag. They are fixed at 120 reads and 30 actions per player per minute. Use caps and
cooldowns in the manifest to shape behavior.

### Can I update tools without a restart?

Yes. Upload a manifest with the admin key, and roll back to any earlier version. Replicas pick up the
change within `--manifest-poll`.

## Games

### My game logic runs in the client, for example in Unity. Can I use it?

Yes, with the relay. Mark tools `"backend": "relay"` and the game client opens an outbound WebSocket
with a ticket your backend mints. Calls go down that socket. See the relay section of
[PROTOCOL.md](../PROTOCOL.md). There is no Unity package yet, only the wire protocol and a Go
sample client.

### Which backend languages are supported?

Any. The contract is a manifest plus a signed HTTP request. Go has a library, other languages
implement the signing described in the protocol document.

### Can the agent do things a player cannot?

It should not. The backend stays authoritative for energy, cooldowns and the economy, and the
agent gets no side door.

### Is there a safe test game?

Yes. `examples/city` is a fake backend that runs with `docker compose up`, and the separate
`tinycity-mock` repository is a fictional game for testing.

## Project

### Is it production ready?

It is a `0.1` release, before 1.0. The manifest format, the signed request and some protocol details may still
change, and each change has an entry in [DECISIONS.md](../DECISIONS.md) and the changelog. Pin a
version.

### How do I report a security problem?

Privately, see [SECURITY.md](../../SECURITY.md). Do not open a public issue.

### How do I contribute?

Read [CONTRIBUTING.md](../../CONTRIBUTING.md). Sign off every commit, add tests for behavior changes, and
put compatibility changes in `docs/DECISIONS.md` first.
