// Command load drives a running gateway through its dev HTTP API (start the gateway with
// --dev-auth) and reports throughput, errors, denials and latency percentiles. It exits 1
// when a threshold is breached, so it can gate a smoke run in CI or watch a soak.
//
//	go run ./test/load --url http://127.0.0.1:8080 --duration 30s --rate 200
//
// An error is a transport failure or a 5xx. A denial (rate limit, cap, cooldown, validation)
// is an expected answer under load and is counted by code, not as an error.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// mix is a weighted list of tool names, picked by a counter so the spread is exact.
type mix struct {
	names []string
	cum   []int
	total int
}

func parseMix(s string) (mix, error) {
	var m mix
	seen := map[string]bool{}
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		name, w, ok := strings.Cut(part, "=")
		n, err := strconv.Atoi(strings.TrimSpace(w))
		if !ok || strings.TrimSpace(name) == "" || err != nil || n <= 0 {
			return mix{}, fmt.Errorf("bad mix entry %q, want tool=weight with weight > 0", part)
		}
		name = strings.TrimSpace(name)
		if seen[name] {
			return mix{}, fmt.Errorf("tool %q listed twice", name)
		}
		seen[name] = true
		m.total += n
		m.names = append(m.names, name)
		m.cum = append(m.cum, m.total)
	}
	return m, nil
}

func (m mix) pick(i int) string {
	r := i % m.total
	for j, c := range m.cum {
		if r < c {
			return m.names[j]
		}
	}
	return m.names[len(m.names)-1]
}

// defaultArgs fit examples/city/manifest.yaml.
var defaultArgs = map[string]string{
	"get_city_state":  `{}`,
	"collect_taxes":   `{}`,
	"sell_resource":   `{"resource":"food","quantity":1}`,
	"build_structure": `{"kind":"house","x":1,"y":1}`,
}

func main() {
	var (
		base     = flag.String("url", "http://127.0.0.1:8080", "gateway base URL (dev auth must be on)")
		duration = flag.Duration("duration", 30*time.Second, "how long to run")
		rate     = flag.Int("rate", 100, "target calls per second across all workers (0 = as fast as the workers can go)")
		workers  = flag.Int("concurrency", 16, "parallel workers")
		players  = flag.Int("players", 50, "distinct simulated players (each has its own limits)")
		mixFlag  = flag.String("mix", "get_city_state=8,sell_resource=2", "tool=weight list")
		argsFile = flag.String("args-file", "", "JSON file {\"tool\": {args}} overriding the built-in arguments")
		maxErr   = flag.Float64("max-error-rate", 0.01, "fail above this fraction of errors (transport failures and 5xx)")
		maxP99   = flag.Duration("max-p99", 500*time.Millisecond, "fail when p99 latency is above this (0 disables)")
		minRPS   = flag.Float64("min-rps", 0, "fail when throughput is below this (0 disables)")
		timeout  = flag.Duration("timeout", 10*time.Second, "per request timeout")
	)
	flag.Parse()

	m, err := parseMix(*mixFlag)
	if err != nil {
		fatal(err)
	}
	args := map[string]string{}
	for k, v := range defaultArgs {
		args[k] = v
	}
	if *argsFile != "" {
		b, err := os.ReadFile(*argsFile)
		if err != nil {
			fatal(err)
		}
		var over map[string]json.RawMessage
		if err := json.Unmarshal(b, &over); err != nil {
			fatal(fmt.Errorf("args-file: %w", err))
		}
		for k, v := range over {
			args[k] = string(v)
		}
	}
	for _, n := range m.names {
		if _, ok := args[n]; !ok {
			fatal(fmt.Errorf("no arguments known for tool %q, add it with --args-file", n))
		}
	}
	if *workers < 1 || *players < 1 || *duration <= 0 || *rate < 0 {
		fatal(errors.New("concurrency, players, duration must be positive and rate not negative"))
	}

	client := &http.Client{Timeout: *timeout, Transport: &http.Transport{MaxIdleConnsPerHost: *workers}}
	ctx, cancel := context.WithTimeout(context.Background(), *duration)
	defer cancel()

	var (
		n       atomic.Int64
		mu      sync.Mutex
		samples []sample
		wg      sync.WaitGroup
		tokens  chan struct{}
	)
	if *rate > 0 {
		tokens = make(chan struct{}, *workers)
		go pace(ctx, *rate, tokens)
	}
	start := time.Now()
	for w := 0; w < *workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var local []sample
			for {
				if tokens != nil {
					select {
					case <-ctx.Done():
					case <-tokens:
					}
				}
				if ctx.Err() != nil {
					break
				}
				i := int(n.Add(1) - 1)
				tool := m.pick(i)
				player := "load-" + strconv.Itoa(i%*players)
				local = append(local, one(ctx, client, *base, player, tool, args[tool]))
			}
			mu.Lock()
			samples = append(samples, local...)
			mu.Unlock()
		}()
	}
	wg.Wait()
	s := summarize(samples, time.Since(start))
	report(s)

	var fails []string
	if s.ErrorRate() > *maxErr {
		fails = append(fails, fmt.Sprintf("error rate %.4f above %.4f", s.ErrorRate(), *maxErr))
	}
	if *maxP99 > 0 && s.P99 > *maxP99 {
		fails = append(fails, fmt.Sprintf("p99 %v above %v", s.P99, *maxP99))
	}
	if *minRPS > 0 && s.RPS() < *minRPS {
		fails = append(fails, fmt.Sprintf("throughput %.1f/s below %.1f/s", s.RPS(), *minRPS))
	}
	if s.Total == 0 {
		fails = append(fails, "no calls completed")
	}
	if len(fails) > 0 {
		for _, f := range fails {
			fmt.Fprintln(os.Stderr, "FAIL:", f)
		}
		os.Exit(1)
	}
	fmt.Println("PASS")
}

