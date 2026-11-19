// Package manifeststore keeps every uploaded manifest version, remembers which one is
// active, and applies the active one to a running gateway. A studio uploads a manifest
// and rolls back through studio-signed endpoints (see Handler), so onboarding needs no
// backend code and no restart.
package manifeststore

import (
	"context"
	"errors"
	"sort"
	"sync"
	"time"
)

// ErrNotFound means the version (or an active version) does not exist for the tenant.
var ErrNotFound = errors.New("manifeststore: not found")

// Version describes one stored manifest.
type Version struct {
	ID        int64     `json:"id"`
	SHA256    string    `json:"sha256"` // of the canonical JSON document
	Actor     string    `json:"actor"`  // who uploaded it (the signed X-AG-Player of the admin call)
	CreatedAt time.Time `json:"created_at"`
	Tools     int       `json:"tools"`
	Active    bool      `json:"active"`
}

// Store persists manifest versions per tenant. Documents are canonical JSON that the
// caller already validated. Implementations must pass storetest.Run.
type Store interface {
	// Put stores a new version and returns it. It does not activate it.
	Put(ctx context.Context, tenant, actor string, doc []byte, tools int) (Version, error)
	// Activate makes an existing version the active one. ErrNotFound if it does not exist.
	Activate(ctx context.Context, tenant string, id int64) error
	// Active returns the active version and its document. ErrNotFound if none is active.
	Active(ctx context.Context, tenant string) (Version, []byte, error)
	// Get returns one version and its document. ErrNotFound if it does not exist.
	Get(ctx context.Context, tenant string, id int64) (Version, []byte, error)
	// List returns the tenant's versions, newest first.
	List(ctx context.Context, tenant string) ([]Version, error)
}

type memEntry struct {
	v   Version
	doc []byte
}

// Memory is an in-process Store: tests and single-process dev. Versions are lost on restart.
type Memory struct {
	now    func() time.Time
	mu     sync.Mutex
	next   int64
	rows   map[string][]memEntry // tenant -> versions in id order
	active map[string]int64
}

// NewMemory builds a Memory store. A nil clock uses time.Now.
func NewMemory(now func() time.Time) *Memory {
	if now == nil {
		now = time.Now
	}
	return &Memory{now: now, rows: map[string][]memEntry{}, active: map[string]int64{}}
}

// Put implements Store.
func (m *Memory) Put(_ context.Context, tenant, actor string, doc []byte, tools int) (Version, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.next++
	v := Version{ID: m.next, SHA256: hashDoc(doc), Actor: actor, CreatedAt: m.now().UTC(), Tools: tools}
	m.rows[tenant] = append(m.rows[tenant], memEntry{v: v, doc: append([]byte(nil), doc...)})
	return v, nil
}

func (m *Memory) find(tenant string, id int64) (memEntry, bool) {
	for _, e := range m.rows[tenant] {
		if e.v.ID == id {
			return e, true
		}
	}
	return memEntry{}, false
}

// Activate implements Store.
func (m *Memory) Activate(_ context.Context, tenant string, id int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.find(tenant, id); !ok {
		return ErrNotFound
	}
	m.active[tenant] = id
	return nil
}

// Active implements Store.
func (m *Memory) Active(ctx context.Context, tenant string) (Version, []byte, error) {
	m.mu.Lock()
	id, ok := m.active[tenant]
	m.mu.Unlock()
	if !ok {
		return Version{}, nil, ErrNotFound
	}
	return m.Get(ctx, tenant, id)
}

// Get implements Store.
func (m *Memory) Get(_ context.Context, tenant string, id int64) (Version, []byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.find(tenant, id)
	if !ok {
		return Version{}, nil, ErrNotFound
	}
	e.v.Active = m.active[tenant] == id
	return e.v, append([]byte(nil), e.doc...), nil
}

// List implements Store.
func (m *Memory) List(_ context.Context, tenant string) ([]Version, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Version, 0, len(m.rows[tenant]))
	for _, e := range m.rows[tenant] {
		v := e.v
		v.Active = m.active[tenant] == v.ID
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID > out[j].ID })
	return out, nil
}
