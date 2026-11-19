package conformance

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/Moustafa-Elgammal/agentgw/internal/audit"
	"github.com/Moustafa-Elgammal/agentgw/internal/confirm"
	"github.com/Moustafa-Elgammal/agentgw/internal/connector"
	"github.com/Moustafa-Elgammal/agentgw/internal/core"
	"github.com/Moustafa-Elgammal/agentgw/internal/gateway"
	"github.com/Moustafa-Elgammal/agentgw/internal/manifest"
	"github.com/Moustafa-Elgammal/agentgw/internal/mcpserver"
	"github.com/Moustafa-Elgammal/agentgw/internal/policy"
	"github.com/Moustafa-Elgammal/agentgw/pkg/agentsig"
)

const (
	manifestPath = "../../examples/city/manifest.yaml"
	keyID        = "k1"
)

var secret = []byte("conformance-secret")

type backendCall struct {
	Method, Path, Player, Idem string
	Body                       string
}

// backend is a fake game that verifies signatures like a real studio backend must.
type backend struct {
	srv   *httptest.Server
	mu    sync.Mutex
	calls []backendCall
	fail  atomic.Bool
}

func newBackend(t *testing.T) *backend {
	t.Helper()
	b := &backend{}
	b.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		player, err := agentsig.Verify(r, body, func(id string) []byte {
			if id == keyID {
				return secret
			}
			return nil
		}, time.Now(), 0)
		if err != nil {
			http.Error(w, err.Error(), http.StatusUnauthorized)
			return
		}
		if b.fail.Load() {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		b.mu.Lock()
		b.calls = append(b.calls, backendCall{r.Method, r.URL.RequestURI(), player, r.Header.Get(agentsig.HeaderIdempotency), string(body)})
		b.mu.Unlock()
		_, _ = w.Write([]byte(`{"done":true}`))
	}))
	t.Cleanup(b.srv.Close)
	return b
}

func (b *backend) got() []backendCall {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]backendCall(nil), b.calls...)
}

type rig struct {
	url   string
	b     *backend
	audit *audit.Memory
	m     *manifest.Manifest
	pol   *policy.Memory
	conf  *confirm.Memory
}

func newRig(t *testing.T, limits policy.Limits) *rig {
	t.Helper()
	m, err := manifest.LoadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	b, a := newBackend(t), &audit.Memory{}
	pol := policy.NewMemory(limits, nil)
	conf := confirm.NewMemory(nil)
	svc := gateway.New(m, pol, connector.NewHTTP(b.srv.URL, keyID, secret, connector.AllowPrivate()), a, gateway.WithConfirmations(conf))
	ts := httptest.NewServer(mcpserver.Handler(svc, mcpserver.DevAuth("conf")))
	t.Cleanup(ts.Close)
	return &rig{ts.URL, b, a, m, pol, conf}
}

type playerRT struct{ player string }

func (p playerRT) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	if p.player != "" {
		r.Header.Set("X-Dev-Player", p.player)
	}
	return http.DefaultTransport.RoundTrip(r)
}

func (g *rig) client(t *testing.T, player string) *mcp.ClientSession {
	t.Helper()
	c := mcp.NewClient(&mcp.Implementation{Name: "conformance", Version: "0"}, nil)
	cs, err := c.Connect(context.Background(), &mcp.StreamableClientTransport{
		Endpoint: g.url, HTTPClient: &http.Client{Transport: playerRT{player}},
	}, nil)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	return cs
}

// call returns the decoded result body and whether the tool reported an error.
func call(t *testing.T, cs *mcp.ClientSession, name string, args map[string]any) (map[string]any, bool) {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("CallTool %s: %v", name, err)
	}
	var body map[string]any
	if err := json.Unmarshal([]byte(res.Content[0].(*mcp.TextContent).Text), &body); err != nil {
		t.Fatalf("result body is not JSON: %v", err)
	}
	return body, res.IsError
}

func sell(qty int) map[string]any {
	return map[string]any{"resource": "food", "quantity": qty}
}

func TestToolListMatchesManifest(t *testing.T) {
	g := newRig(t, policy.DefaultLimits)
	res, err := g.client(t, "p1").ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]core.Tool{}
	for _, tl := range g.m.Tools {
		if tl.Tier != core.TierSensitive {
			want[tl.Name] = tl
		}
	}
	if len(res.Tools) != len(want) {
		t.Fatalf("listed %d tools, manifest exposes %d", len(res.Tools), len(want))
	}
	for _, tl := range res.Tools {
		mt, ok := want[tl.Name]
		if !ok {
			t.Errorf("unexpected tool %q", tl.Name)
			continue
		}
		if tl.Description != mt.Description {
			t.Errorf("%s: description differs from manifest", tl.Name)
		}
		if tl.Annotations == nil || tl.Annotations.ReadOnlyHint != (mt.Tier == core.TierRead) {
			t.Errorf("%s: readOnlyHint must be true exactly for read tier", tl.Name)
		}
		raw, _ := json.Marshal(tl.InputSchema)
		var sc map[string]any
		_ = json.Unmarshal(raw, &sc)
		if sc["type"] != "object" || sc["additionalProperties"] != false {
			t.Errorf("%s: input schema must be a closed object, got %s", tl.Name, raw)
		}
	}
}

