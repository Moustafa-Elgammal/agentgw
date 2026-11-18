// Package authtest is the behavior contract every auth store must pass.
package authtest

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Moustafa-Elgammal/agentgw/internal/auth"
)

// Factory builds a fresh, empty store that reads time from now.
type Factory func(t *testing.T, now func() time.Time) auth.Store

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

var ctx = context.Background()

func grant(player, client, resource string, scope auth.Scope) auth.Grant {
	return auth.Grant{Tenant: "t", Player: player, ClientID: client, Resource: resource, Scope: scope}
}

// Run executes the whole contract against stores made by f.
func Run(t *testing.T, f Factory) {
	t.Helper()
	cases := []struct {
		name string
		fn   func(t *testing.T, f Factory)
	}{
		{"ClientsRegisterAndLookUp", clients},
		{"CodeIsSingleUse", codeSingleUse},
		{"CodeExpires", codeExpires},
		{"ConcurrentTakeCodeHasOneWinner", codeConcurrent},
		{"UpsertGrantReusesTheActiveGrant", grantUpsert},
		{"GrantsAreScopedAndListedOldestFirst", grantList},
		{"RevokeIsOwnerOnlyAndKillsTokens", grantRevoke},
		{"AccessTokenLookupAndExpiry", accessLookup},
		{"RefreshRotates", refreshRotates},
		{"RefreshReuseRevokesTheGrant", refreshReuse},
		{"RefreshExpiresAndRespectsRevocation", refreshExpiry},
		{"ConcurrentRotationHasOneWinner", refreshConcurrent},
		{"ARevokedGrantStartsFresh", regrantAfterRevoke},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) { c.fn(t, f) })
	}
}

func mustNotFound(t *testing.T, err error, what string) {
	t.Helper()
	if !errors.Is(err, auth.ErrNotFound) {
		t.Fatalf("%s: want ErrNotFound, got %v", what, err)
	}
}

func clients(t *testing.T, f Factory) {
	s := f(t, newClock().now)
	want := auth.Client{ID: "c1", Name: "Agent", RedirectURIs: []string{"http://127.0.0.1/cb", "https://app.example/cb"}}
	if err := s.RegisterClient(ctx, want); err != nil {
		t.Fatal(err)
	}
	got, err := s.Client(ctx, "c1")
	if err != nil || got.Name != "Agent" || len(got.RedirectURIs) != 2 || got.RedirectURIs[1] != "https://app.example/cb" {
		t.Fatalf("client = %+v, %v", got, err)
	}
	_, err = s.Client(ctx, "nope")
	mustNotFound(t, err, "unknown client")
}

func code(c *clock, hash string) auth.Code {
	return auth.Code{Hash: hash, ClientID: "c1", RedirectURI: "http://127.0.0.1/cb", Challenge: "chal",
		Resource: "https://gw/mcp", Scope: auth.ScopeOperator, Tenant: "t", Player: "alice", Expires: c.now().Add(auth.CodeTTL)}
}

func codeSingleUse(t *testing.T, f Factory) {
	c := newClock()
	s := f(t, c.now)
	if err := s.PutCode(ctx, code(c, "h1")); err != nil {
		t.Fatal(err)
	}
	got, err := s.TakeCode(ctx, "h1")
	if err != nil || got.Player != "alice" || got.Challenge != "chal" || got.Scope != auth.ScopeOperator || got.Resource != "https://gw/mcp" {
		t.Fatalf("take = %+v, %v", got, err)
	}
	_, err = s.TakeCode(ctx, "h1")
	mustNotFound(t, err, "second take")
	_, err = s.TakeCode(ctx, "unknown")
	mustNotFound(t, err, "unknown code")
}

func codeExpires(t *testing.T, f Factory) {
	c := newClock()
	s := f(t, c.now)
	_ = s.PutCode(ctx, code(c, "h1"))
	c.advance(auth.CodeTTL + time.Second)
	_, err := s.TakeCode(ctx, "h1")
	mustNotFound(t, err, "expired code")
}

func codeConcurrent(t *testing.T, f Factory) {
	c := newClock()
	s := f(t, c.now)
	_ = s.PutCode(ctx, code(c, "h1"))
	var wg sync.WaitGroup
	var won atomic.Int32
	for i := 0; i < 30; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := s.TakeCode(ctx, "h1"); err == nil {
				won.Add(1)
			}
		}()
	}
	wg.Wait()
	if won.Load() != 1 {
		t.Fatalf("%d callers took the same code, want exactly 1", won.Load())
	}
}

