// Package pgstore keeps OAuth state in Postgres so clients, grants and tokens
// survive restarts and are shared by every gateway replica.
//
// Only hashes of tickets, codes and tokens are stored. The interface already
// passes hashes, and nothing here ever sees a plaintext secret.
//
// Atomicity comes from the database, not from locks in Go:
//   - TakeTicket and TakeCode are one UPDATE ... WHERE hash = $1 AND NOT used
//     AND expires > now RETURNING, so exactly one concurrent caller wins;
//   - a partial unique index allows one active grant per
//     (tenant, player, client, resource) and UpsertGrant is one INSERT ... ON
//     CONFLICT, so concurrent consents share a row;
//   - RotateRefresh locks the refresh row (see its comment).
//
// Time comes from the caller's clock so tests control expiry. Replicas need
// reasonably synchronized clocks. Expired rows accumulate until Prune runs;
// nothing here starts background goroutines.
package pgstore

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Moustafa-Elgammal/agentgw/internal/auth"
)

// RevokedRetention is how long Prune keeps a revoked grant before deleting it.
const RevokedRetention = 30 * 24 * time.Hour

// Schema creates the tables and indexes. It is idempotent, so Migrate can run on every start.
const Schema = `
CREATE TABLE IF NOT EXISTS oauth_clients (
  id            text PRIMARY KEY,
  name          text NOT NULL,
  redirect_uris text[] NOT NULL,
  created_at    timestamptz NOT NULL
);
CREATE TABLE IF NOT EXISTS oauth_tickets (
  hash    text PRIMARY KEY,
  tenant  text NOT NULL,
  player  text NOT NULL,
  expires timestamptz NOT NULL,
  used    boolean NOT NULL DEFAULT false
);
CREATE TABLE IF NOT EXISTS oauth_codes (
  hash         text PRIMARY KEY,
  client_id    text NOT NULL,
  redirect_uri text NOT NULL,
  challenge    text NOT NULL,
  resource     text NOT NULL,
  scope        text NOT NULL,
  tenant       text NOT NULL,
  player       text NOT NULL,
  expires      timestamptz NOT NULL,
  used         boolean NOT NULL DEFAULT false
);
CREATE TABLE IF NOT EXISTS oauth_grants (
  id         text PRIMARY KEY,
  tenant     text NOT NULL,
  player     text NOT NULL,
  client_id  text NOT NULL,
  resource   text NOT NULL,
  scope      text NOT NULL,
  created_at timestamptz NOT NULL,
  revoked_at timestamptz
);
CREATE UNIQUE INDEX IF NOT EXISTS oauth_grants_one_active
  ON oauth_grants (tenant, player, client_id, resource)
  WHERE revoked_at IS NULL;
CREATE INDEX IF NOT EXISTS oauth_grants_player
  ON oauth_grants (tenant, player, created_at)
  WHERE revoked_at IS NULL;
CREATE TABLE IF NOT EXISTS oauth_access_tokens (
  hash     text PRIMARY KEY,
  grant_id text NOT NULL REFERENCES oauth_grants (id) ON DELETE CASCADE,
  expires  timestamptz NOT NULL
);
CREATE INDEX IF NOT EXISTS oauth_access_grant ON oauth_access_tokens (grant_id);
CREATE TABLE IF NOT EXISTS oauth_refresh_tokens (
  hash     text PRIMARY KEY,
  grant_id text NOT NULL REFERENCES oauth_grants (id) ON DELETE CASCADE,
  expires  timestamptz NOT NULL,
  rotated  boolean NOT NULL DEFAULT false
);
CREATE INDEX IF NOT EXISTS oauth_refresh_grant ON oauth_refresh_tokens (grant_id);
`

// Store implements auth.Store on Postgres.
type Store struct {
	db  *pgxpool.Pool
	now func() time.Time
}

var _ auth.Store = (*Store)(nil)

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
		return fmt.Errorf("auth pgstore: migrate: %w", err)
	}
	return nil
}

// notFound maps "no row" to the single auth.ErrNotFound and wraps anything else.
func notFound(err error, op string) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return auth.ErrNotFound
	}
	return fmt.Errorf("auth pgstore: %s: %w", op, err)
}

