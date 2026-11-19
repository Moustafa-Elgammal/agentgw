package main

import (
	"testing"
	"time"
)

func ms(n int) time.Duration { return time.Duration(n) * time.Millisecond }

func TestPercentile(t *testing.T) {
	ten := []time.Duration{ms(1), ms(2), ms(3), ms(4), ms(5), ms(6), ms(7), ms(8), ms(9), ms(10)}
	tests := []struct {
		name string
		in   []time.Duration
		p    float64
		want time.Duration
	}{
		{"empty", nil, 99, 0},
		{"single", []time.Duration{ms(7)}, 50, ms(7)},
		{"p50 of ten", ten, 50, ms(5)},
		{"p95 of ten", ten, 95, ms(10)},
		{"p99 of ten", ten, 99, ms(10)},
		{"p100", ten, 100, ms(10)},
		{"p10", ten, 10, ms(1)},
		{"p0 is the minimum", ten, 0, ms(1)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := percentile(tt.in, tt.p); got != tt.want {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
		})
	}
}

func TestSummarize(t *testing.T) {
	s := summarize([]sample{
		{d: ms(1), status: 200},
		{d: ms(2), status: 200},
		{d: ms(3), status: 422, code: "rate_limited"},
		{d: ms(4), status: 422, code: "rate_limited"},
		{d: ms(5), status: 422, code: "invalid_arguments"},
		{d: ms(6), status: 503},
		{d: ms(7), status: 0},
		{d: ms(8), status: 401},
	}, 2*time.Second)
	if s.Total != 8 || s.OK != 2 || s.Errors != 2 {
		t.Fatalf("counts: %+v", s)
	}
	if s.Denials["rate_limited"] != 2 || s.Denials["invalid_arguments"] != 1 || s.Denials["http_401"] != 1 {
		t.Fatalf("denials: %+v", s.Denials)
	}
	if s.RPS() != 4 || s.ErrorRate() != 0.25 || s.Max != ms(8) {
		t.Fatalf("derived: rps %v err %v max %v", s.RPS(), s.ErrorRate(), s.Max)
	}
	if empty := summarize(nil, 0); empty.RPS() != 0 || empty.ErrorRate() != 0 {
		t.Fatalf("empty: %+v", empty)
	}
}

func TestParseMix(t *testing.T) {
	m, err := parseMix("a=3, b=1")
	if err != nil || len(m.names) != 2 || m.total != 4 {
		t.Fatalf("mix = %+v, %v", m, err)
	}
	counts := map[string]int{}
	for i := 0; i < 4; i++ {
		counts[m.pick(i)]++
	}
	if counts["a"] != 3 || counts["b"] != 1 {
		t.Fatalf("pick spread = %v", counts)
	}
	for _, bad := range []string{"", "a", "a=0", "a=x", "=2", "a=1,a=2"} {
		if _, err := parseMix(bad); err == nil {
			t.Errorf("parseMix(%q) accepted", bad)
		}
	}
}

func TestAfterWarmup(t *testing.T) {
	in := []sample{{d: ms(1), at: 0}, {d: ms(2), at: ms(4999)}, {d: ms(3), at: ms(5000)}, {d: ms(4), at: ms(9000)}}
	if got := afterWarmup(in, 0); len(got) != 4 {
		t.Fatalf("no warmup must keep everything, got %d", len(got))
	}
	got := afterWarmup(in, ms(5000))
	if len(got) != 2 || got[0].d != ms(3) || got[1].d != ms(4) {
		t.Fatalf("warmup must drop calls that started before it: %+v", got)
	}
}
