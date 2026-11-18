// Package policy decides whether a call may proceed.
//
// The three-step protocol matters for fairness and safety:
//
//	Admit   - runs first and counts EVERY attempt, valid or not (rate limits).
//	Reserve - claims daily caps and the cooldown for a validated call.
//	Settle  - confirms the claim after a successful call, or releases it on failure.
package policy

import (
	"context"
	"sync"
	"time"

	"github.com/Moustafa-Elgammal/agentgw/internal/core"
)

// Reservation is a pending claim against daily caps. Pass it to Settle. The
// fields are the same for every store; a zero Key means nothing was claimed.
type Reservation struct {
	Key      string        // daily usage record the claim was added to
	Day      string        // UTC day of the claim
	Amount   int64         // amount claimed
	Calls    int           // calls claimed
	CDKey    string        // cooldown record, set on Settle(ok)
	Cooldown time.Duration // cooldown started by a successful call
}

// Policy is the interface the gateway depends on. A Redis-backed implementation
// can replace Memory without changing callers.
type Policy interface {
	Admit(ctx context.Context, p core.Principal, t core.Tool) (Decision, error)
	Reserve(ctx context.Context, p core.Principal, t core.Tool, amount int64) (Reservation, Decision, error)
	Settle(ctx context.Context, r Reservation, ok bool) error
}

// Limits are the gateway-wide rate limits per principal.
type Limits struct {
	ActionsPerMinute int
	ReadsPerMinute   int
}

// DefaultLimits mirror the numbers used in the test environment.
var DefaultLimits = Limits{ActionsPerMinute: 30, ReadsPerMinute: 120}

// Memory is an in-process Policy. Good for tests, single-node and local use.
type Memory struct {
	mu       sync.Mutex
	now      func() time.Time
	limits   Limits
	windows  map[string][]time.Time // rate-limit windows, key = principal|kind
	daily    map[string]*usage      // key = principal|tool|day
	cooldown map[string]time.Time   // key = principal|tool -> next allowed time
	switches map[Switch]bool        // kill switches that are on
}

type usage struct {
	calls  int
	amount int64
}

// NewMemory builds a Memory policy. A nil clock uses time.Now.
func NewMemory(l Limits, now func() time.Time) *Memory {
	if now == nil {
		now = time.Now
	}
	return &Memory{
		now: now, limits: l,
		windows:  map[string][]time.Time{},
		daily:    map[string]*usage{},
		cooldown: map[string]time.Time{},
		switches: map[Switch]bool{},
	}
}

func principalKey(p core.Principal) string { return p.TenantID + "/" + p.PlayerID }

// Admit enforces the tier switch, kill switches and the per-minute rate limit.
func (m *Memory) Admit(_ context.Context, p core.Principal, t core.Tool) (Decision, error) {
	if t.Tier == core.TierSensitive {
		return TierDisabled(t.Name), nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	// Kill switches come first and are not charged to the rate limit: a switched-off
	// tool answers cheaply and the same way every time.
	if m.killed(p, t) {
		return SwitchedOff(t.Name), nil
	}
	kind, max := "read", m.limits.ReadsPerMinute
	if t.Tier.IsAction() {
		kind, max = "action", m.limits.ActionsPerMinute
	}
	now := m.now()
	key := principalKey(p) + "|" + kind
	w := m.windows[key]
	cut := 0
	for cut < len(w) && now.Sub(w[cut]) > time.Minute {
		cut++
	}
	w = w[cut:]
	if len(w) >= max {
		m.windows[key] = w
		return RateLimited(kind, max), nil
	}
	m.windows[key] = append(w, now) // counted before any validation
	return Allow(), nil
}

func (m *Memory) dayKey(p core.Principal, t core.Tool, now time.Time) (string, string) {
	day := now.UTC().Format("2006-01-02")
	return principalKey(p) + "|" + t.Name + "|" + day, day
}

// Reserve claims the cooldown and daily caps for a call whose arguments are valid.
func (m *Memory) Reserve(_ context.Context, p core.Principal, t core.Tool, amount int64) (Reservation, Decision, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.killed(p, t) { // switched off between Admit and Reserve
		return Reservation{}, SwitchedOff(t.Name), nil
	}
	now := m.now()
	cdKey := principalKey(p) + "|" + t.Name
	if t.Caps.CooldownSeconds > 0 {
		if until, ok := m.cooldown[cdKey]; ok && now.Before(until) {
			return Reservation{}, Cooldown(t.Name, int(until.Sub(now).Seconds())+1), nil
		}
	}
	key, day := m.dayKey(p, t, now)
	u := m.daily[key]
	if u == nil {
		u = &usage{}
		m.daily[key] = u
	}
	if t.Caps.PerDayCalls > 0 && u.calls+1 > t.Caps.PerDayCalls {
		return Reservation{}, DailyCallCap(t.Name, t.Caps.PerDayCalls), nil
	}
	if t.Caps.PerDayAmount > 0 && u.amount+amount > t.Caps.PerDayAmount {
		return Reservation{}, DailyAmountCap(t.Name, amount, t.Caps.PerDayAmount-u.amount), nil
	}
	u.calls++
	u.amount += amount
	return Reservation{Key: key, Day: day, Amount: amount, Calls: 1, CDKey: cdKey,
		Cooldown: time.Duration(t.Caps.CooldownSeconds) * time.Second}, Allow(), nil
}

// Settle keeps the claim when ok is true and releases it when the call failed.
func (m *Memory) Settle(_ context.Context, r Reservation, ok bool) error {
	if r.Key == "" {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if !ok {
		if u := m.daily[r.Key]; u != nil {
			u.calls -= r.Calls
			u.amount -= r.Amount
		}
		return nil
	}
	if r.Cooldown > 0 {
		m.cooldown[r.CDKey] = m.now().Add(r.Cooldown) // only a successful call starts the cooldown
	}
	return nil
}
