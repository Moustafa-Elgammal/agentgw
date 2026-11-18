// Package confirmtest is the behavior contract every confirmation store must
// pass. Memory and Postgres both run it.
package confirmtest

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Moustafa-Elgammal/agentgw/internal/confirm"
	"github.com/Moustafa-Elgammal/agentgw/internal/core"
)

// Factory builds a fresh, empty store that reads time from now.
type Factory func(t *testing.T, now func() time.Time) confirm.Store

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func newClock() *clock { return &clock{t: time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)} }

func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

var (
	alice = core.Principal{TenantID: "t", PlayerID: "alice"}
	bob   = core.Principal{TenantID: "t", PlayerID: "bob"}
	ctx   = context.Background()
)

const tool, sha = "build_structure", "aaaa"

// Run executes the whole contract against stores made by f.
func Run(t *testing.T, f Factory) {
	t.Helper()
	cases := []struct {
		name string
		fn   func(t *testing.T, f Factory)
	}{
		{"FirstCallCreatesPendingAndRepeatsShareIt", createsPending},
		{"ApprovedIsClaimedOnceAndConsumed", claimOnceConsume},
		{"FailedCallReturnsClaimToApproved", failedReturnsToApproved},
		{"DenyBlocksUntilExpiryThenAsksAgain", denyBlocks},
		{"DenyRevokesAnApproval", denyRevokes},
		{"ExpiryStartsOver", expiry},
		{"BoundToPlayerToolAndArguments", bindings},
		{"OnlyTheOwnerCanApproveDenyOrList", ownership},
		{"ApproveAndDenyAreIdempotent", idempotent},
		{"SettleIsANoOpWhenNotClaimed", settleNoOp},
		{"ConcurrentGatesClaimExactlyOnce", concurrentClaim},
		{"PendingListsOldestFirst", pendingList},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) { c.fn(t, f) })
	}
}

func gate(t *testing.T, s confirm.Store, p core.Principal, tl, h string) confirm.Gate {
	t.Helper()
	g, err := s.Gate(ctx, p, tl, h)
	if err != nil {
		t.Fatalf("Gate: %v", err)
	}
	return g
}

func want(t *testing.T, g confirm.Gate, st confirm.Status) {
	t.Helper()
	if g.Status != st {
		t.Fatalf("status = %v, want %v (%+v)", g.Status, st, g)
	}
}

func createsPending(t *testing.T, f Factory) {
	s := f(t, newClock().now)
	g1 := gate(t, s, alice, tool, sha)
	want(t, g1, confirm.StatusCreated)
	if g1.ID == "" || g1.ExpiresAt.IsZero() {
		t.Fatalf("created gate needs an id and an expiry: %+v", g1)
	}
	g2 := gate(t, s, alice, tool, sha)
	want(t, g2, confirm.StatusPending)
	if g2.ID != g1.ID {
		t.Fatalf("a repeat must return the same id: %s vs %s", g1.ID, g2.ID)
	}
}

func claimOnceConsume(t *testing.T, f Factory) {
	s := f(t, newClock().now)
	g := gate(t, s, alice, tool, sha)
	if err := s.Approve(ctx, alice, g.ID); err != nil {
		t.Fatal(err)
	}
	c := gate(t, s, alice, tool, sha)
	want(t, c, confirm.StatusClaimed)
	if c.ID != g.ID {
		t.Fatal("the claim is of the same confirmation")
	}
	want(t, gate(t, s, alice, tool, sha), confirm.StatusInUse)
	if err := s.Settle(ctx, c.ID, true); err != nil {
		t.Fatal(err)
	}
	next := gate(t, s, alice, tool, sha)
	want(t, next, confirm.StatusCreated) // single use: a new approval is needed
	if next.ID == g.ID {
		t.Fatal("a consumed confirmation must not come back")
	}
}

func failedReturnsToApproved(t *testing.T, f Factory) {
	s := f(t, newClock().now)
	g := gate(t, s, alice, tool, sha)
	_ = s.Approve(ctx, alice, g.ID)
	c := gate(t, s, alice, tool, sha)
	want(t, c, confirm.StatusClaimed)
	if err := s.Settle(ctx, c.ID, false); err != nil {
		t.Fatal(err)
	}
	again := gate(t, s, alice, tool, sha)
	want(t, again, confirm.StatusClaimed)
	if again.ID != g.ID {
		t.Fatal("the same approval is reused after a failed call")
	}
}

func denyBlocks(t *testing.T, f Factory) {
	c := newClock()
	s := f(t, c.now)
	g := gate(t, s, alice, tool, sha)
	if err := s.Deny(ctx, alice, g.ID); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ { // repeated calls must not nag the player
		want(t, gate(t, s, alice, tool, sha), confirm.StatusDenied)
	}
	if p, _ := s.Pending(ctx, alice); len(p) != 0 {
		t.Fatalf("a denied call must not stay pending: %+v", p)
	}
	c.advance(confirm.TTL + time.Second)
	want(t, gate(t, s, alice, tool, sha), confirm.StatusCreated)
}

func denyRevokes(t *testing.T, f Factory) {
	s := f(t, newClock().now)
	g := gate(t, s, alice, tool, sha)
	_ = s.Approve(ctx, alice, g.ID)
	if err := s.Deny(ctx, alice, g.ID); err != nil {
		t.Fatal(err)
	}
	want(t, gate(t, s, alice, tool, sha), confirm.StatusDenied)
}