func TestBackendCallIsSignedAndAttributed(t *testing.T) {
	g := newRig(t, policy.DefaultLimits)
	cs := g.client(t, "alice")
	for i := 0; i < 2; i++ {
		if body, isErr := call(t, cs, "sell_resource", sell(10)); isErr {
			t.Fatalf("sell failed: %v", body)
		}
	}
	calls := g.b.got() // the backend rejects bad signatures with 401, so reaching here proves signing
	if len(calls) != 2 {
		t.Fatalf("backend saw %d calls, want 2", len(calls))
	}
	for _, c := range calls {
		if c.Player != "alice" || c.Method != "POST" || c.Path != "/api/v1/market/sell" {
			t.Errorf("unexpected backend call %+v", c)
		}
	}
	if calls[0].Idem == "" || calls[0].Idem == calls[1].Idem {
		t.Errorf("idempotency keys must be present and unique: %q %q", calls[0].Idem, calls[1].Idem)
	}
}

func TestInvalidAttemptsCountAgainstRateLimit(t *testing.T) {
	g := newRig(t, policy.Limits{ActionsPerMinute: 2, ReadsPerMinute: 100})
	cs := g.client(t, "p1")
	bad := map[string]any{"resource": "gold", "quantity": 1}
	for i := 0; i < 2; i++ {
		if body, isErr := call(t, cs, "sell_resource", bad); !isErr || body["code"] != "invalid_arguments" {
			t.Fatalf("attempt %d: want invalid_arguments, got %v", i+1, body)
		}
	}
	if body, isErr := call(t, cs, "sell_resource", sell(1)); !isErr || body["code"] != "rate_limited" {
		t.Fatalf("a valid call after two invalid ones must be rate limited, got %v", body)
	}
	if len(g.b.got()) != 0 {
		t.Error("backend must not be reached")
	}
}

func TestFailedBackendCallReleasesReservation(t *testing.T) {
	g := newRig(t, policy.DefaultLimits)
	cs := g.client(t, "p1")

	// Daily cap is 300 coins' worth of quantity: a failed call must not consume it.
	g.b.fail.Store(true)
	if body, isErr := call(t, cs, "sell_resource", sell(300)); !isErr || body["code"] != "backend_unavailable" {
		t.Fatalf("want backend_unavailable, got %v", body)
	}
	g.b.fail.Store(false)
	if body, isErr := call(t, cs, "sell_resource", sell(300)); isErr {
		t.Fatalf("cap must be released after a failed call, got %v", body)
	}
	if body, isErr := call(t, cs, "sell_resource", sell(1)); !isErr || body["code"] != "daily_amount_cap" {
		t.Fatalf("cap must now be spent, got %v", body)
	}
}

func TestFailedBackendCallDoesNotStartCooldown(t *testing.T) {
	g := newRig(t, policy.DefaultLimits)
	cs := g.client(t, "p1")
	g.b.fail.Store(true)
	if _, isErr := call(t, cs, "collect_taxes", nil); !isErr {
		t.Fatal("expected failure")
	}
	g.b.fail.Store(false)
	if body, isErr := call(t, cs, "collect_taxes", nil); isErr {
		t.Fatalf("no cooldown after a failed call, got %v", body)
	}
	if body, isErr := call(t, cs, "collect_taxes", nil); !isErr || body["code"] != "cooldown" {
		t.Fatalf("cooldown must follow a success, got %v", body)
	}
}

func TestDailyCapHoldsUnderConcurrency(t *testing.T) {
	g := newRig(t, policy.Limits{ActionsPerMinute: 1000, ReadsPerMinute: 1000})
	cs := g.client(t, "p1")
	var ok atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "sell_resource", Arguments: sell(100)})
			if err == nil && !res.IsError {
				ok.Add(1)
			}
		}()
	}
	wg.Wait()
	if ok.Load() != 3 || len(g.b.got()) != 3 {
		t.Fatalf("cap of 300 with 100 per call allows exactly 3, got %d ok and %d backend calls", ok.Load(), len(g.b.got()))
	}
}

func TestPlayersAreIsolated(t *testing.T) {
	g := newRig(t, policy.DefaultLimits)
	if _, isErr := call(t, g.client(t, "alice"), "sell_resource", sell(300)); isErr {
		t.Fatal("alice should be able to spend her cap")
	}
	if body, isErr := call(t, g.client(t, "bob"), "sell_resource", sell(300)); isErr {
		t.Fatalf("bob's cap is separate from alice's, got %v", body)
	}
}

