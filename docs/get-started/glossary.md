# Glossary

The words the docs use, in plain language. Each entry says where to read more.

**Agent.** The AI assistant a player uses, for example Claude. It reads your tool list and calls
tools for the player. It is not trusted: the gateway checks every call it makes.

**MCP (Model Context Protocol).** The standard way an AI assistant talks to tools. The gateway
turns your manifest into MCP tools, so any MCP client can use your game. You do not need to know
the protocol to use agentgw. See [Connect an agent](connect-an-agent.md).

**Manifest.** The file where you describe your game's actions: a name, a description for the
agent, a tier, the arguments and their limits, and the backend endpoint each action maps to. It
is YAML by default, JSON also works. See the [manifest guide](../MANIFEST.md) and the
[reference](../reference/manifest.md).

**Tool.** One action in the manifest, such as `sell_resource`. The agent sees a tool as a name, a
description and typed arguments.

**Tier.** How risky a tool is. `read` only looks at state (inventory, prices). `safe` is routine
(collect, repair) and gets a cooldown. `medium` can cost the player something (build, buy, sell)
and gets daily caps. `sensitive` (gifts, paid currency, deletion) is disabled: it is refused with
`tier_disabled` and never listed to agents. The tier decides which scope may call a tool. See
[tiers and policy](../concepts/tiers-and-policy.md).

**Caps.** Limits the studio sets per tool: a number of calls a day, an amount a day (such as
coins sold), and a cooldown. They are checked before the backend is called, so concurrent calls
cannot go over them.

**Cooldown.** The minimum time between two calls of the same tool by the same player.

**Rate limit.** How many calls a player may attempt per minute. Every attempt counts, including
invalid ones.

**Scope (coach and operator).** What a player allows a linked agent to do. **Coach** is read only:
the agent sees state and prices and never sees action tools. **Operator** lets the agent take
limited actions inside your caps. A studio can cap everyone at coach with `--max-scope coach`.

**Grant.** The record that a player linked an agent with a scope. A player can list and revoke
grants from your game, and revoking takes effect on the agent's next request. See
[players, grants and confirmations](../concepts/players-grants-confirmations.md).

**Link ticket.** A short one-time code (for example `ABCD-EFGH`) that your backend asks the
gateway for. Your site shows it to the player, who types it on the consent page. That is how the
gateway learns which player is behind an agent without trusting the agent. It lives 5 minutes.

**Confirmation.** A tool marked `confirm: true` is held until the player approves that exact
call. The agent is told to ask the player and then repeat the call. An approval is used once.

**Signed request.** How the gateway calls your backend. Each request carries a timestamp, a key id,
the player, an idempotency key and an HMAC signature, so your backend knows the call is real.
See the [protocol](../PROTOCOL.md).

**Kill switch.** A switch that turns off one tool, one player or the whole game at once, without a
restart. See the [runbook](../RUNBOOK.md).

**Relay.** For games whose logic runs in the client (a Unity game, for example), the game opens a
WebSocket to the gateway and the gateway sends each call down it. A tool uses it with
`"backend": "relay"`. See the [protocol](../PROTOCOL.md).

**Audit.** A record of every attempt: who, which tool, the outcome and a hash of the arguments.
The arguments themselves are never stored.

**Studio.** You: the team that runs the game and the gateway.

**Fail closed.** When something the gateway depends on is down (the policy store, for example),
it refuses actions rather than allowing them.
