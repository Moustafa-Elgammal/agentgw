using System;
using System.Collections.Generic;
using System.IO;
using System.Net.WebSockets;
using System.Text.Json;
using System.Threading;
using System.Threading.Tasks;

namespace AgentGw.Relay
{
    /// <summary>Returns a fresh relay ticket. Ask your game backend: tickets last 60 seconds.</summary>
    public delegate Task<string> TicketProvider(CancellationToken ct);

    /// <summary>Settings for <see cref="RelayClient"/>.</summary>
    public sealed class RelayOptions
    {
        /// <summary>The gateway's relay endpoint, for example ws://127.0.0.1:8080/v1/relay/connect.</summary>
        public Uri Url { get; set; }
        /// <summary>Called before every connect, so a reconnect never reuses an expired ticket.</summary>
        public TicketProvider GetTicket { get; set; }
        /// <summary>Shown in gateway debug logs. Printable ASCII, 64 characters at most.</summary>
        public string ClientName { get; set; } = "agentgw-relay-client-cs 0.1";
        /// <summary>Reconnect waits: jittered, at least Min, at most Max.</summary>
        public Backoff Backoff { get; set; } = new Backoff();
        /// <summary>A connection that lasted this long resets the backoff.</summary>
        public TimeSpan StableAfter { get; set; } = TimeSpan.FromSeconds(60);
        /// <summary>How many recent call ids to remember per socket.</summary>
        public int DedupeCapacity { get; set; } = 256;
        /// <summary>Answer busy when this many calls already run, if the gateway announced no limit.</summary>
        public int DefaultMaxInFlight { get; set; } = 8;
        /// <summary>Diagnostics. The client never logs tickets or call arguments.</summary>
        public Action<string> Log { get; set; }
    }

    /// <summary>
    /// A game client for the agentgw relay (protocol 1.1). It connects with a ticket, answers
    /// calls through an <see cref="IRelayHandler"/>, reconnects with jittered backoff and a
    /// fresh ticket, and leaves with a bye.
    /// </summary>
    public sealed class RelayClient : IDisposable
    {
        readonly RelayOptions o;
        readonly IRelayHandler handler;
        readonly CancellationTokenSource stop = new CancellationTokenSource();
        readonly object sync = new object();
        Session current;
        volatile bool stopping;

        /// <summary>The socket is open and the gateway's hello arrived.</summary>
        public event Action<ServerHello> Connected;
        /// <summary>The socket ended or could not be opened. The string says why.</summary>
        public event Action<string> Disconnected;
        /// <summary>The client gave up for good (the gateway refused this protocol version).</summary>
        public event Action<string> Fatal;

        public RelayClient(RelayOptions options, IRelayHandler handler)
        {
            if (options == null || options.Url == null || options.GetTicket == null) throw new ArgumentException("Url and GetTicket are required", nameof(options));
            o = options;
            this.handler = handler ?? throw new ArgumentNullException(nameof(handler));
        }

        void Log(string m) { try { o.Log?.Invoke(m); } catch { } }

