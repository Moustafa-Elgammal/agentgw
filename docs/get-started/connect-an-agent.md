# Connect an agent

An agent reaches the gateway through one URL: `/mcp`. It speaks MCP over streamable HTTP. The
gateway is stateless, so each request carries the player's identity and nothing is kept in a
session.

## What the agent sees

1. **A tool list.** Names, descriptions and argument types, all taken from your manifest. Read
   tools are marked read only. Sensitive tools are never listed (they are disabled), and a coach
   grant sees only read tools.
2. **Results.** A success is `{"ok":true,"data":{...}}`. The `data` is whatever your backend
   returned.
3. **Denials the agent can act on.** A refusal comes back as a tool error (`isError`) with a body
   like `{"ok":false,"code":"daily_amount_cap","message":"sell_resource: 400 needed, 250 left today"}`.
   The message says what to do next. The same body is also sent as `structuredContent`.

Common codes: `invalid_arguments` (fix the arguments), `rate_limited` (wait), `cooldown` or
`daily_amount_cap` (try later or less), `scope_insufficient` (ask the player for operator
access), `confirmation_required` (ask the player to approve, then repeat the exact call),
`switched_off` (do not retry), `backend_unavailable` (nothing changed), `outcome_unknown` (the
game may or may not have applied the call, check state first).

## Local trying: dev auth

The demo gateway runs with `--dev-auth`. It takes the player from an `X-Dev-Player` header and
trusts it, so keep it on loopback. After `docker compose up -d --build`, the MCP endpoint is
`http://127.0.0.1:8080/mcp`.

Any MCP client that can send a custom header can connect:

```text
URL:     http://127.0.0.1:8080/mcp
Header:  X-Dev-Player: p1
```

### Try it with plain curl

With `--dev-auth` you can talk to `/mcp` without an MCP client. The player and an `Accept` header that
allows both reply types are required. `MCP-Protocol-Version` is optional here, and real clients send it.

```bash
curl -s -X POST http://127.0.0.1:8080/mcp \
  -H 'X-Dev-Player: p1' -H 'Content-Type: application/json' \
  -H 'Accept: application/json, text/event-stream' -H 'MCP-Protocol-Version: 2025-06-18' \
  -d '{"jsonrpc":"2.0","id":1,"method":"tools/list"}'
```

The answer lists the tools from the manifest, each with its input schema.

## Claude Desktop

Claude Desktop starts local stdio servers and cannot reach an `http://localhost` URL itself.
`gwbridge` fills the gap. It speaks stdio to Claude and forwards every call to the gateway's
`/mcp`. It holds no policy, so limits, scopes and audit stay in the gateway. Setup steps and the
config snippet are in [Use the gateway from Claude Desktop](../CLAUDE-DESKTOP.md).

## Real deployments: OAuth

Without `--dev-auth`, `/mcp` needs a bearer token and the header-trusting dev API is not
mounted. An MCP client finds out how to sign in by itself:

1. The client asks `/mcp` and gets `401` with a pointer to the gateway's OAuth metadata.
2. It registers itself and starts an authorization request with PKCE.
3. The gateway shows a **consent page**. The player is already logged in on **your** site, and the
   gateway has no login of its own. So your backend asks the gateway for a **link ticket**, a short
   code such as `ABCD-EFGH`. It lives 5 minutes and works once. Your site shows it to the player.
4. The player types the code on the consent page and chooses **coach** or **operator**.
5. The client receives a token. Access tokens last 15 minutes and refresh tokens rotate.

The link ticket is how the gateway learns which player is behind the agent, without trusting the
agent or the client. Your backend mints it with a signed request, so only you can say who the
player is.

The player can see and revoke linked agents in your game (you call the signed
`/v1/studio/grants` endpoints). Revoking takes effect on the agent's next request. The full flow
and endpoints are in the [protocol](../PROTOCOL.md) and
[players, grants and confirmations](../concepts/players-grants-confirmations.md).

## Check it works

Ask the agent to call `get_city_state` (in the demo). Then watch the gateway log for
`agent_call` lines, one for every attempt:

```bash
docker compose logs -f gateway | grep agent_call
```

## Next

[How a call flows](../concepts/pipeline.md).
