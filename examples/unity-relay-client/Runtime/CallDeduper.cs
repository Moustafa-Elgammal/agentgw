using System.Collections.Generic;

namespace AgentGw.Relay
{
    /// <summary>
    /// Remembers the last N call ids of one socket. The gateway never resends a call, so this
    /// is a safety net: a repeated id is ignored instead of running the game action twice.
    /// </summary>
    public sealed class CallDeduper
    {
        readonly int capacity;
        readonly HashSet<string> seen = new HashSet<string>();
        readonly Queue<string> order = new Queue<string>();

        public CallDeduper(int capacity = 256) { this.capacity = capacity < 1 ? 1 : capacity; }

        /// <summary>True if the id was already seen. Otherwise remembers it and returns false.</summary>
        public bool Seen(string id)
        {
            lock (seen)
            {
                if (!seen.Add(id)) return true;
                order.Enqueue(id);
                while (order.Count > capacity) seen.Remove(order.Dequeue());
                return false;
            }
        }
    }
}
