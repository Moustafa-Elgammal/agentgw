// Package obs is a small metrics registry that writes the Prometheus text format (0.0.4).
// It has no dependencies. Every metric caps its distinct label sets, so a label that an
// attacker can influence cannot grow memory without bound.
package obs

import (
	"fmt"
	"io"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// MaxSeries is how many distinct label sets one metric keeps. Further sets are folded into
// one series whose label values are all "other".
const MaxSeries = 1000

const overflow = "other"

// Registry holds metrics and renders them. The zero value is not usable; call NewRegistry.
type Registry struct {
	mu      sync.Mutex
	metrics map[string]*family
	hooks   []func()
}

// NewRegistry builds an empty Registry.
func NewRegistry() *Registry { return &Registry{metrics: map[string]*family{}} }

// OnScrape registers fn to run at the start of every WriteTo, before anything is rendered.
// Collectors use it to copy values that live elsewhere (connection pools, the Go runtime)
// into gauges at scrape time, so no ticker is needed and a value is never older than the
// scrape. fn must be fast and must not call WriteTo.
func (r *Registry) OnScrape(fn func()) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.hooks = append(r.hooks, fn)
}

type kind int

const (
	kCounter kind = iota
	kGauge
	kHistogram
)

func (k kind) String() string { return [...]string{"counter", "gauge", "histogram"}[k] }

type family struct {
	name, help string
	kind       kind
	labels     []string
	buckets    []float64

	mu     sync.Mutex
	series map[string]*series // key: label values joined by \xff
}

type series struct {
	values []string
	val    float64  // counter, gauge
	counts []uint64 // histogram: per bucket, not cumulative
	sum    float64  // histogram
	n      uint64   // histogram
}

func (r *Registry) register(name, help string, k kind, labels []string, buckets []float64) *family {
	r.mu.Lock()
	defer r.mu.Unlock()
	if f, ok := r.metrics[name]; ok {
		if f.kind != k || strings.Join(f.labels, ",") != strings.Join(labels, ",") {
			panic("obs: metric " + name + " registered twice with a different shape")
		}
		return f
	}
	f := &family{name: name, help: help, kind: k, labels: labels, buckets: buckets, series: map[string]*series{}}
	r.metrics[name] = f
	return f
}

func (f *family) get(values []string) *series {
	if len(values) != len(f.labels) {
		panic(fmt.Sprintf("obs: %s takes %d label values, got %d", f.name, len(f.labels), len(values)))
	}
	key := strings.Join(values, "\xff")
	f.mu.Lock()
	defer f.mu.Unlock()
	s, ok := f.series[key]
	if ok {
		return s
	}
	if len(f.series) >= MaxSeries {
		values = make([]string, len(f.labels))
		for i := range values {
			values[i] = overflow
		}
		key = strings.Join(values, "\xff")
		if s, ok = f.series[key]; ok {
			return s
		}
	}
	s = &series{values: append([]string(nil), values...)}
	if f.kind == kHistogram {
		s.counts = make([]uint64, len(f.buckets))
	}
	f.series[key] = s
	return s
}

// Counter is a value that only goes up.
type Counter struct{ f *family }

// NewCounter registers a counter with the given label names.
func (r *Registry) NewCounter(name, help string, labels ...string) *Counter {
	return &Counter{r.register(name, help, kCounter, labels, nil)}
}

// Inc adds 1 to the series for the label values.
func (c *Counter) Inc(values ...string) { c.Add(1, values...) }

// Add adds v (ignored if negative) to the series for the label values.
func (c *Counter) Add(v float64, values ...string) {
	if v < 0 {
		return
	}
	s := c.f.get(values)
	c.f.mu.Lock()
	s.val += v
	c.f.mu.Unlock()
}

// Set sets the series to v. It is for collectors that mirror a counter kept elsewhere (a pool
// statistic, the GC pause total): the source is monotonic, so the exposed series is too. A
// value below the current one is ignored, so a source that restarts cannot make it go back.
func (c *Counter) Set(v float64, values ...string) {
	s := c.f.get(values)
	c.f.mu.Lock()
	if v > s.val {
		s.val = v
	}
	c.f.mu.Unlock()
}

// Gauge is a value that goes up and down.
type Gauge struct{ f *family }

// NewGauge registers a gauge with the given label names.
func (r *Registry) NewGauge(name, help string, labels ...string) *Gauge {
	return &Gauge{r.register(name, help, kGauge, labels, nil)}
}

