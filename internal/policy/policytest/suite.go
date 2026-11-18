// Package policytest is the behavior contract every policy store must pass.
// Memory runs it today. The Redis store runs the same suite, so "Redis behaves
// like the spec" is a test, not a promise.
package policytest

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Moustafa-Elgammal/agentgw/internal/core"
	"github.com/Moustafa-Elgammal/agentgw/internal/policy"
)

// Store is what a policy backend must implement.
type Store interface {
	policy.Policy
	policy.Switchboard
}

// Factory builds a fresh, empty store that reads time from now. Each call must
// return a store that shares no state with earlier ones.
type Factory func(t *testing.T, limits policy.Limits, now func() time.Time) Store

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
	player = core.Principal{TenantID: "t", PlayerID: "p1"}
	other  = core.Principal{TenantID: "t", PlayerID: "p2"}
	ctx    = context.Background()
)

var (
	readTool = core.Tool{Name: "state", Tier: core.TierRead}
	actTool  = core.Tool{Name: "build", Tier: core.TierMedium}
	sellTool = core.Tool{Name: "sell", Tier: core.TierMedium, Caps: core.Caps{PerDayAmount: 100, AmountParam: "q"}}
)

// Run executes the whole contract against stores made by f.
func Run(t *testing.T, f Factory) {
	t.Helper()
	cases := []struct {
		name string
		fn   func(t *testing.T, f Factory)
	}{
		{"AdmitCountsEveryAttempt", admitCounts},
		{"ReadsAndActionsHaveSeparateBuckets", separateBuckets},
		{"SensitiveIsDisabled", sensitiveDisabled},
		{"DailyAmountReserveSettle", dailyAmount},
		{"DailyCallCap", dailyCalls},
		{"CapsAreScopedByPlayerAndTool", capScopes},
		{"CooldownStartsOnlyAfterSuccess", cooldown},
		{"KillSwitchScopes", killScopes},
		{"KillSwitchOffRestoresAndListIsStable", killList},
		{"KillSwitchIsNotChargedToRateLimit", killNotCharged},
		{"KillSwitchBlocksReserveAfterAdmit", killReserve},
		{"ConcurrentReservesNeverExceedCap", concurrentCap},
		{"ConcurrentFailuresReleaseEverything", concurrentRelease},
		{"ConcurrentAdmitsNeverExceedRateLimit", concurrentRate},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) { c.fn(t, f) })
	}
}

func admitCounts(t *testing.T, f Factory) {
	c := newClock()
	m := f(t, policy.Limits{ActionsPerMinute: 3, ReadsPerMinute: 5}, c.now)
	for i := 0; i < 3; i++ {
		if d, err := m.Admit(ctx, player, actTool); err != nil || !d.Allowed {
			t.Fatalf("attempt %d should pass: %+v %v", i, d, err)
		}
	}
	d, _ := m.Admit(ctx, player, actTool)
	if d.Allowed || d.Code != "rate_limited" {
		t.Fatalf("4th attempt must be rate limited, got %+v", d)
	}
	if d, _ := m.Admit(ctx, other, actTool); !d.Allowed {
		t.Fatal("limits are per player")
	}
	c.advance(61 * time.Second)
	if d, _ := m.Admit(ctx, player, actTool); !d.Allowed {
		t.Fatal("window should have slid")
	}
}

func separateBuckets(t *testing.T, f Factory) {
	m := f(t, policy.Limits{ActionsPerMinute: 1, ReadsPerMinute: 2}, newClock().now)
	_, _ = m.Admit(ctx, player, actTool)
	if d, _ := m.Admit(ctx, player, readTool); !d.Allowed {
		t.Fatal("reads must not share the action bucket")
	}
}

func sensitiveDisabled(t *testing.T, f Factory) {
	m := f(t, policy.DefaultLimits, newClock().now)
	d, _ := m.Admit(ctx, player, core.Tool{Name: "gift", Tier: core.TierSensitive})
	if d.Allowed || d.Code != "tier_disabled" {
		t.Fatalf("got %+v", d)
	}
}

