# Tutorial 4: Link a player

Verified against agentgw v0.1.0 (see the [changelog](../../CHANGELOG.md)). About 15 minutes.
The script [`04-link-a-player.sh`](04-link-a-player.sh) runs every command below against a
throwaway Docker project, and CI runs that script.

You will run the gateway with real OAuth instead of the demo header, mint a link ticket the way
your game's backend would, let a scripted agent link with the coach scope and then the operator
scope, call a tool with the token, and list and revoke the player's linked agents.

## Prerequisites

- Docker with Compose v2. Nothing else is installed on your machine.
- The repository: `git clone https://github.com/Moustafa-Elgammal/agentgw && cd agentgw`
- Tutorial 1 helps, but is not required.
- Stop any other agentgw stack first. This one uses `127.0.0.1:8080` and `127.0.0.1:9091`. (The
  script uses other ports, so it can run next to a stack you already have.)

## How linking works

Players have no login on the gateway. The player is already logged in to your game. So the game
vouches for them:

1. The player's agent (an MCP client such as Claude Desktop) finds the gateway, registers itself
   and sends the player to a consent page.
2. Your game's **backend** asks the gateway for a short **link ticket** for that player. The game
   shows the code to the player. The request is signed, so only your backend can make it.
3. The player types the code on the consent page and picks **coach** (read only) or **operator**
   (can act, inside the caps).
4. The agent gets a token tied to that player and that scope.

You will play all three roles here: the game's backend, the player and the agent.

## 1. Start the gateway in OAuth mode

The compose override `examples/tutorials/compose.oauth.yml` takes `--dev-auth` away and sets
`--public-url=http://gateway:8080`. The public URL is the address the agent uses, and it is the
OAuth issuer. In this tutorial every client runs inside the compose network, so that is the
service name. In a real deployment it is your public HTTPS URL.

```bash
dc() { docker compose -f docker-compose.yml -f examples/tutorials/compose.oauth.yml "$@"; }
dc up -d --build gateway
curl -s http://127.0.0.1:9091/readyz
```

```json
{"status":"ready"}
```

The secrets are the demo placeholders (`dev-secret`). The studio key id is `dev-1`. Never use
them outside this tutorial.

## 2. Build two small helpers

`studiocall` is a few lines of Go that sends one **signed** request, as your game's backend would.
`oauthclient` does what an agent's MCP client does to link. Both are in `examples/`, and you can
read them. Build them in the toolchain container:

```bash
dc build --quiet dev
dc run --rm -T dev go build -o bin/ ./examples/studiocall ./examples/oauthclient
studio() { dc run --rm -T dev bin/studiocall "$@"; }
oauth()  { dc run --rm -T dev bin/oauthclient "$@"; }
```

You also need a way to speak MCP from the shell. The gateway's MCP endpoint is plain JSON over
HTTP, so `curl` is enough. `$ACCESS` is the token you will get in step 5:

```bash
mcp() {
  curl -s -H "Authorization: Bearer $ACCESS" -H 'Content-Type: application/json' \
    -H 'Accept: application/json, text/event-stream' -H 'MCP-Protocol-Version: 2025-06-18' \
    -d "$1" http://127.0.0.1:8080/mcp
  echo
}
```

## 3. An agent with no token is turned away

```bash
curl -s -i -X POST http://127.0.0.1:8080/mcp -d '{}' | head -n 3
```

```text
HTTP/1.1 401 Unauthorized
Content-Type: text/plain; charset=utf-8
Www-Authenticate: Bearer resource_metadata="http://gateway:8080/.well-known/oauth-protected-resource/mcp"
```

The `Www-Authenticate` header tells the agent where to look. A real MCP client follows it:

```bash
curl -s http://127.0.0.1:8080/.well-known/oauth-protected-resource/mcp
```

```json
{"authorization_servers":["http://gateway:8080"],"bearer_methods_supported":["header"],"resource":"http://gateway:8080/mcp","resource_name":"agentgw game gateway","scopes_supported":["coach","operator"]}
```

## 4. The game's backend mints a link ticket

This is what your backend does when the player taps "Link my AI agent". The signed
`X-AG-Player` header names the player. Here the player is `alice`:

```bash
studio --player alice --expect 201 POST /v1/studio/link-tickets
```

```text
HTTP 201
{"expires_at":"2026-11-19T12:07:20.690715889Z","ticket":"A682-25LS"}
```

Show `A682-25LS` to the player. It is valid for 5 minutes and works once. Keep it for the next
step:

```bash
TICKET=$(studio --player alice --expect 201 POST /v1/studio/link-tickets 2>/dev/null \
  | sed 's/.*"ticket":"\([^"]*\)".*/\1/')
```

(`studiocall` prints the status line on stderr and the body on stdout, so `2>/dev/null` keeps
only the body.)

## 5. The agent links with the coach scope

`oauthclient` finds the gateway, registers, opens the consent page, enters the ticket and picks
the scope for the player, and exchanges the code for tokens (PKCE, S256):

