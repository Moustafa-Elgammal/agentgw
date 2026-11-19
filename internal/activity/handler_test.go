package activity_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Moustafa-Elgammal/agentgw/internal/activity"
	"github.com/Moustafa-Elgammal/agentgw/internal/audit"
	"github.com/Moustafa-Elgammal/agentgw/internal/studiosig"
	"github.com/Moustafa-Elgammal/agentgw/pkg/agentsig"
)

var (
	studioSecret = []byte("studio-secret")
	adminSecret  = []byte("admin-secret")
	t0           = time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC)
)

type rig struct {
	store         *audit.Ring
	player, admin http.Handler
	n             int
}

func newRig(t *testing.T) *rig {
	t.Helper()
	st := &audit.Ring{}
	ev := func(tenant, player, tool, outcome, code string, min int) {
		st.Record(context.Background(), audit.Event{Time: t0.Add(time.Duration(min) * time.Minute), Tenant: tenant,
			Player: player, Tool: tool, Tier: "action", Outcome: outcome, Code: code, ArgsSHA: "sha-" + tool})
	}
	ev("t", "alice", "build", "ok", "", 0)
	ev("t", "bob", "build", "ok", "", 1)
	ev("t", "alice", "sell", "denied", "daily_amount_cap", 2)
	ev("other", "alice", "build", "ok", "", 3) // another tenant, same player id
	ev("t", "alice", "get_state", "ok", "", 4)
	ev("t", "alice", "build", "error", "backend_unavailable", 5)
	pv := studiosig.New(studiosig.Config{KeyID: "studio-1", Secret: studioSecret})
	av := studiosig.New(studiosig.Config{KeyID: "admin-1", Secret: adminSecret})
	return &rig{store: st, player: activity.PlayerHandler(st, "t", pv), admin: activity.AdminHandler(st, "t", av)}
}

func (r *rig) get(h http.Handler, path, keyID string, secret []byte, who, idem string) *httptest.ResponseRecorder {
	r.n++
	if idem == "" {
		idem = fmt.Sprintf("k%d", r.n)
	}
	req := httptest.NewRequest("GET", path, nil)
	agentsig.Sign(req, keyID, secret, who, idem, nil, time.Now())
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w
}

type page struct {
	Events []map[string]any `json:"events"`
	Next   *int64           `json:"next_before"`
}

func decode(t *testing.T, w *httptest.ResponseRecorder) page {
	t.Helper()
	var p page
	if err := json.Unmarshal(w.Body.Bytes(), &p); err != nil {
		t.Fatalf("decode %q: %v", w.Body.String(), err)
	}
	return p
}

func tools(p page) []string {
	var out []string
	for _, e := range p.Events {
		out = append(out, e["tool"].(string)+"/"+e["outcome"].(string))
	}
	return out
}

func TestPlayerFeed(t *testing.T) {
	tests := []struct {
		name  string
		who   string
		query string
		want  []string
	}{
		{"alice sees only her events, newest first, own tenant only", "alice", "", []string{"build/error", "get_state/ok", "sell/denied", "build/ok"}},
		{"bob sees only his", "bob", "", []string{"build/ok"}},
		{"nobody sees nothing", "carol", "", nil},
		{"player param is ignored", "alice", "?player=bob", []string{"build/error", "get_state/ok", "sell/denied", "build/ok"}},
		{"tenant param is ignored", "alice", "?tenant=other", []string{"build/error", "get_state/ok", "sell/denied", "build/ok"}},
		{"filter tool", "alice", "?tool=build", []string{"build/error", "build/ok"}},
		{"filter outcome", "alice", "?outcome=denied", []string{"sell/denied"}},
		{"filter since", "alice", "?since=" + t0.Add(3*time.Minute).Format(time.RFC3339), []string{"build/error", "get_state/ok"}},
		{"limit", "alice", "?limit=2", []string{"build/error", "get_state/ok"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newRig(t)
			w := r.get(r.player, activity.PlayerPath+tt.query, "studio-1", studioSecret, tt.who, "")
			if w.Code != 200 {
				t.Fatalf("status %d: %s", w.Code, w.Body)
			}
			if got := tools(decode(t, w)); fmt.Sprint(got) != fmt.Sprint(tt.want) {
				t.Errorf("got %v, want %v", got, tt.want)
			}
			if w.Header().Get("Cache-Control") != "no-store" {
				t.Error("missing no-store")
			}
		})
	}
}

func TestRowsLeakNothingExtra(t *testing.T) {
	r := newRig(t)
	w := r.get(r.player, activity.PlayerPath+"?limit=1", "studio-1", studioSecret, "alice", "")
	e := decode(t, w).Events[0]
	for _, banned := range []string{"player", "tenant", "arguments", "args"} {
		if _, has := e[banned]; has {
			t.Errorf("row exposes %q: %v", banned, e)
		}
	}
	if e["args_sha256"] != "sha-build" {
		t.Errorf("want the hash, got %v", e["args_sha256"])
	}
}

