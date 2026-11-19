// Tests for the relay client against an in-process fake gateway (HttpListener plus a managed
// WebSocket). No packages, no real gateway: run them with
//   docker compose run --rm unity-client-test
// Exit code 0 when every test passes. The fake gateway speaks protocol 1.1 as docs/PROTOCOL.md
// describes it, so these tests check the client against the spec, not against the Go code.
using System.Net;
using System.Net.Sockets;
using System.Net.WebSockets;
using System.Text;
using System.Text.Json;
using System.Threading.Channels;
using AgentGw.Relay;

var tests = new (string Name, Func<Task> Run)[]
{
    ("client hello names the client and the version", HelloTest.ClientHello),
    ("a call is answered with the same id and data", CallTests.RoundTrip),
    ("a repeated call id runs once", CallTests.Dedupe),
    ("the deadline cancels the handler and sends nothing", CallTests.Deadline),
    ("calls over the gateway's limit get client_busy", CallTests.Busy),
    ("a handler exception does not leak its message", CallTests.Exception),
    ("a 200 KB call is read whole", CallTests.LargeFrame),
    ("a failed result carries code and retry_after_ms", CallTests.FailWithRetry),
    ("unknown frame types are ignored", CallTests.UnknownFrames),
    ("reconnect uses a fresh ticket each time", ConnectionTests.FreshTicket),
    ("close code 4000 waits the longest backoff", ConnectionTests.ReplacedWaits),
    ("stop sends bye and the gateway closes normally", ConnectionTests.Bye),
    ("a refused protocol version is fatal", ConnectionTests.Unsupported),
    ("backoff is jittered, capped and never below the minimum", BackoffTests.Math),
    ("the deduper forgets old ids", BackoffTests.Deduper),
};

int failed = 0;
foreach (var (name, run) in tests)
{
    try
    {
        await run().WaitAsync(TimeSpan.FromSeconds(20));
        Console.WriteLine("PASS  " + name);
    }
    catch (Exception e)
    {
        failed++;
        Console.WriteLine("FAIL  " + name + "\n      " + e.GetType().Name + ": " + e.Message);
    }
}
Console.WriteLine(failed == 0 ? $"\nALL {tests.Length} TESTS PASSED" : $"\n{failed} of {tests.Length} FAILED");
return failed == 0 ? 0 : 1;

static class T
{
    public static void Check(bool ok, string what) { if (!ok) throw new Exception("check failed: " + what); }
}

/// <summary>One accepted socket of the fake gateway.</summary>
sealed class FakeConn
{
    public string Authorization;
    public int? ClientCloseCode;
    readonly WebSocket ws;
    readonly Channel<JsonDocument> frames = Channel.CreateUnbounded<JsonDocument>();
    readonly TaskCompletionSource closed = new();

    public FakeConn(WebSocket ws, string auth, string helloJson)
    {
        this.ws = ws;
        Authorization = auth;
        _ = Pump(helloJson);
    }

    async Task Pump(string helloJson)
    {
        try
        {
            if (helloJson != null) await Send(helloJson);
            var buf = new byte[8192];
            while (ws.State == WebSocketState.Open)
            {
                using var ms = new MemoryStream();
                WebSocketReceiveResult r;
                do
                {
                    r = await ws.ReceiveAsync(buf, CancellationToken.None);
                    if (r.MessageType == WebSocketMessageType.Close) { ClientCloseCode = (int?)ws.CloseStatus; goto done; }
                    ms.Write(buf, 0, r.Count);
                } while (!r.EndOfMessage);
                var doc = JsonDocument.Parse(ms.ToArray());
                if (doc.RootElement.TryGetProperty("type", out var t) && t.GetString() == "bye")
                {
                    await frames.Writer.WriteAsync(doc);
                    try { await ws.CloseAsync(WebSocketCloseStatus.NormalClosure, "bye", CancellationToken.None); } catch { }
                    goto done;
                }
                await frames.Writer.WriteAsync(doc);
            }
        }
        catch (Exception) { }
        done:
        frames.Writer.TryComplete();
        closed.TrySetResult();
    }

