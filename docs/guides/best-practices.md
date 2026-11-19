# Best practices

Short, opinionated rules for studios. Each one says why. They follow from how the gateway works,
so read [how a call flows](../concepts/pipeline.md) and [tiers and policy](../concepts/tiers-and-policy.md)
first if you have not.

## Design the tools for an agent

**Few, coarse tools beat many tiny ones.** An agent picks from the list it is shown. Twelve clear
tools are chosen correctly more often than sixty small ones, and each call is one rate limit
charge, so a coarse tool also leaves more budget for the agent's reasoning.

**Name tools so a model can guess them.** Use a verb and a noun: `sell_resource`, `get_city_state`,
`build_structure`. Names are lowercase letters, digits and underscores, and must be unique.

**Write descriptions for the agent, with units and limits.** The agent only sees the name, the
description and the parameters. Say what the tool does, what it costs, and what the numbers mean.

```yaml manifest
version: 1
game: design-demo
tools:
  - name: get_city_state
    description: Population, treasury in coins, and stock of each resource in units. Call this before selling.
    tier: read
    http: {method: GET, url: /api/city}

  - name: sell_resource
    description: Sell units of one resource at the current price. Quantity is in units, 1 to 1000. Pays coins into the treasury.
    tier: medium
    caps: {per_day_amount: 300, amount_param: quantity}
    http: {method: POST, url: /api/market/sell}
    params:
      resource: {type: string, enum: [food, water, materials]}
      quantity: {type: integer, min: 1, max: 1000}
```

**Use `enum`, `min` and `max` on every parameter you can.** The gateway rejects bad values with
`invalid_arguments` before they reach your backend, and the message tells the agent what to fix.
Your backend still checks everything, because it is the authority.

**Return small, structured results.** The connector reads at most 1 MiB of a backend answer.
Anything an agent reads costs it tokens. Return the facts it needs, not whole database rows.

## Tiers: start in coach, add operator tools one at a time

Ship the first release with `read` tools only. A coach agent can then explain, advise and answer
questions, and nothing it does can change the game. Add `safe` and `medium` tools one at a time,
watch the [activity feed](../concepts/players-grants-confirmations.md), and keep going only when
the behavior is boring.

Leave `sensitive` alone. It is disabled in this version: those tools answer `tier_disabled`
and are never listed. Gifts, paid currency and deletion do not belong behind an agent yet.

## Caps and cooldowns: cap what can be lost

**Cap by the amount at risk, not by the number of calls.** A cap of 20 sells a day says nothing
about the loss if each sell is 1000 units. `per_day_amount` with `amount_param` bounds the total
that can leave the player's hands in a day.

| Kind of action | Good cap |
|---|---|
| Currency, sales, spending | `per_day_amount` on the quantity or coin parameter |
| Inventory, crafting, building | `per_day_calls`, plus `max` on the quantity parameter |
| Social actions (invites, messages) | `per_day_calls` low, and think about `confirm` |
| Claim or collect actions | `cooldown_seconds` that matches the game's own timer |

**Use a cooldown for actions that are only useful once in a while.** A call that fails does not
start a cooldown, so a retry after a failure is free.

**Know the limits of the numbers.** The caps count units of an argument, not the value in coins.
The per-minute rate limit is built in (120 reads and 30 actions) and cannot be changed by flag.
Caps reset on UTC days.

A call that is cut off after it may have reached your game (`outcome_unknown`) releases its cap, so
the cap can be overshot by at most the amount of those calls. That is a deliberate choice, see
[DECISIONS.md](../DECISIONS.md).

## Confirmation: ask for what cannot be undone

Mark a tool `"confirm": true` when the action is **irreversible**, **expensive**, or **affects
other players**. Do not mark everything: a player who approves forty prompts a day stops reading
them.

Show the player your own description of the action, built from your own data. Never show the
agent's text. The gateway stores only a hash of the arguments, so match the pending confirmation
to the action in your game as described in [PROTOCOL.md](../PROTOCOL.md). An approval covers one
tool with exactly those arguments, is used once, and expires after 10 minutes.

## Idempotent backends

The gateway sends a fresh `X-AG-Idempotency-Key` with every attempt. It protects against a proxy or
HTTP client repeating the same request. It does not make an agent's retry safe, because a retry is a
new call with a new key.