func dailyAmount(t *testing.T, f Factory) {
	c := newClock()
	m := f(t, policy.DefaultLimits, c.now)
	r1, d, _ := m.Reserve(ctx, player, sellTool, 60)
	if !d.Allowed {
		t.Fatal("first reserve should pass")
	}
	if _, d, _ := m.Reserve(ctx, player, sellTool, 60); d.Allowed || d.Code != "daily_amount_cap" {
		t.Fatalf("second reserve must hit the cap, got %+v", d)
	}
	_ = m.Settle(ctx, r1, false) // a failed call releases its claim
	r2, d, _ := m.Reserve(ctx, player, sellTool, 60)
	if !d.Allowed {
		t.Fatal("released amount must be reusable")
	}
	_ = m.Settle(ctx, r2, true)
	if _, d, _ := m.Reserve(ctx, player, sellTool, 60); d.Allowed {
		t.Fatal("settled amount must stay claimed")
	}
	c.advance(24 * time.Hour)
	if _, d, _ := m.Reserve(ctx, player, sellTool, 60); !d.Allowed {
		t.Fatal("cap resets on the next UTC day")
	}
}

func dailyCalls(t *testing.T, f Factory) {
	m := f(t, policy.DefaultLimits, newClock().now)
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

func capScopes(t *testing.T, f Factory) {
	m := f(t, policy.DefaultLimits, newClock().now)
	r, d, _ := m.Reserve(ctx, player, sellTool, 100)
	if !d.Allowed {
		t.Fatal("full cap should be claimable")
	}
	_ = m.Settle(ctx, r, true)
	if _, d, _ := m.Reserve(ctx, other, sellTool, 100); !d.Allowed {
		t.Fatal("another player has their own cap")
	}
	otherTenant := core.Principal{TenantID: "t2", PlayerID: "p1"}
	if _, d, _ := m.Reserve(ctx, otherTenant, sellTool, 100); !d.Allowed {
		t.Fatal("another tenant has its own cap")
	}
	second := sellTool
	second.Name = "sell_more"
	if _, d, _ := m.Reserve(ctx, player, second, 100); !d.Allowed {
		t.Fatal("another tool has its own cap")
	}
}

func cooldown(t *testing.T, f Factory) {
	c := newClock()
	m := f(t, policy.DefaultLimits, c.now)
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

func killScopes(t *testing.T, f Factory) {
	buy := core.Tool{Name: "buy", Tier: core.TierMedium}
	state := core.Tool{Name: "state", Tier: core.TierRead}
	tests := []struct {
		name        string
		sw          policy.Switch
		who         core.Principal
		tool        core.Tool
		wantBlocked bool
	}{
		{"global blocks reads", policy.Switch{}, player, state, true},
		{"global blocks actions", policy.Switch{}, player, buy, true},
		{"tenant matches", policy.Switch{Tenant: "t"}, player, buy, true},
		{"other tenant untouched", policy.Switch{Tenant: "x"}, player, buy, false},
		{"tool matches", policy.Switch{Tenant: "t", Tool: "buy"}, player, buy, true},
		{"tool switch spares reads", policy.Switch{Tenant: "t", Tool: "buy"}, player, state, false},
		{"player matches", policy.Switch{Tenant: "t", Player: "p1"}, player, buy, true},
		{"player switch spares others", policy.Switch{Tenant: "t", Player: "p1"}, other, buy, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m := f(t, policy.DefaultLimits, newClock().now)
			if err := m.SetSwitch(ctx, tc.sw, true); err != nil {
				t.Fatal(err)
			}
			d, err := m.Admit(ctx, tc.who, tc.tool)
			if err != nil {
				t.Fatal(err)
			}
			if d.Allowed == tc.wantBlocked {
				t.Fatalf("allowed = %v, want blocked = %v (%+v)", d.Allowed, tc.wantBlocked, d)
			}
			if tc.wantBlocked && d.Code != "switched_off" {
				t.Errorf("code = %q, want switched_off", d.Code)
			}
		})
	}
}

func killList(t *testing.T, f Factory) {
	m := f(t, policy.DefaultLimits, newClock().now)
	tool := core.Tool{Name: "buy", Tier: core.TierMedium}
	_ = m.SetSwitch(ctx, policy.Switch{Tenant: "t", Tool: "buy"}, true)
	_ = m.SetSwitch(ctx, policy.Switch{Tenant: "a"}, true)
	_ = m.SetSwitch(ctx, policy.Switch{Tenant: "a"}, true) // idempotent
	got, err := m.Switches(ctx)
	if err != nil || len(got) != 2 || got[0].Tenant != "a" || got[1].Tool != "buy" {
		t.Fatalf("switches = %+v, %v", got, err)
	}
	_ = m.SetSwitch(ctx, policy.Switch{Tenant: "t", Tool: "buy"}, false)
	if d, _ := m.Admit(ctx, player, tool); !d.Allowed {
		t.Fatalf("switch turned off, call must pass: %+v", d)
	}
}

func killNotCharged(t *testing.T, f Factory) {
	m := f(t, policy.Limits{ActionsPerMinute: 1, ReadsPerMinute: 1}, newClock().now)
	tool := core.Tool{Name: "buy", Tier: core.TierMedium}
	_ = m.SetSwitch(ctx, policy.Switch{Tool: "buy"}, true)
	for i := 0; i < 5; i++ {
		if d, _ := m.Admit(ctx, player, tool); d.Code != "switched_off" {
			t.Fatalf("attempt %d: %+v", i, d)
		}
	}
	_ = m.SetSwitch(ctx, policy.Switch{Tool: "buy"}, false)
	if d, _ := m.Admit(ctx, player, tool); !d.Allowed {
		t.Fatalf("blocked attempts must not have used the budget: %+v", d)
	}
}

func killReserve(t *testing.T, f Factory) {
	m := f(t, policy.DefaultLimits, newClock().now)
	tool := core.Tool{Name: "buy", Tier: core.TierMedium, Caps: core.Caps{PerDayCalls: 5}}
	if d, _ := m.Admit(ctx, player, tool); !d.Allowed {
		t.Fatal("admit should pass")
	}
	_ = m.SetSwitch(ctx, policy.Switch{Tool: "buy"}, true)
	if _, d, _ := m.Reserve(ctx, player, tool, 0); d.Allowed || d.Code != "switched_off" {
		t.Fatalf("reserve must re-check the switch: %+v", d)
	}
}

func concurrentCap(t *testing.T, f Factory) {
	m := f(t, policy.DefaultLimits, newClock().now)
	var wg sync.WaitGroup
	var granted atomic.Int64
	for i := 0; i < 200; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, d, _ := m.Reserve(ctx, player, sellTool, 7)
			if d.Allowed {
				granted.Add(7)
				_ = m.Settle(ctx, r, true)
			}
		}()
	}
	wg.Wait()
	// 100 / 7 = 14 whole claims fit: exactly 98, never more, and never fewer.
	if granted.Load() != 98 {
		t.Fatalf("granted %d, want exactly 98 under a cap of 100", granted.Load())
	}
}

func concurrentRelease(t *testing.T, f Factory) {
	m := f(t, policy.DefaultLimits, newClock().now)
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if r, d, _ := m.Reserve(ctx, player, sellTool, 10); d.Allowed {
				_ = m.Settle(ctx, r, false) // every call fails
			}
		}()
	}
	wg.Wait()
	if _, d, _ := m.Reserve(ctx, player, sellTool, 100); !d.Allowed {
		t.Fatalf("all claims were released, the full cap must be free: %+v", d)
	}
}

func concurrentRate(t *testing.T, f Factory) {
	m := f(t, policy.Limits{ActionsPerMinute: 25, ReadsPerMinute: 25}, newClock().now)
	var wg sync.WaitGroup
	var allowed atomic.Int64
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if d, _ := m.Admit(ctx, player, actTool); d.Allowed {
				allowed.Add(1)
			}
		}()
	}
	wg.Wait()
	if allowed.Load() != 25 {
		t.Fatalf("allowed %d, want exactly 25", allowed.Load())
	}
}
