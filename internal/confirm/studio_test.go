package confirm_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Moustafa-Elgammal/agentgw/internal/audit"
	"github.com/Moustafa-Elgammal/agentgw/internal/confirm"
	"github.com/Moustafa-Elgammal/agentgw/internal/connector"
	"github.com/Moustafa-Elgammal/agentgw/internal/core"
	"github.com/Moustafa-Elgammal/agentgw/internal/gateway"
	"github.com/Moustafa-Elgammal/agentgw/internal/manifest"
	"github.com/Moustafa-Elgammal/agentgw/internal/policy"
	"github.com/Moustafa-Elgammal/agentgw/internal/studiosig"
	"github.com/Moustafa-Elgammal/agentgw/pkg/agentsig"
)

const (
	keyID  = "studio-1"
	tenant = "t1"
)

var secret = []byte("studio-secret")

type clock struct{ t atomic.Int64 }

func newClock() *clock {
	c := &clock{}
	c.t.Store(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC).Unix())
	return c
}
func (c *clock) now() time.Time          { return time.Unix(c.t.Load(), 0) }
func (c *clock) advance(d time.Duration) { c.t.Add(int64(d.Seconds())) }

func pr(player string) core.Principal { return core.Principal{TenantID: tenant, PlayerID: player} }

var idemSeq atomic.Int64

// do sends a request signed as the studio would, with a fresh idempotency key.
func do(t *testing.T, h http.Handler, now time.Time, method, path, player string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, nil)
	agentsig.Sign(req, keyID, secret, player, "idem-"+strconv.FormatInt(idemSeq.Add(1), 10), nil, now)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func newHandler(store confirm.Store, c *clock) http.Handler {
	return confirm.StudioHandler(confirm.StudioConfig{Store: store, Tenant: tenant, KeyID: keyID, Secret: secret, Now: c.now})
}

func mustGate(t *testing.T, s confirm.Store, p core.Principal, tool string) string {
	t.Helper()
	g, err := s.Gate(context.Background(), p, tool, "sha-"+tool)
	if err != nil || g.Status != confirm.StatusCreated {
		t.Fatalf("gate: %+v %v", g, err)
	}
	return g.ID
}

func listIDs(t *testing.T, rec *httptest.ResponseRecorder) []string {
	t.Helper()
	if rec.Code != 200 {
		t.Fatalf("list status %d: %s", rec.Code, rec.Body)
	}
	var out struct {
		C []struct {
			ID        string `json:"id"`
			Tool      string `json:"tool"`
			State     string `json:"state"`
			CreatedAt string `json:"created_at"`
			ExpiresAt string `json:"expires_at"`
			ArgsSHA   string `json:"args_sha256"`
		} `json:"confirmations"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	ids := []string{}
	for _, c := range out.C {
		if c.State != "pending" || c.Tool == "" || c.ArgsSHA == "" || c.CreatedAt == "" || c.ExpiresAt == "" {
			t.Fatalf("bad item %+v", c)
		}
		ids = append(ids, c.ID)
	}
	return ids
}

func TestListScoping(t *testing.T) {
	clk := newClock()
	store := confirm.NewMemory(clk.now)
	a1 := mustGate(t, store, pr("alice"), "build")
	clk.advance(time.Second)
	a2 := mustGate(t, store, pr("alice"), "collect")
	mustGate(t, store, pr("bob"), "build")
	mustGate(t, store, core.Principal{TenantID: "other", PlayerID: "alice"}, "build")
	h := newHandler(store, clk)

	rec := do(t, h, clk.now(), "GET", "/v1/studio/confirmations", "alice")
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Error("missing no-store")
	}
	ids := listIDs(t, rec)
	if len(ids) != 2 || ids[0] != a1 || ids[1] != a2 {
		t.Fatalf("alice sees %v, want [%s %s]", ids, a1, a2)
	}

	rec = do(t, h, clk.now(), "GET", "/v1/studio/confirmations", "nobody")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"confirmations":[]`) {
		t.Fatalf("empty list must be [], got %d %s", rec.Code, rec.Body)
	}
}

