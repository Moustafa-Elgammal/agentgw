package main

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

// migrationLockKey is the Postgres advisory lock key that serializes schema migrations. The value
// is arbitrary but fixed, and spells "agentgw" in ASCII.
const migrationLockKey int64 = 0x61_67_65_6e_74_67_77

// withMigrationLock runs fn while holding a session level advisory lock on one pooled connection.
//
// Every migration is CREATE TABLE IF NOT EXISTS, which is not safe when two sessions run it at
// the same moment: Postgres can fail one of them with a duplicate key error on pg_type. That is
// what happens when several replicas start together (a Kubernetes rollout, an autoscale event).
// With the lock the second replica waits for the first, then finds the tables and does nothing.
// The lock is released when fn returns, and Postgres also drops it if the connection dies.
func withMigrationLock(ctx context.Context, pool *pgxpool.Pool, fn func() error) (err error) {
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("migration lock: %w", err)
	}
	defer conn.Release()
	if _, err = conn.Exec(ctx, "SELECT pg_advisory_lock($1)", migrationLockKey); err != nil {
		return fmt.Errorf("migration lock: %w", err)
	}
	defer func() {
		// Unlock on a fresh context so a cancelled ctx does not leave the lock held on a pooled connection.
		if _, uerr := conn.Exec(context.WithoutCancel(ctx), "SELECT pg_advisory_unlock($1)", migrationLockKey); uerr != nil && err == nil {
			err = fmt.Errorf("migration unlock: %w", uerr)
		}
	}()
	return fn()
}
