package obs

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func render(r *Registry) string {
	var b strings.Builder
	_, _ = r.WriteTo(&b)
	return b.String()
}

func TestExposition(t *testing.T) {
	tests := []struct {
		name  string
		build func(r *Registry)
		want  []string
	}{
		{"counter sorted by labels", func(r *Registry) {
			c := r.NewCounter("c_total", "help", "a")
			c.Inc("z")
			c.Add(2, "b")
			c.Add(-5, "b") // negative ignored
		}, []string{"# HELP c_total help", "# TYPE c_total counter", `c_total{a="b"} 2`, `c_total{a="z"} 1`}},
		{"gauge up and down", func(r *Registry) {
			g := r.NewGauge("g", "h")
			g.Add(3)
			g.Add(-1)
		}, []string{"# TYPE g gauge", "g 2"}},
		{"escaping", func(r *Registry) {
			r.NewCounter("e_total", "h", "v").Inc("a\"b\\c\nd")
		}, []string{`e_total{v="a\"b\\c\nd"} 1`}},
		{"histogram cumulative", func(r *Registry) {
			h := r.NewHistogram("h_seconds", "h", []float64{1, 5}, "t")
			h.Observe(0.5, "x")
			h.Observe(1, "x") // le is inclusive
			h.Observe(3, "x")
			h.Observe(9, "x")
		}, []string{
			`h_seconds_bucket{t="x",le="1"} 2`, `h_seconds_bucket{t="x",le="5"} 3`,
			`h_seconds_bucket{t="x",le="+Inf"} 4`, `h_seconds_sum{t="x"} 13.5`, `h_seconds_count{t="x"} 4`,
		}},
		{"no labels", func(r *Registry) { r.NewCounter("n_total", "h").Inc() }, []string{"n_total 1"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := NewRegistry()
			tt.build(r)
			out := render(r)
			pos := -1
			for _, w := range tt.want {
				i := strings.Index(out, w+"\n")
				if i < 0 {
					t.Fatalf("missing %q in:\n%s", w, out)
				}
				if i < pos {
					t.Fatalf("%q out of order in:\n%s", w, out)
				}
				pos = i
			}
		})
	}
}

func TestDeterministicAndFamiliesSorted(t *testing.T) {
	r := NewRegistry()
	r.NewCounter("b_total", "h").Inc()
	r.NewCounter("a_total", "h").Inc()
	out := render(r)
	if strings.Index(out, "a_total") > strings.Index(out, "b_total") || out != render(r) {
		t.Fatalf("output not sorted or not stable:\n%s", out)
	}
}

func TestShapeMismatchPanics(t *testing.T) {
	r := NewRegistry()
	c := r.NewCounter("x_total", "h", "a")
	if r.NewCounter("x_total", "h", "a").f != c.f {
		t.Error("same shape must return the same metric")
	}
	for name, fn := range map[string]func(){
		"other labels": func() { r.NewCounter("x_total", "h", "b") },
		"other kind":   func() { r.NewGauge("x_total", "h", "a") },
		"wrong values": func() { c.Inc() },
	} {
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("%s: expected panic", name)
				}
			}()
			fn()
		}()
	}
}

func TestCardinalityGuard(t *testing.T) {
	r := NewRegistry()
	c := r.NewCounter("c_total", "h", "tool", "outcome")
	for i := 0; i < MaxSeries+50; i++ {
		c.Inc(fmt.Sprintf("t%d", i), "ok")
	}
	out := render(r)
	if n := strings.Count(out, "c_total{"); n != MaxSeries+1 {
		t.Fatalf("series = %d, want %d", n, MaxSeries+1)
	}
	if !strings.Contains(out, `c_total{tool="other",outcome="other"} 50`) {
		t.Fatalf("overflow not folded:\n%s", out[len(out)-300:])
	}
	c.Inc("t0", "ok") // an existing series keeps counting
	if !strings.Contains(render(r), `c_total{tool="t0",outcome="ok"} 2`) {
		t.Error("existing series stopped counting")
	}
}

func TestConcurrent(t *testing.T) {
	r := NewRegistry()
	c := r.NewCounter("c_total", "h", "k")
	h := r.NewHistogram("h_seconds", "h", nil, "k")
	g := r.NewGauge("g", "h")
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				c.Inc("a")
				h.Observe(0.01, "a")
				g.Add(1)
				_ = render(r)
			}
		}()
	}
	wg.Wait()
	out := render(r)
	for _, w := range []string{`c_total{k="a"} 3200`, `h_seconds_count{k="a"} 3200`, "g 3200"} {
		if !strings.Contains(out, w) {
			t.Errorf("missing %q", w)
		}
	}
}