    public Task Send(string json) =>
        ws.SendAsync(Encoding.UTF8.GetBytes(json), WebSocketMessageType.Text, true, CancellationToken.None);

    public async Task Close(int code, string reason)
    {
        try { await ws.CloseAsync((WebSocketCloseStatus)code, reason, CancellationToken.None); } catch { }
    }

    /// <summary>The next frame the client sent, or null after the timeout.</summary>
    public async Task<JsonElement?> Next(int ms = 3000)
    {
        using var cts = new CancellationTokenSource(ms);
        try { return (await frames.Reader.ReadAsync(cts.Token)).RootElement; }
        catch (OperationCanceledException) { return null; }
        catch (ChannelClosedException) { return null; }
    }

    /// <summary>The next frame of this type, skipping others, or null.</summary>
    public async Task<JsonElement?> NextOfType(string type, int ms = 3000)
    {
        var end = DateTime.UtcNow.AddMilliseconds(ms);
        while (DateTime.UtcNow < end)
        {
            var f = await Next((int)Math.Max(10, (end - DateTime.UtcNow).TotalMilliseconds));
            if (f == null) return null;
            if (f.Value.TryGetProperty("type", out var t) && t.GetString() == type) return f;
        }
        return null;
    }
}

sealed class FakeGateway : IDisposable
{
    readonly HttpListener listener = new();
    readonly Channel<FakeConn> accepted = Channel.CreateUnbounded<FakeConn>();
    public Uri Url;
    public string Hello = "{\"type\":\"hello\",\"protocol\":1,\"minor\":1,\"call_timeout_ms\":10000,\"ping_interval_ms\":20000,\"max_in_flight\":8,\"max_frame\":1048576}";

    public FakeGateway(string helloJson = null)
    {
        if (helloJson != null) Hello = helloJson;
        var probe = new TcpListener(IPAddress.Loopback, 0);
        probe.Start();
        int port = ((IPEndPoint)probe.LocalEndpoint).Port;
        probe.Stop();
        listener.Prefixes.Add($"http://127.0.0.1:{port}/");
        listener.Start();
        Url = new Uri($"ws://127.0.0.1:{port}/v1/relay/connect");
        _ = Loop();
    }

    async Task Loop()
    {
        while (listener.IsListening)
        {
            HttpListenerContext ctx;
            try { ctx = await listener.GetContextAsync(); } catch { return; }
            _ = Task.Run(async () =>
            {
                try
                {
                    var wsc = await ctx.AcceptWebSocketAsync(null);
                    await accepted.Writer.WriteAsync(new FakeConn(wsc.WebSocket, ctx.Request.Headers["Authorization"], Hello));
                }
                catch { }
            });
        }
    }

    public async Task<FakeConn> Accept(int ms = 5000)
    {
        using var cts = new CancellationTokenSource(ms);
        return await accepted.Reader.ReadAsync(cts.Token);
    }

    public void Dispose() { try { listener.Stop(); listener.Close(); } catch { } }
}

/// <summary>A client wired to a fake gateway, with quick backoff.</summary>
sealed class Rig : IDisposable
{
    public FakeGateway Gw;
    public RelayClient Client;
    public int TicketsIssued;
    public CancellationTokenSource Stop = new();
    public Task Running;
    public List<string> Logs = new();
    public string Fatal;

    public Rig(Func<CallRequest, CancellationToken, Task<CallResult>> handler, string hello = null)
    {
        Gw = new FakeGateway(hello);
        Client = new RelayClient(new RelayOptions
        {
            Url = Gw.Url,
            GetTicket = _ => Task.FromResult("ticket-" + Interlocked.Increment(ref TicketsIssued)),
            ClientName = "tests 1.0",
            Backoff = new Backoff { Min = TimeSpan.FromMilliseconds(10), Max = TimeSpan.FromMilliseconds(300), Random = () => 0.5 },
            Log = m => { lock (Logs) Logs.Add(m); },
        }, new DelegateHandler(handler));
        Client.Fatal += m => Fatal = m;
        Running = Client.RunAsync(Stop.Token);
    }

