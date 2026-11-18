// Package pgstore keeps confirmations in Postgres so approvals survive restarts
// and are shared by every gateway replica.
//
// Atomicity comes from the database, not from locks in Go:
//   - claiming is one UPDATE ... WHERE state = 'approved', so concurrent callers
//     cannot both win;
//   - a partial unique index allows only one live confirmation per
//     (tenant, player, tool, arguments), so concurrent first calls share one row.
//
// Time comes from the caller's clock so tests control expiry. Replicas need
// reasonably synchronized clocks.
package pgstore

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Moustafa-Elgammal/agentgw/internal/confirm"
	"github.com/Moustafa-Elgammal/agentgw/internal/core"
)

// Schema creates the table and the index that keeps one live confirmation per call.
// It is idempotent, so Migrate can run on every start.
const Schema = `
CREATE TABLE IF NOT EXISTS confirmations (
  id         text PRIMARY KEY,
  tenant     text NOT NULL,
  player     text NOT NULL,
  tool       text NOT NULL,
  args_sha   text NOT NULL,
  state      text NOT NULL CHECK (state IN ('pending','approved','claimed','consumed','denied','expired')),
  created_at timestamptz NOT NULL,
  expires_at timestamptz NOT NULL
);
CREATE UNIQUE INDEX IF NOT EXISTS confirmations_one_live
  ON confirmations (tenant, player, tool, args_sha)
  WHERE state IN ('pending','approved','claimed','denied');
CREATE INDEX IF NOT EXISTS confirmations_player_pending
  ON confirmations (tenant, player, created_at)
  WHERE state = 'pending';
`

// Store implements confirm.Store on Postgres.
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
		return fmt.Errorf("pgstore: migrate: %w", err)
	}
	return nil
}

// live states are the ones the unique index covers.
const liveStates = `('pending','approved','claimed','denied')`

// Gate implements confirm.Store.
func (s *Store) Gate(ctx context.Context, p core.Principal, tool, argsSHA string) (confirm.Gate, error) {
	now := s.now()
	// Retire expired rows for this call first, so the unique index frees up.
	if _, err := s.db.Exec(ctx, `UPDATE confirmations SET state = 'expired'
		WHERE tenant = $1 AND player = $2 AND tool = $3 AND args_sha = $4
		  AND state IN `+liveStates+` AND expires_at <= $5`,
		p.TenantID, p.PlayerID, tool, argsSHA, now); err != nil {
		return confirm.Gate{}, fmt.Errorf("pgstore: expire: %w", err)
	}

	// A concurrent Approve can slip between our steps, so retry a few times.
	for attempt := 0; attempt < 4; attempt++ {
		var id string
		var exp time.Time

		// 1. Claim an approval. One UPDATE: exactly one concurrent caller gets the row.
		err := s.db.QueryRow(ctx, `UPDATE confirmations SET state = 'claimed'
			WHERE tenant = $1 AND player = $2 AND tool = $3 AND args_sha = $4 AND state = 'approved'
			RETURNING id, expires_at`, p.TenantID, p.PlayerID, tool, argsSHA).Scan(&id, &exp)
		if err == nil {
			return confirm.Gate{Status: confirm.StatusClaimed, ID: id, ExpiresAt: exp}, nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return confirm.Gate{}, fmt.Errorf("pgstore: claim: %w", err)
		}

		// 2. Create a pending one. The unique index makes concurrent creators share a row.
		newID := confirm.NewID()
		err = s.db.QueryRow(ctx, `INSERT INTO confirmations (id, tenant, player, tool, args_sha, state, created_at, expires_at)
			VALUES ($1, $2, $3, $4, $5, 'pending', $6, $7)
			ON CONFLICT (tenant, player, tool, args_sha) WHERE state IN `+liveStates+` DO NOTHING
			RETURNING id, expires_at`,
			newID, p.TenantID, p.PlayerID, tool, argsSHA, now, now.Add(confirm.TTL)).Scan(&id, &exp)
		if err == nil {
			return confirm.Gate{Status: confirm.StatusCreated, ID: id, ExpiresAt: exp}, nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return confirm.Gate{}, fmt.Errorf("pgstore: create: %w", err)
		}

		// 3. Someone else's live row exists: report it.
		var state string
		err = s.db.QueryRow(ctx, `SELECT id, state, expires_at FROM confirmations
			WHERE tenant = $1 AND player = $2 AND tool = $3 AND args_sha = $4 AND state IN `+liveStates,
			p.TenantID, p.PlayerID, tool, argsSHA).Scan(&id, &state, &exp)
		if errors.Is(err, pgx.ErrNoRows) {
			continue // it was settled or expired in between
		}
		if err != nil {
			return confirm.Gate{}, fmt.Errorf("pgstore: lookup: %w", err)
		}
		g := confirm.Gate{ID: id, ExpiresAt: exp}
		switch confirm.State(state) {
		case confirm.StateApproved:
			continue // approved just now: go back and claim it
		case confirm.StateClaimed:
			g.Status = confirm.StatusInUse
		case confirm.StateDenied:
			g.Status = confirm.StatusDenied
		default:
			g.Status = confirm.StatusPending
		}
		return g, nil
	}
	return confirm.Gate{}, errors.New("pgstore: gate: too much contention, try again")
}