func TestDecide(t *testing.T) {
	const base = "/v1/studio/confirmations/"
	tests := []struct {
		name   string
		steps  []string // verbs applied by the owner first
		verb   string
		player string
		id     string // "" means the owner's id
		want   int
		code   string
	}{
		{"approve", nil, "approve", "alice", "", 204, ""},
		{"deny", nil, "deny", "alice", "", 204, ""},
		{"approve twice", []string{"approve"}, "approve", "alice", "", 204, ""},
		{"deny twice", []string{"deny"}, "deny", "alice", "", 204, ""},
		{"approve after deny", []string{"deny"}, "approve", "alice", "", 409, "wrong_state"},
		{"deny after approve", []string{"approve"}, "deny", "alice", "", 204, ""},
		{"other player", nil, "approve", "bob", "", 404, "not_found"},
		{"unknown id", nil, "approve", "alice", "nope", 404, "not_found"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clk := newClock()
			store := confirm.NewMemory(clk.now)
			id := mustGate(t, store, pr("alice"), "build")
			h := newHandler(store, clk)
			for _, s := range tt.steps {
				if rec := do(t, h, clk.now(), "POST", base+id+"/"+s, "alice"); rec.Code != 204 {
					t.Fatalf("setup %s: %d", s, rec.Code)
				}
			}
			use := id
			if tt.id != "" {
				use = tt.id
			}
			rec := do(t, h, clk.now(), "POST", base+use+"/"+tt.verb, tt.player)
			if rec.Code != tt.want {
				t.Fatalf("status %d, want %d (%s)", rec.Code, tt.want, rec.Body)
			}
			if rec.Header().Get("Cache-Control") != "no-store" {
				t.Error("missing no-store")
			}
			if tt.code != "" && !strings.Contains(rec.Body.String(), `"`+tt.code+`"`) {
				t.Errorf("body %s lacks %s", rec.Body, tt.code)
			}
		})
	}
}

func TestApproveThenGateClaims(t *testing.T) {
	clk := newClock()
	store := confirm.NewMemory(clk.now)
	h := newHandler(store, clk)
	id := mustGate(t, store, pr("alice"), "build")

	// Another player's approve changes nothing.
	if rec := do(t, h, clk.now(), "POST", "/v1/studio/confirmations/"+id+"/approve", "bob"); rec.Code != 404 {
		t.Fatal(rec.Code)
	}
	g, _ := store.Gate(context.Background(), pr("alice"), "build", "sha-build")
	if g.Status != confirm.StatusPending {
		t.Fatalf("still pending expected, got %v", g.Status)
	}
	if rec := do(t, h, clk.now(), "POST", "/v1/studio/confirmations/"+id+"/approve", "alice"); rec.Code != 204 {
		t.Fatal(rec.Code)
	}
	g, _ = store.Gate(context.Background(), pr("alice"), "build", "sha-build")
	if g.Status != confirm.StatusClaimed || g.ID != id {
		t.Fatalf("want claimed %s, got %+v", id, g)
	}
}

func TestDenyThenGateDenied(t *testing.T) {
	clk := newClock()
	store := confirm.NewMemory(clk.now)
	h := newHandler(store, clk)
	id := mustGate(t, store, pr("alice"), "build")
	if rec := do(t, h, clk.now(), "POST", "/v1/studio/confirmations/"+id+"/deny", "alice"); rec.Code != 204 {
		t.Fatal(rec.Code)
	}
	g, _ := store.Gate(context.Background(), pr("alice"), "build", "sha-build")
	if g.Status != confirm.StatusDenied {
		t.Fatalf("want denied, got %v", g.Status)
	}
}

func TestExpiredIsNotFound(t *testing.T) {
	clk := newClock()
	store := confirm.NewMemory(clk.now)
	h := newHandler(store, clk)
	id := mustGate(t, store, pr("alice"), "build")
	clk.advance(confirm.TTL + time.Second)
	rec := do(t, h, clk.now(), "POST", "/v1/studio/confirmations/"+id+"/approve", "alice")
	if rec.Code != 404 {
		t.Fatalf("status %d", rec.Code)
	}
	if ids := listIDs(t, do(t, h, clk.now(), "GET", "/v1/studio/confirmations", "alice")); len(ids) != 0 {
		t.Fatalf("expired still listed: %v", ids)
	}
}

