// Package pgstore keeps audit events in Postgres so the activity feed survives restarts and
// covers every gateway replica.
package pgstore

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Moustafa-Elgammal/agentgw/internal/audit"
)

// Schema is idempotent, so Migrate can run on every start.
const Schema = `
CREATE TABLE IF NOT EXISTS audit_events (
  id        bigserial PRIMARY KEY,
  tenant    text NOT NULL,
  player    text NOT NULL,
  tool      text NOT NULL,
  tier      text NOT NULL,
  outcome   text NOT NULL,
  code      text NOT NULL,
  args_sha  text NOT NULL,
  time      timestamptz NOT NULL
);
CREATE INDEX IF NOT EXISTS audit_events_player ON audit_events (tenant, player, id DESC);
CREATE INDEX IF NOT EXISTS audit_events_tenant ON audit_events (tenant, id DESC);
CREATE INDEX IF NOT EXISTS audit_events_time ON audit_events (time);
`

// recordTimeout bounds one INSERT so a slow database cannot hold up the call path.
const recordTimeout = 2 * time.Second

// Store implements audit.Store on Postgres.
type Store struct {
	db *pgxpool.Pool
	// OnError, if set, receives failed inserts. Auditor.Record has no error return, so this is
	// how the caller learns an event was lost. The error text holds no event data.
	OnError func(error)
}

// New builds a Store.
func New(db *pgxpool.Pool) *Store { return &Store{db: db} }

// Migrate creates the schema if it is missing.
func (s *Store) Migrate(ctx context.Context) error {
	if _, err := s.db.Exec(ctx, Schema); err != nil {
		return fmt.Errorf("audit pgstore: migrate: %w", err)
	}
	return nil
}

// Record implements audit.Auditor with one INSERT. It uses its own short deadline and never
// panics; a failure goes to OnError.
func (s *Store) Record(ctx context.Context, e audit.Event) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), recordTimeout)
	defer cancel()
	_, err := s.db.Exec(ctx, `INSERT INTO audit_events (tenant, player, tool, tier, outcome, code, args_sha, time)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`,
		e.Tenant, e.Player, e.Tool, e.Tier, e.Outcome, e.Code, e.ArgsSHA, e.Time.UTC())
	if err != nil && s.OnError != nil {
		s.OnError(fmt.Errorf("audit pgstore: record: %w", err))
	}
}

// Query implements audit.Store.
func (s *Store) Query(ctx context.Context, q audit.Query) ([]audit.Stored, error) {
	where := []string{"tenant = $1"}
	args := []any{q.Tenant}
	add := func(cond string, v any) {
		args = append(args, v)
		where = append(where, strings.Replace(cond, "?", "$"+strconv.Itoa(len(args)), 1))
	}
	if q.Player != "" {
		add("player = ?", q.Player)
	}
	if q.Tool != "" {
		add("tool = ?", q.Tool)
	}
	if q.Outcome != "" {
		add("outcome = ?", q.Outcome)
	}
	if !q.Since.IsZero() {
		add("time >= ?", q.Since.UTC())
	}
	if q.Before != 0 {
		add("id < ?", q.Before)
	}
	args = append(args, q.Clamp())
	rows, err := s.db.Query(ctx, `SELECT id, tenant, player, tool, tier, outcome, code, args_sha, time
		FROM audit_events WHERE `+strings.Join(where, " AND ")+
		` ORDER BY id DESC LIMIT $`+strconv.Itoa(len(args)), args...)
	if err != nil {
		return nil, fmt.Errorf("audit pgstore: query: %w", err)
	}
	defer rows.Close()
	out := []audit.Stored{}
	for rows.Next() {
		var e audit.Stored
		if err := rows.Scan(&e.ID, &e.Tenant, &e.Player, &e.Tool, &e.Tier, &e.Outcome, &e.Code, &e.ArgsSHA, &e.Time); err != nil {
			return nil, fmt.Errorf("audit pgstore: query: %w", err)
		}
		e.Time = e.Time.UTC()
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("audit pgstore: query: %w", err)
	}
	return out, nil
}

// Prune implements audit.Store.
func (s *Store) Prune(ctx context.Context, before time.Time) (int64, error) {
	tag, err := s.db.Exec(ctx, `DELETE FROM audit_events WHERE time < $1`, before.UTC())
	if err != nil {
		return 0, fmt.Errorf("audit pgstore: prune: %w", err)
	}
	return tag.RowsAffected(), nil
}
