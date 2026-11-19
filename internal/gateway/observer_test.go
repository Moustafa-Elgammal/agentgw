package gateway

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Moustafa-Elgammal/agentgw/internal/audit"
	"github.com/Moustafa-Elgammal/agentgw/internal/connector"
	"github.com/Moustafa-Elgammal/agentgw/internal/manifest"
	"github.com/Moustafa-Elgammal/agentgw/internal/policy"
)

type obsRec struct {
	mu   sync.Mutex
	seen [][2]string
	d    []time.Duration
}

func (o *obsRec) ObserveCall(tool, outcome string, d time.Duration) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.seen = append(o.seen, [2]string{tool, outcome})
	o.d = append(o.d, d)
}

func TestObserverSeesEveryCallOnce(t *testing.T) {
	m, err := manifest.Parse(strings.NewReader(doc))
	if err != nil {
		t.Fatal(err)
	}
	var fail bool
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if fail {
			w.WriteHeader(500)
			return
		}
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()
	rec := &obsRec{}
	svc := New(m, policy.NewMemory(policy.DefaultLimits, nil), connector.NewHTTP(srv.URL, "k", secret, connector.AllowPrivate()), &audit.Memory{}, WithObserver(rec))

	tests := []struct {
		name, tool, args string
		backendDown      bool
		wantTool, want   string
	}{
		{"ok", "get_state", `{}`, false, "get_state", "ok"},
		{"unknown tool is bounded", "made_up_by_attacker", `{}`, false, "unknown", "unknown_tool"},
		{"invalid arguments", "sell_resource", `{"resource":"gold","quantity":1}`, false, "sell_resource", "invalid_arguments"},
		{"backend refuses", "get_state", `{}`, true, "get_state", ""}, // code is whatever the backend path yields; checked below
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mu.Lock()
			fail = tt.backendDown
			mu.Unlock()
			before := len(rec.seen)
			res := svc.Call(context.Background(), player, tt.tool, json.RawMessage(tt.args))
			if len(rec.seen) != before+1 {
				t.Fatalf("observer called %d times, want 1", len(rec.seen)-before)
			}
			got := rec.seen[before]
			want := tt.want
			if want == "" {
				want = res.Code
			}
			if got[0] != tt.wantTool || got[1] != want {
				t.Fatalf("observed %v, want [%s %s] (result %+v)", got, tt.wantTool, want, res)
			}
			if rec.d[before] < 0 {
				t.Error("negative duration")
			}
		})
	}
}

func TestNoObserverIsFine(t *testing.T) {
	f := setup(t, policy.DefaultLimits)
	if res := call(f, "get_state", `{}`); !res.OK {
		t.Fatalf("%+v", res)
	}
}
