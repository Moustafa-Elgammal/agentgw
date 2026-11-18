package bridge

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/Moustafa-Elgammal/agentgw/internal/audit"
	"github.com/Moustafa-Elgammal/agentgw/internal/core"
	"github.com/Moustafa-Elgammal/agentgw/internal/gateway"
	"github.com/Moustafa-Elgammal/agentgw/internal/manifest"
	"github.com/Moustafa-Elgammal/agentgw/internal/mcpserver"
	"github.com/Moustafa-Elgammal/agentgw/internal/policy"
)

const doc = `{"version":1,"tools":[
 {"name":"get_state","description":"State.","tier":"read","http":{"method":"GET","url":"/state"}},
 {"name":"sell_resource","description":"Sell.","tier":"medium",
  "http":{"method":"POST","url":"/sell"},
  "params":{"resource":{"type":"string","enum":["food","water"]},
            "quantity":{"type":"integer","min":1,"max":1000}}}
]}`

// fakeConn records calls. With block set it waits for cancellation and reports it.
type fakeConn struct {
	mu        sync.Mutex
	calls     []string
	block     bool
	started   chan struct{}
	cancelled chan struct{}
}

func (f *fakeConn) Call(ctx context.Context, p core.Principal, t core.Tool, _ map[string]any, _ string) (core.Result, error) {
	f.mu.Lock()
	f.calls = append(f.calls, p.PlayerID+":"+t.Name)
	block := f.block
	f.mu.Unlock()
	if block {
		close(f.started)
		<-ctx.Done()
		close(f.cancelled)
		return core.Result{}, ctx.Err()
	}
	return core.Result{OK: true, Data: json.RawMessage(`{"done":true}`)}, nil
}

func (f *fakeConn) got() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

type upstream struct {
	url  string
	conn *fakeConn
	ts   *httptest.Server
}

func newUpstream(t *testing.T, auth mcpserver.Authenticator) upstream {
	t.Helper()
	m, err := manifest.Parse(strings.NewReader(doc))
	if err != nil {
		t.Fatal(err)
	}
	conn := &fakeConn{started: make(chan struct{}), cancelled: make(chan struct{})}
	svc := gateway.New(m, policy.NewMemory(policy.DefaultLimits, nil), conn, &audit.Memory{})
	if auth == nil {
		auth = mcpserver.DevAuth("t")
	}
	ts := httptest.NewServer(mcpserver.Handler(svc, auth))
	t.Cleanup(ts.Close)
	return upstream{ts.URL, conn, ts}
}

// runBridge starts Run with an in-memory local transport and returns the local client
// session, the log buffer and a channel with Run's result.
func runBridge(t *testing.T, cfg Config) (*mcp.ClientSession, *syncBuf, <-chan error) {
	t.Helper()
	localSrv, localCli := mcp.NewInMemoryTransports()
	logs := &syncBuf{}
	cfg.Transport = localSrv
	cfg.Log = logs
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Run(ctx, cfg) }()
	t.Cleanup(cancel)

	c := mcp.NewClient(&mcp.Implementation{Name: "local", Version: "0"}, nil)
	cs, err := c.Connect(ctx, localCli, nil)
	if err != nil {
		select {
		case rerr := <-done:
			t.Fatalf("connect: %v (Run: %v, logs: %s)", err, rerr, logs.String())
		default:
			t.Fatalf("connect: %v", err)
		}
	}
	t.Cleanup(func() { _ = cs.Close() })
	return cs, logs, done
}

type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func devHeaders(player string) http.Header {
	return http.Header{"X-Dev-Player": {player}}
}

func call(t *testing.T, cs *mcp.ClientSession, name string, args map[string]any) *mcp.CallToolResult {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("CallTool %s: %v", name, err)
	}
	return res
}

func text(t *testing.T, res *mcp.CallToolResult) string {
	t.Helper()
	return res.Content[0].(*mcp.TextContent).Text
}

