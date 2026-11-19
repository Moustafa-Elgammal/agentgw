package gateway

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/Moustafa-Elgammal/agentgw/internal/audit"
	"github.com/Moustafa-Elgammal/agentgw/internal/connector"
	"github.com/Moustafa-Elgammal/agentgw/internal/manifest"
	"github.com/Moustafa-Elgammal/agentgw/internal/policy"
)

const swapDoc = `{"version":1,"tools":[
 {"name":"get_state","description":"State.","tier":"read","http":{"method":"GET","url":"/state"}},
 {"name":"look_around","description":"Look.","tier":"read","http":{"method":"GET","url":"/look"}}]}`

func TestSetManifestSwapsTools(t *testing.T) {
	m2, err := manifest.Parse(strings.NewReader(swapDoc))
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		tool   string
		before string // code, "" means ok
		after  string
		inList bool // in Tools() after
	}{
		{"get_state", "", "", true},
		{"collect", "", "unknown_tool", false},    // removed by the swap
		{"look_around", "unknown_tool", "", true}, // added by the swap
	}
	f := setup(t, policy.DefaultLimits)
	for _, tt := range tests {
		if got := call(f, tt.tool, `{}`).Code; got != tt.before {
			t.Errorf("before %s: code %q, want %q", tt.tool, got, tt.before)
		}
	}
	f.svc.SetManifest(m2)
	listed := map[string]bool{}
	for _, tl := range f.svc.Tools() {
		listed[tl.Name] = true
	}
	for _, tt := range tests {
		if got := call(f, tt.tool, `{}`).Code; got != tt.after {
			t.Errorf("after %s: code %q, want %q", tt.tool, got, tt.after)
		}
		if listed[tt.tool] != tt.inList {
			t.Errorf("%s listed = %v, want %v", tt.tool, listed[tt.tool], tt.inList)
		}
	}
}

func TestSetManifestConcurrentWithCalls(t *testing.T) {
	m1, _ := manifest.Parse(strings.NewReader(doc))
	m2, _ := manifest.Parse(strings.NewReader(swapDoc))
	// Own backend: the shared fixture's hit counter is not safe for concurrent calls.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`{}`)) }))
	defer srv.Close()
	svc := New(m1, policy.NewMemory(policy.Limits{ActionsPerMinute: 1 << 20, ReadsPerMinute: 1 << 20}, nil),
		connector.NewHTTP(srv.URL, "k1", secret), &audit.Memory{})
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				svc.Call(context.Background(), player, "get_state", json.RawMessage(`{}`))
				_ = svc.Tools()
			}
		}()
	}
	for j := 0; j < 100; j++ {
		svc.SetManifest(m2)
		svc.SetManifest(m1)
	}
	wg.Wait()
}
