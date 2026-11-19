# Cookbook

Small recipes. Each manifest block is checked by the real validator in CI, so you can paste it.
Background is in the [best practices](best-practices.md).

## A read-only coach manifest

Start here. A coach agent can look but not touch. This is the safest first release.

```yaml manifest
version: 1
game: coach-only
tools:
  - name: get_city_state
    description: Population, treasury in coins, and stock of each resource in units.
    tier: read
    http: {method: GET, url: /api/city}

  - name: get_prices
    description: Current buy and sell price in coins per unit for each resource.
    tier: read
    http: {method: GET, url: /api/market/prices}

  - name: get_building
    description: "Details of one building by id: type, level, output per hour."
    tier: read
    http: {method: GET, url: "/api/buildings/{building_id}"}
    params:
      building_id: {type: integer, min: 1}
```

Notes:

- `{building_id}` in the URL must have a parameter of the same name. The value is placed in the
  path as one segment. `.`, `..` and an empty value are refused.
- Read tools take no caps and cannot require confirmation. The validator rejects both.
- Players can also link an agent as **coach** even if the manifest has action tools: coach grants
  never see them. A read-only manifest just makes that true for everyone.

## An operator manifest

Add action tools one at a time. Each tier has its own controls.

```yaml manifest
version: 1
game: operator
tools:
  - name: get_city_state
    description: Population, treasury in coins, and stock of each resource in units.
    tier: read
    http: {method: GET, url: /api/city}

  - name: collect_taxes
    description: Move pending taxes into the treasury. Usable once a minute.
    tier: safe
    caps: {cooldown_seconds: 60}
    http: {method: POST, url: /api/taxes/collect}

  - name: sell_resource
    description: Sell units of one resource at the current price. At most 300 units a day.
    tier: medium
    caps: {per_day_amount: 300, amount_param: quantity, per_day_calls: 20}
    http: {method: POST, url: /api/market/sell}
    params:
      resource: {type: string, enum: [food, water, materials]}
      quantity: {type: integer, min: 1, max: 1000}
```

## Per-tier caps

Pick the control that matches the risk.

| Tier | Use | Example |
|---|---|---|
| `read` | rate limit only | state, prices, inventory |
| `safe` | a cooldown | collect, repair, claim a reward |
| `medium` | cooldown and daily caps | sell, buy, build |

A `medium` tool can combine all three controls:

```yaml manifest
version: 1
tools:
  - name: buy_resource
    description: Buy units of one resource. At most 500 units and 10 purchases a day, and a 30 second pause between purchases.
    tier: medium
    caps: {per_day_amount: 500, amount_param: quantity, per_day_calls: 10, cooldown_seconds: 30}
    http: {method: POST, url: /api/market/buy}
    params:
      resource: {type: string, enum: [food, water, materials]}
      quantity: {type: integer, min: 1, max: 200}
```

Say the limit in the description. The agent then plans around it instead of finding it by
failing. When it does fail, `daily_amount_cap` tells it how many units are left.

## Rate limit tuning

The per-minute limits are fixed in the gateway: **120 reads and 30 actions per player per minute**.
Every attempt counts, valid or not. There is no flag for them in this version, so you tune the
behavior through the manifest instead:

- Make tools coarser. One `get_city_state` that returns everything uses one read, ten small
  reads use ten.
- Put the numbers an agent needs in the answer, so it does not call again to find out.
- Use `cooldown_seconds` on actions that are only worth doing now and then.
- Watch `rate_limited` in the activity feed or in the `gateway_calls_total` metric. If real players
  hit it, your tools are too fine grained, or an agent is looping.

Limits are per player. Without Redis they are per process and reset on restart, so use Redis for
anything real.

## A shop tool with confirmation

Spending real value should ask the player first. `confirm` makes every call wait for approval.

```yaml manifest
version: 1
game: shop
tools:
  - name: list_shop_items
    description: "Items for sale: id, name and price in gems."
    tier: read
    http: {method: GET, url: /api/shop/items}

  - name: buy_shop_item
    description: Buy one shop item by id for gems. The player must approve every purchase.
    tier: medium
    confirm: true
    caps: {per_day_calls: 5}
    http: {method: POST, url: /api/shop/buy}
    params:
      item_id: {type: integer, min: 1}
      quantity: {type: integer, min: 1, max: 10}
```

What happens:

1. The agent calls `buy_shop_item`. The result is `confirmation_required` with a confirmation id.
2. Your game shows the player a prompt you wrote, for example "Buy 2 Crystal Lamp for 40 gems".
   Do not use the agent's words.
3. The player approves. Your backend calls the signed approve endpoint.
4. The agent repeats the exact same call. It now runs, and the approval is used up.

If the arguments change, a new approval is needed. If the call fails after approval, the approval
goes back to the player. See [players, grants and confirmations](../concepts/players-grants-confirmations.md).

## Handling backend errors

The gateway maps what your backend answers to a code the agent can act on.

| Your backend answers | The agent gets | Agent should |
|---|---|---|
| 2xx with JSON | `ok: true` and the data | go on |
| 4xx with a short text | `backend_rejected` and your text (at most 500 characters) | read the text, change the call or stop |
| 5xx, connection refused, timeout before sending | `backend_unavailable` | wait and try later |
| a timeout or cancel after sending | `outcome_unknown` | read the game state first |

So make your 4xx text useful. "not_enough_food: you have 12, need 50" lets the agent correct
itself. "bad request" does not. Do not put secrets or player data in it: the agent and the
player's client see it.

Use 4xx for rule violations and 5xx for faults. A 5xx releases the cap and starts no cooldown. A
4xx also releases the cap, and the message goes to the agent.

### Recipe for `outcome_unknown`

The call was cut off after it may have reached your game. It may or may not have happened.

1. Give the agent a read tool that shows the effect, for example the treasury after a sale.
2. Tell it in the description: "if the result is outcome_unknown, call get_city_state before
   repeating".
3. In your backend, honor `X-AG-Idempotency-Key` so a transport level repeat does not apply twice.
   An agent retry is a new call with a new key, so state is the check, not the key.

## Multi-step plans an agent can follow

Agents follow a plan better when the tools point at each other. Put the order in the descriptions:

```yaml manifest
version: 1
game: plans
tools:
  - name: get_city_state
    description: "Treasury in coins and stock in units. Step 1 of selling: read the stock first."
    tier: read
    http: {method: GET, url: /api/city}

  - name: get_prices
    description: "Sell price in coins per unit. Step 2 of selling: compare it with your stock."
    tier: read
    http: {method: GET, url: /api/market/prices}

  - name: sell_resource
    description: "Step 3 of selling: sell units you hold. Do not sell more than get_city_state shows."
    tier: medium
    caps: {per_day_amount: 300, amount_param: quantity}
    http: {method: POST, url: /api/market/sell}
    params:
      resource: {type: string, enum: [food, water, materials]}
      quantity: {type: integer, min: 1, max: 1000}
```

Keep the plan short, and keep the gateway the safety net: even if an agent skips a step, the
caps, cooldowns and your backend rules still apply.

## More

[Troubleshooting](troubleshooting.md), [FAQ](faq.md), [manifest reference](../reference/manifest.md).
