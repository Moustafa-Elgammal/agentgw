package manifeststore

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"

	"github.com/Moustafa-Elgammal/agentgw/internal/manifest"
)

func hashDoc(doc []byte) string {
	s := sha256.Sum256(doc)
	return hex.EncodeToString(s[:])
}

// Applier receives the manifest that should now be served. gateway.Service implements it.
type Applier interface {
	SetManifest(*manifest.Manifest)
}

// Manager ties a Store to a running gateway for one tenant: it validates uploads, activates
// versions, and applies the active version, also when another replica changed it (Sync).
type Manager struct {
	store   Store
	tenant  string
	applier Applier

	mu      sync.Mutex
	applied int64 // id of the version last applied; 0 means none yet
}

// NewManager builds a Manager.
func NewManager(store Store, tenant string, applier Applier) *Manager {
	return &Manager{store: store, tenant: tenant, applier: applier}
}

// Format selects how Upload reads a document.
type Format int

// Upload formats.
const (
	JSON Format = iota
	YAML
)

// ErrInvalid wraps a manifest that failed validation, so callers can tell a bad upload
// from a store failure.
var ErrInvalid = errors.New("manifeststore: invalid manifest")

func parse(raw []byte, f Format) (*manifest.Manifest, error) {
	var m *manifest.Manifest
	var err error
	if f == YAML {
		m, err = manifest.ParseYAML(bytes.NewReader(raw))
	} else {
		m, err = manifest.Parse(bytes.NewReader(raw))
	}
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	return m, nil
}

// Upload validates raw, stores it as a new version, activates it and applies it. The
// stored document is the canonical JSON of the parsed manifest, so JSON and YAML uploads
// of the same manifest hash the same. A manifest that fails validation changes nothing.
func (g *Manager) Upload(ctx context.Context, actor string, raw []byte, f Format) (Version, error) {
	m, err := parse(raw, f)
	if err != nil {
		return Version{}, err
	}
	doc, err := m.Marshal()
	if err != nil {
		return Version{}, err
	}
	v, err := g.store.Put(ctx, g.tenant, actor, doc, len(m.Tools))
	if err != nil {
		return Version{}, err
	}
	if err := g.store.Activate(ctx, g.tenant, v.ID); err != nil {
		return Version{}, err
	}
	v.Active = true
	g.apply(v.ID, m)
	return v, nil
}

// Rollback activates an existing version and applies it. The stored document is validated
// again, because the validator may have become stricter since it was uploaded; if it no
// longer passes, nothing changes.
func (g *Manager) Rollback(ctx context.Context, id int64) (Version, error) {
	v, doc, err := g.store.Get(ctx, g.tenant, id)
	if err != nil {
		return Version{}, err
	}
	m, err := parse(doc, JSON)
	if err != nil {
		return Version{}, err
	}
	if err := g.store.Activate(ctx, g.tenant, id); err != nil {
		return Version{}, err
	}
	v.Active = true
	g.apply(id, m)
	return v, nil
}

// Bootstrap seeds the store from raw when the tenant has no active version, so a fresh
// deployment starts from its manifest file. If a version is already active it is left alone
// (the store is the source of truth once a studio has uploaded). Either way the active
// version ends up applied. It reports the active version and whether it seeded.
func (g *Manager) Bootstrap(ctx context.Context, actor string, raw []byte, f Format) (Version, bool, error) {
	v, _, err := g.store.Active(ctx, g.tenant)
	switch {
	case err == nil:
		return v, false, g.Sync(ctx)
	case errors.Is(err, ErrNotFound):
		v, err := g.Upload(ctx, actor, raw, f)
		return v, true, err
	default:
		return Version{}, false, err
	}
}

// Sync applies the store's active version if it differs from what is running. Replicas
// call it on a timer so a rollback on one reaches all. A stored document that no longer
// validates, or a store error, leaves the running manifest untouched.
func (g *Manager) Sync(ctx context.Context) error {
	v, doc, err := g.store.Active(ctx, g.tenant)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil
		}
		return err
	}
	g.mu.Lock()
	same := g.applied == v.ID
	g.mu.Unlock()
	if same {
		return nil
	}
	m, err := parse(doc, JSON)
	if err != nil {
		return err
	}
	g.apply(v.ID, m)
	return nil
}

func (g *Manager) apply(id int64, m *manifest.Manifest) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.applier.SetManifest(m)
	g.applied = id
}

// Store exposes the underlying store for read endpoints.
func (g *Manager) Store() Store { return g.store }

// Tenant is the tenant this Manager serves.
func (g *Manager) Tenant() string { return g.tenant }
