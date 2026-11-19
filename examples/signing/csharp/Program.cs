// A tiny test runner (no NuGet packages, so it works offline): dotnet run exits nonzero on a failure.
using System.Text;
using System.Text.Json;

var V = JsonDocument.Parse(File.ReadAllText(Path.Combine(AppContext.BaseDirectory, "vector.json"))).RootElement;
string S(string name) => V.GetProperty(name).GetString()!;
long ts = V.GetProperty("timestamp").GetInt64();
var secret = Encoding.UTF8.GetBytes(S("secret"));
var body = Encoding.UTF8.GetBytes(S("body"));
int failed = 0, total = 0;

void Check(string name, Action test)
{
    total++;
    try { test(); Console.WriteLine($"ok   {name}"); }
    catch (Exception e) { failed++; Console.WriteLine($"FAIL {name}: {e.Message}"); }
}
void Must(bool cond, string msg = "assertion failed") { if (!cond) throw new Exception(msg); }
void MustReject(Action a) { try { a(); } catch (Rejected) { return; } throw new Exception("was accepted"); }

Dictionary<string, string> Headers(Action<Dictionary<string, string>>? change = null)
{
    var h = new Dictionary<string, string>
    {
        ["X-AG-Key-Id"] = S("key_id"), ["X-AG-Timestamp"] = ts.ToString(), ["X-AG-Player"] = S("player"),
        ["X-AG-Idempotency-Key"] = S("idempotency_key"), ["X-AG-Signature"] = S("signature"),
    };
    change?.Invoke(h);
    return h;
}
Verifier V1(long? now = null) => new(new() { [S("key_id")] = secret }, () => now ?? ts);

Check("matches the published vector", () =>
{
    Must(Verifier.SignedString(ts, S("key_id"), S("method"), S("path"), S("player"), S("idempotency_key"), body) == S("signed_string"), "signed string");
    Must(Verifier.Sign(secret, ts, S("key_id"), S("method"), S("path"), S("player"), S("idempotency_key"), body) == S("signature"), "signature");
});
Check("accepts the vector", () => Must(V1().Check(S("method"), S("path"), Headers(), body).Player == "player-7"));

var cases = new (string Name, Action<Dictionary<string, string>> Change, byte[] Body, string Path, long Now)[]
{
    ("tampered signature", h => h["X-AG-Signature"] = new string('0', 64), body, S("path"), ts),
    ("short signature", h => h["X-AG-Signature"] = "abcd", body, S("path"), ts),
    ("tampered player", h => h["X-AG-Player"] = "mallory", body, S("path"), ts),
    ("tampered body", _ => { }, Encoding.UTF8.GetBytes(S("body").Replace("2", "9")), S("path"), ts),
    ("tampered path", _ => { }, body, "/api/v1/pets/feed?src=other", ts),
    ("unknown key", h => h["X-AG-Key-Id"] = "nope", body, S("path"), ts),
    ("missing header", h => h["X-AG-Idempotency-Key"] = "", body, S("path"), ts),
    ("six minutes late", _ => { }, body, S("path"), ts + 360),
    ("six minutes early", _ => { }, body, S("path"), ts - 360),
};
foreach (var c in cases)
    Check($"rejects {c.Name}", () => MustReject(() => V1(c.Now).Check(S("method"), c.Path, Headers(c.Change), c.Body)));

Check("a replayed idempotency key returns the stored result", () =>
{
    var v = V1();
    int calls = 0;
    var first = v.Once(S("key_id"), S("idempotency_key"), () => ++calls);
    var second = v.Once(S("key_id"), S("idempotency_key"), () => ++calls);
    Must(first == (1, false) && second == (1, true) && calls == 1, $"got {first} {second} calls={calls}");
});
Check("a failed action can be retried", () =>
{
    var v = V1();
    try { v.Once<int>("k", "i1", () => throw new InvalidOperationException("backend down")); throw new Exception("no error"); }
    catch (InvalidOperationException) { }
    Must(v.Once("k", "i1", () => 7) == (7, false));
});

Console.WriteLine($"{total - failed}/{total} passed");
return failed == 0 ? 0 : 1;
