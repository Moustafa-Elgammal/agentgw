package audit

import (
	"context"
	"sort"
	"sync"
	"time"
)

// Stored is an Event as kept by a Store: the event plus an id that orders events and
// serves as the paging cursor. Ids increase with insertion order within one store.
type Stored struct {
	ID int64 `json:"id"`
	Event
}

// Query selects events for the activity feed. Zero fields mean no filter, except Tenant,
// which is always required: a store never returns another tenant's events.
type Query struct {
	Tenant  string
	Player  string    // empty: every player (admin feed only)
	Tool    string    // empty: any tool
	Outcome string    // empty: any of ok, denied, error
	Since   time.Time // zero: no lower bound; events at or after Since
	Before  int64     // 0: start from the newest; otherwise only events with id < Before (cursor)
	Limit   int       // 0 means DefaultLimit; capped at MaxLimit
}

// Feed paging bounds.
const (
	DefaultLimit = 50
	MaxLimit     = 200
)

// Clamp returns the limit to use.
func (q Query) Clamp() int {
	switch {
	case q.Limit <= 0:
		return DefaultLimit
	case q.Limit > MaxLimit:
		return MaxLimit
	}
	return q.Limit
}

// Match reports whether a stored event passes the filters (not the cursor or limit).
func (q Query) Match(e Stored) bool {
	return e.Tenant == q.Tenant &&
		(q.Player == "" || e.Player == q.Player) &&
		(q.Tool == "" || e.Tool == q.Tool) &&
		(q.Outcome == "" || e.Outcome == q.Outcome) &&
		(q.Since.IsZero() || !e.Time.Before(q.Since)) &&
		(q.Before == 0 || e.ID < q.Before)
}

// Store is an Auditor whose events can be read back for the activity feed. Implementations
// must pass storetest.Run.
type Store interface {
	Auditor
	// Query returns matching events, newest first, at most q.Clamp() of them.
	Query(ctx context.Context, q Query) ([]Stored, error)
	// Prune deletes events older than before and returns how many were removed.
	Prune(ctx context.Context, before time.Time) (int64, error)
}

// Ring is an in-process Store that keeps the newest Cap events. For tests and single-process
// dev; events are lost on restart.
type Ring struct {
	Cap int // 0 means 10000

	mu     sync.Mutex
	next   int64
	events []Stored // insertion order
}

// Record implements Auditor.
func (r *Ring) Record(_ context.Context, e Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	max := r.Cap
	if max <= 0 {
		max = 10000
	}
	r.next++
	r.events = append(r.events, Stored{ID: r.next, Event: e})
	if len(r.events) > max {
		r.events = append([]Stored(nil), r.events[len(r.events)-max:]...)
	}
}

// Query implements Store.
func (r *Ring) Query(_ context.Context, q Query) ([]Stored, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := []Stored{}
	for i := len(r.events) - 1; i >= 0 && len(out) < q.Clamp(); i-- {
		if q.Match(r.events[i]) {
			out = append(out, r.events[i])
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].ID > out[j].ID })
	return out, nil
}

// Prune implements Store.
func (r *Ring) Prune(_ context.Context, before time.Time) (int64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	kept := r.events[:0:0]
	for _, e := range r.events {
		if !e.Time.Before(before) {
			kept = append(kept, e)
		}
	}
	n := int64(len(r.events) - len(kept))
	r.events = kept
	return n, nil
}
