package main

import (
	"context"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// TestMigrationLockSerializesReplicas starts the schema migration from several pools at once, as
// several replicas do on a rollout. Without the lock Postgres fails one with a duplicate key
// error on pg_type. Needs AGENTGW_TEST_POSTGRES_URL (docker compose run --rm dev).
func TestMigrationLockSerializesReplicas(t *testing.T) {
	url := os.Getenv("AGENTGW_TEST_POSTGRES_URL")
	if url == "" {
		t.Skip("AGENTGW_TEST_POSTGRES_URL not set; run through docker compose run --rm dev")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	admin, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	// A scratch table the "migration" creates, dropped first so every run starts clean.
	const table = "migratelock_probe"
	if _, err := admin.Exec(ctx, "DROP TABLE IF EXISTS "+table); err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = admin.Exec(context.Background(), "DROP TABLE IF EXISTS "+table) }()

	const replicas = 12
	var wg sync.WaitGroup
	errs := make(chan error, replicas)
	for i := 0; i < replicas; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			pool, err := pgxpool.New(ctx, url)
			if err != nil {
				errs <- err
				return
			}
			defer pool.Close()
			errs <- withMigrationLock(ctx, pool, func() error {
				_, err := pool.Exec(ctx, "CREATE TABLE IF NOT EXISTS "+table+" (id bigserial PRIMARY KEY, v text)")
				return err
			})
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Errorf("a replica failed to migrate: %v", err)
		}
	}
}
