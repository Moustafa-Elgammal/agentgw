package main

import (
	"sort"
	"time"
)

// percentile returns the nearest-rank percentile (0 < p <= 100) of sorted durations.
// An empty slice gives 0.
func percentile(sorted []time.Duration, p float64) time.Duration {
	n := len(sorted)
	if n == 0 {
		return 0
	}
	if p <= 0 {
		return sorted[0]
	}
	rank := int(p/100*float64(n) + 0.999999999) // ceil without importing math
	if rank < 1 {
		rank = 1
	}
	if rank > n {
		rank = n
	}
	return sorted[rank-1]
}

// summary is the outcome of a run.
type summary struct {
	Total, OK, Errors int
	Denials           map[string]int // by denial code
	Elapsed           time.Duration
	P50, P95, P99     time.Duration
	Max               time.Duration
}

func (s summary) RPS() float64 {
	if s.Elapsed <= 0 {
		return 0
	}
	return float64(s.Total) / s.Elapsed.Seconds()
}

func (s summary) ErrorRate() float64 {
	if s.Total == 0 {
		return 0
	}
	return float64(s.Errors) / float64(s.Total)
}

type sample struct {
	d      time.Duration
	status int           // 0 means transport error
	code   string        // denial code for a 422
	at     time.Duration // when the call started, since the run began (used to drop a warmup)
}

// afterWarmup returns the samples that started at or after warmup.
func afterWarmup(samples []sample, warmup time.Duration) []sample {
	if warmup <= 0 {
		return samples
	}
	out := make([]sample, 0, len(samples))
	for _, x := range samples {
		if x.at >= warmup {
			out = append(out, x)
		}
	}
	return out
}

// summarize turns samples into a summary. A transport error or a 5xx is an error, a 2xx is
// ok, and a 4xx carrying a code is a denial (expected under load: rate limits, caps).
func summarize(samples []sample, elapsed time.Duration) summary {
	s := summary{Total: len(samples), Denials: map[string]int{}, Elapsed: elapsed}
	ds := make([]time.Duration, 0, len(samples))
	for _, x := range samples {
		ds = append(ds, x.d)
		switch {
		case x.status == 0 || x.status >= 500:
			s.Errors++
		case x.status < 300:
			s.OK++
		default:
			c := x.code
			if c == "" {
				c = "http_" + itoa(x.status)
			}
			s.Denials[c]++
		}
	}
	sort.Slice(ds, func(i, j int) bool { return ds[i] < ds[j] })
	s.P50, s.P95, s.P99 = percentile(ds, 50), percentile(ds, 95), percentile(ds, 99)
	if len(ds) > 0 {
		s.Max = ds[len(ds)-1]
	}
	return s
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [12]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
