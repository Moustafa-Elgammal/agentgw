# Manifest

A running gateway takes its manifest from the store: `--manifest` only seeds a fresh deployment.
After that, upload new versions and roll back through `/v1/studio/manifest` (see PROTOCOL.md).

A manifest lists the tools a game exposes. Write it in YAML, which is the authoring format
used in every example and tutorial. The gateway also reads JSON, which is what it stores and what
the studio API returns. Both formats compile to the same structure and both reject unknown
fields and duplicate keys. The format is chosen by the file extension: `.yaml` or `.yml` is YAML,
anything else is JSON. For an upload, send `Content-Type: application/yaml` for YAML and
`application/json` for JSON.

```yaml manifest
version: 1
game: city-demo
tools:
  - name: sell_resource
    description: Sell food, water or materials at the current sell price.
    tier: medium
    caps: {per_day_amount: 300, amount_param: quantity}
    http: {method: POST, url: /api/v1/market/sell}
    params:
      resource: {type: string, enum: [food, water, materials]}
      quantity: {type: integer, min: 1, max: 1000}
```

The same manifest as JSON:

```json manifest
{
  "version": 1,
  "game": "city-demo",
  "tools": [
    {
      "name": "sell_resource",
      "description": "Sell food, water or materials at the current sell price.",
      "tier": "medium",
      "caps": { "per_day_amount": 300, "amount_param": "quantity" },
      "http": { "method": "POST", "url": "/api/v1/market/sell" },
      "params": {
        "resource": { "type": "string", "enum": ["food", "water", "materials"] },
        "quantity": { "type": "integer", "min": 1, "max": 1000 }
      }
    }
  ]
}
```

`examples/city/manifest.yaml` is the full city example. It has a JSON twin, `manifest.json`, and
a test keeps the two identical.

Tips for clean YAML files: one block per tool, small maps such as `http`, `caps` and each param on
one line, and quote a value that contains a colon followed by a space, or starts with a special character (a description
like `"Overview: coins and food"`). A typo in a key is reported with its line and column.

## Fields

| Field | Rules |
|---|---|
| `version` | Must be `1` |
| `tools[].name` | `^[a-z][a-z0-9_]{1,63}$`, unique |
| `tools[].description` | Required. The agent reads it, so write it for the agent |
| `tools[].tier` | `read`, `safe`, `medium` or `sensitive` (sensitive is disabled in v1) |
| `tools[].backend` | Optional. `http` (default) sends a signed REST call. `relay` sends the call over the player's game client WebSocket (see PROTOCOL.md, Relay) and takes no `http` binding |
| `tools[].http` | `method` GET, POST, PUT, PATCH or DELETE. `url` is a path starting with `/`; `{name}` placeholders take values from params |
| `tools[].params` | `type` is `string`, `integer`, `number` or `boolean`. Optional: `enum` (strings), `min`/`max` (numbers), `optional` |
| `tools[].confirm` | Optional boolean. `true` makes every call wait for the player's approval (see DECISIONS.md, Confirmations). Not allowed on `read` tools |
| `tools[].caps` | `per_day_calls`, `per_day_amount` + `amount_param`, `cooldown_seconds`. Read tools take no daily caps |

Unknown fields are rejected so typos fail loudly. `gwctl validate` reports every problem
at once.

## Request mapping

- `GET` and `DELETE`: remaining params become the query string.
- Other methods: remaining params become a JSON body.
- Params used in `{placeholders}` are removed from the query or body.

## Caps in M0

`per_day_amount` counts units of the integer param named by `amount_param` (for example
`quantity`), not coin value. Value-based caps need a price from the backend and are an
open decision (see DECISIONS.md). Day boundaries are UTC.
