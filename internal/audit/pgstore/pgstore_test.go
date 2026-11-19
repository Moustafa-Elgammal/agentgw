package pgstore

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Moustafa-Elgammal/agentgw/internal/audit"
	"github.com/Moustafa-Elgammal/agentgw/internal/audit/auditest"
)

// These tests need a real Postgres: docker compose run --rm dev starts one and sets
// AGENTGW_TEST_POSTGRES_URL. Without it they skip. Each test gets its own schema.
func testStore(t *testing.T) *Store {
	t.Helper()
	url := os.Getenv("AGENTGW_TEST_POSTGRES_URL")
	if url == "" {
		t.Skip("AGENTGW_TEST_POSTGRES_URL not set; run through docker compose run --rm dev")
	}
	ctx := context.Background()
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	schema := "t_" + hex.EncodeToString(b)

	admin, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatalf("create schema: %v", err)
	}
	t.Cleanup(func() {
		_, _ = admin.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE")
		admin.Close()
	})

	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	s := New(pool)
	if err := s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestPostgresContract(t *testing.T) {
	auditest.Run(t, func(t *testing.T) audit.Store { return testStore(t) })
}

func TestMigrateIsIdempotent(t *testing.T) {
	s := testStore(t)
	for i := 0; i < 2; i++ {
		if err := s.Migrate(context.Background()); err != nil {
			t.Fatalf("migrate #%d: %v", i+2, err)
		}
	}
}

// With the database unreachable every call errors, Record reports through OnError and
// returns quickly without panicking.
func TestFailsClosedWhenPostgresIsDown(t *testing.T) {
	cfg, err := pgxpool.ParseConfig("postgres://u:p@127.0.0.1:1/db?connect_timeout=1")
	if err != nil {
		t.Fatal(err)
	}
	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	s := New(pool)
	var errs atomic.Int32
	s.OnError = func(error) { errs.Add(1) }
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	start := time.Now()
	s.Record(ctx, audit.Event{Tenant: "t", Player: "p"})
	if d := time.Since(start); d > recordTimeout+time.Second {
		t.Errorf("Record blocked for %v", d)
	}
	if errs.Load() != 1 {
		t.Errorf("OnError called %d times, want 1", errs.Load())
	}
	if _, err := s.Query(ctx, audit.Query{Tenant: "t"}); err == nil {
		t.Error("Query must error")
	}
	if _, err := s.Prune(ctx, time.Now()); err == nil {
		t.Error("Prune must error")
	}
	s2 := New(pool) // no OnError: must not panic
	s2.Record(ctx, audit.Event{})
}