// Set sets the series to v.
func (g *Gauge) Set(v float64, values ...string) {
	s := g.f.get(values)
	g.f.mu.Lock()
	s.val = v
	g.f.mu.Unlock()
}

// Add adds v (which may be negative) to the series.
func (g *Gauge) Add(v float64, values ...string) {
	s := g.f.get(values)
	g.f.mu.Lock()
	s.val += v
	g.f.mu.Unlock()
}

// Histogram counts observations into fixed buckets.
type Histogram struct{ f *family }

// DefBuckets suit call latencies in seconds.
var DefBuckets = []float64{.005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5, 10}

// NewHistogram registers a histogram. buckets must be sorted ascending; nil uses DefBuckets.
func (r *Registry) NewHistogram(name, help string, buckets []float64, labels ...string) *Histogram {
	if buckets == nil {
		buckets = DefBuckets
	}
	if !sort.Float64sAreSorted(buckets) {
		panic("obs: histogram buckets must be sorted")
	}
	return &Histogram{r.register(name, help, kHistogram, labels, append([]float64(nil), buckets...))}
}

// Observe records v in the series for the label values.
func (h *Histogram) Observe(v float64, values ...string) {
	s := h.f.get(values)
	h.f.mu.Lock()
	defer h.f.mu.Unlock()
	s.sum += v
	s.n++
	for i, ub := range h.f.buckets {
		if v <= ub {
			s.counts[i]++
			break
		}
	}
}

var escaper = strings.NewReplacer(`\`, `\\`, "\n", `\n`, `"`, `\"`)

func labelText(names, values []string, extra ...string) string {
	var b strings.Builder
	n := 0
	for i, name := range names {
		if n > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, `%s="%s"`, name, escaper.Replace(values[i]))
		n++
	}
	for i := 0; i+1 < len(extra); i += 2 {
		if n > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, `%s="%s"`, extra[i], escaper.Replace(extra[i+1]))
		n++
	}
	if n == 0 {
		return ""
	}
	return "{" + b.String() + "}"
}

func num(v float64) string {
	switch {
	case math.IsInf(v, 1):
		return "+Inf"
	case math.IsInf(v, -1):
		return "-Inf"
	case math.IsNaN(v):
		return "NaN"
	}
	return strconv.FormatFloat(v, 'g', -1, 64)
}

// WriteTo renders every metric, sorted by name and then by label values.
func (r *Registry) WriteTo(w io.Writer) (int64, error) {
	r.mu.Lock()
	hooks := append([]func(){}, r.hooks...)
	r.mu.Unlock()
	for _, fn := range hooks {
		fn()
	}
	r.mu.Lock()
	fams := make([]*family, 0, len(r.metrics))
	for _, f := range r.metrics {
		fams = append(fams, f)
	}
	r.mu.Unlock()
	sort.Slice(fams, func(i, j int) bool { return fams[i].name < fams[j].name })

	var b strings.Builder
	for _, f := range fams {
		f.mu.Lock()
		keys := make([]string, 0, len(f.series))
		for k := range f.series {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		fmt.Fprintf(&b, "# HELP %s %s\n# TYPE %s %s\n", f.name, strings.NewReplacer(`\`, `\\`, "\n", `\n`).Replace(f.help), f.name, f.kind)
		for _, k := range keys {
			s := f.series[k]
			if f.kind != kHistogram {
				fmt.Fprintf(&b, "%s%s %s\n", f.name, labelText(f.labels, s.values), num(s.val))
				continue
			}
			var cum uint64
			for i, ub := range f.buckets {
				cum += s.counts[i]
				fmt.Fprintf(&b, "%s_bucket%s %d\n", f.name, labelText(f.labels, s.values, "le", num(ub)), cum)
			}
			fmt.Fprintf(&b, "%s_bucket%s %d\n", f.name, labelText(f.labels, s.values, "le", "+Inf"), s.n)
			fmt.Fprintf(&b, "%s_sum%s %s\n%s_count%s %d\n", f.name, labelText(f.labels, s.values), num(s.sum), f.name, labelText(f.labels, s.values), s.n)
		}
		f.mu.Unlock()
	}
	n, err := io.WriteString(w, b.String())
	return int64(n), err
}

// Handler serves the registry at GET /metrics.
func (r *Registry) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		_, _ = r.WriteTo(w)
	})
}
