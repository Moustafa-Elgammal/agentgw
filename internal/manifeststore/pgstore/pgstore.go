// Package pgstore keeps manifest versions in Postgres so uploads survive restarts and every
// gateway replica sees the same active version.
package pgstore

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Moustafa-Elgammal/agentgw/internal/manifeststore"
)

// Schema is idempotent, so Migrate can run on every start.
const Schema = `
CREATE TABLE IF NOT EXISTS manifest_versions (
  id         bigserial PRIMARY KEY,
  tenant     text NOT NULL,
  sha256     text NOT NULL,
  actor      text NOT NULL,
  tools      integer NOT NULL,
  doc        bytea NOT NULL,
  created_at timestamptz NOT NULL
);
CREATE INDEX IF NOT EXISTS manifest_versions_tenant ON manifest_versions (tenant, id DESC);
CREATE TABLE IF NOT EXISTS manifest_active (
  tenant     text PRIMARY KEY,
  version_id bigint NOT NULL REFERENCES manifest_versions (id)
);
`

// Store implements manifeststore.Store on Postgres.
type Store struct {
	db  *pgxpool.Pool
	now func() time.Time
}

// New builds a Store. A nil clock uses time.Now.
func New(db *pgxpool.Pool, now func() time.Time) *Store {
	if now == nil {
		now = time.Now
	}
	return &Store{db: db, now: now}
}

// Migrate creates the schema if it is missing.
func (s *Store) Migrate(ctx context.Context) error {
	if _, err := s.db.Exec(ctx, Schema); err != nil {
		return fmt.Errorf("manifest pgstore: migrate: %w", err)
	}
	return nil
}

const cols = `v.id, v.sha256, v.actor, v.created_at, v.tools, (a.version_id IS NOT NULL)`
const from = ` FROM manifest_versions v LEFT JOIN manifest_active a ON a.tenant = v.tenant AND a.version_id = v.id`

func scan(row pgx.Row, v *manifeststore.Version, extra ...any) error {
	return row.Scan(append([]any{&v.ID, &v.SHA256, &v.Actor, &v.CreatedAt, &v.Tools, &v.Active}, extra...)...)
}

// Put implements manifeststore.Store.
func (s *Store) Put(ctx context.Context, tenant, actor string, doc []byte, tools int) (manifeststore.Version, error) {
	v := manifeststore.Version{SHA256: hashHex(doc), Actor: actor, CreatedAt: s.now().UTC(), Tools: tools}
	err := s.db.QueryRow(ctx, `INSERT INTO manifest_versions (tenant, sha256, actor, tools, doc, created_at)
		VALUES ($1,$2,$3,$4,$5,$6) RETURNING id`, tenant, v.SHA256, actor, tools, doc, v.CreatedAt).Scan(&v.ID)
	if err != nil {
		return manifeststore.Version{}, fmt.Errorf("manifest pgstore: put: %w", err)
	}
	return v, nil
}

// Activate implements manifeststore.Store. One statement: it only writes if the version
// exists for the tenant, so another tenant's id can never be activated.
func (s *Store) Activate(ctx context.Context, tenant string, id int64) error {
	tag, err := s.db.Exec(ctx, `INSERT INTO manifest_active (tenant, version_id)
		SELECT tenant, id FROM manifest_versions WHERE tenant = $1 AND id = $2
		ON CONFLICT (tenant) DO UPDATE SET version_id = EXCLUDED.version_id`, tenant, id)
	if err != nil {
		return fmt.Errorf("manifest pgstore: activate: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return manifeststore.ErrNotFound
	}
	return nil
}

// Active implements manifeststore.Store.
func (s *Store) Active(ctx context.Context, tenant string) (manifeststore.Version, []byte, error) {
	var v manifeststore.Version
	var doc []byte
	err := scan(s.db.QueryRow(ctx, `SELECT `+cols+`, v.doc`+from+` WHERE a.tenant = $1`, tenant), &v, &doc)
	return result(v, doc, err)
}

// Get implements manifeststore.Store.
func (s *Store) Get(ctx context.Context, tenant string, id int64) (manifeststore.Version, []byte, error) {
	var v manifeststore.Version
	var doc []byte
	err := scan(s.db.QueryRow(ctx, `SELECT `+cols+`, v.doc`+from+` WHERE v.tenant = $1 AND v.id = $2`, tenant, id), &v, &doc)
	return result(v, doc, err)
}

func result(v manifeststore.Version, doc []byte, err error) (manifeststore.Version, []byte, error) {
	if errors.Is(err, pgx.ErrNoRows) {
		return manifeststore.Version{}, nil, manifeststore.ErrNotFound
	}
	if err != nil {
		return manifeststore.Version{}, nil, fmt.Errorf("manifest pgstore: read: %w", err)
	}
	v.CreatedAt = v.CreatedAt.UTC()
	return v, doc, nil
}

// List implements manifeststore.Store.
func (s *Store) List(ctx context.Context, tenant string) ([]manifeststore.Version, error) {
	rows, err := s.db.Query(ctx, `SELECT `+cols+from+` WHERE v.tenant = $1 ORDER BY v.id DESC`, tenant)
	if err != nil {
		return nil, fmt.Errorf("manifest pgstore: list: %w", err)
	}
	defer rows.Close()
	out := []manifeststore.Version{}
	for rows.Next() {
		var v manifeststore.Version
		if err := scan(rows, &v); err != nil {
			return nil, fmt.Errorf("manifest pgstore: list: %w", err)
		}
		v.CreatedAt = v.CreatedAt.UTC()
		out = append(out, v)
	}
	return out, rows.Err()
}