func grantUpsert(t *testing.T, f Factory) {
	s := f(t, newClock().now)
	g1, err := s.UpsertGrant(ctx, grant("alice", "c1", "https://gw/mcp", auth.ScopeCoach))
	if err != nil || g1.ID == "" {
		t.Fatalf("upsert = %+v, %v", g1, err)
	}
	g2, err := s.UpsertGrant(ctx, grant("alice", "c1", "https://gw/mcp", auth.ScopeOperator))
	if err != nil || g2.ID != g1.ID || g2.Scope != auth.ScopeOperator {
		t.Fatalf("consenting again must update the same grant: %+v, %v", g2, err)
	}
	list, _ := s.Grants(ctx, "t", "alice")
	if len(list) != 1 || list[0].Scope != auth.ScopeOperator {
		t.Fatalf("grants = %+v", list)
	}
	other, _ := s.UpsertGrant(ctx, grant("alice", "c2", "https://gw/mcp", auth.ScopeCoach))
	if other.ID == g1.ID {
		t.Fatal("another client gets its own grant")
	}
	other2, _ := s.UpsertGrant(ctx, grant("alice", "c1", "https://other/mcp", auth.ScopeCoach))
	if other2.ID == g1.ID {
		t.Fatal("another resource gets its own grant")
	}
}

func grantList(t *testing.T, f Factory) {
	c := newClock()
	s := f(t, c.now)
	a, _ := s.UpsertGrant(ctx, grant("alice", "c1", "r", auth.ScopeCoach))
	c.advance(time.Second)
	b, _ := s.UpsertGrant(ctx, grant("alice", "c2", "r", auth.ScopeCoach))
	_, _ = s.UpsertGrant(ctx, grant("bob", "c1", "r", auth.ScopeCoach))
	list, err := s.Grants(ctx, "t", "alice")
	if err != nil || len(list) != 2 || list[0].ID != a.ID || list[1].ID != b.ID {
		t.Fatalf("grants = %+v, %v", list, err)
	}
	if list, _ := s.Grants(ctx, "other-tenant", "alice"); len(list) != 0 {
		t.Fatalf("tenants are separate: %+v", list)
	}
}

func grantRevoke(t *testing.T, f Factory) {
	c := newClock()
	s := f(t, c.now)
	g, _ := s.UpsertGrant(ctx, grant("alice", "c1", "r", auth.ScopeOperator))
	_ = s.PutAccess(ctx, "acc", g.ID, c.now().Add(auth.AccessTTL))
	_ = s.PutRefresh(ctx, "ref", g.ID, c.now().Add(auth.RefreshTTL))

	mustNotFound(t, s.RevokeGrant(ctx, "t", "bob", g.ID), "bob revoking alice's grant")
	mustNotFound(t, s.RevokeGrant(ctx, "t", "alice", "no-such"), "unknown grant")
	if _, err := s.Access(ctx, "acc"); err != nil {
		t.Fatalf("a failed revoke must change nothing: %v", err)
	}
	if err := s.RevokeGrant(ctx, "t", "alice", g.ID); err != nil {
		t.Fatal(err)
	}
	_, err := s.Access(ctx, "acc")
	mustNotFound(t, err, "access after revoke")
	_, err = s.RotateRefresh(ctx, "ref", "ref2", c.now().Add(auth.RefreshTTL))
	mustNotFound(t, err, "refresh after revoke")
	mustNotFound(t, s.RevokeGrant(ctx, "t", "alice", g.ID), "revoking twice")
	if list, _ := s.Grants(ctx, "t", "alice"); len(list) != 0 {
		t.Fatalf("revoked grants are not listed: %+v", list)
	}
}

func accessLookup(t *testing.T, f Factory) {
	c := newClock()
	s := f(t, c.now)
	g, _ := s.UpsertGrant(ctx, grant("alice", "c1", "https://gw/mcp", auth.ScopeCoach))
	_ = s.PutAccess(ctx, "acc", g.ID, c.now().Add(auth.AccessTTL))
	got, err := s.Access(ctx, "acc")
	if err != nil || got.ID != g.ID || got.Player != "alice" || got.Scope != auth.ScopeCoach || got.Resource != "https://gw/mcp" || got.ClientID != "c1" {
		t.Fatalf("access = %+v, %v", got, err)
	}
	_, err = s.Access(ctx, "unknown")
	mustNotFound(t, err, "unknown token")
	c.advance(auth.AccessTTL + time.Second)
	_, err = s.Access(ctx, "acc")
	mustNotFound(t, err, "expired token")
}

