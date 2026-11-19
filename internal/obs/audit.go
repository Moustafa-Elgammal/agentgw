package obs

import "time"

// AuditMetrics reports the audit batcher: queue depth, drops, batch size and write time. It
// implements audit.BatchObserver.
type AuditMetrics struct {
	depth     *Gauge
	dropped   *Counter
	batchSize *Histogram
	flushDur  *Histogram
	flushErrs *Counter
}

// NewAuditMetrics registers the audit metrics on r. depth is read on every scrape.
func NewAuditMetrics(r *Registry, depth func() int) *AuditMetrics {
	m := &AuditMetrics{
		depth:     r.NewGauge("audit_queue_depth", "Audit events queued and not yet written."),
		dropped:   r.NewCounter("audit_dropped_total", "Audit events that were never written, by reason (overflow, write_error, closed). Alert on any rate.", "reason"),
		batchSize: r.NewHistogram("audit_batch_size", "Events per audit batch write.", []float64{1, 5, 10, 50, 100, 250, 500, 1000, 5000}),
		flushDur:  r.NewHistogram("audit_flush_duration_seconds", "Time to write one audit batch.", nil),
		flushErrs: r.NewCounter("audit_flush_errors_total", "Audit batch writes that failed."),
	}
	r.OnScrape(func() { m.depth.Set(float64(depth())) })
	return m
}

// Dropped implements audit.BatchObserver.
func (m *AuditMetrics) Dropped(reason string, n int) { m.dropped.Add(float64(n), reason) }

// Flushed implements audit.BatchObserver.
func (m *AuditMetrics) Flushed(size int, d time.Duration, err error) {
	m.batchSize.Observe(float64(size))
	m.flushDur.Observe(d.Seconds())
	if err != nil {
		m.flushErrs.Inc()
	}
}
