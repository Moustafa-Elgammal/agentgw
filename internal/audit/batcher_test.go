package audit_test

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Moustafa-Elgammal/agentgw/internal/audit"
	"github.com/Moustafa-Elgammal/agentgw/internal/audit/auditest"
)

// sink is a Ring that can also write batches, with hooks to hold up or fail writes.
type sink struct {
	*audit.Ring
	mu      sync.Mutex
	batches []int // size of each WriteBatch call
	order   []string
	gate    chan struct{} // when non-nil, WriteBatch waits for it to close
	fail    atomic.Bool
}

func newSink() *sink { return &sink{Ring: &audit.Ring{Cap: 1 << 20}} }

func (s *sink) WriteBatch(ctx context.Context, events []audit.Event) error {
	if g := s.gate; g != nil {
		select {
		case <-g:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if s.fail.Load() {
		return errors.New("store down")
	}
	s.mu.Lock()
	s.batches = append(s.batches, len(events))
	for _, e := range events {
		s.order = append(s.order, e.Tool)
	}
	s.mu.Unlock()
	for _, e := range events {
		s.Record(context.Background(), e)
	}
	return nil
}

func (s *sink) written() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.order)
}

type obsRec struct {
	mu      sync.Mutex
	dropped map[string]int
	sizes   []int
	errs    int
}

func (o *obsRec) Dropped(reason string, n int) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.dropped == nil {
		o.dropped = map[string]int{}
	}
	o.dropped[reason] += n
}

func (o *obsRec) Flushed(size int, _ time.Duration, err error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.sizes = append(o.sizes, size)
	if err != nil {
		o.errs++
	}
}

func (o *obsRec) drops(reason string) int {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.dropped[reason]
}

func ev(tool string) audit.Event {
	return audit.Event{Time: time.Unix(1700000000, 0).UTC(), Tenant: "t1", Player: "p1", Tool: tool, Tier: "read", Outcome: "ok", ArgsSHA: "x"}
}

func mustBatcher(t *testing.T, s audit.Store, cfg audit.BatcherConfig) *audit.Batcher {
	t.Helper()
	b, err := audit.NewBatcher(s, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = b.Close(context.Background()) })
	return b
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for i := 0; i < 400; i++ {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// The Batcher is an audit.Store like any other, so it passes the same contract as Ring and Postgres.
func TestBatcherContract(t *testing.T) {
	auditest.Run(t, func(t *testing.T) audit.Store {
		return mustBatcher(t, newSink(), audit.BatcherConfig{Queue: 1024, BatchSize: 7, FlushInterval: time.Hour})
	})
}

func TestNewBatcherRejects(t *testing.T) {
	if _, err := audit.NewBatcher(&audit.Ring{}, audit.BatcherConfig{Queue: 10}); err == nil {
		t.Error("a store without WriteBatch must be rejected")
	}
	if _, err := audit.NewBatcher(newSink(), audit.BatcherConfig{}); err == nil {
		t.Error("a zero queue must be rejected")
	}
}

func TestBatchesBySizeThenInterval(t *testing.T) {
	s := newSink()
	o := &obsRec{}
	b := mustBatcher(t, s, audit.BatcherConfig{Queue: 100, BatchSize: 3, FlushInterval: 30 * time.Millisecond, Observer: o})
	ctx := context.Background()
	for i := 0; i < 7; i++ {
		b.Record(ctx, ev("t"+strconv.Itoa(i)))
	}
	eventually(t, "all seven written", func() bool { return s.written() == 7 })
	s.mu.Lock()
	got := append([]int(nil), s.batches...)
	s.mu.Unlock()
	if len(got) != 3 || got[0] != 3 || got[1] != 3 || got[2] != 1 {
		t.Errorf("batches = %v, want [3 3 1] (two full, the last by interval)", got)
	}
}

func TestOrderIsKept(t *testing.T) {
	s := newSink()
	b := mustBatcher(t, s, audit.BatcherConfig{Queue: 5000, BatchSize: 64, FlushInterval: time.Millisecond})
	for i := 0; i < 2000; i++ {
		b.Record(context.Background(), ev(strconv.Itoa(i)))
	}
	if err := b.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.order) != 2000 {
		t.Fatalf("wrote %d of 2000", len(s.order))
	}
	for i, tool := range s.order {
		if tool != strconv.Itoa(i) {
			t.Fatalf("event %d is %q: order lost", i, tool)
		}
	}
}

func TestOverflowDropNeverBlocks(t *testing.T) {
	s := newSink()
	s.gate = make(chan struct{})
	o := &obsRec{}
	b := mustBatcher(t, s, audit.BatcherConfig{Queue: 2, BatchSize: 1, FlushInterval: time.Millisecond, Overflow: audit.Drop, Observer: o})
	start := time.Now()
	for i := 0; i < 50; i++ {
		b.Record(context.Background(), ev("x"))
	}
	if d := time.Since(start); d > time.Second {
		t.Fatalf("Record blocked for %v with a stuck store", d)
	}
	if got := o.drops(audit.DropOverflow); got < 40 || int64(got) != b.Dropped() {
		t.Errorf("dropped %d (counter %d), want most of 50", got, b.Dropped())
	}
	close(s.gate)
}

