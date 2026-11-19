# Guides

Follow the tutorials in order to onboard a toy game, then keep the rest for reference. Every
tutorial has a script that CI runs against the compose stack, so a command that stops working
fails the build.

## Learning path

| Step | Time | You will |
|---|---|---|
| [1. Play the example game with an agent](../tutorials/01-play-the-example-game.md) | 10 to 15 min | run the demo, connect an agent, watch caps and the audit |
| [2. Describe your game's actions](../tutorials/02-describe-your-actions.md) | 15 min | write a manifest from scratch and validate it |
| [3. Make your backend accept signed calls](../tutorials/03-accept-signed-calls.md) | 20 min | verify signatures in Go, Python, TypeScript or C# |
| [4. Link a player](../tutorials/04-link-a-player.md) | 15 min | mint a link ticket, choose coach or operator, list and revoke grants |
| [5. Ask the player before big actions](../tutorials/05-ask-before-big-actions.md) | 15 min | mark a tool `confirm` and build the approve and deny path |
| [6. Go live](../tutorials/06-go-live.md) | 20 min | use Postgres and Redis, the admin key, upload, roll back, rotate keys |
| [7. Test your integration](../tutorials/07-test-your-integration.md) | 15 min | run the conformance suite against your setup |

## Reference guides

- [Best practices](best-practices.md): design tools for an agent, tiers, caps, confirmation, keys, operations.
- [Cookbook](cookbook.md): manifests and recipes you can paste.
- [Troubleshooting](troubleshooting.md): every result code, with cause, check and fix.
- [FAQ](faq.md): short answers.

## Before you start

You need Docker with Compose v2 and nothing else. Every command runs in containers. The concepts
behind the guides are in [how a call flows](../concepts/pipeline.md),
[tiers and policy](../concepts/tiers-and-policy.md) and
[players, grants and confirmations](../concepts/players-grants-confirmations.md).

## Versions

Each tutorial states the version it was verified against. See the
[changelog](../../CHANGELOG.md) for changes since.
