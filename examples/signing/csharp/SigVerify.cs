// Verify the gateway's signed calls. .NET standard library only. Copy this into your backend.
//
//   var v = new Verifier(new() { ["dev-1"] = Encoding.UTF8.GetBytes("dev-secret") });
//   var who = v.Check(method, pathAndQuery, headers, bodyBytes);   // throws Rejected
//   var (result, replayed) = v.Once(who.KeyId, who.IdempotencyKey, () => DoTheAction());
using System.Security.Cryptography;
using System.Text;

/// <summary>The request is not from the gateway. Answer 401 and say nothing more.</summary>
public class Rejected(string reason) : Exception(reason);

public record Caller(string Player, string KeyId, string IdempotencyKey);

public class Verifier(Dictionary<string, byte[]> secrets, Func<double>? now = null)
{
    public const int MaxSkew = 300; // seconds, both directions
    static readonly string[] Required = ["x-ag-key-id", "x-ag-timestamp", "x-ag-player", "x-ag-idempotency-key", "x-ag-signature"];

    readonly Func<double> clock = now ?? (() => DateTimeOffset.UtcNow.ToUnixTimeMilliseconds() / 1000.0);
    readonly object gate = new();
    readonly Dictionary<string, (double Expires, Lazy<object?> Value)> results = new();

    public static string SignedString(long ts, string keyId, string method, string path, string player, string idem, byte[] body) =>
        string.Join("\n", "v1", ts.ToString(), keyId, method.ToUpperInvariant(), path, player, idem,
            Convert.ToHexString(SHA256.HashData(body)).ToLowerInvariant());

    public static string Sign(byte[] secret, long ts, string keyId, string method, string path, string player, string idem, byte[] body) =>
        Convert.ToHexString(HMACSHA256.HashData(secret, Encoding.UTF8.GetBytes(SignedString(ts, keyId, method, path, player, idem, body)))).ToLowerInvariant();

    public Caller Check(string method, string path, IDictionary<string, string> headers, byte[] body)
    {
        var h = headers.ToDictionary(kv => kv.Key.ToLowerInvariant(), kv => kv.Value);
        if (Required.Any(name => !h.TryGetValue(name, out var v) || v == "")) throw new Rejected("missing header");
        if (!long.TryParse(h["x-ag-timestamp"], out var ts)) throw new Rejected("bad timestamp");
        if (Math.Abs(clock() - ts) > MaxSkew) throw new Rejected("timestamp outside the allowed skew");
        if (!secrets.TryGetValue(h["x-ag-key-id"], out var secret)) throw new Rejected("unknown key");
        var want = Encoding.ASCII.GetBytes(Sign(secret, ts, h["x-ag-key-id"], method, path, h["x-ag-player"], h["x-ag-idempotency-key"], body));
        var got = Encoding.ASCII.GetBytes(h["x-ag-signature"]);
        // Constant time. FixedTimeEquals returns false for different lengths.
        if (!CryptographicOperations.FixedTimeEquals(want, got)) throw new Rejected("signature mismatch");
        return new Caller(h["x-ag-player"], h["x-ag-key-id"], h["x-ag-idempotency-key"]);
    }

    /// <summary>Run action once per idempotency key. A repeat gets the stored answer.
    /// Call it only after Check succeeded: the key is trusted only once it is signed.</summary>
    public (T Result, bool Replayed) Once<T>(string keyId, string idem, Func<T> action)
    {
        var k = $"{keyId}/{idem}";
        Lazy<object?> value;
        bool replayed;
        lock (gate)
        {
            var t = clock();
            foreach (var old in results.Where(e => t > e.Value.Expires && e.Value.Value.IsValueCreated).Select(e => e.Key).ToList()) results.Remove(old);
            replayed = results.TryGetValue(k, out var hit);
            if (replayed) value = hit.Value;
            else
            {
                value = new Lazy<object?>(() => action(), LazyThreadSafetyMode.ExecutionAndPublication);
                results[k] = (t + 2 * MaxSkew, value);
            }
        }
        try { return ((T)value.Value!, replayed); }
        catch
        {
            lock (gate) results.Remove(k); // it did not finish: let a retry run
            throw;
        }
    }
}