func TestOverflowBlockWaitsForTheCallerDeadline(t *testing.T) {
	s := newSink()
	s.gate = make(chan struct{})
	o := &obsRec{}
	b := mustBatcher(t, s, audit.BatcherConfig{Queue: 1, BatchSize: 1, FlushInterval: time.Millisecond, Overflow: audit.Block, Observer: o})
	// Fill the writer (stuck on the gate) and the queue.
	b.Record(context.Background(), ev("a"))
	eventually(t, "writer picked up the first event", func() bool { return b.Depth() == 0 })
	b.Record(context.Background(), ev("b"))

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Millisecond)
	defer cancel()
	start := time.Now()
	b.Record(ctx, ev("c"))
	if d := time.Since(start); d < 50*time.Millisecond || d > 2*time.Second {
		t.Errorf("a blocked Record took %v, want about the 60ms deadline", d)
	}
	if o.drops(audit.DropOverflow) != 1 {
		t.Errorf("overflow drops = %d, want 1", o.drops(audit.DropOverflow))
	}

	// With room again, a blocked Record goes through instead of dropping.
	done := make(chan struct{})
	go func() { b.Record(context.Background(), ev("d")); close(done) }()
	time.Sleep(20 * time.Millisecond)
	close(s.gate)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("blocked Record never got room")
	}
	if o.drops(audit.DropOverflow) != 1 {
		t.Errorf("a Record that found room was dropped")
	}
}

func TestFailedWriteIsCountedAndLaterEventsStillWrite(t *testing.T) {
	s := newSink()
	s.fail.Store(true)
	o := &obsRec{}
	var onErr atomic.Int32
	b := mustBatcher(t, s, audit.BatcherConfig{Queue: 100, BatchSize: 100, FlushInterval: time.Hour, Observer: o,
		OnError: func(error) { onErr.Add(1) }})
	ctx := context.Background()
	for i := 0; i < 5; i++ {
		b.Record(ctx, ev("lost"))
	}
	_ = b.Flush(ctx)
	if o.drops(audit.DropWriteError) != 5 || onErr.Load() != 1 || o.errs != 1 {
		t.Fatalf("write_error drops %d, OnError calls %d, flush errors %d; want 5, 1, 1", o.drops(audit.DropWriteError), onErr.Load(), o.errs)
	}
	s.fail.Store(false)
	b.Record(ctx, ev("kept"))
	_ = b.Flush(ctx)
	if s.written() != 1 {
		t.Errorf("store recovered but wrote %d events, want 1", s.written())
	}
}

func TestCloseDrainsAndLaterRecordsAreCounted(t *testing.T) {
	s := newSink()
	o := &obsRec{}
	b := mustBatcher(t, s, audit.BatcherConfig{Queue: 100, BatchSize: 1000, FlushInterval: time.Hour, Observer: o})
	for i := 0; i < 50; i++ {
		b.Record(context.Background(), ev("e"))
	}
	if err := b.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if s.written() != 50 {
		t.Errorf("Close wrote %d of 50", s.written())
	}
	b.Record(context.Background(), ev("late")) // must not panic
	if o.drops(audit.DropClosed) != 1 {
		t.Errorf("closed drops = %d, want 1", o.drops(audit.DropClosed))
	}
	if err := b.Close(context.Background()); err != nil {
		t.Errorf("second Close: %v", err)
	}
	if err := b.Flush(context.Background()); err != nil {
		t.Errorf("Flush after Close: %v", err)
	}
}

func TestCloseWakesABlockedRecord(t *testing.T) {
	s := newSink()
	s.gate = make(chan struct{})
	b := mustBatcher(t, s, audit.BatcherConfig{Queue: 1, BatchSize: 1, FlushInterval: time.Millisecond, Overflow: audit.Block})
	b.Record(context.Background(), ev("a"))
	eventually(t, "writer picked up the first event", func() bool { return b.Depth() == 0 })
	b.Record(context.Background(), ev("b"))
	blocked := make(chan struct{})
	go func() { b.Record(context.Background(), ev("c")); close(blocked) }()
	time.Sleep(20 * time.Millisecond)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_ = b.Close(ctx) // the writer is stuck, so Close times out, but the blocked Record must wake
	select {
	case <-blocked:
	case <-time.After(2 * time.Second):
		t.Fatal("Close left a Record blocked")
	}
	close(s.gate)
}

func TestQuerySeesQueuedEvents(t *testing.T) {
	s := newSink()
	b := mustBatcher(t, s, audit.BatcherConfig{Queue: 100, BatchSize: 100, FlushInterval: time.Hour})
	b.Record(context.Background(), ev("queued"))
	got, err := b.Query(context.Background(), audit.Query{Tenant: "t1"})
	if err != nil || len(got) != 1 || got[0].Tool != "queued" {
		t.Fatalf("Query = %v, %v; want the queued event", got, err)
	}
}

func TestConcurrentRecordsAreAllAccounted(t *testing.T) {
	s := newSink()
	o := &obsRec{}
	b := mustBatcher(t, s, audit.BatcherConfig{Queue: 64, BatchSize: 16, FlushInterval: time.Millisecond, Overflow: audit.Drop, Observer: o})
	var wg sync.WaitGroup
	const workers, each = 16, 500
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < each; i++ {
				b.Record(context.Background(), ev("c"))
			}
		}()
	}
	wg.Wait()
	_ = b.Flush(context.Background())
	if total := s.written() + int(b.Dropped()); total != workers*each {
		t.Errorf("written %d + dropped %d = %d, want %d", s.written(), b.Dropped(), total, workers*each)
	}
}
