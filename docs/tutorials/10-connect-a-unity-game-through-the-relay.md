# Tutorial 10: Connect a Unity game through the relay

Verified against agentgw v0.1.0 (see the [changelog](../../CHANGELOG.md)). About 25 minutes.
The script [`10-connect-a-unity-game-through-the-relay.sh`](10-connect-a-unity-game-through-the-relay.sh)
runs every command below against a throwaway Docker project, and CI can run that script.

**An honest note first.** This tutorial was verified with the C# console sample game and the
client's test suite, not inside the Unity editor. The console sample uses the same `RelayClient`
that a Unity game would use, so the connection, the calls, the takeover and the goodbye are the
real thing. The Unity-specific parts (the package layout, the `MonoBehaviour`, the JSON library
choice) are written from the Unity documentation and are **not compiled here**. Check them in your
project.

In the earlier tutorials the gateway called a backend of yours. Many Unity games have no such
backend: the rules run **inside the game client**, on the player's machine. The gateway cannot call
a game on a laptop behind a router. The relay turns the connection around: **the game opens an
outbound WebSocket to the gateway, and the gateway sends the agent's tool calls down it.**

You will run the gateway with the relay on, declare two tools that run in the game, connect a
stand-in game, and call it as an agent.

## Prerequisites

- Docker with Compose v2, plus `curl`.
- The repository: `git clone https://github.com/Moustafa-Elgammal/agentgw && cd agentgw`
- [Tutorial 4](04-link-a-player.md) helps, because the agent links to a player in the same way.
- Stop any other agentgw stack first. This one uses `127.0.0.1:8080` and `127.0.0.1:9091`. (The
  script uses other ports for the gateway, so it can run next to a stack you already have.)

## How it fits together

```text
agent (MCP)        gateway                      your game (Unity client)         your backend
    |  tools/call ---->|                                  |                            |
    |                  |<---- GET /v1/relay/connect  -----|  (ticket from your backend)|
    |                  |        WebSocket, ticket         |<---- mint ticket ----------|
    |                  |---- call {id, tool, args} ------>|                            |
    |                  |<--- result {id, ok, data} -------|                            |
    |<---- result -----|                                  |                            |
```

Three parts, three jobs:

- **Your backend** (any small server) holds the studio key. It mints a **relay ticket** for a
  player. The game never sees the studio key.
- **The game** connects with the ticket and answers calls. It is the authority on the game state,
  so it decides whether an action is allowed.
- **The gateway** keeps everything it keeps for any other tool: scopes, rate limits, caps,
  confirmation, audit. A relay tool differs only in **where the call goes**.

## 1. Turn the relay on

The relay is off until the gateway has a secret to sign tickets with:

```bash
export AGENTGW_RELAY_SECRET="$(openssl rand -hex 32)"   # the demo uses a placeholder instead
```

The tutorial's compose file sets a placeholder, and starts the OAuth demo stack with a manifest
whose tools use the relay:

```bash
dc() { docker compose -f docker-compose.yml -f examples/tutorials/compose.oauth.yml -f examples/tutorials/compose.relay.yml "$@"; }
dc up -d --build gateway
curl -s http://127.0.0.1:9091/readyz
```

(`dc` is shorthand for the three compose files. Use it for every command below, so they all share
one project and one network.)

```json
{"status":"ready"}
```

Two things to know. The relay is **never mounted under `--dev-auth`**, so the stack uses real OAuth
(as production does). And with several gateway replicas, give them all the **same** secret and the
same Redis: a ticket is checked by whichever replica the game reaches, and a call is routed to the
replica that holds the player's socket.

## 2. Declare tools that run in the game

`examples/tutorials/relay-game/manifest.yaml`:

```yaml
version: 1
game: relay-demo
tools:
  - name: get_state
    description: The game's wood and farm counts. Call this first.
    tier: read
    backend: relay

  - name: build_farm
    description: Build a farm. A farm costs 5 wood. You start with 10 wood.
    tier: medium
    backend: relay
```

`backend: relay` is the only change from the manifests of [tutorial 2](02-describe-your-actions.md).
The gateway still checks scopes, argument types and limits before it sends anything. A gateway can
mix relay tools and REST tools in one manifest.

## 3. Build the stand-in game