func TestCursorPaging(t *testing.T) {
	r := newRig(t)
	var seen []string
	path := activity.PlayerPath + "?limit=3"
	for i := 0; i < 5; i++ {
		p := decode(t, r.get(r.player, path, "studio-1", studioSecret, "alice", ""))
		seen = append(seen, tools(p)...)
		if p.Next == nil {
			break
		}
		path = fmt.Sprintf("%s?limit=3&before=%d", activity.PlayerPath, *p.Next)
	}
	want := []string{"build/error", "get_state/ok", "sell/denied", "build/ok"}
	if fmt.Sprint(seen) != fmt.Sprint(want) {
		t.Errorf("paged %v, want %v", seen, want)
	}
	// An exact fit has no next page.
	if p := decode(t, r.get(r.player, activity.PlayerPath+"?limit=4", "studio-1", studioSecret, "alice", "")); p.Next != nil {
		t.Errorf("limit equal to the row count must not offer next_before, got %d", *p.Next)
	}
}

func TestBadParams(t *testing.T) {
	for _, q := range []string{"?outcome=weird", "?since=yesterday", "?limit=abc", "?limit=-1", "?before=x", "?before=-3"} {
		t.Run(q, func(t *testing.T) {
			r := newRig(t)
			w := r.get(r.player, activity.PlayerPath+q, "studio-1", studioSecret, "alice", "")
			if w.Code != 400 || w.Body.String() != `{"error":"invalid_request"}`+"\n" {
				t.Errorf("got %d %s", w.Code, w.Body)
			}
		})
	}
}

func TestLimitIsClamped(t *testing.T) {
	r := newRig(t)
	if w := r.get(r.player, activity.PlayerPath+"?limit=100000", "studio-1", studioSecret, "alice", ""); w.Code != 200 {
		t.Errorf("huge limit: %d", w.Code)
	}
}

func TestSignatureRules(t *testing.T) {
	r := newRig(t)
	unsigned := httptest.NewRecorder()
	r.player.ServeHTTP(unsigned, httptest.NewRequest("GET", activity.PlayerPath, nil))
	tests := []struct {
		name string
		w    *httptest.ResponseRecorder
	}{
		{"unsigned", unsigned},
		{"wrong secret", r.get(r.player, activity.PlayerPath, "studio-1", []byte("nope"), "alice", "")},
		{"unknown key", r.get(r.player, activity.PlayerPath, "other", studioSecret, "alice", "")},
		{"admin key on the player feed", r.get(r.player, activity.PlayerPath, "admin-1", adminSecret, "alice", "")},
		{"studio key on the admin feed", r.get(r.admin, activity.AdminPath, "studio-1", studioSecret, "root", "")},
	}
	for _, tt := range tests {
		if tt.w.Code != 401 || tt.w.Body.String() != `{"error":"invalid_client"}`+"\n" {
			t.Errorf("%s: %d %s", tt.name, tt.w.Code, tt.w.Body)
		}
	}
}

func TestReplayRefused(t *testing.T) {
	r := newRig(t)
	if w := r.get(r.player, activity.PlayerPath, "studio-1", studioSecret, "alice", "same"); w.Code != 200 {
		t.Fatalf("first: %d", w.Code)
	}
	if w := r.get(r.player, activity.PlayerPath, "studio-1", studioSecret, "alice", "same"); w.Code != 401 {
		t.Errorf("replay: %d", w.Code)
	}
}

func TestAdminFeed(t *testing.T) {
	tests := []struct {
		name  string
		query string
		want  []string
	}{
		{"all players of the tenant", "", []string{"build/error", "get_state/ok", "sell/denied", "build/ok", "build/ok"}},
		{"one player", "?player=bob", []string{"build/ok"}},
		{"player and outcome", "?player=alice&outcome=error", []string{"build/error"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newRig(t)
			w := r.get(r.admin, activity.AdminPath+tt.query, "admin-1", adminSecret, "ops-1", "")
			if w.Code != 200 {
				t.Fatalf("status %d: %s", w.Code, w.Body)
			}
			p := decode(t, w)
			if got := tools(p); fmt.Sprint(got) != fmt.Sprint(tt.want) {
				t.Errorf("got %v, want %v", got, tt.want)
			}
			for _, e := range p.Events {
				if e["player"] == nil || e["player"] == "" {
					t.Errorf("admin rows must name the player: %v", e)
				}
			}
		})
	}
}

type failing struct{ *audit.Ring }

func (failing) Query(context.Context, audit.Query) ([]audit.Stored, error) {
	return nil, fmt.Errorf("db down")
}

func TestStoreErrorIs503(t *testing.T) {
	v := studiosig.New(studiosig.Config{KeyID: "studio-1", Secret: studioSecret})
	h := activity.PlayerHandler(failing{&audit.Ring{}}, "t", v)
	req := httptest.NewRequest("GET", activity.PlayerPath, nil)
	agentsig.Sign(req, "studio-1", studioSecret, "alice", "k", nil, time.Now())
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != 503 || w.Body.String() != `{"error":"temporarily_unavailable"}`+"\n" {
		t.Errorf("got %d %s", w.Code, w.Body)
	}
}

func TestOnlyGET(t *testing.T) {
	r := newRig(t)
	req := httptest.NewRequest("POST", activity.PlayerPath, nil)
	agentsig.Sign(req, "studio-1", studioSecret, "alice", "k", nil, time.Now())
	w := httptest.NewRecorder()
	r.player.ServeHTTP(w, req)
	if w.Code == 200 {
		t.Error("POST must not be served")
	}
}
