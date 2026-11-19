using System;
using System.Text.Json;

namespace AgentGw.Relay
{
    /// <summary>Relay wire protocol constants. See docs/PROTOCOL.md, "Relay".</summary>
    public static class RelayProtocol
    {
        public const int Major = 1;
        public const int Minor = 1;
        /// <summary>The largest frame either side may send.</summary>
        public const int MaxFrame = 1 << 20;
        /// <summary>Close code the gateway uses when a newer connection of the same player replaced this one.</summary>
        public const int CloseReplaced = 4000;
        /// <summary>Result code for "I cannot take this call now".</summary>
        public const string CodeClientBusy = "client_busy";
    }

    /// <summary>What the gateway announced in its hello frame. Zero means "not announced" (a v1 gateway).</summary>
    public sealed class ServerHello
    {
        public int Protocol { get; set; }
        public int Minor { get; set; }
        public int CallTimeoutMs { get; set; }
        public int PingIntervalMs { get; set; }
        public int MaxInFlight { get; set; }
        public int MaxFrame { get; set; }
    }

    /// <summary>One tool call from the gateway. Answer it before <see cref="DeadlineMs"/> passes.</summary>
    public sealed class CallRequest
    {
        public string Id { get; set; }
        public string Tool { get; set; }
        public string Player { get; set; }
        /// <summary>A fresh unique key for this call. The gateway never resends a call.</summary>
        public string Idem { get; set; }
        /// <summary>The tool arguments as a JSON object (validated by the gateway against the manifest).</summary>
        public JsonElement Args { get; set; }
        /// <summary>How long the gateway waits, from when it sent the frame. 0 means not announced.</summary>
        public int DeadlineMs { get; set; }
    }

    /// <summary>The answer to a call.</summary>
    public sealed class CallResult
    {
        public bool Ok { get; private set; }
        public string Code { get; private set; }
        public string Message { get; private set; }
        /// <summary>The result data as raw JSON, or null.</summary>
        public string DataJson { get; private set; }
        /// <summary>When a retry makes sense, for failed results. 0 means no hint.</summary>
        public int RetryAfterMs { get; private set; }

        /// <summary>Success with raw JSON data (for example <c>{"built":1}</c>).</summary>
        public static CallResult Success(string dataJson = null) => new CallResult { Ok = true, DataJson = dataJson };

        /// <summary>Success with an object that System.Text.Json can serialize.</summary>
        public static CallResult Success(object data) => new CallResult { Ok = true, DataJson = JsonSerializer.Serialize(data) };

        /// <summary>
        /// A failure the agent can read. Use a stable <paramref name="code"/> such as
        /// "not_enough_wood", and say in the message how to fix it.
        /// </summary>
        public static CallResult Fail(string code, string message, int retryAfterMs = 0) =>
            new CallResult { Ok = false, Code = code, Message = message, RetryAfterMs = retryAfterMs };

        /// <summary>The result to give when the game cannot take the call right now.</summary>
        public static CallResult Busy(int retryAfterMs = 500) =>
            Fail(RelayProtocol.CodeClientBusy, "the game is busy, try again shortly", retryAfterMs);
    }
}