func TestToolsMatchUpstream(t *testing.T) {
	up := newUpstream(t, nil)
	direct := mcp.NewClient(&mcp.Implementation{Name: "direct", Version: "0"}, nil)
	dcs, err := direct.Connect(context.Background(), &mcp.StreamableClientTransport{
		Endpoint: up.url, HTTPClient: &http.Client{Transport: headerRT{http.DefaultTransport, devHeaders("p"), &atomic.Bool{}}},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = dcs.Close() }()
	want, err := dcs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}

	cs, _, _ := runBridge(t, Config{URL: up.url, Headers: devHeaders("p")})
	got, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Tools) != 2 || len(want.Tools) != 2 {
		t.Fatalf("want 2 tools, got %d (direct %d)", len(got.Tools), len(want.Tools))
	}
	for i, w := range want.Tools {
		g := got.Tools[i]
		if g.Name != w.Name || g.Description != w.Description {
			t.Errorf("tool %d: %s/%q vs %s/%q", i, g.Name, g.Description, w.Name, w.Description)
		}
		if !reflect.DeepEqual(g.InputSchema, w.InputSchema) {
			t.Errorf("%s input schema differs:\n%v\n%v", w.Name, g.InputSchema, w.InputSchema)
		}
		if !reflect.DeepEqual(g.OutputSchema, w.OutputSchema) {
			t.Errorf("%s output schema differs", w.Name)
		}
		if !reflect.DeepEqual(g.Annotations, w.Annotations) {
			t.Errorf("%s annotations differ: %v vs %v", w.Name, g.Annotations, w.Annotations)
		}
	}
}

func TestCalls(t *testing.T) {
	tests := []struct {
		name     string
		tool     string
		args     map[string]any
		wantErr  bool
		wantCode string
		wantCall bool
	}{
		{"read", "get_state", nil, false, "", true},
		{"action", "sell_resource", map[string]any{"resource": "food", "quantity": 5}, false, "", true},
		{"denial stays a result", "sell_resource", map[string]any{"resource": "gold", "quantity": 5}, true, "invalid_arguments", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			up := newUpstream(t, nil)
			cs, _, _ := runBridge(t, Config{URL: up.url, Headers: devHeaders("desktop")})
			res := call(t, cs, tc.tool, tc.args)
			if res.IsError != tc.wantErr {
				t.Fatalf("isError = %v: %s", res.IsError, text(t, res))
			}
			var body map[string]any
			if err := json.Unmarshal([]byte(text(t, res)), &body); err != nil {
				t.Fatalf("text is not JSON: %v", err)
			}
			if tc.wantCode != "" {
				if body["code"] != tc.wantCode {
					t.Errorf("code = %v", body["code"])
				}
				sc, _ := res.StructuredContent.(map[string]any)
				if sc["code"] != tc.wantCode || sc["ok"] != false {
					t.Errorf("structuredContent = %v", res.StructuredContent)
				}
			} else if sc, _ := res.StructuredContent.(map[string]any); sc["ok"] != true {
				t.Errorf("structuredContent = %v", res.StructuredContent)
			}
			calls := up.conn.got()
			if tc.wantCall {
				if len(calls) != 1 || calls[0] != "desktop:"+tc.tool {
					t.Errorf("principal must come from the header, calls = %v", calls)
				}
			} else if len(calls) != 0 {
				t.Errorf("backend must not be reached: %v", calls)
			}
		})
	}
}

func TestUnreachableUpstreamFailsRun(t *testing.T) {
	ts := httptest.NewServer(http.NotFoundHandler())
	url := ts.URL + "/mcp"
	ts.Close()
	var logs syncBuf
	localSrv, _ := mcp.NewInMemoryTransports()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	err := Run(ctx, Config{URL: url, Transport: localSrv, Log: &logs})
	if err == nil || !strings.Contains(err.Error(), url) {
		t.Fatalf("want an error naming the url, got %v", err)
	}
}

