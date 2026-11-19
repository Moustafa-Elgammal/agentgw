package obs

import (
	"net/http"
	"strconv"
	"time"
)

// Metrics is the gateway's standard metric set. It implements gateway.Observer and
// relay.Observer.
type Metrics struct {
	Reg *Registry

	calls      *Counter
	callDur    *Histogram
	relayConns *Gauge
	relayCalls *Counter
	httpReqs   *Counter
}

// NewMetrics registers the standard metrics on a fresh Registry. version labels
// agentgw_build_info.
func NewMetrics(version string) *Metrics {
	r := NewRegistry()
	m := &Metrics{
		Reg:        r,
		calls:      r.NewCounter("gateway_calls_total", "Tool calls by tool and outcome (ok, or the denial code).", "tool", "outcome"),
		callDur:    r.NewHistogram("gateway_call_duration_seconds", "Tool call duration in seconds.", nil, "tool"),
		relayConns: r.NewGauge("relay_connections", "Game client sockets held by this replica."),
		relayCalls: r.NewCounter("relay_calls_total", "Relay calls by result (ok, denied, offline, error).", "result"),
		httpReqs:   r.NewCounter("http_requests_total", "HTTP requests by route pattern and status code.", "route", "code"),
	}
	r.NewGauge("agentgw_build_info", "Build information; the value is always 1.", "version").Set(1, version)
	return m
}

// ObserveCall implements gateway.Observer.
func (m *Metrics) ObserveCall(tool, outcome string, d time.Duration) {
	m.calls.Inc(tool, outcome)
	m.callDur.Observe(d.Seconds(), tool)
}

// Connections implements relay.Observer.
func (m *Metrics) Connections(delta int) { m.relayConns.Add(float64(delta)) }

// Call implements relay.Observer.
func (m *Metrics) Call(result string) { m.relayCalls.Inc(result) }

type statusWriter struct {
	http.ResponseWriter
	code int
}

func (w *statusWriter) WriteHeader(c int) {
	if w.code == 0 {
		w.code = c
	}
	w.ResponseWriter.WriteHeader(c)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	if w.code == 0 {
		w.code = http.StatusOK
	}
	return w.ResponseWriter.Write(b)
}

// Unwrap lets http.ResponseController reach the real writer.
func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// Middleware counts requests by the mux pattern that served them, never by the raw path, so
// the label set stays as small as the route table. Wrap the ServeMux itself: the pattern is
// known only after the mux has matched. A request no pattern matched counts as "unmatched".
func (m *Metrics) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sw := &statusWriter{ResponseWriter: w}
		next.ServeHTTP(sw, r)
		route := r.Pattern
		if route == "" {
			route = "unmatched"
		}
		code := sw.code
		if code == 0 {
			code = http.StatusOK
		}
		m.httpReqs.Inc(route, strconv.Itoa(code))
	})
}
