package auth_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Moustafa-Elgammal/agentgw/internal/auth"
	"github.com/Moustafa-Elgammal/agentgw/internal/studiosig"
	"github.com/Moustafa-Elgammal/agentgw/pkg/agentsig"
)

// studioKey and studioSecret are shared with server_test.go (same package).
const studioTen = "game"

type studioEnv struct {
	st *auth.Memory
	rs *auth.ResourceServer
	h  http.Handler
}

// newStudioEnv serves the studio endpoints over st, or over a fresh memory store when st is nil.
func newStudioEnv(t *testing.T, st auth.Store) *studioEnv {
	t.Helper()
	mem := auth.NewMemory(nil)
	if st == nil {
		st = mem
	}
	srv := auth.NewServer(auth.ServerConfig{Store: st, Issuer: "https://gw.example", Resource: resource,
		Tenant: studioTen, StudioKeyID: studioKey, StudioSecret: studioSecret})
	rs := &auth.ResourceServer{Store: st, Resource: resource, Tenant: studioTen, MetadataURL: "https://gw.example/m"}
	return &studioEnv{st: mem, rs: rs, h: srv.StudioHandler()}
}

func (e *studioEnv) grant(t *testing.T, tenant, player, client string, scope auth.Scope) auth.Grant {
	t.Helper()
	g, err := e.st.UpsertGrant(context.Background(), auth.Grant{Tenant: tenant, Player: player, ClientID: client, Resource: resource, Scope: scope})
	if err != nil {
		t.Fatal(err)
	}
	return g
}

func (e *studioEnv) authed(tok string) bool {
	_, err := e.rs.Authenticate(authReq("Bearer " + tok))
	return err == nil
}

var idemSeq atomic.Int64

// signed builds a studio request with a fresh idempotency key, as the replay rule requires.
func signed(method, path, player string) *http.Request {
	r := httptest.NewRequest(method, path, nil)
	agentsig.Sign(r, studioKey, studioSecret, player, "idem-"+strconv.FormatInt(idemSeq.Add(1), 10), nil, time.Now())
	return r
}

func (e *studioEnv) do(r *http.Request) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	e.h.ServeHTTP(w, r)
	return w
}

type listBody struct {
	Grants []map[string]any `json:"grants"`
}

func TestStudioList(t *testing.T) {
	e := newStudioEnv(t, nil)
	ctx := context.Background()
	_ = e.st.RegisterClient(ctx, auth.Client{ID: "c1", Name: "Claude on the laptop"})
	a1 := e.grant(t, studioTen, "alice", "c1", auth.ScopeOperator)
	e.grant(t, studioTen, "alice", "gone", auth.ScopeCoach) // client not registered
	e.grant(t, studioTen, "bob", "c1", auth.ScopeCoach)
	e.grant(t, "elsewhere", "alice", "c1", auth.ScopeCoach)

	tests := []struct {
		player string
		want   int
	}{{"alice", 2}, {"bob", 1}, {"carol", 0}}
	for _, tc := range tests {
		t.Run(tc.player, func(t *testing.T) {
			w := e.do(signed("GET", "/v1/studio/grants", tc.player))
			if w.Code != 200 || w.Header().Get("Cache-Control") != "no-store" {
				t.Fatalf("code=%d cache=%q", w.Code, w.Header().Get("Cache-Control"))
			}
			var b listBody
			if err := json.Unmarshal(w.Body.Bytes(), &b); err != nil {
				t.Fatal(err)
			}
			if b.Grants == nil || len(b.Grants) != tc.want {
				t.Fatalf("grants=%v want %d (non-nil)", b.Grants, tc.want)
			}
		})
	}

	w := e.do(signed("GET", "/v1/studio/grants", "alice"))
	var b listBody
	_ = json.Unmarshal(w.Body.Bytes(), &b)
	first, second := b.Grants[0], b.Grants[1]
	if first["id"] != a1.ID || first["client_name"] != "Claude on the laptop" || first["scope"] != "operator" || first["resource"] != resource || first["client_id"] != "c1" || first["created_at"] == "" {
		t.Fatalf("first=%v", first)
	}
	if second["client_name"] != "" {
		t.Fatalf("missing client should have empty name: %v", second)
	}
	if w := e.do(signed("GET", "/v1/studio/grants", "carol")); w.Body.String() != "{\"grants\":[]}\n" {
		t.Fatalf("empty body %q", w.Body.String())
	}
}

