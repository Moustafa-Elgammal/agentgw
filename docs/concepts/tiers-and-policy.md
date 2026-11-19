# Tiers and policy

Policy is how the gateway keeps an agent inside limits you chose. Most of it is set in the
manifest, a little in gateway flags, and the live counters live in Redis.

## Tiers

Every tool has a tier. The tier decides which controls apply.

| Tier | Typical tools | Controls | State |
|---|---|---|---|
| `read` | state, inventory, prices | rate limit | enabled |
| `safe` | collect, repair, plant | rate limit, cooldown | enabled |
| `medium` | build, buy, sell | rate limit, cooldown, daily caps | enabled |
| `sensitive` | gifts, paid currency, deletion | none: disabled | refused with `tier_disabled`, never listed to agents |

Read tools cannot require confirmation and cannot have daily caps. The validator rejects both.

## Limits

**Rate limit.** Per player, per minute. Reads and actions have separate budgets: 120 reads and 30
actions. Every attempt counts, even an invalid one. The numbers are built into the gateway today
and are not a flag. Over the budget gives `rate_limited`.

**Daily caps.** Set per tool in the manifest.

```yaml manifest
version: 1
game: caps-demo
tools:
  - name: collect_taxes
    description: Move pending taxes into the treasury.
    tier: safe
    caps: {cooldown_seconds: 60}
    http: {method: POST, url: /api/taxes/collect}

  - name: sell_resource
    description: Sell a resource at the current price.
    tier: medium
    caps: {per_day_calls: 20, per_day_amount: 300, amount_param: quantity}
    http: {method: POST, url: /api/market/sell}
    params:
      resource: {type: string, enum: [food, water]}
      quantity: {type: integer, min: 1, max: 1000}
```

- `per_day_calls` limits how many calls succeed in a day (`daily_call_cap`).
- `per_day_amount` limits a total, taken from an integer argument named by `amount_param`
  (`daily_amount_cap`). The message says how much is left.
- Days are UTC days. Caps are per player and per tool.

**Cooldowns.** `cooldown_seconds` is the wait after a successful call of that tool. A call that
fails does not start one (`cooldown` says how many seconds remain).

**Reserve and settle.** Caps are claimed before the call and released if the call fails, so a
failed call costs nothing and concurrent calls cannot go over a cap. See
[how a call flows](pipeline.md).

## Kill switches

A kill switch turns off everything matching a tenant, a tool and a player. Empty fields match
everything. A matching call gets `switched_off`, and the message tells the agent not to retry. A
switch blocks every tier it matches, reads included, so to keep reads working switch off action
tools by name. Switches are checked first, and are not charged to the rate limit.

Today the endpoint for switches is the dev admin API (`GET` and `POST /v1/admin/switches`, behind
`X-Dev-Admin: true`, loopback only). A studio-signed switch endpoint is not built yet. In
production, you can also remove a tool by uploading or rolling back a manifest, and a player
can revoke an agent. See the [runbook](../RUNBOOK.md).

## Fail closed

If a store the safety checks depend on is unreachable, the answer is no.

- Redis down: `policy_unavailable`, for reads and actions.
- Confirmation store down: `confirmation_unavailable`, nothing is changed.
- A failed backend call: `backend_unavailable`, the reservation is released.

## Who controls what

| Where | What | Examples |
|---|---|---|
| **Manifest** (uploaded, versioned, rolled back without a restart) | Tools, tiers, argument types and ranges, `confirm`, caps, cooldowns | `per_day_amount`, `confirm: true` |
| **Gateway flags and environment** | Stores, keys, listeners, retention, the relay | `--redis-addr`, `--postgres-url`, `--audit-retention`, `AGENTGW_SIGNING_SECRET` |
| **Redis** (live state) | Counters, cooldown clocks, kill switches, shared by every replica | rate windows, daily usage |
| **The player** | Scope of each grant, approvals, revocation | coach or operator |
| **Your backend** | The game rules themselves | prices, energy, legality |

Without `--redis-addr` the counters live in the process. They reset on restart and are not shared
between replicas, so use Redis for anything real.

## More

[Manifest reference](../MANIFEST.md), [security model](../SECURITY-MODEL.md),
[runbook](../RUNBOOK.md).