// Settle implements confirm.Store.
func (s *Store) Settle(ctx context.Context, id string, ok bool) error {
	next := "approved"
	if ok {
		next = "consumed"
	}
	if _, err := s.db.Exec(ctx, `UPDATE confirmations SET state = $2 WHERE id = $1 AND state = 'claimed'`, id, next); err != nil {
		return fmt.Errorf("pgstore: settle: %w", err)
	}
	return nil
}

// decide moves a live confirmation owned by p from one of the from states to to.
// A confirmation already in to is success, any other live state is ErrState.
func (s *Store) decide(ctx context.Context, p core.Principal, id, to string, from ...string) error {
	now := s.now()
	tag, err := s.db.Exec(ctx, `UPDATE confirmations SET state = $5
		WHERE id = $1 AND tenant = $2 AND player = $3 AND expires_at > $4 AND state = ANY($6)`,
		id, p.TenantID, p.PlayerID, now, to, from)
	if err != nil {
		return fmt.Errorf("pgstore: %s: %w", to, err)
	}
	if tag.RowsAffected() == 1 {
		return nil
	}
	var state string
	err = s.db.QueryRow(ctx, `SELECT state FROM confirmations
		WHERE id = $1 AND tenant = $2 AND player = $3 AND expires_at > $4`,
		id, p.TenantID, p.PlayerID, now).Scan(&state)
	if errors.Is(err, pgx.ErrNoRows) {
		return confirm.ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("pgstore: %s: %w", to, err)
	}
	if state == to {
		return nil
	}
	return confirm.ErrState
}

// Approve implements confirm.Store.
func (s *Store) Approve(ctx context.Context, p core.Principal, id string) error {
	return s.decide(ctx, p, id, "approved", "pending")
}

// Deny implements confirm.Store.
func (s *Store) Deny(ctx context.Context, p core.Principal, id string) error {
	return s.decide(ctx, p, id, "denied", "pending", "approved")
}

// Pending implements confirm.Store.
func (s *Store) Pending(ctx context.Context, p core.Principal) ([]confirm.Confirmation, error) {
	rows, err := s.db.Query(ctx, `SELECT id, tenant, player, tool, args_sha, state, created_at, expires_at
		FROM confirmations WHERE tenant = $1 AND player = $2 AND state = 'pending' AND expires_at > $3
		ORDER BY created_at, id`, p.TenantID, p.PlayerID, s.now())
	if err != nil {
		return nil, fmt.Errorf("pgstore: pending: %w", err)
	}
	defer rows.Close()
	var out []confirm.Confirmation
	for rows.Next() {
		var c confirm.Confirmation
		var state string
		if err := rows.Scan(&c.ID, &c.Tenant, &c.Player, &c.Tool, &c.ArgsSHA, &state, &c.CreatedAt, &c.ExpiresAt); err != nil {
			return nil, fmt.Errorf("pgstore: pending: %w", err)
		}
		c.State = confirm.State(state)
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("pgstore: pending: %w", err)
	}
	return out, nil
}
