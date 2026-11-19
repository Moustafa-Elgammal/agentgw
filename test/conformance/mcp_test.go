package conformance

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/Moustafa-Elgammal/agentgw/internal/audit"
	"github.com/Moustafa-Elgammal/agentgw/internal/connector"
	"github.com/Moustafa-Elgammal/agentgw/internal/gateway"
	"github.com/Moustafa-Elgammal/agentgw/internal/manifest"
	"github.com/Moustafa-Elgammal/agentgw/internal/mcpserver"
	"github.com/Moustafa-Elgammal/agentgw/internal/policy"
	"github.com/Moustafa-Elgammal/agentgw/pkg/agentsig"
)

// slowBackend verifies signatures, and for the paths in block it holds the request open
// until the request context is cancelled, reporting both events.
type slowBackend struct {
	srv       *httptest.Server
	block     atomic.Bool
	started   chan string
	cancelled chan string
}

func newSlowBackend(t *testing.T) *slowBackend {
	t.Helper()
	b := &slowBackend{started: make(chan string, 16), cancelled: make(chan string, 16)}
	b.block.Store(true)
	b.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if _, err := agentsig.Verify(r, body, func(id string) []byte {
			if id == keyID {
				return secret
			}
			return nil
		}, time.Now(), 0); err != nil {
			http.Error(w, err.Error(), http.StatusUnauthorized)
			return
		}
		if b.block.Load() {
			b.started <- r.URL.Path
			select {
			case <-r.Context().Done():
				b.cancelled <- r.URL.Path
			case <-time.After(10 * time.Second):
			}
			return
		}
		_, _ = w.Write([]byte(`{"done":true}`))
	}))
	t.Cleanup(b.srv.Close)
	return b
}

func (b *slowBackend) unblock() { b.block.Store(false) }

func expect(t *testing.T, ch chan string, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for: %s", what)
	}
}

// TestCancelledCallStopsBackendAndReleasesEverything covers a client that gives up on an
// in-flight call, by cancelling its context or by hitting a deadline.
func TestCancelledCallStopsBackendAndReleasesEverything(t *testing.T) {
	modes := []struct {
		name string
		ctx  func() (context.Context, context.CancelFunc, func())
	}{
		{"client cancel", func() (context.Context, context.CancelFunc, func()) {
			ctx, cancel := context.WithCancel(context.Background())
			return ctx, cancel, func() { time.Sleep(100 * time.Millisecond); cancel() }
		}},
		{"client deadline", func() (context.Context, context.CancelFunc, func()) {
			ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
			return ctx, cancel, func() {}
		}},
	}
	tools := []struct {
		name, path string
		args       map[string]any
	}{
		{"sell_resource", "/api/v1/market/sell", sell(300)},
		{"collect_taxes", "/api/v1/taxes/collect", nil},
	}
	for _, mode := range modes {
		for _, tl := range tools {
			t.Run(mode.name+"/"+tl.name, func(t *testing.T) {
				m, err := manifest.LoadFile(manifestPath)
				if err != nil {
					t.Fatal(err)
				}
				b, a := newSlowBackend(t), &audit.Memory{}
				pol := policy.NewMemory(policy.DefaultLimits, nil)
				svc := gateway.New(m, pol, connector.NewHTTP(b.srv.URL, keyID, secret, connector.AllowPrivate()), a)
				ts := httptest.NewServer(mcpserver.Handler(svc, mcpserver.DevAuth("conf")))
				defer ts.Close()
				g := &rig{url: ts.URL}
				cs := g.client(t, "p1")

				before := runtime.NumGoroutine()
				ctx, cancel, trigger := mode.ctx()
				defer cancel()
				done := make(chan error, 1)
				go func() {
					_, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: tl.name, Arguments: tl.args})
					done <- err
				}()
				expect(t, b.started, "backend to receive the request")
				trigger()
				if err := <-done; err == nil {
					t.Fatal("a cancelled call must return an error to the client")
				}
				expect(t, b.cancelled, "backend request context to be cancelled")

				// The gateway finishes its bookkeeping after the client has gone.
				deadline := time.Now().Add(5 * time.Second)
				for len(a.Events()) == 0 && time.Now().Before(deadline) {
					time.Sleep(10 * time.Millisecond)
				}
				ev := a.Events()
				if len(ev) != 1 || ev[0].Outcome != "error" || ev[0].Code != "outcome_unknown" {
					t.Fatalf("want exactly one outcome_unknown audit event, got %+v", ev)
				}

				// Reservation released and no cooldown: the same call goes through at once.
				b.unblock()
				cs2 := g.client(t, "p1")
				res, err := cs2.CallTool(context.Background(), &mcp.CallToolParams{Name: tl.name, Arguments: tl.args})
				if err != nil || res.IsError {
					t.Fatalf("follow-up call must succeed after a cancel: %v %+v", err, res)
				}
				if tl.name == "sell_resource" { // the whole cap of 300 was free again, now it is spent
					res, _ := cs2.CallTool(context.Background(), &mcp.CallToolParams{Name: tl.name, Arguments: sell(1)})
					var body map[string]any
					_ = json.Unmarshal([]byte(res.Content[0].(*mcp.TextContent).Text), &body)
					if body["code"] != "daily_amount_cap" {
						t.Fatalf("cap must be spent by the follow-up only, got %v", body)
					}
				} else {
					res, _ := cs2.CallTool(context.Background(), &mcp.CallToolParams{Name: tl.name})
					var body map[string]any
					_ = json.Unmarshal([]byte(res.Content[0].(*mcp.TextContent).Text), &body)
					if body["code"] != "cooldown" {
						t.Fatalf("cooldown must start only after the success, got %v", body)
					}
				}
				if n := len(a.Events()); n != 3 {
					t.Errorf("want 3 audit events in total, got %d", n)
				}

				_ = cs.Close()
				_ = cs2.Close()
				http.DefaultClient.CloseIdleConnections()
				http.DefaultTransport.(*http.Transport).CloseIdleConnections()
				var after int
				for i := 0; i < 100; i++ {
					after = runtime.NumGoroutine()
					if after <= before+4 {
						return
					}
					time.Sleep(50 * time.Millisecond)
				}
				t.Errorf("goroutines grew from %d to %d", before, after)
			})
		}
	}
}