func TestAuditStoresHashesNotArguments(t *testing.T) {
	g := newRig(t, policy.DefaultLimits)
	call(t, g.client(t, "p1"), "sell_resource", map[string]any{"resource": "materials", "quantity": 7})
	events := g.audit.Events()
	if len(events) != 1 {
		t.Fatalf("want 1 audit event, got %d", len(events))
	}
	raw, _ := json.Marshal(events[0])
	if strings.Contains(string(raw), "materials") || len(events[0].ArgsSHA) != 64 {
		t.Errorf("audit must hold a 64 char hash and no argument values: %s", raw)
	}
	if events[0].Player != "p1" || events[0].Tool != "sell_resource" || events[0].Outcome != "ok" {
		t.Errorf("unexpected event %+v", events[0])
	}
}

func TestUnknownToolIsAToolErrorNotATransportFailure(t *testing.T) {
	g := newRig(t, policy.DefaultLimits)
	res, err := g.client(t, "p1").CallTool(context.Background(), &mcp.CallToolParams{Name: "launch_missiles"})
	if err == nil && !res.IsError {
		t.Fatal("unknown tool must not succeed")
	}
	if len(g.b.got()) != 0 {
		t.Error("backend must not be reached")
	}
}

func TestUnauthenticatedRequestIsRejected(t *testing.T) {
	g := newRig(t, policy.DefaultLimits)
	resp, err := http.Post(g.url, "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", resp.StatusCode)
	}
}

func TestKillSwitchStopsCallsAndCanBeLifted(t *testing.T) {
	g := newRig(t, policy.DefaultLimits)
	cs := g.client(t, "p1")
	sw := policy.Switch{Tenant: "conf", Tool: "sell_resource"}
	ctx := context.Background()

	if err := g.pol.SetSwitch(ctx, sw, true); err != nil {
		t.Fatal(err)
	}
	body, isErr := call(t, cs, "sell_resource", sell(1))
	if !isErr || body["code"] != "switched_off" {
		t.Fatalf("want switched_off, got %v", body)
	}
	if _, isErr := call(t, cs, "get_city_state", nil); isErr {
		t.Fatal("a tool switch must not stop other tools")
	}
	if n := len(g.b.got()); n != 1 { // only the read reached the backend
		t.Fatalf("backend calls = %d, want 1", n)
	}
	ev := g.audit.Events()
	if ev[0].Outcome != "denied" || ev[0].Code != "switched_off" {
		t.Errorf("denial must be audited, got %+v", ev[0])
	}

	if err := g.pol.SetSwitch(ctx, sw, false); err != nil {
		t.Fatal(err)
	}
	if body, isErr := call(t, cs, "sell_resource", sell(1)); isErr {
		t.Fatalf("switch lifted, call must pass: %v", body)
	}
}

func TestConfirmedToolNeedsThePlayerNotTheAgent(t *testing.T) {
	g := newRig(t, policy.DefaultLimits)
	cs := g.client(t, "alice")
	args := map[string]any{"kind": "farm", "x": 2, "y": 3}
	alice := core.Principal{TenantID: "conf", PlayerID: "alice"}
	ctx := context.Background()

	body, isErr := call(t, cs, "build_structure", args)
	if !isErr || body["code"] != "confirmation_required" {
		t.Fatalf("want confirmation_required, got %v", body)
	}
	id := body["data"].(map[string]any)["confirmation_id"].(string)
	if len(g.b.got()) != 0 {
		t.Fatal("nothing may reach the backend before the player approves")
	}

	// The agent has no tool to approve, and another player cannot approve for alice.
	if err := g.conf.Approve(ctx, core.Principal{TenantID: "conf", PlayerID: "mallory"}, id); err == nil {
		t.Fatal("only the owning player can approve")
	}
	if body, _ := call(t, cs, "build_structure", args); body["code"] != "confirmation_required" {
		t.Fatalf("still waiting, got %v", body)
	}

	if err := g.conf.Approve(ctx, alice, id); err != nil {
		t.Fatal(err)
	}
	if body, isErr := call(t, cs, "build_structure", map[string]any{"kind": "farm", "x": 2, "y": 4}); !isErr || body["code"] != "confirmation_required" {
		t.Fatalf("approval covers only the exact arguments, got %v", body)
	}
	if body, isErr := call(t, cs, "build_structure", args); isErr {
		t.Fatalf("approved call must run: %v", body)
	}
	calls := g.b.got()
	if len(calls) != 1 || calls[0].Path != "/api/v1/build" || calls[0].Player != "alice" {
		t.Fatalf("backend calls = %+v", calls)
	}
	if body, _ := call(t, cs, "build_structure", args); body["code"] != "confirmation_required" {
		t.Fatalf("an approval is single use, got %v", body)
	}
}