func TestSignatureFailures(t *testing.T) {
	clk := newClock()
	store := confirm.NewMemory(clk.now)
	h := newHandler(store, clk)
	id := mustGate(t, store, pr("alice"), "build")
	path := "/v1/studio/confirmations/" + id + "/approve"

	tests := []struct {
		name string
		mut  func(*http.Request)
		sign func(*http.Request)
	}{
		{"wrong secret", nil, func(r *http.Request) {
			agentsig.Sign(r, keyID, []byte("nope"), "alice", "i", nil, clk.now())
		}},
		{"wrong key id", nil, func(r *http.Request) {
			agentsig.Sign(r, "other", secret, "alice", "i", nil, clk.now())
		}},
		{"stale timestamp", nil, func(r *http.Request) {
			agentsig.Sign(r, keyID, secret, "alice", "i", nil, clk.now().Add(-10*time.Minute))
		}},
		{"future timestamp", nil, func(r *http.Request) {
			agentsig.Sign(r, keyID, secret, "alice", "i", nil, clk.now().Add(10*time.Minute))
		}},
		{"unsigned", nil, func(*http.Request) {}},
		{"missing player", func(r *http.Request) { r.Header.Del(agentsig.HeaderPlayer) }, nil},
		{"missing signature", func(r *http.Request) { r.Header.Del(agentsig.HeaderSignature) }, nil},
		{"missing idempotency", func(r *http.Request) { r.Header.Del(agentsig.HeaderIdempotency) }, nil},
		{"tampered player", func(r *http.Request) { r.Header.Set(agentsig.HeaderPlayer, "bob") }, nil},
		{"tampered path", func(r *http.Request) { r.URL.Path = "/v1/studio/confirmations/" + id + "/deny" }, nil},
		{"bearer only", func(r *http.Request) { r.Header.Set("Authorization", "Bearer abc") }, func(*http.Request) {}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest("POST", path, nil)
			if tt.sign != nil {
				tt.sign(req)
			} else {
				agentsig.Sign(req, keyID, secret, "alice", "i", nil, clk.now())
			}
			if tt.mut != nil {
				tt.mut(req)
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != 401 || !strings.Contains(rec.Body.String(), `"invalid_client"`) {
				t.Fatalf("got %d %s", rec.Code, rec.Body)
			}
			if rec.Header().Get("Cache-Control") != "no-store" {
				t.Error("missing no-store")
			}
			g, _ := store.Gate(context.Background(), pr("alice"), "build", "sha-build")
			if g.Status != confirm.StatusPending {
				t.Fatalf("a rejected request changed state: %v", g.Status)
			}
		})
	}

	t.Run("body tampered", func(t *testing.T) {
		req := httptest.NewRequest("POST", path, strings.NewReader("x"))
		agentsig.Sign(req, keyID, secret, "alice", "i", []byte("y"), clk.now())
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != 401 {
			t.Fatal(rec.Code)
		}
	})
	t.Run("body too large", func(t *testing.T) {
		big := strings.Repeat("a", 65<<10)
		req := httptest.NewRequest("POST", path, strings.NewReader(big))
		agentsig.Sign(req, keyID, secret, "alice", "i", []byte(big), clk.now())
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != 401 {
			t.Fatal(rec.Code)
		}
	})
	t.Run("empty secret config", func(t *testing.T) {
		h := confirm.StudioHandler(confirm.StudioConfig{Store: store, Tenant: tenant, KeyID: keyID, Now: clk.now})
		req := httptest.NewRequest("POST", path, nil)
		agentsig.Sign(req, keyID, nil, "alice", "i", nil, clk.now())
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != 401 {
			t.Fatal(rec.Code)
		}
	})
}

type failStore struct{ confirm.Store }

var errBoom = errors.New("boom")

func (failStore) Pending(context.Context, core.Principal) ([]confirm.Confirmation, error) {
	return nil, errBoom
}
func (failStore) Approve(context.Context, core.Principal, string) error { return errBoom }
func (failStore) Deny(context.Context, core.Principal, string) error    { return errBoom }

func TestStoreFailureIs503(t *testing.T) {
	clk := newClock()
	h := newHandler(failStore{confirm.NewMemory(clk.now)}, clk)
	for _, c := range []struct{ method, path string }{
		{"GET", "/v1/studio/confirmations"},
		{"POST", "/v1/studio/confirmations/x/approve"},
		{"POST", "/v1/studio/confirmations/x/deny"},
	} {
		rec := do(t, h, clk.now(), c.method, c.path, "alice")
		if rec.Code != 503 || !strings.Contains(rec.Body.String(), `"temporarily_unavailable"`) {
			t.Errorf("%s %s: %d %s", c.method, c.path, rec.Code, rec.Body)
		}
	}
}

func TestNilClockDefaults(t *testing.T) {
	store := confirm.NewMemory(nil)
	h := confirm.StudioHandler(confirm.StudioConfig{Store: store, Tenant: tenant, KeyID: keyID, Secret: secret})
	if rec := do(t, h, time.Now(), "GET", "/v1/studio/confirmations", "alice"); rec.Code != 200 {
		t.Fatal(rec.Code)
	}
}

const e2eDoc = `{"version":1,"tools":[
 {"name":"build","description":"Build.","tier":"medium","confirm":true,
  "http":{"method":"POST","url":"/build"},"params":{"x":{"type":"integer","min":0,"max":4}}}]}`

