package mcpserver

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/Moustafa-Elgammal/agentgw/internal/audit"
	"github.com/Moustafa-Elgammal/agentgw/internal/core"
	"github.com/Moustafa-Elgammal/agentgw/internal/gateway"
	"github.com/Moustafa-Elgammal/agentgw/internal/manifest"
	"github.com/Moustafa-Elgammal/agentgw/internal/policy"
)

const doc = `{"version":1,"tools":[
 {"name":"get_state","description":"State.","tier":"read","http":{"method":"GET","url":"/state"}},
 {"name":"sell_resource","description":"Sell.","tier":"medium",
  "caps":{"per_day_amount":100,"amount_param":"quantity"},
  "http":{"method":"POST","url":"/sell"},
  "params":{"resource":{"type":"string","enum":["food","water"]},
            "quantity":{"type":"integer","min":1,"max":1000}}},
 {"name":"gift","description":"Gift.","tier":"sensitive","http":{"method":"POST","url":"/gift"}}
]}`

// fakeConn records calls and returns a fixed successful result.
type fakeConn struct {
	mu    sync.Mutex
	calls []string // "player:tool"
}

func (f *fakeConn) Call(_ context.Context, p core.Principal, t core.Tool, _ map[string]any, _ string) (core.Result, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, p.PlayerID+":"+t.Name)
	return core.Result{OK: true, Data: json.RawMessage(`{"done":true}`)}, nil
}

type headerRT struct{ player string }

func (h headerRT) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	if h.player != "" {
		r.Header.Set("X-Dev-Player", h.player)
	}
	return http.DefaultTransport.RoundTrip(r)
}

type env struct {
	url   string
	conn  *fakeConn
	audit *audit.Memory
}

func setup(t *testing.T, limits policy.Limits) env {
	t.Helper()
	m, err := manifest.Parse(strings.NewReader(doc))
	if err != nil {
		t.Fatal(err)
	}
	conn, a := &fakeConn{}, &audit.Memory{}
	svc := gateway.New(m, policy.NewMemory(limits, nil), conn, a)
	ts := httptest.NewServer(Handler(svc, DevAuth("t")))
	t.Cleanup(ts.Close)
	return env{ts.URL, conn, a}
}

func connect(t *testing.T, url, player string) *mcp.ClientSession {
	t.Helper()
	c := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0"}, nil)
	cs, err := c.Connect(context.Background(), &mcp.StreamableClientTransport{
		Endpoint: url, HTTPClient: &http.Client{Transport: headerRT{player}},
	}, nil)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	return cs
}

func callTool(t *testing.T, cs *mcp.ClientSession, name string, args map[string]any) (map[string]any, bool) {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("CallTool %s: %v", name, err)
	}
	var body map[string]any
	if err := json.Unmarshal([]byte(res.Content[0].(*mcp.TextContent).Text), &body); err != nil {
		t.Fatalf("result is not JSON: %v", err)
	}
	return body, res.IsError
}

func TestListToolsHidesSensitiveAndMarksReadOnly(t *testing.T) {
	e := setup(t, policy.DefaultLimits)
	res, err := connect(t, e.url, "p1").ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, tl := range res.Tools {
		got[tl.Name] = tl.Annotations != nil && tl.Annotations.ReadOnlyHint
	}
	names := make([]string, 0, len(got))
	for n := range got {
		names = append(names, n)
	}
	sort.Strings(names)
	if strings.Join(names, ",") != "get_state,sell_resource" {
		t.Fatalf("tools = %v, sensitive tool must not be listed", names)
	}
	if !got["get_state"] || got["sell_resource"] {
		t.Errorf("read-only hints wrong: %v", got)
	}
}

func TestCallTool(t *testing.T) {
	tests := []struct {
		name     string
		tool     string
		args     map[string]any
		wantErr  bool
		wantCode string
		wantCall bool
	}{
		{"read ok", "get_state", nil, false, "", true},
		{"action ok", "sell_resource", map[string]any{"resource": "food", "quantity": 10}, false, "", true},
		{"invalid enum is a tool error with a code", "sell_resource", map[string]any{"resource": "gold", "quantity": 10}, true, "invalid_arguments", false},
		{"missing argument", "sell_resource", map[string]any{"resource": "food"}, true, "invalid_arguments", false},
		{"over daily cap", "sell_resource", map[string]any{"resource": "food", "quantity": 101}, true, "daily_amount_cap", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			e := setup(t, policy.DefaultLimits)
			body, isErr := callTool(t, connect(t, e.url, "p1"), tc.tool, tc.args)
			if isErr != tc.wantErr {
				t.Fatalf("isError = %v, body %v", isErr, body)
			}
			if tc.wantCode != "" && body["code"] != tc.wantCode {
				t.Errorf("code = %v, want %s", body["code"], tc.wantCode)
			}
			if (len(e.conn.calls) == 1) != tc.wantCall {
				t.Errorf("backend calls = %v, wantCall %v", e.conn.calls, tc.wantCall)
			}
			if len(e.audit.Events()) != 1 {
				t.Errorf("want exactly one audit event, got %d", len(e.audit.Events()))
			}
		})
	}
}

func TestRateLimitCountsInvalidAttemptsOverMCP(t *testing.T) {
	e := setup(t, policy.Limits{ActionsPerMinute: 2, ReadsPerMinute: 10})
	cs := connect(t, e.url, "p1")
	bad := map[string]any{"resource": "gold", "quantity": 1}
	callTool(t, cs, "sell_resource", bad)
	callTool(t, cs, "sell_resource", bad)
	body, isErr := callTool(t, cs, "sell_resource", map[string]any{"resource": "food", "quantity": 1})
	if !isErr || body["code"] != "rate_limited" {
		t.Fatalf("third attempt should be rate limited, got %v", body)
	}
}

