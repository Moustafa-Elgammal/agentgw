# Tutorial 2: Describe your game's actions

Verified against agentgw v0.1.0 (see the [changelog](../../CHANGELOG.md)). About 20 minutes.
The script [`02-describe-your-actions.sh`](02-describe-your-actions.sh) extracts the manifest
from this page, validates it, breaks it on purpose, and then runs it through a real gateway in
front of a small backend. CI runs that script.

You will write a manifest for a tiny REST API, validate it with `gwctl`, read the errors it
reports, and call your tools through a gateway.

## Prerequisites

- Docker with Compose v2, and the repository cloned.
- [Tutorial 1](01-play-the-example-game.md) helps but is not required.

## The game

Pawprint Pets is a toy pet shop with four endpoints. Nothing about it is real; it only exists so
you have an API to describe. A Go and a Python version of the backend are in
`examples/pet-backend-go` and `examples/pet-backend-python`, and you will accept signed calls
in [tutorial 3](03-accept-signed-calls.md).

| Endpoint | What it does |
|---|---|
| `GET /api/v1/shop` | coins, and the names of your pets |
| `GET /api/v1/pets/{name}` | one pet: species and hunger (0 full, 100 starving) |
| `POST /api/v1/pets/feed` | `{"pet","food","amount"}`: feed a pet. Kibble costs 1 coin a unit, fish 2 |
| `POST /api/v1/pets/adopt` | `{"name","species"}`: adopt a pet for 50 coins |

## 1. Decide what an agent may do

Do not expose every endpoint at the same level. For each one, ask what the worst outcome of a
bad call is.

- Reading the shop or a pet loses nothing: tier `read`.
- Feeding spends a few coins and is easy to undo: tier `safe`, with a cap on how much food can
  be given in a day.
- Adopting spends 50 coins and creates something that will want feeding: tier `medium`, and the
  player must approve it (`confirm`).

The four tiers are `read`, `safe`, `medium` and `sensitive`. `sensitive` tools are disabled
unless the studio turns them on. The reasoning behind tiers is in
[Tiers and policy](../concepts/tiers-and-policy.md).

## 2. Write the manifest

```json manifest
{
  "version": 1,
  "game": "pawprint-pets",
  "tools": [
    {"name": "get_shop", "description": "The shop's coins and the names of the pets you own. Call this first.",
     "tier": "read", "http": {"method": "GET", "url": "/api/v1/shop"}},
    {"name": "get_pet", "description": "How one pet is doing. hunger runs from 0 (full) to 100 (starving).",
     "tier": "read", "http": {"method": "GET", "url": "/api/v1/pets/{name}"},
     "params": {"name": {"type": "string"}}},
    {"name": "feed_pet", "description": "Feed a pet. Each unit lowers hunger by 10. kibble costs 1 coin a unit, fish costs 2.",
     "tier": "safe", "caps": {"per_day_amount": 10, "amount_param": "amount"},
     "http": {"method": "POST", "url": "/api/v1/pets/feed"},
     "params": {
       "pet": {"type": "string"},
       "food": {"type": "string", "enum": ["kibble", "fish"]},
       "amount": {"type": "integer", "min": 1, "max": 5}}},
    {"name": "adopt_pet", "description": "Adopt a new pet for 50 coins. The player must approve each adoption.",
     "tier": "medium", "confirm": true, "caps": {"per_day_calls": 3, "cooldown_seconds": 30},
     "http": {"method": "POST", "url": "/api/v1/pets/adopt"},
     "params": {
       "name": {"type": "string"},
       "species": {"type": "string", "enum": ["cat", "dog", "rabbit"]}}}
  ]
}
```

Save it as `pets.json`. It is also in the repository as `examples/pet-shop/manifest.json`. What
each part does:

- **`name` and `description`.** The agent reads them to decide what to call. Write the
  description for the agent: say what the tool does, the units, and the limits. "Each unit
  lowers hunger by 10" lets the agent compute how much to feed.
- **`http`.** How the gateway reaches your API. `{name}` in the URL is filled from the `name`
  param. For `GET` and `DELETE` the other params go in the query string, for the rest they go
  in a JSON body.
- **`params`.** Every argument is checked before your backend sees it. `enum` rejects other
  values, `min` and `max` bound integers. Unknown arguments are refused.
- **`caps`.** `per_day_amount` adds up the `amount` argument, so `feed_pet` can hand out at most
  10 units a day however the agent splits them. `per_day_calls` counts calls and
  `cooldown_seconds` spaces them out.
- **`confirm`.** The gateway holds the call until the player approves it in your game's own UI
  ([tutorial 5](05-ask-before-big-actions.md)).

The complete field list is in the [manifest reference](../reference/manifest.md).

## 3. Validate it

```bash
docker compose run --rm dev go run ./cmd/gwctl validate pets.json
docker compose run --rm dev go run ./cmd/gwctl tools pets.json
```

```text
ok: 4 tools
read       get_shop
read       get_pet
safe       feed_pet
medium     adopt_pet
```

## 4. Read the errors

`gwctl` reports every problem at once, so you fix them in one pass. Here is a manifest with
mistakes:

