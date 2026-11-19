// A tiny console "game" that plays the part of a Unity client: it connects to the gateway's
// relay and answers the tools an agent calls. The game state is two numbers in memory.
//
//   RELAY_URL     ws://127.0.0.1:8080/v1/relay/connect  (default)
//   RELAY_TICKET  a ticket to use once (for a quick try), or
//   TICKET_URL    an HTTP endpoint of your backend that returns {"ticket":"..."} (used on every
//                 connect, so reconnects get a fresh ticket)
using System.Text.Json;
using AgentGw.Relay;

var url = Environment.GetEnvironmentVariable("RELAY_URL") ?? "ws://127.0.0.1:8080/v1/relay/connect";
var fixedTicket = Environment.GetEnvironmentVariable("RELAY_TICKET");
var ticketUrl = Environment.GetEnvironmentVariable("TICKET_URL");
if (string.IsNullOrEmpty(fixedTicket) && string.IsNullOrEmpty(ticketUrl))
{
    Console.Error.WriteLine("set RELAY_TICKET or TICKET_URL");
    return 2;
}

var http = new HttpClient();
int wood = 10, farms = 0;
var gate = new object();

async Task<string> GetTicket(CancellationToken ct)
{
    if (!string.IsNullOrEmpty(ticketUrl))
    {
        using var doc = JsonDocument.Parse(await http.GetStringAsync(ticketUrl, ct));
        return doc.RootElement.GetProperty("ticket").GetString();
    }
    return fixedTicket;
}

Task<CallResult> Handle(CallRequest call, CancellationToken ct)
{
    lock (gate)
    {
        switch (call.Tool)
        {
            case "get_state":
                return Task.FromResult(CallResult.Success(new { wood, farms }));
            case "build_farm":
                if (wood < 5) return Task.FromResult(CallResult.Fail("not_enough_wood", "a farm costs 5 wood, you have " + wood));
                wood -= 5; farms++;
                return Task.FromResult(CallResult.Success(new { wood, farms }));
            default:
                return Task.FromResult(CallResult.Fail("unknown_tool", "this game has no tool " + call.Tool));
        }
    }
}

var client = new RelayClient(new RelayOptions
{
    Url = new Uri(url),
    GetTicket = GetTicket,
    ClientName = "console-game 0.1",
    Log = m => Console.WriteLine("[relay] " + m),
}, new DelegateHandler(Handle));
client.Connected += h => Console.WriteLine($"connected: gateway protocol {h.Protocol}.{h.Minor}, up to {h.MaxInFlight} calls at once, {h.CallTimeoutMs} ms per call");
client.Fatal += m => Console.WriteLine("giving up: " + m);

using var quit = new CancellationTokenSource();
Console.CancelKeyPress += (_, e) => { e.Cancel = true; _ = client.StopAsync("ctrl-c"); };
await client.RunAsync(quit.Token);
return 0;
