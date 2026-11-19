# Quickstart

Run the whole demo in about five minutes. You need Docker with Compose v2 and nothing else.
Nothing is installed on your machine, and every port binds to `127.0.0.1`.

The demo is a fictional city-builder. A fake city backend sits behind the gateway. The gateway
runs with `--dev-auth`, which takes the player from a header. That is for local trying only.

## 1. Start it

```bash
git clone https://github.com/Moustafa-Elgammal/agentgw && cd agentgw
docker compose up --build -d
```

This starts the gateway, Redis (limits and caps), Postgres (approvals and audit) and the fake
backend. Check that it is up:

```bash
curl -s http://127.0.0.1:8080/healthz
```

## 2. See the tools

```bash
curl -s -H 'X-Dev-Player: p1' http://127.0.0.1:8080/v1/tools
```

```json
{"tools":[
 {"name":"build_structure","description":"Build a structure on a free tile of the 5x5 grid.","tier":"medium","params":{"kind":"string","x":"integer","y":"integer"}},
 {"name":"collect_taxes","description":"Move pending taxes into the treasury. Cooldown applies.","tier":"safe"},
 {"name":"get_city_state","description":"Overview of the city: coins, resources, population. Call this first.","tier":"read"},
 {"name":"sell_resource","description":"Sell food, water or materials at the current sell price.","tier":"medium","params":{"quantity":"integer","resource":"string"}}]}
```

These four tools come from `examples/city/manifest.json`. (The output above is wrapped for
reading.)

## 3. Call a tool

```bash
curl -s -X POST -H 'X-Dev-Player: p1' -d '{}' \
  http://127.0.0.1:8080/v1/tools/get_city_state/call
```

```json
{"ok":true,"data":{"coins":500,"resources":{"food":60,"materials":60,"water":60},"buildings":0}}
```

Now sell some food:

```bash
curl -s -X POST -H 'X-Dev-Player: p1' \
  -d '{"arguments":{"resource":"food","quantity":50}}' \
  http://127.0.0.1:8080/v1/tools/sell_resource/call
```

```json
{"ok":true,"data":{"coins":600,"earned":100}}
```

## 4. See a denial and read its code

A denial is a normal answer, not a crash. It has `"ok":false`, a machine-readable `code` and a
message the agent can act on.

Bad arguments. The manifest says `quantity` is at most 1000:

```bash
curl -s -X POST -H 'X-Dev-Player: p1' \
  -d '{"arguments":{"resource":"food","quantity":5000}}' \
  http://127.0.0.1:8080/v1/tools/sell_resource/call
```

```json
{"ok":false,"code":"invalid_arguments","message":"argument \"quantity\" must be at most 1000"}
```

A daily cap. `sell_resource` allows 300 units a day, and 50 are already used:

```bash
curl -s -X POST -H 'X-Dev-Player: p1' \
  -d '{"arguments":{"resource":"water","quantity":400}}' \
  http://127.0.0.1:8080/v1/tools/sell_resource/call
```

```json
{"ok":false,"code":"daily_amount_cap","message":"sell_resource: 400 needed, 250 left today"}
```

A cooldown. Call `collect_taxes` twice:

```bash
for i in 1 2; do curl -s -X POST -H 'X-Dev-Player: p1' -d '{}' \
  http://127.0.0.1:8080/v1/tools/collect_taxes/call; echo; done
```

```json
{"ok":true,"data":{"coins":625,"collected":25}}
{"ok":false,"code":"cooldown","message":"collect_taxes is available again in 60 seconds"}
```

A rate limit. Reads are limited to 120 a minute per player. After 120 quick calls, the next
one is refused:

```json
{"ok":false,"code":"rate_limited","message":"max 120 reads per minute. Wait and retry."}
```

Other codes you may meet: `unknown_tool`, `scope_insufficient`, `switched_off`,
`tier_disabled`, `daily_call_cap`, `backend_rejected` (your backend said no),
`backend_unavailable`, `confirmation_required`.

## 5. Approve a sensitive action

`build_structure` is marked `"confirm": true`, so the player must approve each call.

```bash
curl -s -X POST -H 'X-Dev-Player: p1' \
  -d '{"arguments":{"kind":"farm","x":1,"y":1}}' \
  http://127.0.0.1:8080/v1/tools/build_structure/call
```

```json
{"ok":false,"code":"confirmation_required","message":"build_structure needs the player's approval. Ask the player to approve confirmation 3959...e5bb3bd0effe5e3ff2 in the game, then repeat this exact call. It expires at 11:30 UTC.","data":{"confirmation_id":"3959...","expires_at":"2026-11-19T11:30:36Z"}}
```

The agent tells the player. The player approves. In this demo the dev API does it (in a real
deployment your game's backend does it with a signed request):

```bash
curl -s -H 'X-Dev-Player: p1' http://127.0.0.1:8080/v1/confirmations
curl -s -X POST -H 'X-Dev-Player: p1' \
  http://127.0.0.1:8080/v1/confirmations/<id>/approve
```

```json
{"ok":true,"result":"approved"}
```

Repeat the exact same call and it goes through:

```json
{"ok":true,"data":{"buildings":1,"coins":565}}
```

The approval is used up. A second identical call asks for approval again.

## 6. Read the audit trail

Every attempt is an audit event. In the demo they go to the gateway log:

```bash
docker compose logs gateway | grep agent_call | grep '"player":"p1"'
```

```json
{"level":"INFO","msg":"agent_call","tenant":"dev","player":"p1","tool":"sell_resource","tier":"medium","outcome":"ok","code":"","args_sha256":"6ac4cafb..."}
{"level":"INFO","msg":"agent_call","tenant":"dev","player":"p1","tool":"sell_resource","tier":"medium","outcome":"denied","code":"invalid_arguments","args_sha256":"f82ba1c7..."}
{"level":"INFO","msg":"agent_call","tenant":"dev","player":"p1","tool":"build_structure","tier":"medium","outcome":"denied","code":"confirmation_required","args_sha256":"ce5a5142..."}
```

The log holds a hash of the arguments, never the arguments. The player and admin activity feeds
(`/v1/studio/activity`) are served when you run with OAuth instead of `--dev-auth`. See the
[protocol](../PROTOCOL.md).

## 7. Use your own manifest

Write a manifest for your game. This one has two tools:

```json manifest
{
  "version": 1,
  "game": "my-game",
  "tools": [
    {"name": "get_player", "description": "The player's profile and balance.",
     "tier": "read", "http": {"method": "GET", "url": "/api/player"}},
    {"name": "claim_daily_reward", "description": "Claim today's login reward. Once per day.",
     "tier": "safe", "caps": {"per_day_calls": 1},
     "http": {"method": "POST", "url": "/api/rewards/daily"}}
  ]
}
```

Save it as `my.json` and check it. The command runs in a container:

```bash
docker compose run --rm dev go run ./cmd/gwctl validate my.json
docker compose run --rm dev go run ./cmd/gwctl tools my.json
```

```text
ok: 2 tools
read       get_player
safe       claim_daily_reward
```

A mistake is reported with every problem at once:

```text
manifest invalid:
  - tools[0] ("x"): name must match ^[a-z][a-z0-9_]{1,63}$
  - tools[0] ("x"): http.url must be a path starting with /
```

To run the gateway on your manifest, change `--manifest` and `--backend-url` in the `gateway`
service of `docker-compose.yml` and mount your file. The full format is in the
[manifest reference](../MANIFEST.md).

## 8. Clean up

```bash
docker compose down
```

## Next

[Connect an agent](connect-an-agent.md), or read [how a call flows](../concepts/pipeline.md).
