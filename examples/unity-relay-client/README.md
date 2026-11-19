# agentgw relay client for Unity (C#)

A small reference client for the [agentgw](../../README.md) relay. Use it when your game's
logic runs in the client (a Unity game, for example) and has no backend endpoint the gateway can
call. The game opens an outbound WebSocket to the gateway, and the gateway sends it the tool calls
an AI agent makes for the player. The game runs them and answers.

**Status.** Plain C# with no engine references, written against relay protocol 1.1
([docs/PROTOCOL.md](../../docs/PROTOCOL.md), "Relay"). It is tested with .NET 8 against an
in-process fake gateway and against the real gateway (see below). **It has not been run inside
Unity.** Treat the Unity package layout and the sample MonoBehaviour as a starting point to check
in your project.

## What it does

- Connects with a ticket and reads the gateway's hello (limits, call timeout, ping interval).
- Answers each `call` through your `IRelayHandler`, with the call's deadline as a cancellation
  token, and ignores a call id it has already seen.
- Refuses calls over the gateway's in-flight limit with `client_busy` and a retry hint.
- Reconnects with exponential backoff and full jitter, asking for a fresh ticket every time, and
  waits the longest time after code 4000 (another client of the same player took over).
- Leaves with a `bye` frame on `StopAsync`.
- Never logs tickets or call arguments, and never sends an exception message to the agent.

## Layout

| Path | What |
|---|---|
| `Runtime/` | the library (`AgentGw.Relay` asmdef): `RelayClient`, `IRelayHandler`, `CallRequest`, `CallResult`, `Backoff`, `CallDeduper`, `RelayJson` |
| `Samples~/Basic/` | a Unity `MonoBehaviour` that answers two tools on the main thread (**not run in Unity**) |
| `sample/` | a console game (two tools, in-memory state) |
| `tests/` | 15 tests against an in-process fake gateway, no packages |
| `e2e.sh` | the console game against the real gateway in Docker |
| `package.json` | Unity package manifest (`com.agentgw.relay-client`) |

## Use it in Unity

1. Add the folder as a local package (Package Manager, "Add package from disk", pick
   `package.json`), or copy `Runtime/` into `Assets/`.
2. The client uses `System.Text.Json`, which Unity does not ship. Add it (for example with
   NuGetForUnity), or replace the one file `Runtime/RelayJson.cs` with a version that uses
   Newtonsoft.Json (`com.unity.nuget.newtonsoft-json`). No other file touches JSON frames.
3. `ClientWebSocket` works in the Unity editor and on desktop and mobile players. It does not work
   in WebGL builds: they need the browser's WebSocket, which needs a small adapter, and the gateway
   must allow your page's origin (`WithOriginPatterns`).
4. Get a ticket from **your own game backend** before every connect. The backend calls
   `POST /v1/studio/relay/tickets`, signed with the studio key, and returns the ticket to the
   game. The game never holds the studio key.
5. Handlers run on a thread pool thread. Hand the work to the main thread and await it, as
   `Samples~/Basic/RelayBehaviour.cs` does.
6. IL2CPP can strip reflection that `CallResult.Success(object)` uses. If a build fails to
   serialize, pass raw JSON: `CallResult.Success("{\"wood\":3}")`.

```csharp
var client = new RelayClient(new RelayOptions
{
    Url = new Uri("wss://game.example/v1/relay/connect"),
    GetTicket = ct => MyBackend.FetchRelayTicketAsync(ct),
}, new DelegateHandler(async (call, ct) =>
{
    if (call.Tool == "build_farm") return CallResult.Success(new { built = 1 });
    return CallResult.Fail("unknown_tool", "this game has no tool " + call.Tool);
}));
_ = client.RunAsync(cancellationToken);
// ...
await client.StopAsync("app quit");
```

## Rules for handlers

- Answer before `call.DeadlineMs`. The token is cancelled at the deadline: stop work you have
  not started. After the deadline the gateway has told the agent the outcome is unknown and drops
  your late result.
- Return failures as `CallResult.Fail("a_stable_code", "what the agent should do about it")`.
  The agent reads the code and message. Put `retryAfterMs` on failures that are temporary.
- The gateway never resends a call, so `idem` is only for your own bookkeeping. If a call was in
  flight when the socket dropped, its outcome is unknown to the agent: keep your game state
  consistent either way.
- Validate arguments again. The gateway checks them against the manifest, but the game owns its
  rules (the sample checks that a farm is affordable).

## Run the tests

```bash
docker compose run --rm unity-client-test        # from the agentgw repository root
```

Expect `ALL 15 TESTS PASSED`. The tests check the client against protocol 1.1 as the docs describe
it: hello, calls, results, dedupe, deadlines, busy, reconnect with a fresh ticket, backoff, bye.

## Try it against the real gateway

```bash
bash examples/unity-relay-client/e2e.sh
```

It starts the gateway with the relay on (OAuth mode, as production would), publishes the console
game, mints a relay ticket for a player, connects, then links an agent and calls the game's tools
over MCP. It checks a success, a refusal that comes from the game, a clean `bye` (counted in
`relay_socket_closes_total{reason="bye"}`) and `client_offline` afterwards.

To point the console game at your own gateway:

```bash
RELAY_URL=ws://host.docker.internal:8080/v1/relay/connect RELAY_TICKET=<ticket> \
  docker compose run --rm unity-client-sample
```

Tickets last 60 seconds, so mint one right before you start the game.