func TestStudioRevokeOne(t *testing.T) {
	e := newStudioEnv(t, nil)
	alice := e.grant(t, studioTen, "alice", "c1", auth.ScopeOperator)
	bob := e.grant(t, studioTen, "bob", "c1", auth.ScopeOperator)
	aTok, bTok := issue(t, e.st, alice), issue(t, e.st, bob)

	tests := []struct {
		name, player, id string
		code             int
	}{
		{"another player's grant", "alice", bob.ID, 404},
		{"unknown id", "alice", "nope", 404},
		{"own grant", "alice", alice.ID, 204},
		{"again is not found", "alice", alice.ID, 404},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			w := e.do(signed("DELETE", "/v1/studio/grants/"+tc.id, tc.player))
			if w.Code != tc.code || w.Header().Get("Cache-Control") != "no-store" {
				t.Fatalf("code=%d want %d", w.Code, tc.code)
			}
			if tc.code == 404 && w.Body.String() != "{\"error\":\"not_found\"}\n" {
				t.Fatalf("body %q", w.Body.String())
			}
		})
	}
	if e.authed(aTok) {
		t.Fatal("revoked grant's token still authenticates")
	}
	if !e.authed(bTok) {
		t.Fatal("other player's token must still work")
	}
}

func TestStudioRevokeAll(t *testing.T) {
	e := newStudioEnv(t, nil)
	a1 := e.grant(t, studioTen, "alice", "c1", auth.ScopeOperator)
	a2 := e.grant(t, studioTen, "alice", "c2", auth.ScopeCoach)
	bob := e.grant(t, studioTen, "bob", "c1", auth.ScopeOperator)
	t1, t2, tb := issue(t, e.st, a1), issue(t, e.st, a2), issue(t, e.st, bob)

	for i, want := range []string{"{\"revoked\":2}\n", "{\"revoked\":0}\n"} {
		w := e.do(signed("DELETE", "/v1/studio/grants", "alice"))
		if w.Code != 200 || w.Body.String() != want {
			t.Fatalf("call %d: %d %q", i, w.Code, w.Body.String())
		}
	}
	if e.authed(t1) || e.authed(t2) {
		t.Fatal("tokens survived revoke all")
	}
	if !e.authed(tb) {
		t.Fatal("bob's token must survive")
	}
}

func TestStudioBadSignature(t *testing.T) {
	e := newStudioEnv(t, nil)
	g := e.grant(t, studioTen, "alice", "c1", auth.ScopeOperator)
	tok := issue(t, e.st, g)

	sign := func(key string, secret []byte, path string, at time.Time) *http.Request {
		r := httptest.NewRequest("DELETE", path, nil)
		agentsig.Sign(r, key, secret, "alice", "i", nil, at)
		return r
	}
	tests := []struct {
		name string
		req  func() *http.Request
	}{
		{"wrong secret", func() *http.Request { return sign(studioKey, []byte("x"), "/v1/studio/grants", time.Now()) }},
		{"wrong key id", func() *http.Request { return sign("other", studioSecret, "/v1/studio/grants", time.Now()) }},
		{"no headers", func() *http.Request { return httptest.NewRequest("DELETE", "/v1/studio/grants", nil) }},
		{"stale timestamp", func() *http.Request {
			return sign(studioKey, studioSecret, "/v1/studio/grants", time.Now().Add(-time.Hour))
		}},
		{"tampered path", func() *http.Request {
			r := sign(studioKey, studioSecret, "/v1/studio/grants/"+g.ID, time.Now())
			r.URL.Path = "/v1/studio/grants"
			return r
		}},
		{"tampered player", func() *http.Request {
			r := sign(studioKey, studioSecret, "/v1/studio/grants", time.Now())
			r.Header.Set(agentsig.HeaderPlayer, "bob")
			return r
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			w := e.do(tc.req())
			if w.Code != 401 || w.Body.String() != "{\"error\":\"invalid_client\"}\n" || w.Header().Get("Cache-Control") != "no-store" {
				t.Fatalf("code=%d body=%q", w.Code, w.Body.String())
			}
		})
	}
	if w := e.do(httptest.NewRequest("GET", "/v1/studio/grants", nil)); w.Code != 401 {
		t.Fatalf("unsigned GET code=%d", w.Code)
	}
	if !e.authed(tok) {
		t.Fatal("a rejected request revoked a grant")
	}
}

// failingStore errors on the calls the studio endpoints make.
type failingStore struct {
	*auth.Memory
	failGrants, failRevoke, failClient bool
}

var errBoom = errors.New("store down")

func (f *failingStore) Grants(ctx context.Context, tenant, player string) ([]auth.Grant, error) {
	if f.failGrants {
		return nil, errBoom
	}
	return f.Memory.Grants(ctx, tenant, player)
}