    public void Dispose()
    {
        Stop.Cancel();
        try { Running.Wait(2000); } catch { }
        Client.Dispose();
        Gw.Dispose();
    }

    public static string Call(string id, string tool = "build_farm", string args = "{}", int deadline = 5000) =>
        $"{{\"type\":\"call\",\"id\":\"{id}\",\"tool\":\"{tool}\",\"player\":\"alice\",\"idem\":\"k-{id}\",\"deadline_ms\":{deadline},\"args\":{args}}}";
}

static class HelloTest
{
    public static async Task ClientHello()
    {
        using var rig = new Rig((c, ct) => Task.FromResult(CallResult.Success()));
        var conn = await rig.Gw.Accept();
        T.Check(conn.Authorization == "Bearer ticket-1", "the ticket goes in the Authorization header");
        var f = await conn.NextOfType("hello");
        T.Check(f != null, "client sends hello");
        var h = f.Value;
        T.Check(h.GetProperty("protocol").GetInt32() == 1 && h.GetProperty("minor").GetInt32() == 1, "versions 1.1");
        T.Check(h.GetProperty("client").GetString() == "tests 1.0", "client name");
    }
}

static class CallTests
{
    public static async Task RoundTrip()
    {
        using var rig = new Rig((c, ct) => Task.FromResult(CallResult.Success(new { tool = c.Tool, player = c.Player, idem = c.Idem, deadline = c.DeadlineMs })));
        var conn = await rig.Gw.Accept();
        await conn.Send(Rig.Call("c1", "build_farm", "{\"kind\":\"farm\"}", 4000));
        var r = await conn.NextOfType("result");
        T.Check(r != null, "a result arrives");
        var e = r.Value;
        T.Check(e.GetProperty("id").GetString() == "c1" && e.GetProperty("ok").GetBoolean(), "same id, ok");
        var d = e.GetProperty("data");
        T.Check(d.GetProperty("tool").GetString() == "build_farm" && d.GetProperty("player").GetString() == "alice", "tool and player passed in");
        T.Check(d.GetProperty("idem").GetString() == "k-c1" && d.GetProperty("deadline").GetInt32() == 4000, "idem and deadline passed in");
    }

    public static async Task Dedupe()
    {
        int runs = 0;
        using var rig = new Rig((c, ct) => { Interlocked.Increment(ref runs); return Task.FromResult(CallResult.Success()); });
        var conn = await rig.Gw.Accept();
        await conn.Send(Rig.Call("same"));
        await conn.Send(Rig.Call("same"));
        await conn.Send(Rig.Call("other"));
        var a = await conn.NextOfType("result");
        var b = await conn.NextOfType("result");
        var extra = await conn.NextOfType("result", 300);
        T.Check(a != null && b != null, "two results");
        T.Check(extra == null, "no third result");
        T.Check(runs == 2, "the repeated id ran once, got " + runs + " runs");
    }

    public static async Task Deadline()
    {
        bool cancelled = false;
        using var rig = new Rig(async (c, ct) =>
        {
            try { await Task.Delay(5000, ct); }
            catch (OperationCanceledException) { cancelled = true; throw; }
            return CallResult.Success();
        });
        var conn = await rig.Gw.Accept();
        await conn.Send(Rig.Call("slow", deadline: 100));
        var r = await conn.NextOfType("result", 600);
        T.Check(r == null, "no result after the gateway's deadline");
        T.Check(cancelled, "the handler saw its token cancelled");
    }