// PutTicket implements auth.Store.
func (s *Store) PutTicket(ctx context.Context, hash, tenant, player string, expires time.Time) error {
	_, err := s.db.Exec(ctx, `INSERT INTO oauth_tickets (hash, tenant, player, expires)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (hash) DO UPDATE SET tenant = EXCLUDED.tenant, player = EXCLUDED.player,
		  expires = EXCLUDED.expires, used = false`, hash, tenant, player, expires)
	if err != nil {
		return fmt.Errorf("auth pgstore: put ticket: %w", err)
	}
	return nil
}

// TakeTicket implements auth.Store.
func (s *Store) TakeTicket(ctx context.Context, hash string) (string, string, error) {
	var tenant, player string
	err := s.db.QueryRow(ctx, `UPDATE oauth_tickets SET used = true
		WHERE hash = $1 AND NOT used AND expires > $2
		RETURNING tenant, player`, hash, s.now()).Scan(&tenant, &player)
	if err != nil {
		return "", "", notFound(err, "take ticket")
	}
	return tenant, player, nil
}

// RegisterClient implements auth.Store.
func (s *Store) RegisterClient(ctx context.Context, c auth.Client) error {
	if c.CreatedAt.IsZero() {
		c.CreatedAt = s.now()
	}
	uris := c.RedirectURIs
	if uris == nil {
		uris = []string{}
	}
	_, err := s.db.Exec(ctx, `INSERT INTO oauth_clients (id, name, redirect_uris, created_at)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (id) DO UPDATE SET name = EXCLUDED.name, redirect_uris = EXCLUDED.redirect_uris,
		  created_at = EXCLUDED.created_at`, c.ID, c.Name, uris, c.CreatedAt)
	if err != nil {
		return fmt.Errorf("auth pgstore: register client: %w", err)
	}
	return nil
}

// Client implements auth.Store.
func (s *Store) Client(ctx context.Context, id string) (auth.Client, error) {
	var c auth.Client
	err := s.db.QueryRow(ctx, `SELECT id, name, redirect_uris, created_at FROM oauth_clients WHERE id = $1`, id).
		Scan(&c.ID, &c.Name, &c.RedirectURIs, &c.CreatedAt)
	if err != nil {
		return auth.Client{}, notFound(err, "client")
	}
	return c, nil
}

// PutCode implements auth.Store.
func (s *Store) PutCode(ctx context.Context, c auth.Code) error {
	_, err := s.db.Exec(ctx, `INSERT INTO oauth_codes
		(hash, client_id, redirect_uri, challenge, resource, scope, tenant, player, expires)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		ON CONFLICT (hash) DO UPDATE SET client_id = EXCLUDED.client_id, redirect_uri = EXCLUDED.redirect_uri,
		  challenge = EXCLUDED.challenge, resource = EXCLUDED.resource, scope = EXCLUDED.scope,
		  tenant = EXCLUDED.tenant, player = EXCLUDED.player, expires = EXCLUDED.expires, used = false`,
		c.Hash, c.ClientID, c.RedirectURI, c.Challenge, c.Resource, string(c.Scope), c.Tenant, c.Player, c.Expires)
	if err != nil {
		return fmt.Errorf("auth pgstore: put code: %w", err)
	}
	return nil
}

// TakeCode implements auth.Store.
func (s *Store) TakeCode(ctx context.Context, hash string) (auth.Code, error) {
	c := auth.Code{Hash: hash}
	var scope string
	err := s.db.QueryRow(ctx, `UPDATE oauth_codes SET used = true
		WHERE hash = $1 AND NOT used AND expires > $2
		RETURNING client_id, redirect_uri, challenge, resource, scope, tenant, player, expires`,
		hash, s.now()).Scan(&c.ClientID, &c.RedirectURI, &c.Challenge, &c.Resource, &scope, &c.Tenant, &c.Player, &c.Expires)
	if err != nil {
		return auth.Code{}, notFound(err, "take code")
	}
	c.Scope = auth.Scope(scope)
	return c, nil
}

const grantCols = `id, tenant, player, client_id, resource, scope, created_at`

func scanGrant(row pgx.Row) (auth.Grant, error) {
	var g auth.Grant
	var scope string
	if err := row.Scan(&g.ID, &g.Tenant, &g.Player, &g.ClientID, &g.Resource, &scope, &g.CreatedAt); err != nil {
		return auth.Grant{}, err
	}
	g.Scope = auth.Scope(scope)
	return g, nil
}

