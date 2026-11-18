package confirm

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"sort"
	"sync"
	"time"

	"github.com/Moustafa-Elgammal/agentgw/internal/core"
)

// Memory is an in-process Store for tests and single-process use.
type Memory struct {
	mu   sync.Mutex
	now  func() time.Time
	byID map[string]*Confirmation
}

// NewMemory builds a Memory store. A nil clock uses time.Now.
func NewMemory(now func() time.Time) *Memory {
	if now == nil {
		now = time.Now
	}
	return &Memory{now: now, byID: map[string]*Confirmation{}}
}

// NewID returns a random confirmation id. Stores share it.
func NewID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func (m *Memory) owns(c *Confirmation, p core.Principal) bool {
	return c.Tenant == p.TenantID && c.Player == p.PlayerID
}

func (m *Memory) live(c *Confirmation) bool { return m.now().Before(c.ExpiresAt) }

// Gate implements Store.
func (m *Memory) Gate(_ context.Context, p core.Principal, tool, argsSHA string) (Gate, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var match *Confirmation
	for _, c := range m.byID {
		if !m.owns(c, p) || c.Tool != tool || c.ArgsSHA != argsSHA || !m.live(c) || c.State == StateConsumed {
			continue
		}
		// Prefer the state that lets the call move furthest: approved, then claimed, pending, denied.
		if match == nil || rank(c.State) > rank(match.State) {
			match = c
		}
	}
	if match != nil {
		g := Gate{ID: match.ID, ExpiresAt: match.ExpiresAt}
		switch match.State {
		case StateApproved:
			match.State = StateClaimed
			g.Status = StatusClaimed
		case StateClaimed:
			g.Status = StatusInUse
		case StateDenied:
			g.Status = StatusDenied
		default:
			g.Status = StatusPending
		}
		return g, nil
	}
	now := m.now()
	c := &Confirmation{ID: NewID(), Tenant: p.TenantID, Player: p.PlayerID, Tool: tool, ArgsSHA: argsSHA,
		State: StatePending, CreatedAt: now, ExpiresAt: now.Add(TTL)}
	m.byID[c.ID] = c
	return Gate{Status: StatusCreated, ID: c.ID, ExpiresAt: c.ExpiresAt}, nil
}

func rank(s State) int {
	switch s {
	case StateApproved:
		return 4
	case StateClaimed:
		return 3
	case StatePending:
		return 2
	case StateDenied:
		return 1
	}
	return 0
}

// Settle implements Store.
func (m *Memory) Settle(_ context.Context, id string, ok bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	c := m.byID[id]
	if c == nil || c.State != StateClaimed {
		return nil
	}
	if ok {
		c.State = StateConsumed
	} else {
		c.State = StateApproved
	}
	return nil
}

func (m *Memory) find(p core.Principal, id string) *Confirmation {
	c := m.byID[id]
	if c == nil || !m.owns(c, p) || !m.live(c) {
		return nil
	}
	return c
}

// Approve implements Store.
func (m *Memory) Approve(_ context.Context, p core.Principal, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	c := m.find(p, id)
	if c == nil {
		return ErrNotFound
	}
	switch c.State {
	case StatePending:
		c.State = StateApproved
		return nil
	case StateApproved:
		return nil
	}
	return ErrState
}

// Deny implements Store.
func (m *Memory) Deny(_ context.Context, p core.Principal, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	c := m.find(p, id)
	if c == nil {
		return ErrNotFound
	}
	switch c.State {
	case StatePending, StateApproved:
		c.State = StateDenied
		return nil
	case StateDenied:
		return nil
	}
	return ErrState
}

// Pending implements Store.
func (m *Memory) Pending(_ context.Context, p core.Principal) ([]Confirmation, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []Confirmation
	for _, c := range m.byID {
		if m.owns(c, p) && c.State == StatePending && m.live(c) {
			out = append(out, *c)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].CreatedAt.Before(out[j].CreatedAt)
		}
		return out[i].ID < out[j].ID
	})
	return out, nil
}
