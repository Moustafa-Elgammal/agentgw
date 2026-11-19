# Players, grants and confirmations

The player owns the relationship with their agent. The gateway has no player login. Your game
knows who the player is, and it tells the gateway with signed requests.

## Two scopes

When a player links an agent they choose a scope. It is stored on the grant.

- **Coach** allows read tools only. Action tools are not even listed to the agent. If it names
  one anyway, the answer is `scope_insufficient`.
- **Operator** allows read and action tools, inside the caps in your manifest.

The scope is checked before the rate limit and cannot be talked around. Under `--dev-auth` no
scope applies, which is one more reason the demo mode is for local use only.

## Linking an agent

1. The agent's client starts OAuth against the gateway (PKCE, dynamic client registration).
2. Your backend asks the gateway for a **link ticket** with a signed request, naming the player
   in `X-AG-Player`. The ticket is a short code (`ABCD-EFGH`), valid for 5 minutes, single use.
3. Your game shows the code. The player types it into the gateway's consent page and picks coach
   or operator.
4. The client gets a token that stands for that one player. The grant records the client, the
   scope and the player.

Guessing tickets is limited to 10 attempts a minute per address, and every failure looks the
same. The details are in [PROTOCOL.md](../PROTOCOL.md) and the "Auth (M3)" section of
[DECISIONS.md](../DECISIONS.md).

## Grants and revocation

A grant is one linked agent for one player. Your game can show the list and let the player
remove agents, using studio-signed endpoints:

- `GET /v1/studio/grants` lists the player's active grants.
- `DELETE /v1/studio/grants/{id}` revokes one grant and every token it holds.
- `DELETE /v1/studio/grants` revokes all of the player's grants ("log out all agents").

Revocation takes effect on the agent's next request, because every request looks the token up.
A player can only see and revoke their own grants.

## Confirmations

A tool with `"confirm": true` needs the player's approval for each call. Use it for actions the
player would want to see first.

1. The agent calls the tool. The gateway answers `confirmation_required`, with a confirmation id
   and an expiry. The message tells the agent to ask the player.
2. Your game shows the pending confirmation. The player approves or denies. Your backend calls
   `POST /v1/studio/confirmations/{id}/approve` or `/deny`, signed with the studio key. The
   agent's own token can never reach these routes.
3. The agent repeats the **exact same call**. If it was approved, it runs. If it was denied, the
   agent gets `confirmation_denied` and is told not to repeat it.

Details worth knowing:

- An approval is bound to one tool and one exact set of arguments. A different argument needs a
  new approval.
- It is used once. A second identical call asks again.
- It expires after 10 minutes.
- If the call then fails, the approval goes back to the player.
- The gateway stores only a hash of the arguments. Show the player your own description of the
  action, not the hash. The protocol document explains how to match a pending confirmation to
  something you know.

In the demo (`--dev-auth`) the dev API plays the player's part: `GET /v1/confirmations` and
`POST /v1/confirmations/{id}/approve`. See the [quickstart](../get-started/quickstart.md).

## Activity feed

Players can see what their agents did. The studio exposes it in the game with a signed request:

- `GET /v1/studio/activity` returns the player's own events, newest first. Filters are `tool`,
  `outcome`, `since`, `limit` and `before` for paging.
- Admins use `GET /v1/studio/admin/activity` with the admin key, and can also filter by player.

Events carry the time, tool, tier, outcome, code and a hash of the arguments. They are kept for
`--audit-retention` (30 days by default) and are in Postgres, or in memory if no database is set.
The feeds are served in OAuth mode, not under `--dev-auth`.

## More

[Signed request protocol](../PROTOCOL.md), [security model](../SECURITY-MODEL.md).