// UpsertGrant implements auth.Store. The partial unique index covers only active
// rows, so a revoked grant never conflicts and never comes back: consenting again
// inserts a new row with a new ID.
func (s *Store) UpsertGrant(ctx context.Context, g auth.Grant) (auth.Grant, error) {
	if g.ID == "" {
		g.ID = auth.NewID()
	}
	if g.CreatedAt.IsZero() {
		g.CreatedAt = s.now()
	}
	out, err := scanGrant(s.db.QueryRow(ctx, `INSERT INTO oauth_grants
		(id, tenant, player, client_id, resource, scope, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT (tenant, player, client_id, resource) WHERE revoked_at IS NULL
		DO UPDATE SET scope = EXCLUDED.scope
		RETURNING `+grantCols,
		g.ID, g.Tenant, g.Player, g.ClientID, g.Resource, string(g.Scope), g.CreatedAt))
	if err != nil {
		return auth.Grant{}, fmt.Errorf("auth pgstore: upsert grant: %w", err)
	}
	return out, nil
}

// Grants implements auth.Store.
func (s *Store) Grants(ctx context.Context, tenant, player string) ([]auth.Grant, error) {
	rows, err := s.db.Query(ctx, `SELECT `+grantCols+` FROM oauth_grants
		WHERE tenant = $1 AND player = $2 AND revoked_at IS NULL
		ORDER BY created_at, id`, tenant, player)
	if err != nil {
		return nil, fmt.Errorf("auth pgstore: grants: %w", err)
	}
	defer rows.Close()
	var out []auth.Grant
	for rows.Next() {
		g, err := scanGrant(rows)
		if err != nil {
			return nil, fmt.Errorf("auth pgstore: grants: %w", err)
		}
		out = append(out, g)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("auth pgstore: grants: %w", err)
	}
	return out, nil
}