func TestMetricsObservers(t *testing.T) {
	m := NewMetrics("v1.2.3")
	m.ObserveCall("build", "ok", 20*time.Millisecond)
	m.ObserveCall("build", "rate_limited", time.Millisecond)
	m.Connections(1)
	m.Connections(1)
	m.Connections(-1)
	m.Call("offline")
	out := render(m.Reg)
	for _, w := range []string{
		`gateway_calls_total{tool="build",outcome="ok"} 1`, `gateway_calls_total{tool="build",outcome="rate_limited"} 1`,
		`gateway_call_duration_seconds_count{tool="build"} 2`, "relay_connections 1",
		`relay_calls_total{result="offline"} 1`, `agentgw_build_info{version="v1.2.3"} 1`,
	} {
		if !strings.Contains(out, w+"\n") {
			t.Errorf("missing %q in:\n%s", w, out)
		}
	}
}

func TestMiddlewareUsesPatternNotPath(t *testing.T) {
	m := NewMetrics("t")
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/thing/{id}", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusTeapot) })
	mux.HandleFunc("GET /ok", func(http.ResponseWriter, *http.Request) {}) // implicit 200
	h := m.Middleware(mux)
	for _, p := range []string{"/v1/thing/1", "/v1/thing/2", "/ok", "/nope/a", "/nope/b"} {
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", p, nil))
	}
	out := render(m.Reg)
	for _, w := range []string{
		`http_requests_total{route="GET /v1/thing/{id}",code="418"} 2`,
		`http_requests_total{route="GET /ok",code="200"} 1`,
		`http_requests_total{route="unmatched",code="404"} 2`,
	} {
		if !strings.Contains(out, w+"\n") {
			t.Errorf("missing %q in:\n%s", w, out)
		}
	}
	if strings.Contains(out, "/nope") {
		t.Error("raw path leaked into a label")
	}
}

func TestOps(t *testing.T) {
	ok := Check{"redis", func(context.Context) error { return nil }}
	bad := Check{"postgres", func(context.Context) error { return errors.New("down") }}
	hang := Check{"hang", func(ctx context.Context) error { <-ctx.Done(); return ctx.Err() }}
	tests := []struct {
		name   string
		checks []Check
		code   int
		want   []string
	}{
		{"none", nil, 200, []string{`"ready"`}},
		{"all pass", []Check{ok, ok}, 200, []string{`"ready"`}},
		{"one fails", []Check{ok, bad}, 503, []string{`"unready"`, `"check":"postgres"`, `"error":"down"`}},
		{"sorted", []Check{bad, {"a", func(context.Context) error { return errors.New("x") }}}, 503, []string{`"check":"a"`}},
		{"timeout", []Check{hang}, 503, []string{`"check":"hang"`, "deadline exceeded"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.name == "timeout" && testing.Short() {
				t.Skip("waits for the check timeout")
			}
			rec := httptest.NewRecorder()
			Ops(NewRegistry(), tt.checks...).ServeHTTP(rec, httptest.NewRequest("GET", "/readyz", nil))
			if rec.Code != tt.code {
				t.Fatalf("code = %d, body %s", rec.Code, rec.Body)
			}
			for _, w := range tt.want {
				if !strings.Contains(rec.Body.String(), w) {
					t.Errorf("body %s lacks %q", rec.Body, w)
				}
			}
		})
	}
}

func TestOpsHealthAndMetrics(t *testing.T) {
	m := NewMetrics("v")
	h := Ops(m.Reg, Check{"bad", func(context.Context) error { return errors.New("x") }})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/healthz", nil))
	if rec.Code != 200 {
		t.Errorf("healthz must not depend on checks: %d", rec.Code)
	}
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))
	if rec.Code != 200 || !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/plain; version=0.0.4") || !strings.Contains(rec.Body.String(), "agentgw_build_info") {
		t.Errorf("metrics: %d %q", rec.Code, rec.Header().Get("Content-Type"))
	}
	rec = httptest.NewRecorder()
	Ops(nil).ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))
	if rec.Code != 404 {
		t.Errorf("nil registry should leave /metrics off, got %d", rec.Code)
	}
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/healthz", nil))
	if rec.Code != 405 {
		t.Errorf("POST healthz = %d", rec.Code)
	}
}