The reference client is in `examples/unity-relay-client`. It is plain C# with no Unity references.
This tutorial runs its console sample, a tiny game with two numbers (10 wood, 0 farms) in memory.

```bash
dc build --quiet dev
dc run --rm -T dev go build -o bin/ ./examples/studiocall ./examples/oauthclient
mkdir -p .relay-tutorial-out
dc run --rm -T -v "$PWD/.relay-tutorial-out:/out" --entrypoint sh unity-client-sample \
  -c 'cp -r /s /tmp/s && cd /tmp/s/sample && dotnet publish -c Release -o /out >/dev/null'
```

## 4. Before the game connects

Link an agent as alice, the way [tutorial 4](04-link-a-player.md) does, and call a tool:

```bash
studio() { dc run --rm -T dev bin/studiocall "$@"; }
LT=$(studio --player alice --expect 201 POST /v1/studio/link-tickets | sed 's/.*"ticket":"\([^"]*\)".*/\1/')
oauth() { dc run --rm -T dev bin/oauthclient "$@"; }
ACCESS=$(oauth --ticket "$LT" --scope operator | sed 's/.*"access_token":"\([^"]*\)".*/\1/')
curl -s -H "Authorization: Bearer $ACCESS" -H 'Content-Type: application/json' \
  -H 'Accept: application/json, text/event-stream' -H 'MCP-Protocol-Version: 2025-06-18' \
  -d '{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"get_state","arguments":{}}}' \
  http://127.0.0.1:8080/mcp
```

```json
{"code":"client_offline","message":"the player's game client is not connected","ok":false}
```

`client_offline` is the relay's way of saying "there is no socket for this player". It is a
**denied** result (nothing ran), so the agent can try again later.

## 5. Mint a ticket and connect