func expiry(t *testing.T, f Factory) {
	c := newClock()
	s := f(t, c.now)
	g := gate(t, s, alice, tool, sha)
	_ = s.Approve(ctx, alice, g.ID)
	c.advance(confirm.TTL + time.Second)
	fresh := gate(t, s, alice, tool, sha)
	want(t, fresh, confirm.StatusCreated) // the approval expired unused
	if fresh.ID == g.ID {
		t.Fatal("expired confirmation must not be reused")
	}
	if err := s.Approve(ctx, alice, g.ID); !errors.Is(err, confirm.ErrNotFound) {
		t.Fatalf("approving an expired confirmation: %v", err)
	}
}

func bindings(t *testing.T, f Factory) {
	s := f(t, newClock().now)
	base := gate(t, s, alice, tool, sha)
	_ = s.Approve(ctx, alice, base.ID)

	want(t, gate(t, s, alice, tool, "bbbb"), confirm.StatusCreated)                                          // other arguments
	want(t, gate(t, s, alice, "other_tool", sha), confirm.StatusCreated)                                     // other tool
	want(t, gate(t, s, bob, tool, sha), confirm.StatusCreated)                                               // other player
	want(t, gate(t, s, core.Principal{TenantID: "t2", PlayerID: "alice"}, tool, sha), confirm.StatusCreated) // other tenant
	want(t, gate(t, s, alice, tool, sha), confirm.StatusClaimed)                                             // alice's own approval is intact
}

func ownership(t *testing.T, f Factory) {
	s := f(t, newClock().now)
	g := gate(t, s, alice, tool, sha)
	if err := s.Approve(ctx, bob, g.ID); !errors.Is(err, confirm.ErrNotFound) {
		t.Fatalf("bob approving alice's confirmation: %v", err)
	}
	if err := s.Deny(ctx, bob, g.ID); !errors.Is(err, confirm.ErrNotFound) {
		t.Fatalf("bob denying alice's confirmation: %v", err)
	}
	if err := s.Approve(ctx, alice, "no-such-id"); !errors.Is(err, confirm.ErrNotFound) {
		t.Fatalf("unknown id: %v", err)
	}
	if p, _ := s.Pending(ctx, bob); len(p) != 0 {
		t.Fatalf("bob must not see alice's confirmations: %+v", p)
	}
	want(t, gate(t, s, alice, tool, sha), confirm.StatusPending) // still pending: bob changed nothing
}

func idempotent(t *testing.T, f Factory) {
	s := f(t, newClock().now)
	g := gate(t, s, alice, tool, sha)
	for i := 0; i < 2; i++ {
		if err := s.Approve(ctx, alice, g.ID); err != nil {
			t.Fatalf("approve #%d: %v", i+1, err)
		}
	}
	_ = s.Deny(ctx, alice, g.ID)
	for i := 0; i < 2; i++ {
		if err := s.Deny(ctx, alice, g.ID); err != nil {
			t.Fatalf("deny #%d: %v", i+1, err)
		}
	}
	if err := s.Approve(ctx, alice, g.ID); !errors.Is(err, confirm.ErrState) {
		t.Fatalf("approving a denied confirmation must be refused: %v", err)
	}
}

func settleNoOp(t *testing.T, f Factory) {
	s := f(t, newClock().now)
	g := gate(t, s, alice, tool, sha)
	if err := s.Settle(ctx, g.ID, true); err != nil { // pending, not claimed
		t.Fatal(err)
	}
	want(t, gate(t, s, alice, tool, sha), confirm.StatusPending)
	if err := s.Settle(ctx, "no-such-id", false); err != nil {
		t.Fatal(err)
	}
}

func concurrentClaim(t *testing.T, f Factory) {
	s := f(t, newClock().now)
	g := gate(t, s, alice, tool, sha)
	_ = s.Approve(ctx, alice, g.ID)
	var wg sync.WaitGroup
	var claimed, inUse atomic.Int32
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, err := s.Gate(ctx, alice, tool, sha)
			if err != nil {
				return
			}
			switch r.Status {
			case confirm.StatusClaimed:
				claimed.Add(1)
			case confirm.StatusInUse:
				inUse.Add(1)
			}
		}()
	}
	wg.Wait()
	if claimed.Load() != 1 || inUse.Load() != 49 {
		t.Fatalf("one approval must be claimed exactly once: claimed %d, in use %d", claimed.Load(), inUse.Load())
	}
}

func pendingList(t *testing.T, f Factory) {
	c := newClock()
	s := f(t, c.now)
	a := gate(t, s, alice, "t1", sha)
	c.advance(time.Second)
	b := gate(t, s, alice, "t2", sha)
	gate(t, s, bob, "t3", sha)
	got, err := s.Pending(ctx, alice)
	if err != nil || len(got) != 2 || got[0].ID != a.ID || got[1].ID != b.ID {
		t.Fatalf("pending = %+v, %v", got, err)
	}
	if got[0].Player != "alice" || got[0].State != confirm.StatePending || got[0].Tool != "t1" || got[0].ArgsSHA != sha {
		t.Errorf("fields not populated: %+v", got[0])
	}
}
