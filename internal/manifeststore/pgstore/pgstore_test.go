package pgstore

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Moustafa-Elgammal/agentgw/internal/manifeststore"
	"github.com/Moustafa-Elgammal/agentgw/internal/manifeststore/storetest"
)

// These tests need a real Postgres: docker compose run --rm dev starts one and sets
// AGENTGW_TEST_POSTGRES_URL. Without it they skip. Each test gets its own schema.
func testStore(t *testing.T, now func() time.Time) *Store {
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
	s := New(pool, now)
	if err := s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestPostgresContract(t *testing.T) {
	storetest.Run(t, func(t *testing.T, now func() time.Time) manifeststore.Store { return testStore(t, now) })
}

func TestMigrateIsIdempotent(t *testing.T) {
	s := testStore(t, nil)
	for i := 0; i < 2; i++ {
		if err := s.Migrate(context.Background()); err != nil {
			t.Fatalf("migrate #%d: %v", i+2, err)
		}
	}
}

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
	s := New(pool, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if _, err := s.Put(ctx, "t", "a", []byte(`{}`), 1); err == nil {
		t.Error("Put must error")
	}
	if err := s.Activate(ctx, "t", 1); err == nil || errors.Is(err, manifeststore.ErrNotFound) {
		t.Errorf("Activate must error with a store failure, got %v", err)
	}
	if _, _, err := s.Active(ctx, "t"); err == nil || errors.Is(err, manifeststore.ErrNotFound) {
		t.Errorf("Active must error with a store failure, got %v", err)
	}
	if _, _, err := s.Get(ctx, "t", 1); err == nil || errors.Is(err, manifeststore.ErrNotFound) {
		t.Errorf("Get must error with a store failure, got %v", err)
	}
	if _, err := s.List(ctx, "t"); err == nil {
		t.Error("List must error")
	}
}
