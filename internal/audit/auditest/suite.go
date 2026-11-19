// Package auditest is the behavior contract every audit.Store must pass. Ring and Postgres
// both run it.
package auditest

import (
	"context"
	"testing"
	"time"

	"github.com/Moustafa-Elgammal/agentgw/internal/audit"
)

// Factory builds a fresh, empty store.
type Factory func(t *testing.T) audit.Store

var base = time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)

func ev(tenant, player, tool, outcome string, at time.Duration) audit.Event {
	return audit.Event{Time: base.Add(at), Tenant: tenant, Player: player, Tool: tool, Tier: "action",
		Outcome: outcome, ArgsSHA: "sha"}
}

func ids(es []audit.Stored) []int64 {
	out := make([]int64, len(es))
	for i, e := range es {
		out[i] = e.ID
	}
	return out
}

// Run runs the contract.
func Run(t *testing.T, mk Factory) {
	ctx := context.Background()
	seed := func(t *testing.T, s audit.Store) {
		t.Helper()
		for _, e := range []audit.Event{
			ev("t1", "alice", "get_state", "ok", 0),
			ev("t1", "alice", "build", "denied", time.Minute),
			ev("t1", "bob", "build", "ok", 2*time.Minute),
			ev("t2", "alice", "build", "ok", 3*time.Minute),
			ev("t1", "alice", "sell", "error", 4*time.Minute),
		} {
			s.Record(ctx, e)
		}
	}

	t.Run("newest first with all fields", func(t *testing.T) {
		s := mk(t)
		e := audit.Event{Time: base, Tenant: "t1", Player: "alice", Tool: "build", Tier: "action",
			Outcome: "denied", Code: "daily_amount_cap", ArgsSHA: "abc"}
		s.Record(ctx, e)
		s.Record(ctx, ev("t1", "alice", "sell", "ok", time.Minute))
		got, err := s.Query(ctx, audit.Query{Tenant: "t1"})
		if err != nil || len(got) != 2 {
			t.Fatalf("got %v, %v", got, err)
		}
		if got[0].ID <= got[1].ID || got[0].Tool != "sell" {
			t.Errorf("not newest first: %v", ids(got))
		}
		last := got[1]
		if last.Tenant != e.Tenant || last.Player != e.Player || last.Tool != e.Tool || last.Tier != e.Tier ||
			last.Outcome != e.Outcome || last.Code != e.Code || last.ArgsSHA != e.ArgsSHA || !last.Time.Equal(e.Time) {
			t.Errorf("round trip lost data: %+v", last)
		}
	})

	t.Run("empty store", func(t *testing.T) {
		got, err := mk(t).Query(ctx, audit.Query{Tenant: "t1"})
		if err != nil || len(got) != 0 {
			t.Errorf("got %v, %v", got, err)
		}
	})

	filters := []struct {
		name string
		q    audit.Query
		want int
	}{
		{"tenant isolation t1", audit.Query{Tenant: "t1"}, 4},
		{"tenant isolation t2", audit.Query{Tenant: "t2"}, 1},
		{"unknown tenant", audit.Query{Tenant: "nope"}, 0},
		{"player", audit.Query{Tenant: "t1", Player: "alice"}, 3},
		{"other player", audit.Query{Tenant: "t1", Player: "bob"}, 1},
		{"player in other tenant only", audit.Query{Tenant: "t2", Player: "bob"}, 0},
		{"tool", audit.Query{Tenant: "t1", Tool: "build"}, 2},
		{"outcome", audit.Query{Tenant: "t1", Outcome: "denied"}, 1},
		{"since is inclusive", audit.Query{Tenant: "t1", Since: base.Add(time.Minute)}, 3},
		{"since after all", audit.Query{Tenant: "t1", Since: base.Add(time.Hour)}, 0},
		{"combined", audit.Query{Tenant: "t1", Player: "alice", Tool: "build", Outcome: "denied"}, 1},
		{"combined no match", audit.Query{Tenant: "t1", Player: "bob", Outcome: "denied"}, 0},
	}
	for _, tt := range filters {
		t.Run("filter "+tt.name, func(t *testing.T) {
			s := mk(t)
			seed(t, s)
			got, err := s.Query(ctx, tt.q)
			if err != nil || len(got) != tt.want {
				t.Fatalf("got %d events (%v), want %d", len(got), err, tt.want)
			}
			for _, e := range got {
				if e.Tenant != tt.q.Tenant {
					t.Errorf("leaked tenant %q", e.Tenant)
				}
			}
		})
	}

	t.Run("cursor paging has no duplicates or gaps", func(t *testing.T) {
		s := mk(t)
		for i := 0; i < 25; i++ {
			s.Record(ctx, ev("t1", "alice", "build", "ok", time.Duration(i)*time.Second))
		}
		s.Record(ctx, ev("t2", "alice", "build", "ok", 0)) // other tenant interleaved
		for i := 0; i < 5; i++ {
			s.Record(ctx, ev("t1", "alice", "build", "ok", time.Duration(25+i)*time.Second))
		}
		seen := map[int64]bool{}
		var all []int64
		q := audit.Query{Tenant: "t1", Limit: 7}
		for pages := 0; pages < 10; pages++ {
			got, err := s.Query(ctx, q)
			if err != nil {
				t.Fatal(err)
			}
			if len(got) == 0 {
				break
			}
			if len(got) > 7 {
				t.Fatalf("page of %d exceeds limit", len(got))
			}
			for _, e := range got {
				if seen[e.ID] {
					t.Fatalf("duplicate id %d", e.ID)
				}
				seen[e.ID] = true
				if n := len(all); n > 0 && e.ID >= all[n-1] {
					t.Fatalf("not strictly descending: %d after %d", e.ID, all[n-1])
				}
				all = append(all, e.ID)
			}
			q.Before = got[len(got)-1].ID
		}
		if len(all) != 30 {
			t.Errorf("paged %d events, want 30", len(all))
		}
	})

	t.Run("limit clamp", func(t *testing.T) {
		s := mk(t)
		for i := 0; i < audit.MaxLimit+20; i++ {
			s.Record(ctx, ev("t1", "alice", "build", "ok", time.Duration(i)*time.Second))
		}
		for _, tt := range []struct{ limit, want int }{
			{0, audit.DefaultLimit}, {-3, audit.DefaultLimit}, {5, 5},
			{audit.MaxLimit + 1000, audit.MaxLimit},
		} {
			got, err := s.Query(ctx, audit.Query{Tenant: "t1", Limit: tt.limit})
			if err != nil || len(got) != tt.want {
				t.Errorf("limit %d: got %d (%v), want %d", tt.limit, len(got), err, tt.want)
			}
		}
	})

	t.Run("prune removes only older events", func(t *testing.T) {
		s := mk(t)
		seed(t, s) // times base+0..4m, t2 included
		n, err := s.Prune(ctx, base.Add(2*time.Minute))
		if err != nil || n != 2 {
			t.Fatalf("pruned %d (%v), want 2", n, err)
		}
		got, _ := s.Query(ctx, audit.Query{Tenant: "t1"})
		if len(got) != 2 {
			t.Errorf("t1 has %d events, want 2", len(got))
		}
		for _, e := range got {
			if e.Time.Before(base.Add(2 * time.Minute)) {
				t.Errorf("old event survived: %+v", e)
			}
		}
		if got, _ := s.Query(ctx, audit.Query{Tenant: "t2"}); len(got) != 1 {
			t.Errorf("t2 event at the cutoff boundary or later must survive, got %d", len(got))
		}
		if n, err := s.Prune(ctx, base.Add(2*time.Minute)); err != nil || n != 0 {
			t.Errorf("second prune = %d, %v", n, err)
		}
	})

	t.Run("ids keep increasing after prune", func(t *testing.T) {
		s := mk(t)
		s.Record(ctx, ev("t1", "alice", "build", "ok", 0))
		first, _ := s.Query(ctx, audit.Query{Tenant: "t1"})
		if _, err := s.Prune(ctx, base.Add(time.Hour)); err != nil {
			t.Fatal(err)
		}
		s.Record(ctx, ev("t1", "alice", "build", "ok", 0))
		second, _ := s.Query(ctx, audit.Query{Tenant: "t1"})
		if len(first) != 1 || len(second) != 1 || second[0].ID <= first[0].ID {
			t.Errorf("ids must never be reused: %v then %v", ids(first), ids(second))
		}
	})

	t.Run("record never panics on empty fields", func(t *testing.T) {
		s := mk(t)
		s.Record(ctx, audit.Event{})
		s.Record(ctx, audit.Event{Tenant: "t1"})
		if got, err := s.Query(ctx, audit.Query{Tenant: "t1"}); err != nil || len(got) != 1 {
			t.Errorf("got %v, %v", got, err)
		}
	})
}
