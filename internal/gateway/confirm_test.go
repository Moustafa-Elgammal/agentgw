package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Moustafa-Elgammal/agentgw/internal/audit"
	"github.com/Moustafa-Elgammal/agentgw/internal/confirm"
	"github.com/Moustafa-Elgammal/agentgw/internal/connector"
	"github.com/Moustafa-Elgammal/agentgw/internal/core"
	"github.com/Moustafa-Elgammal/agentgw/internal/manifest"
	"github.com/Moustafa-Elgammal/agentgw/internal/policy"
)

const confirmDoc = `{"version":1,"tools":[
 {"name":"build","description":"Build.","tier":"medium","confirm":true,
  "http":{"method":"POST","url":"/build"},
  "params":{"kind":{"type":"string","enum":["house","farm"]},"x":{"type":"integer","min":0,"max":4}}},
 {"name":"collect","description":"Collect.","tier":"safe","confirm":true,
  "caps":{"cooldown_seconds":60},"http":{"method":"POST","url":"/collect"}}
]}`

type cfx struct {
	svc   *Service
	store *confirm.Memory
	audit *audit.Memory
	hits  *int
	fail  *bool
}

func setupConfirm(t *testing.T, store confirm.Store, opts ...Option) cfx {
	t.Helper()
	m, err := manifest.Parse(strings.NewReader(confirmDoc))
	if err != nil {
		t.Fatal(err)
	}
	hits, fail := new(int), new(bool)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		*hits++
		if *fail {
			w.WriteHeader(500)
			return
		}
		_, _ = w.Write([]byte(`{"done":true}`))
	}))
	t.Cleanup(srv.Close)
	a := &audit.Memory{}
	if store != nil {
		opts = append(opts, WithConfirmations(store))
	}
	svc := New(m, policy.NewMemory(policy.DefaultLimits, nil), connector.NewHTTP(srv.URL, "k1", secret), a, opts...)
	mem, _ := store.(*confirm.Memory)
	return cfx{svc, mem, a, hits, fail}
}

func (f cfx) call(name, args string) core.Result {
	return f.svc.Call(context.Background(), player, name, json.RawMessage(args))
}

// idOf extracts data.confirmation_id from a confirmation_required result.
func idOf(t *testing.T, r core.Result) string {
	t.Helper()
	var d struct {
		ID string `json:"confirmation_id"`
	}
	if err := json.Unmarshal(r.Data, &d); err != nil || d.ID == "" {
		t.Fatalf("result carries no confirmation_id: %+v (%v)", r, err)
	}
	return d.ID
}

const buildArgs = `{"kind":"house","x":1}`

func TestConfirmationFlow(t *testing.T) {
	f := setupConfirm(t, confirm.NewMemory(nil))

	r1 := f.call("build", buildArgs)
	if r1.OK || r1.Code != "confirmation_required" {
		t.Fatalf("first call must ask for approval, got %+v", r1)
	}
	id := idOf(t, r1)
	if *f.hits != 0 {
		t.Fatal("the backend must not be reached before approval")
	}

	// The same call, written differently, is the same confirmation.
	r2 := f.call("build", ` { "x": 1, "kind": "house" } `)
	if r2.Code != "confirmation_required" || idOf(t, r2) != id {
		t.Fatalf("a repeat must share the pending confirmation, got %+v", r2)
	}

	if err := f.store.Approve(context.Background(), player, id); err != nil {
		t.Fatal(err)
	}
	if r := f.call("build", buildArgs); !r.OK {
		t.Fatalf("approved call must run: %+v", r)
	}
	if *f.hits != 1 {
		t.Fatalf("backend hits = %d, want 1", *f.hits)
	}

	// Single use.
	if r := f.call("build", buildArgs); r.Code != "confirmation_required" || idOf(t, r) == id {
		t.Fatalf("an approval is good for one call, got %+v", r)
	}
	if *f.hits != 1 {
		t.Fatal("a consumed approval must not reach the backend")
	}
}

func TestApprovalBindsToArguments(t *testing.T) {
	f := setupConfirm(t, confirm.NewMemory(nil))
	id := idOf(t, f.call("build", buildArgs))
	_ = f.store.Approve(context.Background(), player, id)
	if r := f.call("build", `{"kind":"farm","x":1}`); r.Code != "confirmation_required" {
		t.Fatalf("changed arguments need a new approval, got %+v", r)
	}
	if r := f.call("build", `{"kind":"house","x":2}`); r.Code != "confirmation_required" {
		t.Fatalf("changed arguments need a new approval, got %+v", r)
	}
	if *f.hits != 0 {
		t.Fatal("nothing should have run")
	}
}

