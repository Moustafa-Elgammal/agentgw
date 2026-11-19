# Tutorial 1: Play the example game with an agent

Verified against agentgw v0.1.0 (see the [changelog](../../CHANGELOG.md)). About 15 minutes.
The script [`01-play-the-example-game.sh`](01-play-the-example-game.sh) runs every command below
against a throwaway Docker project, and CI runs that script.

You will start the example game, call its tools by hand, run into a daily cap and a cooldown,
connect an agent through the `gwbridge` stdio bridge, and read the audit trail.

## Prerequisites

- Docker with Compose v2. Nothing else is installed on your machine.
- The repository: `git clone https://github.com/Moustafa-Elgammal/agentgw && cd agentgw`
- Optional, for the last step of the agent part: Claude Desktop, or any MCP client that can
  launch a stdio server.

The basics (start, call, denials) are in the [quickstart](../get-started/quickstart.md). This
tutorial repeats only what it needs and then goes deeper.

## 1. Start the demo

```bash
docker compose up --build -d
curl -s http://127.0.0.1:8080/healthz
```

The gateway runs with `--dev-auth`, so the player comes from the `X-Dev-Player` header. That is
for local trying only. The script uses port 18101 instead of 8080 so it never collides with a
demo you already have running. Everything else is identical.

A small helper makes the next commands shorter:

```bash
call() { curl -s -X POST -H 'X-Dev-Player: p1' -d "$2" "http://127.0.0.1:8080/v1/tools/$1/call"; }
call get_city_state '{}'
call sell_resource '{"arguments":{"resource":"food","quantity":50}}'
```

You should see `"coins":500` first and `"earned":100` after the sale.

## 2. Run into a daily cap

In `examples/city/manifest.json`, `sell_resource` has `"caps": {"per_day_amount": 300,
"amount_param": "quantity"}`. The cap counts the amount sold, not the number of calls. You have
sold 50. Ask for 400 more:

```bash
call sell_resource '{"arguments":{"resource":"water","quantity":400}}'
```

```json
{"ok":false,"code":"daily_amount_cap","message":"sell_resource: 400 needed, 250 left today"}
```

The call never reached the game. The gateway reserves the amount before it calls the backend,
so concurrent calls cannot slip past the cap. The message says how much is left, so an agent can
retry with `250`.

## 3. Run into a cooldown

`collect_taxes` has `"cooldown_seconds": 60`:

```bash
call collect_taxes '{}'
call collect_taxes '{}'
```

```json
{"ok":true,"data":{"coins":625,"collected":25}}
{"ok":false,"code":"cooldown","message":"collect_taxes is available again in 60 seconds"}
```

## 4. Connect an agent through gwbridge

Claude Desktop launches stdio MCP servers. It cannot reach `http://localhost`, so `gwbridge`
sits in between: MCP over stdio on one side, the gateway's `/mcp` endpoint on the other. It
holds no policy, so the caps above apply to the agent exactly as they applied to `curl`.

Build the image:

```bash
docker compose build bridge
```

Ask it for the tool list, the way an MCP client does. This sends three JSON-RPC lines
(`initialize`, `initialized`, `tools/list`) to the bridge's stdin. The network name is
`<project>_default`, and the project is `agentgw` unless you changed it:

```bash
{
  printf '%s\n' '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"tutorial","version":"1"}}}'
  printf '%s\n' '{"jsonrpc":"2.0","method":"notifications/initialized"}'
  printf '%s\n' '{"jsonrpc":"2.0","id":2,"method":"tools/list"}'
  sleep 3
} | docker run -i --rm --network agentgw_default agentgw-bridge:latest \
      --url http://gateway:8080/mcp --header "X-Dev-Player: desktop"
```

The reply lists `get_city_state`, `collect_taxes`, `build_structure` and `sell_resource`, each
with the description and parameter schema from the manifest. A call goes the same way, with
`"method":"tools/call","params":{"name":"get_city_state","arguments":{}}`.

To use it from Claude Desktop, add this to `claude_desktop_config.json` (use the absolute path
to your `docker`):

```json
{"mcpServers":{"city":{"command":"/usr/local/bin/docker","args":["run","-i","--rm","agentgw-bridge:latest","--url","http://host.docker.internal:8080/mcp","--header","X-Dev-Player: desktop"]}}}
```

Restart Claude Desktop and ask it: "Look at my city, then sell 100 food." Then ask it to
sell 400 water. It meets the same `daily_amount_cap` and, because the code and message are
plain, it will usually adjust the amount by itself. More on this in
[Connect an agent](../get-started/connect-an-agent.md).

`X-Dev-Player` only works against a gateway started with `--dev-auth`. A real deployment gives
the bridge a bearer token instead. That is [tutorial 4](04-link-a-player.md).

## 5. Read the audit trail

Every attempt, allowed or refused, is an audit event. In the demo they go to the gateway log:

```bash
docker compose logs gateway | grep agent_call
```

You will find the denials and the bridge's calls (`"player":"desktop"`):

```json
{"level":"INFO","msg":"agent_call","tenant":"dev","player":"p1","tool":"sell_resource","tier":"medium","outcome":"denied","code":"daily_amount_cap","args_sha256":"..."}
{"level":"INFO","msg":"agent_call","tenant":"dev","player":"desktop","tool":"get_city_state","tier":"read","outcome":"ok","code":"","args_sha256":"..."}
```

Look at what is not there: the arguments. The audit stores a SHA-256 of them, so it can prove
two calls were identical without keeping game data. With OAuth the same events feed the player's
activity feed (`/v1/studio/activity`).

## 6. Clean up

```bash
docker compose down
```

## What you learned

- A cap counts what you can lose (units sold), a cooldown spaces calls out, and both answer
  with a code the agent can read.
- `gwbridge` turns any gateway into a stdio MCP server, and it adds no policy of its own.
- The audit log records every attempt and never the arguments.

## Next

[Tutorial 2: describe your game's actions](02-describe-your-actions.md).