func refreshRotates(t *testing.T, f Factory) {
	c := newClock()
	s := f(t, c.now)
	g, _ := s.UpsertGrant(ctx, grant("alice", "c1", "r", auth.ScopeOperator))
	_ = s.PutRefresh(ctx, "r1", g.ID, c.now().Add(auth.RefreshTTL))
	got, err := s.RotateRefresh(ctx, "r1", "r2", c.now().Add(auth.RefreshTTL))
	if err != nil || got.ID != g.ID {
		t.Fatalf("rotate = %+v, %v", got, err)
	}
	if _, err := s.RotateRefresh(ctx, "r2", "r3", c.now().Add(auth.RefreshTTL)); err != nil {
		t.Fatalf("the new token must rotate again: %v", err)
	}
	_, err = s.RotateRefresh(ctx, "unknown", "x", c.now().Add(auth.RefreshTTL))
	mustNotFound(t, err, "unknown refresh token")
}

func refreshReuse(t *testing.T, f Factory) {
	c := newClock()
	s := f(t, c.now)
	g, _ := s.UpsertGrant(ctx, grant("alice", "c1", "r", auth.ScopeOperator))
	_ = s.PutAccess(ctx, "acc", g.ID, c.now().Add(auth.AccessTTL))
	_ = s.PutRefresh(ctx, "r1", g.ID, c.now().Add(auth.RefreshTTL))
	_, _ = s.RotateRefresh(ctx, "r1", "r2", c.now().Add(auth.RefreshTTL))

	// An attacker replays the old token. The grant dies, including the legitimate new tokens.
	if _, err := s.RotateRefresh(ctx, "r1", "evil", c.now().Add(auth.RefreshTTL)); !errors.Is(err, auth.ErrReuse) {
		t.Fatalf("want ErrReuse, got %v", err)
	}
	_, err := s.RotateRefresh(ctx, "r2", "r3", c.now().Add(auth.RefreshTTL))
	mustNotFound(t, err, "legitimate refresh after reuse")
	_, err = s.Access(ctx, "acc")
	mustNotFound(t, err, "access after reuse")
}

func refreshExpiry(t *testing.T, f Factory) {
	c := newClock()
	s := f(t, c.now)
	g, _ := s.UpsertGrant(ctx, grant("alice", "c1", "r", auth.ScopeOperator))
	_ = s.PutRefresh(ctx, "r1", g.ID, c.now().Add(auth.RefreshTTL))
	c.advance(auth.RefreshTTL + time.Second)
	_, err := s.RotateRefresh(ctx, "r1", "r2", c.now().Add(auth.RefreshTTL))
	mustNotFound(t, err, "expired refresh token")
}

func refreshConcurrent(t *testing.T, f Factory) {
	c := newClock()
	s := f(t, c.now)
	g, _ := s.UpsertGrant(ctx, grant("alice", "c1", "r", auth.ScopeOperator))
	_ = s.PutRefresh(ctx, "r1", g.ID, c.now().Add(auth.RefreshTTL))
	var wg sync.WaitGroup
	var ok, reuse atomic.Int32
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := s.RotateRefresh(ctx, "r1", fmt.Sprintf("new-%d", i), c.now().Add(auth.RefreshTTL))
			switch {
			case err == nil:
				ok.Add(1)
			case errors.Is(err, auth.ErrReuse):
				reuse.Add(1)
			}
		}(i)
	}
	wg.Wait()
	if ok.Load() != 1 {
		t.Fatalf("%d rotations succeeded, want exactly 1 (reuse results: %d)", ok.Load(), reuse.Load())
	}
}

func regrantAfterRevoke(t *testing.T, f Factory) {
	s := f(t, newClock().now)
	g1, _ := s.UpsertGrant(ctx, grant("alice", "c1", "r", auth.ScopeOperator))
	_ = s.RevokeGrant(ctx, "t", "alice", g1.ID)
	g2, err := s.UpsertGrant(ctx, grant("alice", "c1", "r", auth.ScopeCoach))
	if err != nil || g2.ID == g1.ID || g2.Scope != auth.ScopeCoach {
		t.Fatalf("a revoked grant must not come back: %+v vs %+v, %v", g2, g1, err)
	}
}