        /// <summary>Connects and keeps the connection until <see cref="StopAsync"/> or the token is cancelled.</summary>
        public async Task RunAsync(CancellationToken ct = default)
        {
            using (var run = CancellationTokenSource.CreateLinkedTokenSource(ct, stop.Token))
            {
                int attempt = 0;
                while (!run.IsCancellationRequested)
                {
                    var started = DateTime.UtcNow;
                    int closeCode = 0;
                    string why;
                    try
                    {
                        var ticket = await o.GetTicket(run.Token).ConfigureAwait(false);
                        using (var ws = new ClientWebSocket())
                        {
                            ws.Options.SetRequestHeader("Authorization", "Bearer " + ticket);
                            await ws.ConnectAsync(o.Url, run.Token).ConfigureAwait(false);
                            var s = new Session(this, ws);
                            lock (sync) current = s;
                            try { closeCode = await s.RunAsync(run.Token).ConfigureAwait(false); }
                            finally { lock (sync) current = null; }
                        }
                        why = closeCode == 0 ? "connection ended" : "closed with code " + closeCode;
                    }
                    catch (OperationCanceledException) when (run.IsCancellationRequested) { break; }
                    catch (Exception e) { why = e.GetType().Name + ": " + e.Message; }

                    Log("disconnected: " + why);
                    try { Disconnected?.Invoke(why); } catch { }
                    if (stopping) break; // we asked for this close, so do not reconnect
                    if (closeCode == 1002)
                    {
                        // The gateway does not speak our protocol version. Retrying cannot help.
                        try { Fatal?.Invoke("the gateway refused protocol " + RelayProtocol.Major + "." + RelayProtocol.Minor); } catch { }
                        return;
                    }
                    if (DateTime.UtcNow - started >= o.StableAfter) attempt = 0;

                    // 4000: another client of this player took over. Do not fight it, wait the longest time.
                    // 1008: the gateway thought we misbehaved. Do not hammer it either.
                    var wait = closeCode == RelayProtocol.CloseReplaced || closeCode == 1008 ? o.Backoff.Max : o.Backoff.Delay(attempt++);
                    Log("reconnecting in " + (int)wait.TotalMilliseconds + " ms");
                    try { await Task.Delay(wait, run.Token).ConfigureAwait(false); }
                    catch (OperationCanceledException) { break; }
                }
            }
        }

        /// <summary>Says goodbye (the gateway closes normally) and stops <see cref="RunAsync"/>.</summary>
        public async Task StopAsync(string reason = "stopping")
        {
            stopping = true;
            Session s;
            lock (sync) s = current;
            if (s != null) await s.ByeAsync(reason).ConfigureAwait(false);
            stop.Cancel();
        }

        public void Dispose() { stop.Cancel(); stop.Dispose(); }

        /// <summary>One open socket.</summary>
        sealed class Session
        {
            readonly RelayClient c;
            readonly ClientWebSocket ws;
            readonly SemaphoreSlim sendLock = new SemaphoreSlim(1, 1);
            readonly CallDeduper dedupe;
            readonly CancellationTokenSource conn = new CancellationTokenSource();
            readonly List<Task> running = new List<Task>();
            ServerHello hello = new ServerHello();
            int inFlight;

            public Session(RelayClient c, ClientWebSocket ws)
            {
                this.c = c;
                this.ws = ws;
                dedupe = new CallDeduper(c.o.DedupeCapacity);
            }

            public async Task<int> RunAsync(CancellationToken ct)
            {
                try
                {
                    while (!ct.IsCancellationRequested && ws.State == WebSocketState.Open)
                    {
                        var text = await ReceiveAsync(ct).ConfigureAwait(false);
                        if (text == null) break; // closed
                        JsonDocument doc;
                        try { doc = JsonDocument.Parse(text); }
                        catch (JsonException) { c.Log("ignoring a frame that is not JSON"); continue; }
                        using (doc) await OnFrameAsync(doc.RootElement).ConfigureAwait(false);
                    }
                }
                finally
                {
                    conn.Cancel(); // stops calls that are still running
                    Task[] tasks;
                    lock (running) tasks = running.ToArray();
                    try { await Task.WhenAll(tasks).ConfigureAwait(false); } catch { }
                }
                return ws.CloseStatus.HasValue ? (int)ws.CloseStatus.Value : 0;
            }

            async Task OnFrameAsync(JsonElement root)
            {
                switch (RelayJson.FrameType(root))
                {
                    case "hello":
                        hello = RelayJson.ParseHello(root);
                        try { c.Connected?.Invoke(hello); } catch { }
                        await SendAsync(RelayJson.ClientHello(c.o.ClientName), conn.Token).ConfigureAwait(false);
                        break;
                    case "call":
                        StartCall(RelayJson.ParseCall(root));
                        break;
                    default:
                        break; // unknown frame types are ignored, so the protocol can grow
                }
            }

