package obs

import (
	"os"
	"runtime"
	"time"
)

// RegisterRuntime adds Go runtime gauges to r, refreshed at every scrape. The names follow the
// usual Prometheus Go collector where one exists. GC pause time is a plain counter
// (go_gc_pause_seconds_total) rather than the client library's summary, because this registry
// has no summaries.
//
//	go_goroutines                       goroutines now
//	go_memstats_heap_alloc_bytes        bytes of live and not yet swept heap objects
//	go_memstats_heap_inuse_bytes        bytes in in-use heap spans
//	go_memstats_sys_bytes               bytes obtained from the OS
//	go_gc_cycles_total                  completed GC cycles
//	go_gc_pause_seconds_total           total stop-the-world GC pause time
//	process_open_fds                    open file descriptors (Linux only, omitted elsewhere)
func RegisterRuntime(r *Registry) {
	goroutines := r.NewGauge("go_goroutines", "Number of goroutines that currently exist.")
	heapAlloc := r.NewGauge("go_memstats_heap_alloc_bytes", "Bytes of allocated heap objects.")
	heapInuse := r.NewGauge("go_memstats_heap_inuse_bytes", "Bytes in in-use heap spans.")
	sys := r.NewGauge("go_memstats_sys_bytes", "Bytes of memory obtained from the OS.")
	gcCycles := r.NewCounter("go_gc_cycles_total", "Completed garbage collection cycles.")
	gcPause := r.NewCounter("go_gc_pause_seconds_total", "Total stop-the-world GC pause time in seconds.")
	fds := r.NewGauge("process_open_fds", "Number of open file descriptors.")
	r.OnScrape(func() {
		var m runtime.MemStats
		runtime.ReadMemStats(&m)
		goroutines.Set(float64(runtime.NumGoroutine()))
		heapAlloc.Set(float64(m.HeapAlloc))
		heapInuse.Set(float64(m.HeapInuse))
		sys.Set(float64(m.Sys))
		gcCycles.Set(float64(m.NumGC))
		gcPause.Set(float64(m.PauseTotalNs) / 1e9)
		if n, ok := openFDs(); ok {
			fds.Set(float64(n))
		}
	})
}

// openFDs counts /proc/self/fd entries. It reports false where that directory does not exist.
func openFDs() (int, bool) {
	ents, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		return 0, false
	}
	return len(ents), true
}

// PoolStats is a snapshot of a database connection pool. cmd/gateway fills it from pgxpool, so
// this package stays free of the driver.
type PoolStats struct {
	Acquired, Idle, Total, Max int32 // connections in use, idle, open, and the cap
	AcquireCount               int64 // successful acquires, ever
	EmptyAcquireCount          int64 // acquires that had to wait because the pool was empty
	CanceledAcquireCount       int64 // acquires abandoned by their context
	AcquireDuration            time.Duration
}

// RegisterPgxPool adds Postgres pool metrics, refreshed at every scrape from stats:
//
//	agentgw_pgx_pool_acquired_conns, _idle_conns, _total_conns, _max_conns   gauges
//	agentgw_pgx_pool_acquire_total                                           counter
//	agentgw_pgx_pool_empty_acquire_total                                     counter (waited for a connection)
//	agentgw_pgx_pool_canceled_acquire_total                                  counter
//	agentgw_pgx_pool_acquire_duration_seconds_total                          counter (time spent acquiring)
//
// Saturation is acquired / max near 1 together with a rising empty_acquire rate.
func RegisterPgxPool(r *Registry, stats func() PoolStats) {
	acquired := r.NewGauge("agentgw_pgx_pool_acquired_conns", "Postgres connections in use by a query now.")
	idle := r.NewGauge("agentgw_pgx_pool_idle_conns", "Postgres connections open and idle.")
	total := r.NewGauge("agentgw_pgx_pool_total_conns", "Postgres connections open (in use, idle or being built).")
	maxc := r.NewGauge("agentgw_pgx_pool_max_conns", "Largest number of Postgres connections the pool may open.")
	acq := r.NewCounter("agentgw_pgx_pool_acquire_total", "Successful connection acquires.")
	empty := r.NewCounter("agentgw_pgx_pool_empty_acquire_total", "Acquires that waited because no connection was idle.")
	canceled := r.NewCounter("agentgw_pgx_pool_canceled_acquire_total", "Acquires abandoned because their context ended.")
	dur := r.NewCounter("agentgw_pgx_pool_acquire_duration_seconds_total", "Total time spent acquiring connections, in seconds.")
	r.OnScrape(func() {
		s := stats()
		acquired.Set(float64(s.Acquired))
		idle.Set(float64(s.Idle))
		total.Set(float64(s.Total))
		maxc.Set(float64(s.Max))
		acq.Set(float64(s.AcquireCount))
		empty.Set(float64(s.EmptyAcquireCount))
		canceled.Set(float64(s.CanceledAcquireCount))
		dur.Set(s.AcquireDuration.Seconds())
	})
}

// RedisPoolStats is a snapshot of a Redis client pool. cmd/gateway fills it from go-redis.
type RedisPoolStats struct {
	Total, Idle            uint32 // open connections, and how many are idle
	Hits, Misses, Timeouts uint32 // idle connection found, had to dial, waited past the pool timeout
}

// RegisterRedisPool adds Redis pool metrics, refreshed at every scrape from stats:
// agentgw_redis_pool_total_conns and _idle_conns (gauges), and _hits_total, _misses_total and
// _timeouts_total (counters). A rising timeouts counter means the pool is too small.
func RegisterRedisPool(r *Registry, stats func() RedisPoolStats) {
	total := r.NewGauge("agentgw_redis_pool_total_conns", "Redis connections open.")
	idle := r.NewGauge("agentgw_redis_pool_idle_conns", "Redis connections open and idle.")
	hits := r.NewCounter("agentgw_redis_pool_hits_total", "Times an idle Redis connection was found.")
	misses := r.NewCounter("agentgw_redis_pool_misses_total", "Times no idle Redis connection was found and one was dialed.")
	timeouts := r.NewCounter("agentgw_redis_pool_timeouts_total", "Times waiting for a Redis connection timed out.")
	r.OnScrape(func() {
		s := stats()
		total.Set(float64(s.Total))
		idle.Set(float64(s.Idle))
		hits.Set(float64(s.Hits))
		misses.Set(float64(s.Misses))
		timeouts.Set(float64(s.Timeouts))
	})
}
