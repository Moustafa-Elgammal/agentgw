package obs

// RelayExt holds the relay metrics added with protocol v1.1. It implements relay.ExtObserver.
// It lives in its own file so the standard metric set in metrics.go stays untouched.
type RelayExt struct {
	busy   *Counter
	closes *Counter
}

// NewRelayExt registers the v1.1 relay metrics on r:
//
//	relay_busy_total                  calls refused because a socket was at its in-flight limit
//	relay_socket_closes_total{reason} sockets that ended, by reason (bye, flood, malformed,
//	                                  unsupported, ping, replaced, disconnected)
func NewRelayExt(r *Registry) *RelayExt {
	return &RelayExt{
		busy:   r.NewCounter("relay_busy_total", "Relay calls refused because the player's socket was at its in-flight limit."),
		closes: r.NewCounter("relay_socket_closes_total", "Relay sockets that ended, by reason.", "reason"),
	}
}

// Busy implements relay.ExtObserver.
func (e *RelayExt) Busy() { e.busy.Inc() }

// Closed implements relay.ExtObserver.
func (e *RelayExt) Closed(reason string) { e.closes.Inc(reason) }
