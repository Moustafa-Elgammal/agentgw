# Manifest

A manifest lists the tools a game exposes. Two formats compile to the same structure: JSON
(the wire format) and YAML (the authoring format, `.yaml` or `.yml`). Both reject unknown
fields and duplicate keys. `examples/city/manifest.yaml` is the YAML twin of `manifest.json`,
and a test keeps them identical.

```json
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

## Fields

| Field | Rules |
|---|---|
| `version` | Must be `1` |
| `tools[].name` | `^[a-z][a-z0-9_]{1,63}$`, unique |
| `tools[].description` | Required. The agent reads it, so write it for the agent |
| `tools[].tier` | `read`, `safe`, `medium` or `sensitive` (sensitive is disabled in v1) |
| `tools[].http` | `method` GET, POST, PUT, PATCH or DELETE. `url` is a path starting with `/`; `{name}` placeholders take values from params |
| `tools[].params` | `type` is `string`, `integer`, `number` or `boolean`. Optional: `enum` (strings), `min`/`max` (numbers), `optional` |
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