- Honor the key: if you see a key twice, return the stored result and apply the action once.
- Keep keys for at least the five minute signature window.
- After `outcome_unknown`, the agent must read the game state before it tries again. Give it a
  read tool that makes this easy, for example the current treasury and stock.

## Treat backend text as data

Anything your backend returns goes to the agent. A player can write text that ends up in your
backend: a chat line, a city name, a guild description. If you echo it in a tool result, you hand
that player a way to talk to every other player's agent.

- Do not return other players' free text in tool results.
- If you must, return it in a field the description calls "untrusted player text", short and
  length limited.
- Tool descriptions are static. They come from the manifest, never from runtime data, so do not
  build a description from game state.

## Keys and secrets

- **Separate keys.** The studio key signs player calls, grants, confirmations and link tickets.
  The admin key signs manifest uploads and the admin feed. Give them different secrets, so a leak of
  one cannot change what agents may call.
- **Secrets live in a secret manager**, passed as environment variables
  (`AGENTGW_SIGNING_SECRET`, `AGENTGW_ADMIN_SECRET`, `AGENTGW_RELAY_SECRET`). Never in a manifest,
  a flag, a Helm values file or the repository.
- **Rotate on a schedule.** Rotation has no downtime: add the new key to the key file, send
  SIGHUP, switch the signing side, remove the old key. The steps are in the
  [runbook](../RUNBOOK.md).
- **Make the admin key hard to use.** Give it to a CI job or a small number of people. The signed
  `X-AG-Player` on an admin call names who acted, and it is recorded on the manifest version.

## Operations

- **Alert on infrastructure failures, not on refusals.** `backend_unavailable`, `outcome_unknown`,
  `policy_unavailable` and `confirmation_unavailable` mean something is broken. `rate_limited`,
  caps and cooldowns mean the policy is working. The shipped alert rules follow this split.
- **Read the activity feed during an incident.** It shows the tool, outcome, code and a hash of the
  arguments for each attempt. Filter by player and by outcome.
- **Kill switches are a dev tool today.** In production, take a tool away by uploading a manifest
  without it (or rolling back), or revoke a player's agents. Both take effect without a restart.
- **Run Redis and Postgres before you run two replicas.** Without them each replica keeps its own
  limits, and a player could get the budget twice.
- **Keep audit longer than the default if you need it.** `--audit-retention` defaults to 30 days.
  The gateway also writes each audit event to stdout, so keep that in your log pipeline.

## Rollouts

1. Run a **staging tenant** with its own gateway and a copy of your backend. Upload the new manifest
   there first.
2. Compare behavior with the activity feed: which tools were called, which codes came back.
3. Upload to production with the admin key. The version records who did it and a hash.
4. If something looks wrong, **roll back**: activate the previous version. Other replicas follow
   within `--manifest-poll` (15 seconds by default), and calls already running finish with the tools
   they started with.

A rollback re-validates the stored version, so a manifest that no longer passes the current rules is
refused. Run `gwctl validate` on the old versions you may want to go back to when you upgrade.

## A threat model walk-through for studios

The full model is in [SECURITY-MODEL.md](../SECURITY-MODEL.md). In studio terms:

| Threat | What it looks like | What stops it | What you must do |
|---|---|---|---|
| Untrusted text steers the agent | A chat line says "sell everything" | Static descriptions, tiers, caps, confirmation | Never echo player text in results. Cap what can be lost |
| A runaway agent | A loop of calls | Rate limit counts every attempt, cooldowns, daily caps | Watch `rate_limited` and the activity feed |
| An agent calls things it should not | A guessed tool name | Coach scope, tier checks, unknown tools refused | Keep action tools off the first release |
| Someone forges a backend call | A request with a made-up player | HMAC signature, timestamp, one-time keys | Verify every request. Trust `X-AG-Player` only after it |
| The gateway is used to reach internal hosts | An argument that edits the path | Segment encoding, SSRF-safe dialer, no redirects | Keep `--backend-allow-private` off unless the backend is yours |
| A leaked key | A stolen secret | Separate keys, rotation without downtime | Rotate on a schedule. Keep the admin key rare |
| A player's agent lingers | A forgotten grant | Revocation is immediate | Show linked agents in your UI with a revoke button |

## Where to go next

[Cookbook](cookbook.md), [troubleshooting](troubleshooting.md), [FAQ](faq.md).