func TestUnauthorizedIsClear(t *testing.T) {
	up := newUpstream(t, func(*http.Request) (core.Principal, error) {
		return core.Principal{}, errors.New("bad token")
	})
	var logs syncBuf
	localSrv, _ := mcp.NewInMemoryTransports()
	err := Run(context.Background(), Config{URL: up.url, Transport: localSrv, Log: &logs})
	if err == nil || !strings.Contains(err.Error(), "401") || !strings.Contains(err.Error(), "--header") {
		t.Fatalf("want a clear 401 error, got %v", err)
	}
	if !strings.Contains(logs.String(), "--header") {
		t.Errorf("stderr log should mention the header, got %q", logs.String())
	}
}

func TestUpstreamFailureAfterStartIsToolError(t *testing.T) {
	up := newUpstream(t, nil)
	cs, logs, _ := runBridge(t, Config{URL: up.url, Headers: devHeaders("p")})
	up.ts.Close()
	res := call(t, cs, "get_state", nil)
	if !res.IsError || !strings.Contains(text(t, res), "could not be reached") {
		t.Fatalf("want a tool error, got %+v", res)
	}
	if logs.String() == "" {
		t.Error("failure should be logged")
	}
	// The local session survives.
	if err := cs.Ping(context.Background(), nil); err != nil {
		t.Errorf("local session should survive: %v", err)
	}
}

func TestCancelPropagatesUpstream(t *testing.T) {
	up := newUpstream(t, nil)
	up.conn.mu.Lock()
	up.conn.block = true
	up.conn.mu.Unlock()
	cs, _, _ := runBridge(t, Config{URL: up.url, Headers: devHeaders("p")})

	ctx, cancel := context.WithCancel(context.Background())
	errc := make(chan error, 1)
	go func() {
		_, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "sell_resource", Arguments: map[string]any{"resource": "food", "quantity": 1}})
		errc <- err
	}()
	select {
	case <-up.conn.started:
	case <-time.After(5 * time.Second):
		t.Fatal("upstream call never started")
	}
	cancel()
	select {
	case <-up.conn.cancelled:
	case <-time.After(5 * time.Second):
		t.Fatal("upstream call was not cancelled")
	}
	if err := <-errc; err == nil {
		t.Error("cancelled call should return an error")
	}
}

func TestParseHeader(t *testing.T) {
	tests := []struct {
		in, name, value string
		wantErr         bool
	}{
		{"X-Dev-Player: desktop", "X-Dev-Player", "desktop", false},
		{"Authorization: Bearer a:b", "Authorization", "Bearer a:b", false},
		{"Empty:", "Empty", "", false},
		{"nocolon", "", "", true},
		{": v", "", "", true},
	}
	for _, tc := range tests {
		n, v, err := ParseHeader(tc.in)
		if (err != nil) != tc.wantErr || n != tc.name || v != tc.value {
			t.Errorf("%q: got %q %q %v", tc.in, n, v, err)
		}
	}
}

// The bridge must never touch stdout: it is the protocol channel. Everything goes to Config.Log.
func TestNothingWrittenToStdout(t *testing.T) {
	up := newUpstream(t, nil)
	r, w, err := osPipe()
	if err != nil {
		t.Fatal(err)
	}
	restore := swapStdout(w)
	cs, logs, _ := runBridge(t, Config{URL: up.url, Headers: devHeaders("p")})
	call(t, cs, "get_state", nil)
	call(t, cs, "sell_resource", map[string]any{"resource": "gold", "quantity": 1})
	up.ts.Close()
	call(t, cs, "get_state", nil)
	restore()
	_ = w.Close()
	out := make([]byte, 4096)
	n, _ := r.Read(out)
	if n != 0 {
		t.Errorf("bridge wrote to stdout: %q", out[:n])
	}
	if !strings.Contains(logs.String(), "connected") {
		t.Errorf("expected diagnostics in the supplied writer, got %q", logs.String())
	}
}