// RevokeGrant implements auth.Store. Tokens are not deleted: every token lookup joins
// the grant and treats a revoked one as not found, so the revocation is immediate.
func (s *Store) RevokeGrant(ctx context.Context, tenant, player, grantID string) error {
	tag, err := s.db.Exec(ctx, `UPDATE oauth_grants SET revoked_at = $4
		WHERE id = $1 AND tenant = $2 AND player = $3 AND revoked_at IS NULL`,
		grantID, tenant, player, s.now())
	if err != nil {
		return fmt.Errorf("auth pgstore: revoke grant: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return auth.ErrNotFound
	}
	return nil
}

// PutAccess implements auth.Store.
func (s *Store) PutAccess(ctx context.Context, hash, grantID string, expires time.Time) error {
	_, err := s.db.Exec(ctx, `INSERT INTO oauth_access_tokens (hash, grant_id, expires) VALUES ($1, $2, $3)
		ON CONFLICT (hash) DO UPDATE SET grant_id = EXCLUDED.grant_id, expires = EXCLUDED.expires`,
		hash, grantID, expires)
	if err != nil {
		return fmt.Errorf("auth pgstore: put access: %w", err)
	}
	return nil
}

// Access implements auth.Store.
func (s *Store) Access(ctx context.Context, hash string) (auth.Grant, error) {
	g, err := scanGrant(s.db.QueryRow(ctx, `SELECT g.id, g.tenant, g.player, g.client_id, g.resource, g.scope, g.created_at
		FROM oauth_access_tokens t JOIN oauth_grants g ON g.id = t.grant_id
		WHERE t.hash = $1 AND t.expires > $2 AND g.revoked_at IS NULL`, hash, s.now()))
	if err != nil {
		return auth.Grant{}, notFound(err, "access")
	}
	return g, nil
}

// PutRefresh implements auth.Store.
func (s *Store) PutRefresh(ctx context.Context, hash, grantID string, expires time.Time) error {
	_, err := s.db.Exec(ctx, `INSERT INTO oauth_refresh_tokens (hash, grant_id, expires) VALUES ($1, $2, $3)
		ON CONFLICT (hash) DO UPDATE SET grant_id = EXCLUDED.grant_id, expires = EXCLUDED.expires, rotated = false`,
		hash, grantID, expires)
	if err != nil {
		return fmt.Errorf("auth pgstore: put refresh: %w", err)
	}
	return nil
}

// RotateRefresh implements auth.Store.
//
// It runs in one transaction that starts by locking the old token's row with
// SELECT ... FOR UPDATE. Concurrent callers presenting the same token queue on that
// lock. Under READ COMMITTED a waiter re-reads the row after the lock is released,
// so it sees the winner's committed rotated = true. Exactly one caller therefore
// sees rotated = false and rotates; every other caller sees rotated = true, which is
// token reuse, and revokes the grant. A conditional UPDATE alone could pick the
// winner but could not tell "lost the race" from "unknown token" without a second
// read, so the explicit lock keeps the decision and the reuse handling in one place.
//
// The lock is taken on the refresh row only and the grant row is touched afterwards,
// so the order is the same for every caller and cannot deadlock with RevokeGrant,
// which locks the grant row alone.
func (s *Store) RotateRefresh(ctx context.Context, oldHash, newHash string, newExpires time.Time) (auth.Grant, error) {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return auth.Grant{}, fmt.Errorf("auth pgstore: rotate: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }() // no-op after Commit

	var grantID string
	var expires time.Time
	var rotated bool
	err = tx.QueryRow(ctx, `SELECT grant_id, expires, rotated FROM oauth_refresh_tokens
		WHERE hash = $1 FOR UPDATE`, oldHash).Scan(&grantID, &expires, &rotated)
	if err != nil {
		return auth.Grant{}, notFound(err, "rotate")
	}
	g, err := scanGrant(tx.QueryRow(ctx, `SELECT `+grantCols+` FROM oauth_grants
		WHERE id = $1 AND revoked_at IS NULL`, grantID))
	if err != nil {
		return auth.Grant{}, notFound(err, "rotate") // revoked grant: not found
	}
	now := s.now()
	if rotated {
		// A rotated token came back: assume it was stolen and kill the whole grant.
		// The revocation must commit even though the call fails.
		if _, err := tx.Exec(ctx, `UPDATE oauth_grants SET revoked_at = $2 WHERE id = $1 AND revoked_at IS NULL`,
			grantID, now); err != nil {
			return auth.Grant{}, fmt.Errorf("auth pgstore: rotate: %w", err)
		}
		if err := tx.Commit(ctx); err != nil {
			return auth.Grant{}, fmt.Errorf("auth pgstore: rotate: %w", err)
		}
		return auth.Grant{}, auth.ErrReuse
	}
	if !now.Before(expires) {
		return auth.Grant{}, auth.ErrNotFound
	}
	if _, err := tx.Exec(ctx, `UPDATE oauth_refresh_tokens SET rotated = true WHERE hash = $1`, oldHash); err != nil {
		return auth.Grant{}, fmt.Errorf("auth pgstore: rotate: %w", err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO oauth_refresh_tokens (hash, grant_id, expires) VALUES ($1, $2, $3)`,
		newHash, grantID, newExpires); err != nil {
		return auth.Grant{}, fmt.Errorf("auth pgstore: rotate: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return auth.Grant{}, fmt.Errorf("auth pgstore: rotate: %w", err)
	}
	return g, nil
}

// Prune deletes expired tickets, codes and tokens, and grants revoked more than
// RevokedRetention ago (their tokens go with them). It returns the number of rows
// deleted. Rotated refresh tokens are kept until they expire so that reuse is still
// detected. Call it periodically from the caller; the store runs no goroutines.
func (s *Store) Prune(ctx context.Context) (int64, error) {
	now := s.now()
	var total int64
	for _, q := range []struct {
		sql string
		at  time.Time
	}{
		{`DELETE FROM oauth_tickets WHERE expires <= $1`, now},
		{`DELETE FROM oauth_codes WHERE expires <= $1`, now},
		{`DELETE FROM oauth_access_tokens WHERE expires <= $1`, now},
		{`DELETE FROM oauth_refresh_tokens WHERE expires <= $1`, now},
		{`DELETE FROM oauth_grants WHERE revoked_at IS NOT NULL AND revoked_at <= $1`, now.Add(-RevokedRetention)},
	} {
		tag, err := s.db.Exec(ctx, q.sql, q.at)
		if err != nil {
			return total, fmt.Errorf("auth pgstore: prune: %w", err)
		}
		total += tag.RowsAffected()
	}
	return total, nil
}
