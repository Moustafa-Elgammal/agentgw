# Pet shop backend (python)

A toy REST API for the fictional Pawprint Pets game. It accepts the gateway's signed calls
(see [tutorial 3](../../docs/tutorials/03-accept-signed-calls.md)) and honors idempotency keys.
The manifest that describes it is [`../pet-shop/manifest.yaml`](../pet-shop/manifest.yaml), built up
in [tutorial 2](../../docs/tutorials/02-describe-your-actions.md).

```bash
docker compose -f examples/pet-backend-python/compose.yml up --build -d
curl -s -X POST -H 'X-Dev-Player: p1' -d '{"arguments":{"pet":"rex","food":"fish","amount":2}}' \
  http://127.0.0.1:8090/v1/tools/feed_pet/call
docker compose -f examples/pet-backend-python/compose.yml down
```

The gateway is on `127.0.0.1:${GW_PORT:-8090}` and the backend on `127.0.0.1:${PET_PORT:-9190}`.
The gateway runs with `--dev-auth` and in-memory stores: local trying only.

| Endpoint | What it does |
|---|---|
| `GET /api/v1/shop` | coins and pet names |
| `GET /api/v1/pets/{name}` | one pet |
| `POST /api/v1/pets/feed` | feed a pet (kibble 1 coin, fish 2 coins a unit) |
| `POST /api/v1/pets/adopt` | adopt a pet for 50 coins |

State is in memory and resets on restart. Refusals are a `4xx` with a short code and hint, such as
`not_enough_coins: need 50, have 20`; the gateway passes them to the agent as `backend_rejected`.
