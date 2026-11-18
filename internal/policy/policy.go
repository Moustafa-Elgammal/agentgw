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
	"fmt"
	"sync"
	"time"

	"github.com/Moustafa-Elgammal/agentgw/internal/core"
)

// Decision is the outcome of a policy check.
type Decision struct {
	Allowed bool
	Code    string
	Message string
}

func allow() Decision { return Decision{Allowed: true} }

func deny(code, format string, a ...any) Decision {
	return Decision{Code: code, Message: fmt.Sprintf(format, a...)}
}

// Reservation is a pending claim against daily caps. Pass it to Settle.
type Reservation struct {
	key    string
	day    string
	amount int64
	calls  int
	cdKey  string
	cdFor  time.Duration
}

// Policy is the interface the gateway depends on. A Redis-backed implementation
// can replace Memory in M3 without changing callers.
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
	}
}

func principalKey(p core.Principal) string { return p.TenantID + "/" + p.PlayerID }

// Admit enforces the tier switch and the per-minute rate limit.
func (m *Memory) Admit(_ context.Context, p core.Principal, t core.Tool) (Decision, error) {
	if t.Tier == core.TierSensitive {
		return deny("tier_disabled", "%s is a sensitive action and is disabled", t.Name), nil
	}
	kind, max := "read", m.limits.ReadsPerMinute
	if t.Tier.IsAction() {
		kind, max = "action", m.limits.ActionsPerMinute
	}
	m.mu.Lock()
	defer m.mu.Unlock()
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
		return deny("rate_limited", "max %d %ss per minute. Wait and retry.", max, kind), nil
	}
	m.windows[key] = append(w, now) // counted before any validation
	return allow(), nil
}

func (m *Memory) dayKey(p core.Principal, t core.Tool, now time.Time) (string, string) {
	day := now.UTC().Format("2006-01-02")
	return principalKey(p) + "|" + t.Name + "|" + day, day
}

// Reserve claims the cooldown and daily caps for a call whose arguments are valid.
func (m *Memory) Reserve(_ context.Context, p core.Principal, t core.Tool, amount int64) (Reservation, Decision, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now()
	cdKey := principalKey(p) + "|" + t.Name
	if t.Caps.CooldownSeconds > 0 {
		if until, ok := m.cooldown[cdKey]; ok && now.Before(until) {
			return Reservation{}, deny("cooldown", "%s is available again in %d seconds",
				t.Name, int(until.Sub(now).Seconds())+1), nil
		}
	}
	key, day := m.dayKey(p, t, now)
	u := m.daily[key]
	if u == nil {
		u = &usage{}
		m.daily[key] = u
	}
	if t.Caps.PerDayCalls > 0 && u.calls+1 > t.Caps.PerDayCalls {
		return Reservation{}, deny("daily_call_cap", "%s: %d calls allowed per day, all used", t.Name, t.Caps.PerDayCalls), nil
	}
	if t.Caps.PerDayAmount > 0 && u.amount+amount > t.Caps.PerDayAmount {
		return Reservation{}, deny("daily_amount_cap", "%s: %d needed, %d left today",
			t.Name, amount, t.Caps.PerDayAmount-u.amount), nil
	}
	u.calls++
	u.amount += amount
	return Reservation{key: key, day: day, amount: amount, calls: 1, cdKey: cdKey,
		cdFor: time.Duration(t.Caps.CooldownSeconds) * time.Second}, allow(), nil
}

// Settle keeps the claim when ok is true and releases it when the call failed.
func (m *Memory) Settle(_ context.Context, r Reservation, ok bool) error {
	if r.key == "" {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if !ok {
		if u := m.daily[r.key]; u != nil {
			u.calls -= r.calls
			u.amount -= r.amount
		}
		return nil
	}
	if r.cdFor > 0 {
		m.cooldown[r.cdKey] = m.now().Add(r.cdFor) // only a successful call starts the cooldown
	}
	return nil
}