```bash
COACH=$(oauth --ticket "$TICKET" --scope coach)
echo "$COACH"
ACCESS=$(echo "$COACH" | sed 's/.*"access_token":"\([^"]*\)".*/\1/')
COACH_ACCESS=$ACCESS
```

```json
{"client_id":"10afc397...","access_token":"-HEwtP5j...","refresh_token":"rmW11Ya2...","scope":"coach","token_type":"Bearer"}
```

Tokens are random strings the gateway stores as hashes. There is no JWT to decode and nothing to
leak from the database.

## 6. A coach sees one read-only tool and cannot act

```bash
mcp '{"jsonrpc":"2.0","id":1,"method":"tools/list"}' | grep -o '"name":"[a-z_]*"'
```

```text
"name":"get_city_state"
```

Only the read tool is listed. Guessing the name of an action tool does not work either:

```bash
mcp '{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"sell_resource","arguments":{"resource":"food","quantity":5}}}'
```

```json
{"jsonrpc":"2.0","id":2,"error":{"code":-32602,"message":"unknown tool \"sell_resource\""}}
```

The backend never saw the call. The scope check happens in the gateway, before anything else.

## 7. Link again as an operator

The same player can link the same agent again with a wider scope. Mint a new ticket (each one is
single use), then run the same client with `--scope operator`:

```bash
TICKET=$(studio --player alice --expect 201 POST /v1/studio/link-tickets 2>/dev/null \
  | sed 's/.*"ticket":"\([^"]*\)".*/\1/')
OPERATOR=$(oauth --ticket "$TICKET" --scope operator)
ACCESS=$(echo "$OPERATOR" | sed 's/.*"access_token":"\([^"]*\)".*/\1/')
OPERATOR_ACCESS=$ACCESS
mcp '{"jsonrpc":"2.0","id":1,"method":"tools/list"}' | grep -o '"name":"[a-z_]*"'
```

```text
"name":"build_structure"
"name":"collect_taxes"
"name":"get_city_state"
"name":"sell_resource"
```

Now the agent can act, inside the caps from the manifest:

```bash
mcp '{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"sell_resource","arguments":{"resource":"food","quantity":5}}}'
```

```json
{"jsonrpc":"2.0","id":2,"result":{"content":[{"type":"text","text":"{\"data\":{\"coins\":510,\"earned\":10},\"ok\":true}"}],"structuredContent":{"data":{"coins":510,"earned":10},"ok":true}}}
```

The gateway signed the call to the backend with `alice` as the player. The agent never chose who
it plays as. That comes from the grant.

## 8. Show the player their linked agents

The game's settings screen can list what the player has linked. Your backend asks the gateway:

```bash
studio --player alice --expect 200 GET /v1/studio/grants
```

```json
{"grants":[
 {"id":"bde6f16b...","client_id":"10afc397...","client_name":"tutorial agent","scope":"coach","resource":"http://gateway:8080/mcp","created_at":"2026-11-19T12:02:21.30095Z"},
 {"id":"8ec30a33...","client_id":"8de6390b...","client_name":"tutorial agent","scope":"operator","resource":"http://gateway:8080/mcp","created_at":"2026-11-19T12:02:23.025502Z"}]}
```

(Wrapped for reading.) A player only ever sees their own grants, because the player is the signed
`X-AG-Player` header.

## 9. Revoke one, then all

The player taps Revoke on the coach grant. Use its id:

```bash
studio --player alice --expect 204 DELETE /v1/studio/grants/<coach grant id>
```

```text
HTTP 204
```

The coach token stops working at once, because tokens are looked up on every call and not read
from the token itself. The operator token still works:

```bash
curl -s -o /dev/null -w '%{http_code}\n' -H "Authorization: Bearer $COACH_ACCESS" \
  -H 'Content-Type: application/json' -H 'Accept: application/json, text/event-stream' \
  -H 'MCP-Protocol-Version: 2025-06-18' -d '{"jsonrpc":"2.0","id":1,"method":"tools/list"}' \
  http://127.0.0.1:8080/mcp
```

```text
401
```

A "log out all
agents" button is one call:

```bash
studio --player alice --expect 200 DELETE /v1/studio/grants
```

```json
{"revoked":1}
```

Every token of every grant of `alice` is dead now. Repeating the call returns `{"revoked":0}`.

## Clean up

```bash
dc down -v
```

## What you learned

- Players link without a gateway login: your backend mints a ticket, the player types it on the
  consent page and chooses coach or operator.
- A coach token lists and calls only read tools. An operator token can act inside the caps.
- The player on every call comes from the grant, never from the agent.
- The studio lists and revokes grants with signed calls. Revocation takes effect on the next call.
- Refresh tokens rotate. If an old refresh token is used again, the gateway treats it as stolen
  and kills the whole grant. The scripted test for that is `test/conformance/auth_test.go`.

## Next

[Tutorial 5: Ask the player before big actions](05-ask-before-big-actions.md). It uses the same
stack.

Reference: the [studio API](../reference/studio-api.md), the
[protocol](../PROTOCOL.md) and [players, grants and confirmations](../concepts/players-grants-confirmations.md).
