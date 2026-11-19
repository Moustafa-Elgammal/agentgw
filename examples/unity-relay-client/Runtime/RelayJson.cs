using System;
using System.IO;
using System.Text;
using System.Text.Json;

namespace AgentGw.Relay
{
    /// <summary>
    /// Every JSON read and write of the client is here. Unity does not ship System.Text.Json,
    /// so a Unity project either adds it (NuGetForUnity) or swaps this one file for
    /// Newtonsoft.Json (com.unity.nuget.newtonsoft-json). Nothing else touches JSON frames.
    /// </summary>
    internal static class RelayJson
    {
        /// <summary>The frame's "type", or null when it is not a JSON object with a string type.</summary>
        public static string FrameType(JsonElement root) =>
            root.ValueKind == JsonValueKind.Object && root.TryGetProperty("type", out var t) && t.ValueKind == JsonValueKind.String
                ? t.GetString()
                : null;

        public static ServerHello ParseHello(JsonElement root) => new ServerHello
        {
            Protocol = Int(root, "protocol"),
            Minor = Int(root, "minor"),
            CallTimeoutMs = Int(root, "call_timeout_ms"),
            PingIntervalMs = Int(root, "ping_interval_ms"),
            MaxInFlight = Int(root, "max_in_flight"),
            MaxFrame = Int(root, "max_frame"),
        };

        public static CallRequest ParseCall(JsonElement root)
        {
            var c = new CallRequest
            {
                Id = Str(root, "id"),
                Tool = Str(root, "tool"),
                Player = Str(root, "player"),
                Idem = Str(root, "idem"),
                DeadlineMs = Int(root, "deadline_ms"),
            };
            c.Args = root.TryGetProperty("args", out var a) ? a.Clone() : JsonDocument.Parse("{}").RootElement.Clone();
            return c;
        }

        public static byte[] ClientHello(string client) => Write(w =>
        {
            w.WriteString("type", "hello");
            w.WriteNumber("protocol", RelayProtocol.Major);
            w.WriteNumber("minor", RelayProtocol.Minor);
            w.WriteString("client", client);
            w.WriteStartArray("capabilities");
            w.WriteStringValue("deadline");
            w.WriteStringValue("busy");
            w.WriteStringValue("bye");
            w.WriteEndArray();
        });

        public static byte[] Bye(string reason) => Write(w =>
        {
            w.WriteString("type", "bye");
            w.WriteString("reason", reason ?? "");
        });

        public static byte[] Result(string id, CallResult r) => Write(w =>
        {
            w.WriteString("type", "result");
            w.WriteString("id", id);
            w.WriteBoolean("ok", r.Ok);
            if (!r.Ok)
            {
                w.WriteString("code", string.IsNullOrEmpty(r.Code) ? "client_error" : r.Code);
                if (!string.IsNullOrEmpty(r.Message)) w.WriteString("message", r.Message);
                if (r.RetryAfterMs > 0) w.WriteNumber("retry_after_ms", r.RetryAfterMs);
            }
            if (!string.IsNullOrEmpty(r.DataJson))
            {
                using (var doc = JsonDocument.Parse(r.DataJson))
                {
                    w.WritePropertyName("data");
                    doc.RootElement.WriteTo(w);
                }
            }
        });

        static byte[] Write(Action<Utf8JsonWriter> body)
        {
            var ms = new MemoryStream();
            using (var w = new Utf8JsonWriter(ms))
            {
                w.WriteStartObject();
                body(w);
                w.WriteEndObject();
            }
            return ms.ToArray();
        }

        static int Int(JsonElement e, string name) =>
            e.TryGetProperty(name, out var v) && v.ValueKind == JsonValueKind.Number && v.TryGetInt32(out var i) ? i : 0;

        static string Str(JsonElement e, string name) =>
            e.TryGetProperty(name, out var v) && v.ValueKind == JsonValueKind.String ? v.GetString() : null;
    }
}