func TestSensitiveToolCannotBeCalled(t *testing.T) {
	e := setup(t, policy.DefaultLimits)
	res, err := connect(t, e.url, "p1").CallTool(context.Background(), &mcp.CallToolParams{Name: "gift"})
	if err == nil && !res.IsError {
		t.Fatal("sensitive tool must not be callable")
	}
	if len(e.conn.calls) != 0 {
		t.Errorf("backend must not be reached: %v", e.conn.calls)
	}
}

func TestPrincipalIsPerRequest(t *testing.T) {
	e := setup(t, policy.DefaultLimits)
	callTool(t, connect(t, e.url, "alice"), "get_state", nil)
	callTool(t, connect(t, e.url, "bob"), "get_state", nil)
	if strings.Join(e.conn.calls, ",") != "alice:get_state,bob:get_state" {
		t.Errorf("calls = %v", e.conn.calls)
	}
}

func TestMissingPlayerIsRejected(t *testing.T) {
	e := setup(t, policy.DefaultLimits)
	resp, err := http.Post(e.url, "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", resp.StatusCode)
	}
}

func TestScopeDecidesWhichToolsAreListed(t *testing.T) {
	m, err := manifest.Parse(strings.NewReader(doc))
	if err != nil {
		t.Fatal(err)
	}
	svc := gateway.New(m, policy.NewMemory(policy.DefaultLimits, nil), &fakeConn{}, &audit.Memory{})
	for scope, want := range map[core.Scope]string{core.ScopeCoach: "get_state", core.ScopeOperator: "get_state,sell_resource"} {
		auth := func(*http.Request) (core.Principal, error) {
			return core.Principal{TenantID: "t", PlayerID: "p1", Scope: scope}, nil
		}
		ts := httptest.NewServer(Handler(svc, auth))
		res, err := connect(t, ts.URL, "").ListTools(context.Background(), nil)
		ts.Close()
		if err != nil {
			t.Fatal(err)
		}
		var names []string
		for _, tl := range res.Tools {
			names = append(names, tl.Name)
		}
		sort.Strings(names)
		if strings.Join(names, ",") != want {
			t.Errorf("scope %s lists %v, want %s", scope, names, want)
		}
	}
}

type challenge struct{}

func (challenge) Error() string { return "bearer token required" }
func (challenge) WWWAuthenticate() string {
	return `Bearer resource_metadata="https://gw.example/.well-known/oauth-protected-resource/mcp"`
}

func TestUnauthorizedCarriesTheChallenge(t *testing.T) {
	m, _ := manifest.Parse(strings.NewReader(doc))
	svc := gateway.New(m, policy.NewMemory(policy.DefaultLimits, nil), &fakeConn{}, &audit.Memory{})
	ts := httptest.NewServer(Handler(svc, func(*http.Request) (core.Principal, error) { return core.Principal{}, challenge{} }))
	defer ts.Close()
	resp, err := http.Post(ts.URL, "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != 401 || !strings.Contains(resp.Header.Get("WWW-Authenticate"), "resource_metadata=") {
		t.Fatalf("status %d, header %q", resp.StatusCode, resp.Header.Get("WWW-Authenticate"))
	}
}

func TestMaxScopeHidesActionToolsEvenForOperatorGrants(t *testing.T) {
	m, err := manifest.Parse(strings.NewReader(doc))
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name  string
		max   core.Scope
		scope core.Scope
		want  string
	}{
		{"no cap, operator", "", core.ScopeOperator, "get_state,sell_resource"},
		{"operator cap, operator", core.ScopeOperator, core.ScopeOperator, "get_state,sell_resource"},
		{"coach cap, operator grant", core.ScopeCoach, core.ScopeOperator, "get_state"},
		{"coach cap, coach grant", core.ScopeCoach, core.ScopeCoach, "get_state"},
		{"coach cap, unlimited (dev) caller", core.ScopeCoach, "", "get_state"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := gateway.New(m, policy.NewMemory(policy.DefaultLimits, nil), &fakeConn{}, &audit.Memory{}, gateway.WithMaxScope(tt.max))
			auth := func(*http.Request) (core.Principal, error) {
				return core.Principal{TenantID: "t", PlayerID: "p1", Scope: tt.scope}, nil
			}
			ts := httptest.NewServer(Handler(svc, auth))
			defer ts.Close()
			cs := connect(t, ts.URL, "")
			res, err := cs.ListTools(context.Background(), nil)
			if err != nil {
				t.Fatal(err)
			}
			var names []string
			for _, tl := range res.Tools {
				names = append(names, tl.Name)
			}
			sort.Strings(names)
			if strings.Join(names, ",") != tt.want {
				t.Errorf("lists %v, want %s", names, tt.want)
			}
			// What is hidden is also refused. Over MCP the tool is simply unknown. Through the
			// pipeline itself (the dev API path) the answer is scope_insufficient.
			wantDenied := !strings.Contains(tt.want, "sell_resource")
			_, mcpErr := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "sell_resource", Arguments: map[string]any{"resource": "food", "quantity": 1}})
			if wantDenied != (mcpErr != nil) {
				t.Errorf("MCP call error = %v, want denied=%v", mcpErr, wantDenied)
			}
			res2 := svc.Call(context.Background(), core.Principal{TenantID: "t", PlayerID: "p1", Scope: tt.scope}, "sell_resource", json.RawMessage(`{"resource":"food","quantity":1}`))
			if wantDenied && res2.Code != "scope_insufficient" || !wantDenied && !res2.OK {
				t.Errorf("pipeline call = %+v, want denied=%v", res2, wantDenied)
			}
		})
	}
}