func TestDeniedConfirmation(t *testing.T) {
	f := setupConfirm(t, confirm.NewMemory(nil))
	id := idOf(t, f.call("build", buildArgs))
	_ = f.store.Deny(context.Background(), player, id)
	for i := 0; i < 2; i++ {
		if r := f.call("build", buildArgs); r.Code != "confirmation_denied" {
			t.Fatalf("want confirmation_denied, got %+v", r)
		}
	}
	if *f.hits != 0 {
		t.Fatal("a denied call must not run")
	}
}

func TestFailedBackendCallKeepsTheApproval(t *testing.T) {
	f := setupConfirm(t, confirm.NewMemory(nil))
	id := idOf(t, f.call("build", buildArgs))
	_ = f.store.Approve(context.Background(), player, id)

	*f.fail = true
	if r := f.call("build", buildArgs); r.Code != "backend_unavailable" {
		t.Fatalf("want backend_unavailable, got %+v", r)
	}
	*f.fail = false
	if r := f.call("build", buildArgs); !r.OK {
		t.Fatalf("the approval must survive a backend outage, got %+v", r)
	}
}

func TestPolicyDenialKeepsTheApproval(t *testing.T) {
	f := setupConfirm(t, confirm.NewMemory(nil))
	// First confirmed collect succeeds and starts the cooldown.
	id := idOf(t, f.call("collect", `{}`))
	_ = f.store.Approve(context.Background(), player, id)
	if r := f.call("collect", `{}`); !r.OK {
		t.Fatalf("first collect: %+v", r)
	}
	// Second one is approved but hits the cooldown. The approval must not be burned.
	id2 := idOf(t, f.call("collect", `{}`))
	_ = f.store.Approve(context.Background(), player, id2)
	if r := f.call("collect", `{}`); r.Code != "cooldown" {
		t.Fatalf("want cooldown, got %+v", r)
	}
	g, err := f.store.Gate(context.Background(), player, "collect", argsDigest(map[string]any{}))
	if err != nil || g.Status != confirm.StatusClaimed || g.ID != id2 {
		t.Fatalf("the approval must still be usable: %+v %v", g, err)
	}
}

func TestOtherPlayersApprovalDoesNotCount(t *testing.T) {
	f := setupConfirm(t, confirm.NewMemory(nil))
	id := idOf(t, f.call("build", buildArgs))
	_ = f.store.Approve(context.Background(), player, id)
	other := core.Principal{TenantID: "t", PlayerID: "p2"}
	if r := f.svc.Call(context.Background(), other, "build", json.RawMessage(buildArgs)); r.Code != "confirmation_required" {
		t.Fatalf("p1's approval must not cover p2, got %+v", r)
	}
}

type brokenConfirm struct{ confirm.Store }

func (brokenConfirm) Gate(context.Context, core.Principal, string, string) (confirm.Gate, error) {
	return confirm.Gate{}, errors.New("db down")
}

func TestConfirmationStoreFailuresFailClosed(t *testing.T) {
	tests := []struct {
		name  string
		store confirm.Store
	}{
		{"store down", brokenConfirm{}},
		{"no store configured", nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var f cfx
			if tc.store == nil {
				f = setupConfirm(t, nil)
			} else {
				f = setupConfirm(t, tc.store)
			}
			r := f.call("build", buildArgs)
			if r.OK || r.Code != "confirmation_unavailable" || *f.hits != 0 {
				t.Fatalf("must refuse and not reach the backend: %+v hits=%d", r, *f.hits)
			}
			ev := f.audit.Events()
			if len(ev) != 1 || ev[0].Outcome != "error" || ev[0].Code != "confirmation_unavailable" {
				t.Fatalf("outage must be audited as an error: %+v", ev)
			}
		})
	}
}

func TestConfirmationRequiredIsAudited(t *testing.T) {
	f := setupConfirm(t, confirm.NewMemory(nil))
	f.call("build", buildArgs)
	ev := f.audit.Events()
	if len(ev) != 1 || ev[0].Outcome != "denied" || ev[0].Code != "confirmation_required" {
		t.Fatalf("got %+v", ev)
	}
}