    public static async Task Busy()
    {
        var gate = new TaskCompletionSource();
        using var rig = new Rig(async (c, ct) => { await gate.Task; return CallResult.Success(); },
            "{\"type\":\"hello\",\"protocol\":1,\"minor\":1,\"max_in_flight\":1}");
        var conn = await rig.Gw.Accept();
        await conn.Send(Rig.Call("first"));
        await Task.Delay(100);
        await conn.Send(Rig.Call("second"));
        var busy = await conn.NextOfType("result");
        T.Check(busy != null && busy.Value.GetProperty("id").GetString() == "second", "the second call is answered first");
        T.Check(!busy.Value.GetProperty("ok").GetBoolean() && busy.Value.GetProperty("code").GetString() == "client_busy", "code client_busy");
        T.Check(busy.Value.GetProperty("retry_after_ms").GetInt32() > 0, "retry_after_ms is set");
        gate.SetResult();
        var ok = await conn.NextOfType("result");
        T.Check(ok != null && ok.Value.GetProperty("id").GetString() == "first" && ok.Value.GetProperty("ok").GetBoolean(), "the first call still completes");
    }

    public static async Task Exception()
    {
        using var rig = new Rig((c, ct) => throw new InvalidOperationException("secret internal detail"));
        var conn = await rig.Gw.Accept();
        await conn.Send(Rig.Call("boom"));
        var r = await conn.NextOfType("result");
        T.Check(r != null, "a result arrives");
        var s = r.Value.GetRawText();
        T.Check(!r.Value.GetProperty("ok").GetBoolean() && r.Value.GetProperty("code").GetString() == "client_error", "client_error");
        T.Check(!s.Contains("secret"), "the exception text stays inside the game");
    }

    public static async Task LargeFrame()
    {
        using var rig = new Rig((c, ct) => Task.FromResult(CallResult.Success(new { length = c.Args.GetProperty("blob").GetString().Length })));
        var conn = await rig.Gw.Accept();
        await conn.Send(Rig.Call("big", args: "{\"blob\":\"" + new string('x', 200_000) + "\"}"));
        var r = await conn.NextOfType("result");
        T.Check(r != null && r.Value.GetProperty("data").GetProperty("length").GetInt32() == 200_000, "the whole blob arrived");
    }

    public static async Task FailWithRetry()
    {
        using var rig = new Rig((c, ct) => Task.FromResult(CallResult.Fail("not_enough_wood", "a farm costs 5 wood", 2000)));
        var conn = await rig.Gw.Accept();
        await conn.Send(Rig.Call("poor"));
        var r = (await conn.NextOfType("result")).Value;
        T.Check(!r.GetProperty("ok").GetBoolean(), "not ok");
        T.Check(r.GetProperty("code").GetString() == "not_enough_wood" && r.GetProperty("message").GetString() == "a farm costs 5 wood", "code and message");
        T.Check(r.GetProperty("retry_after_ms").GetInt32() == 2000, "retry_after_ms");
        T.Check(!r.TryGetProperty("data", out _), "no data on a failure");
    }

    public static async Task UnknownFrames()
    {
        using var rig = new Rig((c, ct) => Task.FromResult(CallResult.Success()));
        var conn = await rig.Gw.Accept();
        await conn.Send("{\"type\":\"future-thing\",\"x\":1}");
        await conn.Send("not json at all");
        await conn.Send(Rig.Call("still-works"));
        var r = await conn.NextOfType("result");
        T.Check(r != null && r.Value.GetProperty("id").GetString() == "still-works", "calls after odd frames still work");
    }
}

static class ConnectionTests
{
    public static async Task FreshTicket()
    {
        using var rig = new Rig((c, ct) => Task.FromResult(CallResult.Success()));
        var first = await rig.Gw.Accept();
        T.Check(first.Authorization == "Bearer ticket-1", "first ticket");
        await first.Close(1011, "boom");
        var second = await rig.Gw.Accept();
        T.Check(second.Authorization == "Bearer ticket-2", "a reconnect fetches a new ticket, got " + second.Authorization);
        await second.Send(Rig.Call("after-reconnect"));
        T.Check(await second.NextOfType("result") != null, "calls work on the new socket");
    }