Your backend asks the gateway for a ticket. This is a studio-signed call, like every studio call
(signing code for Go, Python, TypeScript and C# is in [tutorial 3](03-accept-signed-calls.md)). The
signed `X-AG-Player` is the player the ticket is for:

```bash
studio --player alice --expect 201 POST /v1/studio/relay/tickets
```

```json
{"ticket":"eyJ0Ijoi...","expires_at":"2026-01-02T03:05:05Z"}
```

A ticket lives **60 seconds**, names one player and one tenant, and is checked **before** the
WebSocket upgrade (a bad or expired one gets `401 invalid_ticket`). The backend returns it to the
game, and the game connects at once. Start the console game with it:

```bash
dc run -d -T -v "$PWD/.relay-tutorial-out:/out" \
  -e RELAY_URL=ws://gateway:8080/v1/relay/connect -e RELAY_TICKET="$TICKET" \
  --entrypoint dotnet unity-client-sample /out/ConsoleGame.dll
```

The game's output shows the gateway's `hello` frame, the limits it announces:

```text
connected: gateway protocol 1.1, up to 8 calls at once, 10000 ms per call
```

`up to 8 calls at once` is `--relay-max-in-flight` (more calls than that get `client_busy` at
once, without reaching the game), and `10000 ms per call` is the deadline each call carries. The
gateway also counts the socket:

```bash
curl -s http://127.0.0.1:9091/metrics | grep '^relay_connections '
```

```text
relay_connections 1
```

## 6. The agent plays the game

The same three calls as before now reach the game, which answers:

```text
get_state   -> {"data":{"wood":10,"farms":0},"ok":true}
build_farm  -> {"data":{"wood":5,"farms":1},"ok":true}
build_farm  -> {"data":{"wood":0,"farms":2},"ok":true}
```

The third `build_farm` shows who owns the rules:

```text
build_farm  -> {"code":"not_enough_wood","message":"a farm costs 5 wood, you have 0","ok":false}
```

The **game** refused it, with a code and a message the agent can act on. The gateway passed the
call (the manifest and the player's limits allowed it), but only the game knows its own wood. A
refusal like this reaches the agent as a failed result with the game's code. Choose stable codes
and write the message for the agent, because it is the agent's only feedback.

## 7. What your Unity project needs

Add the client as a local package (Package Manager, "Add package from disk", pick
`examples/unity-relay-client/package.json`), or copy the `Runtime/` folder into `Assets/`. The
client reads and writes its frames with `System.Text.Json`, which Unity does not ship: add it (for
example with NuGetForUnity) or swap the single file `Runtime/RelayJson.cs` for a version that uses
Newtonsoft.Json. `ClientWebSocket` works in the editor and on desktop and mobile players, **not in
WebGL builds**.

Your game has **three jobs**:

1. **Get a ticket from your backend before every connect.** Tickets are short lived and the
   game must not hold the studio key. Give the client a `GetTicket` delegate that calls your own
   endpoint and returns the string.
2. **Answer calls.** Implement a handler that takes a `CallRequest` (tool name, arguments, deadline)
   and returns `CallResult.Success(...)` or `CallResult.Fail("code", "message")`.
3. **Run and stop the client with your scene.** Start `RunAsync` when the game starts, and call
   `StopAsync` when it ends, so the gateway sees a goodbye and not a dropped socket.

The one Unity-specific trap is threads. The client calls your handler on a **thread pool thread**,
and Unity objects may only be touched on the **main thread**. Queue the work and drain the queue in
`Update`. This is a sketch for the tutorial's two tools, **not compiled in this tutorial** (the
repository has a longer one in `examples/unity-relay-client/Samples~/Basic/RelayBehaviour.cs`, also
not run in Unity):

```csharp
using System;
using System.Collections.Concurrent;
using System.Threading;
using System.Threading.Tasks;
using AgentGw.Relay;
using UnityEngine;

public class RelayBehaviour : MonoBehaviour
{
    [SerializeField] string relayUrl = "wss://game.example.com/v1/relay/connect";

    readonly ConcurrentQueue<Action> mainThread = new ConcurrentQueue<Action>();
    RelayClient client;
    CancellationTokenSource cts;
    int wood = 10, farms = 0;

    void Start()
    {
        cts = new CancellationTokenSource();
        client = new RelayClient(new RelayOptions
        {
            Url = new Uri(relayUrl),
            GetTicket = FetchTicketAsync,                 // job 1
            Log = m => Debug.Log("[relay] " + m),
        }, new DelegateHandler(HandleOnMainThread));      // job 2
        client.Fatal += m => Debug.LogError("[relay] " + m);
        _ = client.RunAsync(cts.Token);                   // job 3
    }

    void Update() { while (mainThread.TryDequeue(out var work)) work(); }

    async void OnDestroy()
    {
        if (client != null) await client.StopAsync("scene closed"); // sends the goodbye
        cts?.Cancel();
    }

    Task<string> FetchTicketAsync(CancellationToken ct) =>
        throw new NotImplementedException("call your backend, return the ticket string");

    Task<CallResult> HandleOnMainThread(CallRequest call, CancellationToken ct)
    {
        var tcs = new TaskCompletionSource<CallResult>();
        ct.Register(() => tcs.TrySetCanceled());
        mainThread.Enqueue(() =>
        {
            if (ct.IsCancellationRequested) return;       // the gateway already gave up
            tcs.TrySetResult(Run(call));
        });
        return tcs.Task;
    }

    CallResult Run(CallRequest call)
    {
        switch (call.Tool)
        {
            case "get_state": return CallResult.Success(new { wood, farms });
            case "build_farm":
                if (wood < 5) return CallResult.Fail("not_enough_wood", "a farm costs 5 wood, you have " + wood);
                wood -= 5; farms++;
                return CallResult.Success(new { wood, farms });
            default: return CallResult.Fail("unknown_tool", "this game has no tool " + call.Tool);
        }
    }
}
```

Rules that keep a handler honest:

- **Answer before `call.DeadlineMs`.** The token is cancelled at the deadline. After it, the gateway
  has told the agent the outcome is unknown, and drops your late result. Stop work you have not
  started.
- **Validate again.** The gateway checks arguments against the manifest, but the game owns its rules.
- **The gateway never resends a call.** `call.Idem` is for your own bookkeeping. If a call was running
  when the socket dropped, the agent was told `outcome_unknown`, so keep your state consistent either way.
- On IL2CPP, reflection used by `CallResult.Success(object)` can be stripped. If a build fails to
  serialize, pass raw JSON: `CallResult.Success("{\"wood\":3}")`.

## 8. A second client for the same player takes over

A player has **one socket**. If the same player connects again, from a second device or a restarted
game, the gateway closes the old socket with **code 4000** and keeps the new one. Mint a second
ticket and start a second game:

```bash
studio --player alice --expect 201 POST /v1/studio/relay/tickets   # a new ticket
# start a second console game with it, as in step 5
```

The first game's output shows how the reference client behaves when it is replaced:

```text
[relay] disconnected: closed with code 4000
[relay] reconnecting in 30000 ms
```

It waits the **longest** backoff (30 seconds) after a 4000, because the player has another client
open and fighting for the socket helps nobody. The gateway counts it:

```text
relay_socket_closes_total{reason="replaced"} 1
```

Calls now go to the second game, which has its own fresh state (10 wood), so `get_state` answers
`{"wood":10}` again. (In the tutorial the first game is stopped, because with a fixed ticket it
would retry the same one. A real game fetches a fresh ticket each time.) If you ever see
`reason="replaced"` climbing in production, two clients of one player are replacing each other.

## 9. When the game closes

A game that leaves on purpose sends a **bye** (`StopAsync` does), and the gateway closes with code
1000. The console game does this when you press Ctrl-C:

```text
[relay] disconnected: closed with code 1000
```

From then on the agent's calls fail at once, with the same denied result as before the game connected:

```json
{"code":"client_offline","message":"the player's game client is not connected","ok":false}
```

A game that vanishes (crash, killed process, lost network) is noticed when it stops answering the
gateway's pings, every 20 seconds, and the socket closes with 1001. A call that was **in flight**
at that moment has an unknown outcome: the agent is told `outcome_unknown`, not `client_offline`,
because the game may have run it.

## 10. Limits, reconnecting and retry hints

- **Backpressure.** At most `--relay-max-in-flight` calls (default 8) wait on one player's socket.
  A ninth gets `client_busy` from the gateway at once. A game that is busy can answer a call with
  `CallResult.Fail("client_busy", "...", retryAfterMs: 1000)`; the agent then reads "retry after
  1000 ms" in the message. Put `retryAfterMs` on any failure that is temporary.
- **Frames.** Frames are JSON text, at most 1 MiB, and at most `--relay-max-frames-per-sec`
  (default 100) per second. Above that the socket closes with 1008.
- **Reconnecting.** A closed socket is never resumed. Open a new one, with a **fresh ticket**.
  Back off exponentially with jitter (0 to `min(30 s, 1 s * 2^attempt)`, never faster than once a
  second), and wait the longest time after 4000 or 1008. The reference client does all of this and
  asks your `GetTicket` for a new ticket on every attempt.
- **Replicas.** With several gateway replicas, the socket lives on one of them and the others
  reach it through Redis. All replicas need the same `AGENTGW_RELAY_SECRET` and the same
  `--redis-addr`. Without Redis the relay only works within one replica.

The full wire protocol, with every frame and close code, is in the
[protocol reference](../PROTOCOL.md) (section "Relay"), and the runbook has a
[relay troubleshooting](../RUNBOOK.md) table.

## 11. Clean up

```bash
dc down -v
rm -rf .relay-tutorial-out
```

## What was verified, and what was not

The script checks, against a real gateway: a call before any connection gives `client_offline`; a
ticket is minted; the game connects and shows the gateway's hello (protocol 1.1, 8 calls at once,
10000 ms); the agent's calls reach the game and its answers come back, including a refusal that
the game makes; a second connection for the same player closes the first with 4000; and a goodbye
makes the next call fail with `client_offline`. The client's own test suite (15 tests, in
`examples/unity-relay-client/tests`) covers reconnecting with a fresh ticket, backoff, deadlines
and `client_busy`.

**Not verified:** anything inside the Unity editor (the package import, the `MonoBehaviour`, a
build with IL2CPP), WebGL, a ticket endpoint on a real backend, and a multi-replica deployment.

## What you learned

- The relay lets a game with no backend be played by an agent: the game connects out, the gateway sends calls in.
- Your backend mints a short lived ticket for each connect. The studio key stays on the server.
- The game decides what is allowed and answers with a result or a coded refusal. The gateway still enforces scopes, limits and audit.
- One socket per player: a second connection replaces the first with code 4000.
- No socket means `client_offline`. A socket that drops mid-call means `outcome_unknown`.

## Next

[Tutorial 6: Go live](06-go-live.md) covers the production deployment, including Redis, which
the relay needs across replicas. The [runbook](../RUNBOOK.md) lists the relay metrics and alerts.
