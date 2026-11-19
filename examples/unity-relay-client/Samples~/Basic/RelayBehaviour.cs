// A Unity sample. It has NOT been run inside Unity: it is written against the public API of
// AgentGw.Relay and the Unity 2021.3 scripting API, and needs checking in your project.
//
// What it shows:
//   * connect when the scene starts, leave with a bye when it ends;
//   * answer tools on the MAIN thread (the relay calls the handler on a thread pool thread,
//     and Unity objects may only be touched on the main thread);
//   * fetch a fresh ticket from your own backend before every connect.
using System;
using System.Collections.Concurrent;
using System.Text.Json;
using System.Threading;
using System.Threading.Tasks;
using AgentGw.Relay;
using UnityEngine;

public class RelayBehaviour : MonoBehaviour
{
    [SerializeField] string relayUrl = "ws://127.0.0.1:8080/v1/relay/connect";
    [SerializeField] string ticketUrl = "https://your-game-backend.example/relay-ticket"; // returns {"ticket":"..."}

    readonly ConcurrentQueue<Action> mainThread = new ConcurrentQueue<Action>();
    RelayClient client;
    CancellationTokenSource cts;
    int wood = 10;

    void Start()
    {
        cts = new CancellationTokenSource();
        client = new RelayClient(new RelayOptions
        {
            Url = new Uri(relayUrl),
            GetTicket = FetchTicketAsync,
            Log = m => Debug.Log("[relay] " + m),
        }, new DelegateHandler(HandleOnMainThread));
        client.Fatal += m => Debug.LogError("[relay] " + m);
        _ = client.RunAsync(cts.Token);
    }

    // Drain the queue here, so game code runs on the main thread.
    void Update()
    {
        while (mainThread.TryDequeue(out var work)) work();
    }

    async void OnDestroy()
    {
        if (client != null) await client.StopAsync("scene closed");
        cts?.Cancel();
    }

    // Ask YOUR backend, which signs a studio request to the gateway (the game never holds the
    // studio key). Replace with your own HTTP call (UnityWebRequest, HttpClient).
    Task<string> FetchTicketAsync(CancellationToken ct) => throw new NotImplementedException("call your backend here");

    Task<CallResult> HandleOnMainThread(CallRequest call, CancellationToken ct)
    {
        var tcs = new TaskCompletionSource<CallResult>();
        ct.Register(() => tcs.TrySetCanceled());
        mainThread.Enqueue(() =>
        {
            if (ct.IsCancellationRequested) return; // the gateway already gave up
            tcs.TrySetResult(Run(call));
        });
        return tcs.Task;
    }

    CallResult Run(CallRequest call)
    {
        switch (call.Tool)
        {
            case "get_wood":
                return CallResult.Success(new { wood });
            case "chop_tree":
                if (!call.Args.TryGetProperty("count", out var n) || n.ValueKind != JsonValueKind.Number)
                    return CallResult.Fail("invalid_arguments", "count must be a number");
                wood += n.GetInt32();
                return CallResult.Success(new { wood });
            default:
                return CallResult.Fail("unknown_tool", "this game has no tool " + call.Tool);
        }
    }
}
