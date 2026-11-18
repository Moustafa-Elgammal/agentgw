package auth

import (
	"context"
	"sort"
	"sync"
	"time"
)

// Memory is an in-process Store for tests and single-process use.
type Memory struct {
	mu      sync.Mutex
	now     func() time.Time
	clients map[string]Client
	codes   map[string]*codeEntry
	grants  map[string]*grantEntry
	access  map[string]tokenEntry
	refresh map[string]*refreshEntry
	tickets map[string]*ticketEntry
}

type ticketEntry struct {
	tenant, player string
	expires        time.Time
	used           bool
}

type codeEntry struct {
	Code
	used bool
}

type grantEntry struct {
	Grant
	revoked bool
}

type tokenEntry struct {
	grantID string
	expires time.Time
}

type refreshEntry struct {
	grantID string
	expires time.Time
	rotated bool
}

// NewMemory builds a Memory store. A nil clock uses time.Now.
func NewMemory(now func() time.Time) *Memory {
	if now == nil {
		now = time.Now
	}
	return &Memory{now: now, clients: map[string]Client{}, codes: map[string]*codeEntry{},
		grants: map[string]*grantEntry{}, access: map[string]tokenEntry{}, refresh: map[string]*refreshEntry{}, tickets: map[string]*ticketEntry{}}
}

// PutTicket implements Store.
func (m *Memory) PutTicket(_ context.Context, hash, tenant, player string, expires time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.tickets[hash] = &ticketEntry{tenant: tenant, player: player, expires: expires}
	return nil
}

// TakeTicket implements Store.
func (m *Memory) TakeTicket(_ context.Context, hash string) (string, string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.tickets[hash]
	if !ok || e.used || !m.now().Before(e.expires) {
		return "", "", ErrNotFound
	}
	e.used = true
	return e.tenant, e.player, nil
}

// RegisterClient implements Store.
func (m *Memory) RegisterClient(_ context.Context, c Client) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if c.CreatedAt.IsZero() {
		c.CreatedAt = m.now()
	}
	m.clients[c.ID] = c
	return nil
}

// Client implements Store.
func (m *Memory) Client(_ context.Context, id string) (Client, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	c, ok := m.clients[id]
	if !ok {
		return Client{}, ErrNotFound
	}
	return c, nil
}

// PutCode implements Store.
func (m *Memory) PutCode(_ context.Context, c Code) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.codes[c.Hash] = &codeEntry{Code: c}
	return nil
}

// TakeCode implements Store.
func (m *Memory) TakeCode(_ context.Context, hash string) (Code, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.codes[hash]
	if !ok || e.used || !m.now().Before(e.Expires) {
		return Code{}, ErrNotFound
	}
	e.used = true
	return e.Code, nil
}

// UpsertGrant implements Store.
func (m *Memory) UpsertGrant(_ context.Context, g Grant) (Grant, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, e := range m.grants {
		if !e.revoked && e.Tenant == g.Tenant && e.Player == g.Player && e.ClientID == g.ClientID && e.Resource == g.Resource {
			e.Scope = g.Scope
			return e.Grant, nil
		}
	}
	if g.ID == "" {
		g.ID = NewID()
	}
	if g.CreatedAt.IsZero() {
		g.CreatedAt = m.now()
	}
	m.grants[g.ID] = &grantEntry{Grant: g}
	return g, nil
}

// Grants implements Store.
func (m *Memory) Grants(_ context.Context, tenant, player string) ([]Grant, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []Grant
	for _, e := range m.grants {
		if !e.revoked && e.Tenant == tenant && e.Player == player {
			out = append(out, e.Grant)
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

// RevokeGrant implements Store.
func (m *Memory) RevokeGrant(_ context.Context, tenant, player, grantID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.grants[grantID]
	if !ok || e.revoked || e.Tenant != tenant || e.Player != player {
		return ErrNotFound
	}
	e.revoked = true
	return nil
}

// PutAccess implements Store.
func (m *Memory) PutAccess(_ context.Context, hash, grantID string, expires time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.access[hash] = tokenEntry{grantID, expires}
	return nil
}

func (m *Memory) liveGrant(id string) (Grant, bool) {
	e, ok := m.grants[id]
	if !ok || e.revoked {
		return Grant{}, false
	}
	return e.Grant, true
}

// Access implements Store.
func (m *Memory) Access(_ context.Context, hash string) (Grant, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	t, ok := m.access[hash]
	if !ok || !m.now().Before(t.expires) {
		return Grant{}, ErrNotFound
	}
	g, ok := m.liveGrant(t.grantID)
	if !ok {
		return Grant{}, ErrNotFound
	}
	return g, nil
}

// PutRefresh implements Store.
func (m *Memory) PutRefresh(_ context.Context, hash, grantID string, expires time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.refresh[hash] = &refreshEntry{grantID: grantID, expires: expires}
	return nil
}

// RotateRefresh implements Store.
func (m *Memory) RotateRefresh(_ context.Context, oldHash, newHash string, newExpires time.Time) (Grant, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.refresh[oldHash]
	if !ok {
		return Grant{}, ErrNotFound
	}
	g, live := m.liveGrant(r.grantID)
	if !live {
		return Grant{}, ErrNotFound
	}
	if r.rotated {
		m.grants[r.grantID].revoked = true // a stolen token was replayed: kill the grant
		return Grant{}, ErrReuse
	}
	if !m.now().Before(r.expires) {
		return Grant{}, ErrNotFound
	}
	r.rotated = true
	m.refresh[newHash] = &refreshEntry{grantID: r.grantID, expires: newExpires}
	return g, nil
}
