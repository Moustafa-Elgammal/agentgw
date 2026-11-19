using System;

namespace AgentGw.Relay
{
    /// <summary>
    /// Exponential backoff with full jitter: the wait is a random time between zero and
    /// min(Max, 1 s * 2^attempt), but never below Min. Full jitter keeps a crowd of clients
    /// that lost the gateway at the same moment from reconnecting in step.
    /// </summary>
    public sealed class Backoff
    {
        public TimeSpan Min { get; set; } = TimeSpan.FromSeconds(1);
        public TimeSpan Max { get; set; } = TimeSpan.FromSeconds(30);
        /// <summary>Returns a number in [0, 1). Replace it in tests.</summary>
        public Func<double> Random { get; set; } = new Random().NextDouble;

        /// <summary>The wait before reconnect attempt number <paramref name="attempt"/> (0 for the first).</summary>
        public TimeSpan Delay(int attempt)
        {
            double cap = Math.Min(Max.TotalMilliseconds, 1000.0 * Math.Pow(2, Math.Min(Math.Max(attempt, 0), 20)));
            double wait = Random() * cap;
            return TimeSpan.FromMilliseconds(Math.Max(wait, Min.TotalMilliseconds));
        }
    }
}