func TestEndToEndWithGateway(t *testing.T) {
	m, err := manifest.Parse(strings.NewReader(e2eDoc))
	if err != nil {
		t.Fatal(err)
	}
	var hits atomic.Int32
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		hits.Add(1)
		_, _ = w.Write([]byte(`{"done":true}`))
	}))
	defer backend.Close()

	clk := newClock()
	store := confirm.NewMemory(clk.now)
	svc := gateway.New(m, policy.NewMemory(policy.DefaultLimits, nil),
		connector.NewHTTP(backend.URL, "k1", []byte("s")), &audit.Memory{}, gateway.WithConfirmations(store))
	h := newHandler(store, clk)
	ctx := context.Background()
	args := json.RawMessage(`{"x":2}`)

	r1 := svc.Call(ctx, pr("alice"), "build", args)
	if r1.OK || r1.Code != "confirmation_required" {
		t.Fatalf("first call: %+v", r1)
	}
	var d struct {
		ID string `json:"confirmation_id"`
	}
	if err := json.Unmarshal(r1.Data, &d); err != nil || d.ID == "" {
		t.Fatalf("no id: %v", err)
	}
	if hits.Load() != 0 {
		t.Fatal("backend reached before approval")
	}
	ids := listIDs(t, do(t, h, clk.now(), "GET", "/v1/studio/confirmations", "alice"))
	if len(ids) != 1 || ids[0] != d.ID {
		t.Fatalf("list %v want %s", ids, d.ID)
	}
	if rec := do(t, h, clk.now(), "POST", "/v1/studio/confirmations/"+d.ID+"/approve", "alice"); rec.Code != 204 {
		t.Fatal(rec.Code)
	}
	r2 := svc.Call(ctx, pr("alice"), "build", args)
	if !r2.OK || hits.Load() != 1 {
		t.Fatalf("second call: %+v hits=%d", r2, hits.Load())
	}
}

func TestReplayRefusedOnEveryEndpoint(t *testing.T) {
	const base = "/v1/studio/confirmations"
	tests := []struct {
		name, method, verb string
	}{
		{"list", "GET", ""},
		{"approve", "POST", "/approve"},
		{"deny", "POST", "/deny"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clk := newClock()
			store := confirm.NewMemory(clk.now)
			id := mustGate(t, store, pr("alice"), "build")
			h := newHandler(store, clk)
			path := base
			if tt.verb != "" {
				path = base + "/" + id + tt.verb
			}
			req := httptest.NewRequest(tt.method, path, nil)
			agentsig.Sign(req, keyID, secret, "alice", "captured", nil, clk.now())
			send := func() int {
				r := httptest.NewRequest(tt.method, path, nil)
				r.Header = req.Header.Clone()
				rec := httptest.NewRecorder()
				h.ServeHTTP(rec, r)
				return rec.Code
			}
			if c := send(); c != 200 && c != 204 {
				t.Fatalf("first: %d", c)
			}
			if c := send(); c != 401 {
				t.Fatalf("replay: %d", c)
			}
		})
	}
}

// The studio's earlier signed deny and approve cannot be replayed to move a
// confirmation again, and a captured request cannot cross to another handler that
// shares the verifier.
func TestReplayedDecisionsAreRefused(t *testing.T) {
	clk := newClock()
	store := confirm.NewMemory(clk.now)
	v := studiosig.New(studiosig.Config{KeyID: keyID, Secret: secret, Now: clk.now})
	cfg := confirm.StudioConfig{Store: store, Tenant: tenant, KeyID: keyID, Secret: secret, Now: clk.now, Verifier: v}
	h := confirm.StudioHandler(cfg)
	other := confirm.StudioHandler(cfg) // a second mount sharing the verifier

	send := func(h http.Handler, hdr http.Header, id, verb string) int {
		r := httptest.NewRequest("POST", "/v1/studio/confirmations/"+id+"/"+verb, nil)
		r.Header = hdr.Clone()
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		return rec.Code
	}
	signedFor := func(id, verb, idem string) http.Header {
		r := httptest.NewRequest("POST", "/v1/studio/confirmations/"+id+"/"+verb, nil)
		agentsig.Sign(r, keyID, secret, "alice", idem, nil, clk.now())
		return r.Header
	}

	id := mustGate(t, store, pr("alice"), "build")
	approve := signedFor(id, "approve", "approve-1")
	if c := send(h, approve, id, "approve"); c != 204 {
		t.Fatalf("approve: %d", c)
	}
	if c := send(other, approve, id, "approve"); c != 401 {
		t.Fatalf("approve replayed on a shared verifier: %d", c)
	}
	// A deny that reuses the approve's key is refused, so the approval stands.
	if c := send(h, signedFor(id, "deny", "approve-1"), id, "deny"); c != 401 {
		t.Fatalf("deny with a used key: %d", c)
	}
	g, _ := store.Gate(context.Background(), pr("alice"), "build", "sha-build")
	if g.Status != confirm.StatusClaimed || g.ID != id {
		t.Fatalf("approval did not survive: %+v", g)
	}

	id2 := mustGate(t, store, pr("alice"), "raze")
	deny := signedFor(id2, "deny", "deny-1")
	if c := send(h, deny, id2, "deny"); c != 204 {
		t.Fatalf("deny: %d", c)
	}
	if c := send(h, deny, id2, "deny"); c != 401 {
		t.Fatalf("deny replayed: %d", c)
	}
}
