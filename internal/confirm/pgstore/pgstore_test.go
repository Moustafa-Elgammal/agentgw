package pgstore

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Moustafa-Elgammal/agentgw/internal/confirm"
	"github.com/Moustafa-Elgammal/agentgw/internal/confirm/confirmtest"
	"github.com/Moustafa-Elgammal/agentgw/internal/core"
)

// These tests need a real Postgres: docker compose run --rm dev make test starts
// one and sets AGENTGW_TEST_POSTGRES_URL. Without it they skip. Each test gets its
// own schema, dropped afterwards.
func testStore(t *testing.T, now func() time.Time) *Store {
	t.Helper()
	url := os.Getenv("AGENTGW_TEST_POSTGRES_URL")
	if url == "" {
		t.Skip("AGENTGW_TEST_POSTGRES_URL not set; run through docker compose run --rm dev")
	}
	ctx := context.Background()
	schema := "t_" + confirm.NewID()[:12]

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
	confirmtest.Run(t, func(t *testing.T, now func() time.Time) confirm.Store { return testStore(t, now) })
}

func TestMigrateIsIdempotent(t *testing.T) {
	s := testStore(t, nil)
	for i := 0; i < 2; i++ {
		if err := s.Migrate(context.Background()); err != nil {
			t.Fatalf("migrate #%d: %v", i+2, err)
		}
	}
}

// A second store on the same database stands in for a second replica or a restart.
func TestApprovalSurvivesANewStore(t *testing.T) {
	a := testStore(t, nil)
	p := core.Principal{TenantID: "t", PlayerID: "alice"}
	ctx := context.Background()
	g, err := a.Gate(ctx, p, "build", "sha")
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Approve(ctx, p, g.ID); err != nil {
		t.Fatal(err)
	}
	b := New(a.db, nil) // same database, fresh store
	g2, err := b.Gate(ctx, p, "build", "sha")
	if err != nil || g2.Status != confirm.StatusClaimed || g2.ID != g.ID {
		t.Fatalf("the other replica must see the approval: %+v %v", g2, err)
	}
}

// With the database unreachable every call errors and none succeeds.
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
	p := core.Principal{TenantID: "t", PlayerID: "alice"}
	if g, err := s.Gate(ctx, p, "build", "sha"); err == nil || g.Status == confirm.StatusClaimed {
		t.Errorf("Gate must error, got %+v %v", g, err)
	}
	if err := s.Approve(ctx, p, "x"); err == nil {
		t.Error("Approve must error")
	}
	if _, err := s.Pending(ctx, p); err == nil {
		t.Error("Pending must error")
	}
}
