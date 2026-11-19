package obs

import (
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestPgxPoolMetrics(t *testing.T) {
	tests := []struct {
		name  string
		stats PoolStats
		want  []string
	}{
		{"idle pool", PoolStats{Acquired: 0, Idle: 4, Total: 4, Max: 8, AcquireCount: 100}, []string{
			"# TYPE agentgw_pgx_pool_acquired_conns gauge", "agentgw_pgx_pool_acquired_conns 0",
			"agentgw_pgx_pool_idle_conns 4", "agentgw_pgx_pool_total_conns 4", "agentgw_pgx_pool_max_conns 8",
			"# TYPE agentgw_pgx_pool_acquire_total counter", "agentgw_pgx_pool_acquire_total 100",
			"agentgw_pgx_pool_empty_acquire_total 0",
		}},
		{"saturated pool", PoolStats{Acquired: 8, Idle: 0, Total: 8, Max: 8, AcquireCount: 5000, EmptyAcquireCount: 37, CanceledAcquireCount: 2, AcquireDuration: 1500 * time.Millisecond}, []string{
			"agentgw_pgx_pool_acquired_conns 8", "agentgw_pgx_pool_max_conns 8",
			"agentgw_pgx_pool_empty_acquire_total 37", "agentgw_pgx_pool_canceled_acquire_total 2",
			"# TYPE agentgw_pgx_pool_acquire_duration_seconds_total counter", "agentgw_pgx_pool_acquire_duration_seconds_total 1.5",
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := NewRegistry()
			RegisterPgxPool(r, func() PoolStats { return tt.stats })
			out := render(r)
			for _, w := range tt.want {
				if !strings.Contains(out, w+"\n") {
					t.Errorf("missing %q in:\n%s", w, out)
				}
			}
		})
	}
}

func TestRedisPoolMetrics(t *testing.T) {
	r := NewRegistry()
	RegisterRedisPool(r, func() RedisPoolStats {
		return RedisPoolStats{Total: 10, Idle: 7, Hits: 900, Misses: 12, Timeouts: 3}
	})
	out := render(r)
	for _, w := range []string{
		"# TYPE agentgw_redis_pool_total_conns gauge", "agentgw_redis_pool_total_conns 10", "agentgw_redis_pool_idle_conns 7",
		"# TYPE agentgw_redis_pool_hits_total counter", "agentgw_redis_pool_hits_total 900",
		"agentgw_redis_pool_misses_total 12", "agentgw_redis_pool_timeouts_total 3",
	} {
		if !strings.Contains(out, w+"\n") {
			t.Errorf("missing %q in:\n%s", w, out)
		}
	}
}

func TestRuntimeMetrics(t *testing.T) {
	r := NewRegistry()
	RegisterRuntime(r)
	out := render(r)
	for _, name := range []string{
		"go_goroutines", "go_memstats_heap_alloc_bytes", "go_memstats_heap_inuse_bytes",
		"go_memstats_sys_bytes", "go_gc_cycles_total", "go_gc_pause_seconds_total",
	} {
		if !strings.Contains(out, "# TYPE "+name+" ") {
			t.Errorf("missing metric %s", name)
		}
	}
	if strings.Contains(out, "go_goroutines 0\n") {
		t.Error("go_goroutines must not be 0 in a running program")
	}
	if !strings.Contains(out, "# TYPE go_gc_pause_seconds_total counter") || !strings.Contains(out, "# TYPE go_goroutines gauge") {
		t.Errorf("wrong metric types:\n%s", out)
	}
}

// Values are read at scrape time: a change in the source shows up on the next scrape, with
// no ticker in between.
func TestCollectorsRefreshOnEveryScrape(t *testing.T) {
	var acquired atomic.Int32
	r := NewRegistry()
	RegisterPgxPool(r, func() PoolStats { return PoolStats{Acquired: acquired.Load(), Max: 8} })
	for _, n := range []int32{1, 5, 2} {
		acquired.Store(n)
		want := "agentgw_pgx_pool_acquired_conns " + string(rune('0'+n)) + "\n"
		if out := render(r); !strings.Contains(out, want) {
			t.Fatalf("after acquired=%d, scrape shows:\n%s", n, out)
		}
	}
}

// A mirrored counter never goes backwards, even if its source is reset.
func TestMirroredCounterIsMonotonic(t *testing.T) {
	r := NewRegistry()
	c := r.NewCounter("m_total", "h")
	for _, v := range []float64{10, 4, 12} {
		c.Set(v)
	}
	if out := render(r); !strings.Contains(out, "m_total 12\n") {
		t.Fatalf("got:\n%s", out)
	}
	c.Set(3)
	if out := render(r); !strings.Contains(out, "m_total 12\n") {
		t.Fatalf("a lower value must be ignored:\n%s", out)
	}
}
