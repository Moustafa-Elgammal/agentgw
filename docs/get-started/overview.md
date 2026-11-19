# Overview

agentgw is a gateway that sits between a player's AI agent and your game. The player's agent
can look at the game and, if the player allows it, take a few actions. Your game stays in
charge of everything. The gateway only decides what the agent may ask for, how often, and how
much, and it writes down every attempt.

## Why a gateway

More players now use an AI assistant (Claude, for example) to help them play. Those assistants
talk to tools through a standard called [MCP](https://modelcontextprotocol.io), the Model
Context Protocol. You do not need to know MCP to use agentgw. You describe your game actions in
a file. The gateway turns each action into an MCP tool the agent can call.

Without a gateway you would have to build all of that yourself: tool lists, login for agents,
rate limits, daily caps, an audit trail, a way for players to switch agents off. agentgw does
those parts so you can keep your backend as it is.

## Who is involved

| Who | What they do |
|---|---|
| **Studio** (you) | Writes the manifest, runs the gateway, keeps the game backend, holds the signing keys |
| **Player** | Links their own agent to their account, picks how much it may do, can revoke it any time |
| **Agent** | Reads the tool list, calls tools, reads the results and error codes, corrects itself |

## What the gateway does

- Turns a manifest into MCP tools. Tool descriptions come only from your manifest.
- Checks every call: tier, rate limit, argument types and ranges, daily caps, cooldowns.
- Asks the player to approve sensitive actions, one call at a time.
- Calls your backend with a signed request, so your backend knows the call is real.
- Records an audit event for every attempt, with a hash of the arguments and not the arguments.
- Lets you switch tools off, roll back a manifest, and rotate keys without a restart.

## What it does not do

- **It holds no game logic.** Your backend decides if a build is legal, how much a sale pays,
  and whether the player has enough coins. The gateway cannot override that.
- **It gives the agent no side door.** An agent plays by the same rules as a person. If the
  game says no, the answer is no.
- **It does not trust the agent.** Every attempt counts against the rate limit, even a bad one.
  Backend answers are treated as data and never as instructions to the agent.
- **It does not store player data.** It stores grants, confirmations, manifests and a hashed
  audit log.

## Two modes for players

When a player links an agent, they choose a scope.

- **Coach** is read only. The agent can see state, prices and advice. It never sees action tools.
- **Operator** lets the agent take limited actions for the player, inside the caps you set.

The player can revoke a linked agent at any time from your game's own screen. See
[players, grants and confirmations](../concepts/players-grants-confirmations.md).

## Three ways to connect your game

| Level | Your effort | How it works |
|---|---|---|
| **Manifest only** (level 0) | A manifest file | Each tool maps onto a REST endpoint you already have. The gateway signs the request. |
| **Signed endpoint** (level 1) | One endpoint | Your backend verifies the signature and handles tool calls in any language. Go backends can import `pkg/agentsig`. |
| **Relay** | A game client that opens a WebSocket | For games whose logic runs in the client. The gateway sends each call down the player's socket. Tools use `"backend": "relay"`. |

Levels 0 and 1 use the same signed request, described in the [protocol](../PROTOCOL.md). The
relay is in the same document under "Relay".

## Where to go next

1. [Run the demo in five minutes](quickstart.md).
2. [Connect an agent](connect-an-agent.md).
3. [How a call flows](../concepts/pipeline.md).
4. [The manifest format](../MANIFEST.md).