    public static async Task ReplacedWaits()
    {
        using var rig = new Rig((c, ct) => Task.FromResult(CallResult.Success()));
        var first = await rig.Gw.Accept();
        var t0 = DateTime.UtcNow;
        await first.Close(4000, "replaced");
        await rig.Gw.Accept();
        var waited = (DateTime.UtcNow - t0).TotalMilliseconds;
        T.Check(waited >= 280, $"after 4000 the client waits the maximum (300 ms here), it waited {waited:0} ms");
    }

    public static async Task Bye()
    {
        using var rig = new Rig((c, ct) => Task.FromResult(CallResult.Success()));
        var conn = await rig.Gw.Accept();
        await conn.NextOfType("hello");
        await rig.Client.StopAsync("app quit");
        var bye = await conn.NextOfType("bye");
        T.Check(bye != null && bye.Value.GetProperty("reason").GetString() == "app quit", "a bye frame with the reason");
        await rig.Running.WaitAsync(TimeSpan.FromSeconds(5));
        T.Check(rig.Running.IsCompletedSuccessfully, "RunAsync ends after StopAsync");
    }

    public static async Task Unsupported()
    {
        using var rig = new Rig((c, ct) => Task.FromResult(CallResult.Success()));
        var conn = await rig.Gw.Accept();
        await conn.Close(1002, "unsupported protocol");
        await rig.Running.WaitAsync(TimeSpan.FromSeconds(5));
        T.Check(rig.Fatal != null, "Fatal was raised");
        T.Check(rig.TicketsIssued == 1, "no reconnect after a refused version");
    }
}

static class BackoffTests
{
    public static Task Math()
    {
        var b = new Backoff { Min = TimeSpan.FromSeconds(1), Max = TimeSpan.FromSeconds(30) };
        double r = 0.999;
        b.Random = () => r;
        // With the largest random value the wait approaches the cap: 1, 2, 4, 8, 16, 30, 30...
        double[] caps = { 1, 2, 4, 8, 16, 30, 30, 30 };
        for (int i = 0; i < caps.Length; i++)
        {
            double s = b.Delay(i).TotalSeconds;
            T.Check(s <= caps[i] + 1e-6 && s >= caps[i] * 0.99 - 1e-6 || (caps[i] == 1 && s >= 1), $"attempt {i}: {s:0.###}s, cap {caps[i]}s");
        }
        r = 0.0;
        T.Check(b.Delay(5).TotalSeconds == 1.0, "never below the minimum even with no jitter");
        r = 0.5;
        T.Check(System.Math.Abs(b.Delay(3).TotalSeconds - 4.0) < 1e-6, "half jitter at attempt 3 is 4 s");
        T.Check(b.Delay(10_000).TotalSeconds <= 30, "a huge attempt number cannot overflow");
        T.Check(b.Delay(-5).TotalSeconds >= 1, "a negative attempt is treated as 0");
        // The real random source stays inside [min, cap].
        var real = new Backoff();
        for (int i = 0; i < 200; i++)
        {
            double s = real.Delay(4).TotalSeconds;
            T.Check(s >= 1 && s <= 16, $"real jitter {s:0.###}s outside [1, 16]");
        }
        return Task.CompletedTask;
    }

    public static Task Deduper()
    {
        var d = new CallDeduper(3);
        T.Check(!d.Seen("a") && d.Seen("a"), "a repeat is seen");
        d.Seen("b"); d.Seen("c"); d.Seen("d"); // a falls out
        T.Check(!d.Seen("a"), "an id older than the capacity is forgotten");
        T.Check(d.Seen("d"), "recent ids are remembered");
        return Task.CompletedTask;
    }
}
