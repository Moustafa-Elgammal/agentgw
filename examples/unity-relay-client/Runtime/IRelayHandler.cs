using System;
using System.Threading;
using System.Threading.Tasks;

namespace AgentGw.Relay
{
    /// <summary>
    /// Runs one tool for the game. It may run on a thread pool thread: in Unity, hand the
    /// work to the main thread and await it (see Samples~/Basic).
    /// The token is cancelled when the gateway's deadline passes or the socket closes. Stop
    /// work that has not started yet. If it already changed game state, finish consistently:
    /// the gateway no longer waits, and the agent was told the outcome is unknown.
    /// </summary>
    public interface IRelayHandler
    {
        Task<CallResult> HandleAsync(CallRequest call, CancellationToken ct);
    }

    /// <summary>Wraps a delegate as a handler.</summary>
    public sealed class DelegateHandler : IRelayHandler
    {
        readonly Func<CallRequest, CancellationToken, Task<CallResult>> fn;
        public DelegateHandler(Func<CallRequest, CancellationToken, Task<CallResult>> fn) { this.fn = fn; }
        public Task<CallResult> HandleAsync(CallRequest call, CancellationToken ct) => fn(call, ct);
    }
}