            void StartCall(CallRequest call)
            {
                if (string.IsNullOrEmpty(call.Id)) return;
                if (dedupe.Seen(call.Id)) { c.Log("ignored a repeated call id"); return; }
                int max = hello.MaxInFlight > 0 ? hello.MaxInFlight : c.o.DefaultMaxInFlight;
                if (Interlocked.Increment(ref inFlight) > max)
                {
                    Interlocked.Decrement(ref inFlight);
                    _ = SendResultAsync(call.Id, CallResult.Busy());
                    return;
                }
                var t = Task.Run(() => RunCallAsync(call));
                lock (running) { running.RemoveAll(x => x.IsCompleted); running.Add(t); }
            }

            async Task RunCallAsync(CallRequest call)
            {
                using (var deadline = CancellationTokenSource.CreateLinkedTokenSource(conn.Token))
                {
                    if (call.DeadlineMs > 0) deadline.CancelAfter(call.DeadlineMs);
                    CallResult result;
                    try { result = await c.handler.HandleAsync(call, deadline.Token).ConfigureAwait(false); }
                    catch (OperationCanceledException) { result = null; }
                    catch (Exception e)
                    {
                        // The type only: a message may carry game internals the agent should not see.
                        c.Log("handler failed: " + e.GetType().Name);
                        result = CallResult.Fail("client_error", "the game could not run the tool");
                    }
                    finally { Interlocked.Decrement(ref inFlight); }

                    if (result == null || deadline.IsCancellationRequested)
                    {
                        c.Log("no result sent: the deadline passed or the socket closed");
                        return;
                    }
                    await SendResultAsync(call.Id, result).ConfigureAwait(false);
                }
            }

            Task SendResultAsync(string id, CallResult r) => SendAsync(RelayJson.Result(id, r), conn.Token);

            public async Task ByeAsync(string reason)
            {
                if (ws.State != WebSocketState.Open) return;
                try
                {
                    await SendAsync(RelayJson.Bye(reason), CancellationToken.None).ConfigureAwait(false);
                    using (var t = new CancellationTokenSource(TimeSpan.FromSeconds(2)))
                        await ws.CloseOutputAsync(WebSocketCloseStatus.NormalClosure, "bye", t.Token).ConfigureAwait(false);
                }
                catch (Exception) { /* already closing */ }
            }

            async Task SendAsync(byte[] frame, CancellationToken ct)
            {
                try
                {
                    await sendLock.WaitAsync(ct).ConfigureAwait(false); // ClientWebSocket allows one send at a time
                    try
                    {
                        if (ws.State == WebSocketState.Open)
                            await ws.SendAsync(new ArraySegment<byte>(frame), WebSocketMessageType.Text, true, ct).ConfigureAwait(false);
                    }
                    finally { sendLock.Release(); }
                }
                catch (OperationCanceledException) { }
                catch (WebSocketException) { }
                catch (ObjectDisposedException) { }
            }

            /// <summary>Reads one whole text message, or null when the socket closed.</summary>
            async Task<string> ReceiveAsync(CancellationToken ct)
            {
                var buf = new byte[8192];
                using (var ms = new MemoryStream())
                {
                    while (true)
                    {
                        WebSocketReceiveResult r;
                        try { r = await ws.ReceiveAsync(new ArraySegment<byte>(buf), ct).ConfigureAwait(false); }
                        catch (WebSocketException) { return null; }
                        if (r.MessageType == WebSocketMessageType.Close)
                        {
                            try { await ws.CloseOutputAsync(WebSocketCloseStatus.NormalClosure, "", CancellationToken.None).ConfigureAwait(false); } catch { }
                            return null;
                        }
                        ms.Write(buf, 0, r.Count);
                        if (ms.Length > RelayProtocol.MaxFrame)
                        {
                            await ws.CloseAsync(WebSocketCloseStatus.MessageTooBig, "frame too large", CancellationToken.None).ConfigureAwait(false);
                            return null;
                        }
                        if (r.EndOfMessage) break;
                    }
                    return System.Text.Encoding.UTF8.GetString(ms.GetBuffer(), 0, (int)ms.Length);
                }
            }
        }
    }
}
