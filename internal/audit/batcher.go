package audit

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

// BatchWriter is a store that can write many events in one operation. Postgres does it with
// COPY, which is one transaction and one WAL flush for the whole batch.
type BatchWriter interface {
	WriteBatch(ctx context.Context, events []Event) error
}

// Overflow says what Record does when the queue is full.
type Overflow int

// Overflow policies.
const (
	// Drop discards the event at once and counts it. The call path never waits.
	Drop Overflow = iota
	// Block waits for room until the caller's context ends (the gateway gives audit a 5 second
	// deadline), then drops and counts the event.
	Block
)

// Reasons an event was not written, as reported to BatchObserver.Dropped.
const (
	DropOverflow   = "overflow"    // the queue was full
	DropWriteError = "write_error" // the batch it was in failed to write
	DropClosed     = "closed"      // Record was called after Close
)

// BatchObserver receives what the Batcher does, for metrics. Methods must not block.
type BatchObserver interface {
	// Dropped reports n events that will never be written, and why.
	Dropped(reason string, n int)
	// Flushed reports one write attempt: how many events it held, how long it took, and its error.
	Flushed(size int, d time.Duration, err error)
}

// BatcherConfig tunes a Batcher. Only Queue is required.
type BatcherConfig struct {
	Queue         int           // capacity in events; must be positive
	BatchSize     int           // most events in one write; default 500
	FlushInterval time.Duration // longest an event waits for its batch; default 200ms
	WriteTimeout  time.Duration // bound on one batch write; default 5s
	Overflow      Overflow
	// OnError receives a failed batch write. The text holds no event data.
	OnError  func(error)
	Observer BatchObserver
}

// Batcher is an audit.Store that queues events and writes them in batches from one goroutine.
// Record never waits for the database, so a slow store cannot slow a tool call. The price is
// that a crash can lose the events still queued: at most Queue of them, and normally no more
// than one FlushInterval's worth. Close drains the queue on a graceful shutdown.
//
// One writer keeps events in arrival order, so ids follow the order the gateway recorded them.
// Query flushes first, so within one process an event is readable as soon as Record returned.
// Other replicas see it after at most FlushInterval.
type Batcher struct {
	Store // the wrapped store; Query and Prune flush the queue first, then go to it

	w   BatchWriter
	cfg BatcherConfig

	ch     chan item
	stop   chan struct{} // closed first by Close: wakes Records that are blocked on a full queue
	finish chan struct{} // closed once no Record can send any more: the writer drains and ends
	done   chan struct{} // closed when the writer has ended

	mu     sync.RWMutex // held for reading by Record while it sends, for writing by Close
	closed bool
	once   sync.Once

	dropped atomic.Int64
}

type item struct {
	e       Event
	barrier chan struct{} // non-nil: not an event, a request to flush and then close the channel
}

// NewBatcher wraps inner, which must also implement BatchWriter, and starts the writer.
func NewBatcher(inner Store, cfg BatcherConfig) (*Batcher, error) {
	w, ok := inner.(BatchWriter)
	if !ok {
		return nil, errors.New("audit: the wrapped store cannot write batches")
	}
	if cfg.Queue <= 0 {
		return nil, errors.New("audit: queue must be positive")
	}
	if cfg.BatchSize <= 0 {
		cfg.BatchSize = 500
	}
	if cfg.FlushInterval <= 0 {
		cfg.FlushInterval = 200 * time.Millisecond
	}
	if cfg.WriteTimeout <= 0 {
		cfg.WriteTimeout = 5 * time.Second
	}
	b := &Batcher{
		Store: inner, w: w, cfg: cfg,
		ch:     make(chan item, cfg.Queue),
		stop:   make(chan struct{}),
		finish: make(chan struct{}),
		done:   make(chan struct{}),
	}
	go b.run()
	return b, nil
}

// Record implements Auditor. It enqueues the event and returns without touching the database.
func (b *Batcher) Record(ctx context.Context, e Event) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	if b.closed {
		b.drop(DropClosed, 1)
		return
	}
	select {
	case b.ch <- item{e: e}:
		return
	default:
	}
	if b.cfg.Overflow == Block {
		select {
		case b.ch <- item{e: e}:
			return
		case <-ctx.Done():
		case <-b.stop:
		}
	}
	b.drop(DropOverflow, 1)
}

