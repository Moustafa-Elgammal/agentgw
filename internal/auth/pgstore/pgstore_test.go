package pgstore

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Moustafa-Elgammal/agentgw/internal/auth"
	"github.com/Moustafa-Elgammal/agentgw/internal/auth/authtest"
)

// These tests need a real Postgres: docker compose run --rm dev make test starts
// one and sets AGENTGW_TEST_POSTGRES_URL. Each test gets its own schema, dropped
// afterwards. Without the variable they fail instead of skipping, so a missing
// database cannot hide a broken store.
func testStore(t *testing.T, now func() time.Time) *Store {
	t.Helper()
	url := os.Getenv("AGENTGW_TEST_POSTGRES_URL")
	if url == "" {
		t.Fatal("AGENTGW_TEST_POSTGRES_URL not set; run through docker compose run --rm dev")
	}
	ctx := context.Background()
	schema := "t_" + auth.NewID()[:12]

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
	cfg.MaxConns = 32
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
	authtest.Run(t, func(t *testing.T, now func() time.Time) auth.Store { return testStore(t, now) })
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
func TestStateSurvivesANewStore(t *testing.T) {
	ctx := context.Background()
	a := testStore(t, nil)
	if err := a.RegisterClient(ctx, auth.Client{ID: "c1", Name: "Agent", RedirectURIs: []string{"http://127.0.0.1/cb"}}); err != nil {
		t.Fatal(err)
	}
	g, err := a.UpsertGrant(ctx, auth.Grant{Tenant: "t", Player: "alice", ClientID: "c1", Resource: "r", Scope: auth.ScopeOperator})
	if err != nil {
		t.Fatal(err)
	}
	_, hash := auth.NewToken()
	if err := a.PutAccess(ctx, hash, g.ID, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	b := New(a.db, nil)
	if _, err := b.Client(ctx, "c1"); err != nil {
		t.Fatalf("client: %v", err)
	}
	got, err := b.Access(ctx, hash)
	if err != nil || got.ID != g.ID {
		t.Fatalf("the other replica must see the token: %+v %v", got, err)
	}
	if err := b.RevokeGrant(ctx, "t", "alice", g.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Access(ctx, hash); !errors.Is(err, auth.ErrNotFound) {
		t.Fatalf("a revocation through one replica must reach the other: %v", err)
	}
}

func count(t *testing.T, s *Store, table string) int {
	t.Helper()
	var n int
	if err := s.db.QueryRow(context.Background(), "SELECT count(*) FROM "+table).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestPruneDeletesOnlyWhatIsDead(t *testing.T) {
	ctx := context.Background()
	start := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)
	clock := start
	s := testStore(t, func() time.Time { return clock })

	live, _ := s.UpsertGrant(ctx, auth.Grant{Tenant: "t", Player: "alice", ClientID: "c1", Resource: "r", Scope: auth.ScopeCoach})
	revoked, _ := s.UpsertGrant(ctx, auth.Grant{Tenant: "t", Player: "alice", ClientID: "c2", Resource: "r", Scope: auth.ScopeCoach})
	short, long := start.Add(time.Minute), start.Add(90*24*time.Hour)
	_ = s.PutTicket(ctx, "tk-short", "t", "alice", short)
	_ = s.PutTicket(ctx, "tk-long", "t", "alice", long)
	_ = s.PutCode(ctx, auth.Code{Hash: "code-short", Scope: auth.ScopeCoach, Expires: short})
	_ = s.PutCode(ctx, auth.Code{Hash: "code-long", Scope: auth.ScopeCoach, Expires: long})
	_ = s.PutAccess(ctx, "acc-short", live.ID, short)
	_ = s.PutAccess(ctx, "acc-long", live.ID, long)
	_ = s.PutRefresh(ctx, "ref-short", live.ID, short)
	_ = s.PutRefresh(ctx, "ref-long", live.ID, long)
	_ = s.PutAccess(ctx, "acc-revoked-grant", revoked.ID, long)
	if err := s.RevokeGrant(ctx, "t", "alice", revoked.ID); err != nil {
		t.Fatal(err)
	}

	if n, err := s.Prune(ctx); err != nil || n != 0 {
		t.Fatalf("early prune = %d, %v; nothing is dead yet", n, err)
	}

	// 31 days on: the short rows are expired and the revoked grant is past retention.
	clock = start.Add(RevokedRetention + 24*time.Hour)
	n, err := s.Prune(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// 1+1+1+1 short rows, 1 revoked grant, 1 cascaded access token.
	if n != 5 {
		t.Fatalf("pruned %d rows, want 5 directly counted (4 short rows + 1 grant)", n)
	}
	for table, want := range map[string]int{
		"oauth_tickets": 1, "oauth_codes": 1, "oauth_access_tokens": 1, "oauth_refresh_tokens": 1, "oauth_grants": 1,
	} {
		if got := count(t, s, table); got != want {
			t.Errorf("%s has %d rows after prune, want %d", table, got, want)
		}
	}
	if _, err := s.Access(ctx, "acc-long"); err != nil {
		t.Fatalf("a live token must survive prune: %v", err)
	}
}

func TestPruneKeepsRotatedTokensAndRecentRevocations(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)
	s := testStore(t, func() time.Time { return now })
	g, _ := s.UpsertGrant(ctx, auth.Grant{Tenant: "t", Player: "alice", ClientID: "c1", Resource: "r", Scope: auth.ScopeCoach})
	_ = s.PutRefresh(ctx, "r1", g.ID, now.Add(time.Hour))
	if _, err := s.RotateRefresh(ctx, "r1", "r2", now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	recent, _ := s.UpsertGrant(ctx, auth.Grant{Tenant: "t", Player: "bob", ClientID: "c1", Resource: "r", Scope: auth.ScopeCoach})
	_ = s.RevokeGrant(ctx, "t", "bob", recent.ID)
	if n, err := s.Prune(ctx); err != nil || n != 0 {
		t.Fatalf("prune = %d, %v; live rows, rotated tokens and recent revocations must stay", n, err)
	}
	// The rotated token is still known, so replaying it is detected as reuse.
	if _, err := s.RotateRefresh(ctx, "r1", "evil", now.Add(time.Hour)); !errors.Is(err, auth.ErrReuse) {
		t.Fatalf("want ErrReuse, got %v", err)
	}
}

// Nothing secret reaches the database: the store is only ever given hashes, and a
// full dump of every table must not contain the plaintext.
func TestNoPlaintextIsStored(t *testing.T) {
	ctx := context.Background()
	s := testStore(t, nil)
	plainAcc, hAcc := auth.NewToken()
	plainRef, hRef := auth.NewToken()
	plainCode, hCode := auth.NewToken()
	plainTicket, hTicket := auth.NewToken()
	g, err := s.UpsertGrant(ctx, auth.Grant{Tenant: "t", Player: "alice", ClientID: "c1", Resource: "r", Scope: auth.ScopeCoach})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.RegisterClient(ctx, auth.Client{ID: "c1", Name: "Agent"}); err != nil {
		t.Fatal(err)
	}
	exp := time.Now().Add(time.Hour)
	for _, err := range []error{
		s.PutAccess(ctx, hAcc, g.ID, exp),
		s.PutRefresh(ctx, hRef, g.ID, exp),
		s.PutCode(ctx, auth.Code{Hash: hCode, ClientID: "c1", Scope: auth.ScopeCoach, Expires: exp}),
		s.PutTicket(ctx, hTicket, "t", "alice", exp),
	} {
		if err != nil {
			t.Fatal(err)
		}
	}
	rows, err := s.db.Query(ctx, `SELECT table_name FROM information_schema.tables
		WHERE table_schema = current_schema() AND table_type = 'BASE TABLE'`)
	if err != nil {
		t.Fatal(err)
	}
	var tables []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			t.Fatal(err)
		}
		tables = append(tables, n)
	}
	rows.Close()
	if len(tables) != 6 {
		t.Fatalf("expected 6 tables, found %v", tables)
	}
	seenHash := 0
	for _, table := range tables {
		r, err := s.db.Query(ctx, `SELECT t::text FROM `+table+` t`)
		if err != nil {
			t.Fatal(err)
		}
		for r.Next() {
			var row string
			if err := r.Scan(&row); err != nil {
				t.Fatal(err)
			}
			for _, plain := range []string{plainAcc, plainRef, plainCode, plainTicket} {
				if strings.Contains(row, plain) {
					t.Fatalf("plaintext secret found in %s: %s", table, row)
				}
			}
			for _, h := range []string{hAcc, hRef, hCode, hTicket} {
				if strings.Contains(row, h) {
					seenHash++
				}
			}
		}
		r.Close()
	}
	if seenHash != 4 {
		t.Fatalf("the dump should contain the 4 hashes, found %d", seenHash)
	}
}

// With the database unreachable every call errors and none returns a usable value.
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
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	exp := time.Now().Add(time.Hour)

	// A transport failure must be a real error, never ErrNotFound, so callers can tell
	// "denied" from "could not check".
	real := func(name string, err error) {
		t.Helper()
		if err == nil || errors.Is(err, auth.ErrNotFound) || errors.Is(err, auth.ErrReuse) {
			t.Errorf("%s must fail with a transport error, got %v", name, err)
		}
	}
	real("PutTicket", s.PutTicket(ctx, "h", "t", "p", exp))
	tn, pl, err := s.TakeTicket(ctx, "h")
	real("TakeTicket", err)
	if tn != "" || pl != "" {
		t.Errorf("TakeTicket returned an identity: %q %q", tn, pl)
	}
	real("RegisterClient", s.RegisterClient(ctx, auth.Client{ID: "c"}))
	c, err := s.Client(ctx, "c")
	real("Client", err)
	if c.ID != "" {
		t.Errorf("Client returned %+v", c)
	}
	real("PutCode", s.PutCode(ctx, auth.Code{Hash: "h", Expires: exp}))
	code, err := s.TakeCode(ctx, "h")
	real("TakeCode", err)
	if code.Player != "" || code.Scope != "" {
		t.Errorf("TakeCode returned %+v", code)
	}
	g, err := s.UpsertGrant(ctx, auth.Grant{Tenant: "t", Player: "p", Scope: auth.ScopeOperator})
	real("UpsertGrant", err)
	if g.Scope != "" {
		t.Errorf("UpsertGrant returned a usable grant: %+v", g)
	}
	gs, err := s.Grants(ctx, "t", "p")
	real("Grants", err)
	if len(gs) != 0 {
		t.Errorf("Grants returned %+v", gs)
	}
	real("RevokeGrant", s.RevokeGrant(ctx, "t", "p", "g"))
	real("PutAccess", s.PutAccess(ctx, "h", "g", exp))
	ag, err := s.Access(ctx, "h")
	real("Access", err)
	if ag.ID != "" {
		t.Errorf("Access returned a grant: %+v", ag)
	}
	real("PutRefresh", s.PutRefresh(ctx, "h", "g", exp))
	rg, err := s.RotateRefresh(ctx, "h", "n", exp)
	real("RotateRefresh", err)
	if rg.ID != "" {
		t.Errorf("RotateRefresh returned a grant: %+v", rg)
	}
	real("Migrate", s.Migrate(ctx))
	_, err = s.Prune(ctx)
	real("Prune", err)
}