// pace emits rate tokens per second until ctx ends. It drops a token when every worker is
// busy, so a slow gateway lowers throughput instead of building a backlog.
func pace(ctx context.Context, rate int, tokens chan<- struct{}) {
	t := time.NewTicker(time.Second / time.Duration(rate))
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			select {
			case tokens <- struct{}{}:
			default:
			}
		}
	}
}

func one(ctx context.Context, c *http.Client, base, player, tool, args string) sample {
	body := `{"arguments":` + args + `}`
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(base, "/")+"/v1/tools/"+tool+"/call", bytes.NewReader([]byte(body)))
	if err != nil {
		return sample{}
	}
	req.Header.Set("X-Dev-Player", player)
	req.Header.Set("Content-Type", "application/json")
	t0 := time.Now()
	resp, err := c.Do(req)
	if err != nil {
		return sample{d: time.Since(t0)}
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	s := sample{d: time.Since(t0), status: resp.StatusCode}
	if resp.StatusCode >= 300 {
		var r struct {
			Code string `json:"code"`
		}
		_ = json.Unmarshal(b, &r)
		s.code = r.Code
	}
	return s
}

func report(s summary) {
	fmt.Printf("calls      %d in %v (%.1f/s)\n", s.Total, s.Elapsed.Round(time.Millisecond), s.RPS())
	fmt.Printf("ok         %d\n", s.OK)
	fmt.Printf("errors     %d (%.2f%%)\n", s.Errors, s.ErrorRate()*100)
	fmt.Printf("latency    p50 %v  p95 %v  p99 %v  max %v\n", s.P50, s.P95, s.P99, s.Max)
	codes := make([]string, 0, len(s.Denials))
	for c := range s.Denials {
		codes = append(codes, c)
	}
	sort.Strings(codes)
	denied := 0
	for _, c := range codes {
		denied += s.Denials[c]
	}
	fmt.Printf("denials    %d\n", denied)
	for _, c := range codes {
		fmt.Printf("  %-24s %d\n", c, s.Denials[c])
	}
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "load:", err)
	os.Exit(2)
}