// Query implements Store. It flushes queued events first, so the caller reads its own writes,
// then reads from the wrapped store. A flush that cannot finish before ctx ends is skipped.
func (b *Batcher) Query(ctx context.Context, q Query) ([]Stored, error) {
	_ = b.Flush(ctx)
	return b.Store.Query(ctx, q)
}

// Prune implements Store. It flushes first so events recorded before the call are subject to it.
func (b *Batcher) Prune(ctx context.Context, before time.Time) (int64, error) {
	_ = b.Flush(ctx)
	return b.Store.Prune(ctx, before)
}

// Flush writes everything queued before the call and returns when it is written, or when ctx
// ends. It returns nil immediately after Close.
func (b *Batcher) Flush(ctx context.Context) error {
	b.mu.RLock()
	if b.closed {
		b.mu.RUnlock()
		return nil
	}
	bar := make(chan struct{})
	select {
	case b.ch <- item{barrier: bar}:
		b.mu.RUnlock()
	case <-ctx.Done():
		b.mu.RUnlock()
		return ctx.Err()
	case <-b.stop:
		b.mu.RUnlock()
		return nil
	}
	select {
	case <-bar:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-b.done:
		return nil
	}
}

// Close stops accepting events, writes what is queued and ends the writer. It returns early with
// ctx.Err() if the drain takes longer than ctx allows; the writer still finishes in the
// background. Safe to call more than once.
func (b *Batcher) Close(ctx context.Context) error {
	b.once.Do(func() {
		close(b.stop)
		b.mu.Lock() // waits for every Record in flight, so nothing sends after this
		b.closed = true
		b.mu.Unlock()
		close(b.finish)
	})
	select {
	case <-b.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Depth is how many events are queued right now.
func (b *Batcher) Depth() int { return len(b.ch) }

// Dropped is how many events were discarded since the Batcher started.
func (b *Batcher) Dropped() int64 { return b.dropped.Load() }

func (b *Batcher) drop(reason string, n int) {
	b.dropped.Add(int64(n))
	if b.cfg.Observer != nil {
		b.cfg.Observer.Dropped(reason, n)
	}
}

func (b *Batcher) run() {
	defer close(b.done)
	batch := make([]Event, 0, b.cfg.BatchSize)
	var waiters []chan struct{}
	timer := time.NewTimer(time.Hour)
	stopTimer := func() {
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
	}
	stopTimer()

	flush := func() {
		stopTimer()
		if len(batch) > 0 {
			b.write(batch)
			batch = batch[:0]
		}
		for _, w := range waiters {
			close(w)
		}
		waiters = waiters[:0]
	}
	handle := func(it item) {
		if it.barrier != nil {
			waiters = append(waiters, it.barrier)
			flush()
			return
		}
		batch = append(batch, it.e)
		if len(batch) == 1 {
			stopTimer()
			timer.Reset(b.cfg.FlushInterval)
		}
		if len(batch) >= b.cfg.BatchSize {
			flush()
		}
	}

	for {
		select {
		case it := <-b.ch:
			handle(it)
		case <-timer.C:
			flush()
		case <-b.finish:
			for {
				select {
				case it := <-b.ch:
					handle(it)
				default:
					flush()
					return
				}
			}
		}
	}
}

// write sends one batch. A failure is counted and reported, and the events are not retried, which
// keeps memory bounded and matches a failed single INSERT today.
func (b *Batcher) write(batch []Event) {
	ctx, cancel := context.WithTimeout(context.Background(), b.cfg.WriteTimeout)
	defer cancel()
	start := time.Now()
	err := b.w.WriteBatch(ctx, batch)
	if b.cfg.Observer != nil {
		b.cfg.Observer.Flushed(len(batch), time.Since(start), err)
	}
	if err != nil {
		b.drop(DropWriteError, len(batch))
		if b.cfg.OnError != nil {
			b.cfg.OnError(fmt.Errorf("audit batcher: write %d events: %w", len(batch), err))
		}
	}
}