```json
{
  "version": 1,
  "game": "pawprint-pets",
  "tools": [
    {"name": "Feed Pet", "description": " ", "tier": "admin",
     "http": {"method": "POST", "url": "api/v1/pets/feed"}},
    {"name": "get_pet", "description": "How one pet is doing.", "tier": "read",
     "http": {"method": "GET", "url": "/api/v1/pets/{name}"}},
    {"name": "adopt_pet", "description": "Adopt a pet.", "tier": "read", "confirm": true,
     "http": {"method": "POST", "url": "/api/v1/pets/adopt"}}
  ]
}
```

```text
manifest invalid:
  - tools[0] ("Feed Pet"): name must match ^[a-z][a-z0-9_]{1,63}$
  - tools[0] ("Feed Pet"): description is required (the agent reads it)
  - tools[0] ("Feed Pet"): unknown tier "admin"
  - tools[0] ("Feed Pet"): http.url must be a path starting with /
  - tools[1] ("get_pet"): url placeholder {name} has no matching param
  - tools[2] ("adopt_pet"): read tools cannot require confirmation
```

Each line names the tool and the rule. Fix them: a lowercase name, a description, a real tier, a
leading `/`, a `name` param, and a tier above `read` for a tool that needs approval. The
validator is strict on purpose: unknown fields and duplicate keys are errors too, so a typo
never silently does nothing.

## 5. Run it in front of a backend

Start the gateway and the Go pet shop (use `examples/pet-backend-python/compose.yml` for the
Python one; the API is the same):

```bash
docker compose -f examples/pet-backend-go/compose.yml up --build -d
```

This binds `127.0.0.1:8090`. Call the tools with the same helper as in tutorial 1:

```bash
call() { curl -s -X POST -H 'X-Dev-Player: p1' -d "$2" "http://127.0.0.1:8090/v1/tools/$1/call"; }
call get_shop '{}'
call get_pet '{"arguments":{"name":"rex"}}'
call feed_pet '{"arguments":{"pet":"rex","food":"kibble","amount":5}}'
```

```json
{"ok":true,"data":{"coins":100,"pets":["rex"]}}
{"ok":true,"data":{"hunger":80,"name":"rex","species":"dog"}}
{"ok":true,"data":{"coins":95,"hunger":30,"pet":"rex"}}
```

Now push on the limits. A bad enum value never reaches the backend:

```bash
call feed_pet '{"arguments":{"pet":"rex","food":"pizza","amount":1}}'
```

```json
{"ok":false,"code":"invalid_arguments","message":"invalid food \"pizza\": choose one of [kibble fish]"}
```

Feed another 5, then try 1 more. The daily cap is 10 units:

```bash
call feed_pet '{"arguments":{"pet":"rex","food":"kibble","amount":5}}'
call feed_pet '{"arguments":{"pet":"rex","food":"kibble","amount":1}}'
```

```json
{"ok":false,"code":"daily_amount_cap","message":"feed_pet: 1 needed, 0 left today"}
```

Your own backend can refuse too. Asking about a pet you do not own is a `4xx` from the backend,
and the gateway passes its short message on as `backend_rejected`:

```bash
call get_pet '{"arguments":{"name":"zed"}}'
```

```json
{"ok":false,"code":"backend_rejected","message":"no_such_pet: you do not own that pet"}
```

A good backend refusal is a short code and a hint the agent can act on, as in
`not_enough_coins: need 50, have 20`.

Finally, `adopt_pet` is held until the player approves:

```bash
call adopt_pet '{"arguments":{"name":"milo","species":"cat"}}'
```

```json
{"ok":false,"code":"confirmation_required","message":"adopt_pet needs the player's approval. ...","data":{"confirmation_id":"...","expires_at":"..."}}
```

In this dev setup you approve through the dev API. In a real game your backend does it with a
signed request, which is [tutorial 5](05-ask-before-big-actions.md):

```bash
curl -s -X POST -H 'X-Dev-Player: p1' http://127.0.0.1:8090/v1/confirmations/<confirmation_id>/approve
call adopt_pet '{"arguments":{"name":"milo","species":"cat"}}'
```

```json
{"ok":true,"data":{"adopted":"milo","coins":40}}
```

Clean up with `docker compose -f examples/pet-backend-go/compose.yml down`.

## A variant: games that run in the client

If your game logic runs in the game client, not on a server, you have no REST endpoint to map.
Mark the tool `"backend": "relay"` and drop its `http` block. The gateway then sends the call
over a WebSocket the game client holds open. The rest of the manifest, caps and confirmation
included, works the same. See the Relay section of the [protocol](../PROTOCOL.md).

```json
{"name": "feed_pet", "description": "Feed a pet.", "tier": "safe", "backend": "relay",
 "params": {"pet": {"type": "string"}}}
```

## What you learned

- Choose a tier by the worst outcome of a bad call, and cap what can be lost, not just how
  often a tool is called.
- Write descriptions for the agent: units, limits and what to call first.
- `gwctl validate` reports every problem at once. The gateway checks arguments before your
  backend sees them.
- A `4xx` from your backend, with a short code and hint, reaches the agent as `backend_rejected`.

## Next

[Tutorial 3: make your backend accept signed calls](03-accept-signed-calls.md).
