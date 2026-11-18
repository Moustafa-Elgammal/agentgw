package policy

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Moustafa-Elgammal/agentgw/internal/core"
)

type clock struct{ t time.Time }

func (c *clock) now() time.Time          { return c.t }
func (c *clock) advance(d time.Duration) { c.t = c.t.Add(d) }

var (
	player = core.Principal{TenantID: "t", PlayerID: "p1"}
	other  = core.Principal{TenantID: "t", PlayerID: "p2"}
	ctx    = context.Background()
)

func newClock() *clock { return &clock{t: time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)} }

func TestAdmitCountsEveryAttempt(t *testing.T) {
	c := newClock()
	m := NewMemory(Limits{ActionsPerMinute: 3, ReadsPerMinute: 5}, c.now)
	tool := core.Tool{Name: "sell", Tier: core.TierMedium}
	for i := 0; i < 3; i++ {
		d, _ := m.Admit(ctx, player, tool)
		if !d.Allowed {
			t.Fatalf("attempt %d should pass", i)
		}
	}
	d, _ := m.Admit(ctx, player, tool)
	if d.Allowed || d.Code != "rate_limited" {
		t.Fatalf("4th attempt must be rate limited, got %+v", d)
	}
	if d, _ := m.Admit(ctx, other, tool); !d.Allowed {
		t.Fatal("limits are per player")
	}
	c.advance(61 * time.Second)
	if d, _ := m.Admit(ctx, player, tool); !d.Allowed {
		t.Fatal("window should have slid")
	}
}

func TestReadsAndActionsHaveSeparateBuckets(t *testing.T) {
	m := NewMemory(Limits{ActionsPerMinute: 1, ReadsPerMinute: 2}, newClock().now)
	read := core.Tool{Name: "state", Tier: core.TierRead}
	act := core.Tool{Name: "build", Tier: core.TierMedium}
	_, _ = m.Admit(ctx, player, act)
	if d, _ := m.Admit(ctx, player, read); !d.Allowed {
		t.Fatal("reads must not share the action bucket")
	}
}

func TestSensitiveIsDisabled(t *testing.T) {
	m := NewMemory(DefaultLimits, nil)
	d, _ := m.Admit(ctx, player, core.Tool{Name: "gift", Tier: core.TierSensitive})
	if d.Allowed || d.Code != "tier_disabled" {
		t.Fatalf("got %+v", d)
	}
}

func TestDailyAmountReserveSettle(t *testing.T) {
	c := newClock()
	m := NewMemory(DefaultLimits, c.now)
	tool := core.Tool{Name: "sell", Tier: core.TierMedium, Caps: core.Caps{PerDayAmount: 100, AmountParam: "q"}}

	r1, d, _ := m.Reserve(ctx, player, tool, 60)
	if !d.Allowed {
		t.Fatal("first reserve should pass")
	}
	if _, d, _ := m.Reserve(ctx, player, tool, 60); d.Allowed || d.Code != "daily_amount_cap" {
		t.Fatalf("second reserve must hit the cap, got %+v", d)
	}
	// A failed call releases its claim, so the amount is available again.
	_ = m.Settle(ctx, r1, false)
	r2, d, _ := m.Reserve(ctx, player, tool, 60)
	if !d.Allowed {
		t.Fatal("released amount must be reusable")
	}
	_ = m.Settle(ctx, r2, true)
	if _, d, _ := m.Reserve(ctx, player, tool, 60); d.Allowed {
		t.Fatal("settled amount must stay claimed")
	}
	c.advance(24 * time.Hour)
	if _, d, _ := m.Reserve(ctx, player, tool, 60); !d.Allowed {
		t.Fatal("cap resets on the next UTC day")
	}
}

func TestDailyCallCap(t *testing.T) {
	m := NewMemory(DefaultLimits, newClock().now)
	tool := core.Tool{Name: "collect", Tier: core.TierSafe, Caps: core.Caps{PerDayCalls: 2}}
	for i := 0; i < 2; i++ {
		r, d, _ := m.Reserve(ctx, player, tool, 0)
		if !d.Allowed {
			t.Fatal("within cap")
		}
		_ = m.Settle(ctx, r, true)
	}
	if _, d, _ := m.Reserve(ctx, player, tool, 0); d.Code != "daily_call_cap" {
		t.Fatalf("got %+v", d)
	}
}

func TestCooldownStartsOnlyAfterSuccess(t *testing.T) {
	c := newClock()
	m := NewMemory(DefaultLimits, c.now)
	tool := core.Tool{Name: "collect", Tier: core.TierSafe, Caps: core.Caps{CooldownSeconds: 30}}

	r, _, _ := m.Reserve(ctx, player, tool, 0)
	_ = m.Settle(ctx, r, false)
	if _, d, _ := m.Reserve(ctx, player, tool, 0); !d.Allowed {
		t.Fatal("failed call must not start the cooldown")
	}
	r, _, _ = m.Reserve(ctx, player, tool, 0)
	_ = m.Settle(ctx, r, true)
	if _, d, _ := m.Reserve(ctx, player, tool, 0); d.Code != "cooldown" {
		t.Fatalf("expected cooldown, got %+v", d)
	}
	c.advance(31 * time.Second)
	if _, d, _ := m.Reserve(ctx, player, tool, 0); !d.Allowed {
		t.Fatal("cooldown should have ended")
	}
}

func TestConcurrentReservesNeverExceedCap(t *testing.T) {
	m := NewMemory(DefaultLimits, newClock().now)
	tool := core.Tool{Name: "sell", Tier: core.TierMedium, Caps: core.Caps{PerDayAmount: 100, AmountParam: "q"}}

	var wg sync.WaitGroup
	var granted atomic.Int64
	for i := 0; i < 200; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, d, _ := m.Reserve(ctx, player, tool, 7)
			if d.Allowed {
				granted.Add(7)
				_ = m.Settle(ctx, r, true)
			}
		}()
	}
	wg.Wait()
	if granted.Load() > 100 {
		t.Fatalf("granted %d, cap is 100", granted.Load())
	}
	if granted.Load() != 98 { // 14 reservations of 7 fit in 100
		t.Fatalf("granted %d, want 98", granted.Load())
	}
}