func (f *failingStore) RevokeGrant(ctx context.Context, tenant, player, id string) error {
	if f.failRevoke {
		return errBoom
	}
	return f.Memory.RevokeGrant(ctx, tenant, player, id)
}

func (f *failingStore) Client(ctx context.Context, id string) (auth.Client, error) {
	if f.failClient {
		return auth.Client{}, errBoom
	}
	return f.Memory.Client(ctx, id)
}

func TestStudioStoreFailure(t *testing.T) {
	tests := []struct {
		name         string
		fs           failingStore
		method, path string
	}{
		{"list grants", failingStore{failGrants: true}, "GET", "/v1/studio/grants"},
		{"list client", failingStore{failClient: true}, "GET", "/v1/studio/grants"},
		{"revoke one", failingStore{failRevoke: true}, "DELETE", "/v1/studio/grants/x"},
		{"revoke all grants", failingStore{failGrants: true}, "DELETE", "/v1/studio/grants"},
		{"revoke all revoke", failingStore{failRevoke: true}, "DELETE", "/v1/studio/grants"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fs := tc.fs
			fs.Memory = auth.NewMemory(nil)
			_, _ = fs.UpsertGrant(context.Background(), auth.Grant{Tenant: studioTen, Player: "alice", ClientID: "c1", Resource: resource, Scope: auth.ScopeCoach})
			e := newStudioEnv(t, &fs)
			w := e.do(signed(tc.method, tc.path, "alice"))
			if w.Code != 503 || w.Body.String() != "{\"error\":\"temporarily_unavailable\"}\n" || w.Header().Get("Cache-Control") != "no-store" {
				t.Fatalf("code=%d body=%q", w.Code, w.Body.String())
			}
		})
	}
}

// replay sends r's exact headers again, as an attacker who captured the request would.
func replay(r *http.Request) *http.Request {
	c := httptest.NewRequest(r.Method, r.URL.RequestURI(), nil)
	c.Header = r.Header.Clone()
	return c
}

func TestStudioReplayRefusedOnEveryEndpoint(t *testing.T) {
	tests := []struct {
		name   string
		method string
		path   string
		first  int
	}{
		{"list", "GET", "/v1/studio/grants", 200},
		{"revoke one", "DELETE", "/v1/studio/grants/nope", 404},
		{"revoke all", "DELETE", "/v1/studio/grants", 200},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newStudioEnv(t, nil)
			r := signed(tt.method, tt.path, "alice")
			if w := e.do(replay(r)); w.Code != tt.first {
				t.Fatalf("first: %d %s", w.Code, w.Body)
			}
			w := e.do(replay(r))
			if w.Code != 401 || w.Body.String() != "{\"error\":\"invalid_client\"}\n" {
				t.Fatalf("replay: %d %s", w.Code, w.Body)
			}
		})
	}
	t.Run("link ticket", func(t *testing.T) {
		srv := auth.NewServer(auth.ServerConfig{Store: auth.NewMemory(nil), Issuer: "https://gw.example",
			Resource: resource, Tenant: studioTen, StudioKeyID: studioKey, StudioSecret: studioSecret})
		r := signed("POST", "/v1/studio/link-tickets", "alice")
		do := func() int {
			w := httptest.NewRecorder()
			srv.Handler().ServeHTTP(w, replay(r))
			return w.Code
		}
		if c := do(); c != 201 {
			t.Fatalf("first: %d", c)
		}
		if c := do(); c != 401 {
			t.Fatalf("replay: %d", c)
		}
	})
}

func TestStudioReplayAcrossEndpointsWithSharedVerifier(t *testing.T) {
	v := studiosig.New(studiosig.Config{KeyID: studioKey, Secret: studioSecret})
	srv := auth.NewServer(auth.ServerConfig{Store: auth.NewMemory(nil), Issuer: "https://gw.example",
		Resource: resource, Tenant: studioTen, StudioKeyID: studioKey, StudioSecret: studioSecret, Verifier: v})
	serve := func(h http.Handler, r *http.Request) int {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w.Code
	}
	r := signed("GET", "/v1/studio/grants", "alice")
	if c := serve(srv.StudioHandler(), replay(r)); c != 200 {
		t.Fatalf("first: %d", c)
	}
	// The same idempotency key presented to the ticket endpoint, freshly signed, is refused.
	other := httptest.NewRequest("POST", "/v1/studio/link-tickets", nil)
	agentsig.Sign(other, studioKey, studioSecret, "alice", r.Header.Get(agentsig.HeaderIdempotency), nil, time.Now())
	if c := serve(srv.Handler(), other); c != 401 {
		t.Fatalf("cross endpoint reuse: %d", c)
	}
}
