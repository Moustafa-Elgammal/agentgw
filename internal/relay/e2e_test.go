package relay

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"github.com/Moustafa-Elgammal/agentgw/internal/audit"
	"github.com/Moustafa-Elgammal/agentgw/internal/connector"
	"github.com/Moustafa-Elgammal/agentgw/internal/core"
	"github.com/Moustafa-Elgammal/agentgw/internal/gateway"
	"github.com/Moustafa-Elgammal/agentgw/internal/manifest"
	"github.com/Moustafa-Elgammal/agentgw/internal/policy"
)

const relayDoc = `{"version":1,"tools":[
 {"name":"build_farm","description":"Build a farm.","tier":"medium","backend":"relay",
  "params":{"plot":{"type":"integer","min":1,"max":9}}}]}`

// A relay tool runs the whole pipeline: validation, the relay connector, a fake game client
// on the other end, and an audit event. The client sits on replica 1, the call enters on 0.
func TestRelayToolThroughPipeline(t *testing.T) {
	m, err := manifest.Parse(strings.NewReader(relayDoc))
	if err != nil {
		t.Fatal(err)
	}
	r := newRig(t, 2)
	aud := &audit.Memory{}
	svc := gateway.New(m, policy.NewMemory(policy.DefaultLimits, nil),
		connector.Mux{Relay: NewConnector(r.hubs[0])}, aud)
	p := core.Principal{TenantID: "t1", PlayerID: "alice"}

	// Offline: an expected denial, not a crash.
	if res := svc.Call(context.Background(), p, "build_farm", json.RawMessage(`{"plot":3}`)); res.OK || res.Code != "client_offline" {
		t.Fatalf("offline = %+v", res)
	}

	ws := r.dial(1, "alice")
	done := make(chan core.Result, 1)
	go func() {
		done <- svc.Call(context.Background(), p, "build_farm", json.RawMessage(`{"plot":3}`))
	}()
	c := answer(t, ws, map[string]any{"ok": true, "data": map[string]any{"built": "farm"}})
	if c.Tool != "build_farm" || c.Player != "alice" || c.Args["plot"] != float64(3) {
		t.Fatalf("call frame = %+v", c)
	}
	if res := <-done; !res.OK || !strings.Contains(string(res.Data), "farm") {
		t.Fatalf("result = %+v", res)
	}

	// Validation still runs before anything reaches the socket.
	if res := svc.Call(context.Background(), p, "build_farm", json.RawMessage(`{"plot":99}`)); res.OK {
		t.Fatalf("out of range accepted: %+v", res)
	}
}

type relayObs struct {
	mu    sync.Mutex
	conns int
	calls map[string]int
}

func (o *relayObs) Connections(d int) { o.mu.Lock(); o.conns += d; o.mu.Unlock() }
func (o *relayObs) Call(r string) {
	o.mu.Lock()
	if o.calls == nil {
		o.calls = map[string]int{}
	}
	o.calls[r]++
	o.mu.Unlock()
}
func (o *relayObs) snapshot() (int, map[string]int) {
	o.mu.Lock()
	defer o.mu.Unlock()
	c := map[string]int{}
	for k, v := range o.calls {
		c[k] = v
	}
	return o.conns, c
}

func TestHubObserver(t *testing.T) {
	obs := &relayObs{}
	r := newRig(t, 1, WithObserver(obs))
	ctx := context.Background()

	if _, err := r.hubs[0].Call(ctx, "t1", "bob", "x", nil, "i"); err != nil {
		t.Fatal(err)
	}
	ws := r.dial(0, "alice")
	waitFor(t, func() bool { c, _ := obs.snapshot(); return c == 1 })

	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = r.hubs[0].Call(ctx, "t1", "alice", "x", nil, "i")
	}()
	answer(t, ws, map[string]any{"ok": true})
	<-done
	go func() {
		_, _ = r.hubs[0].Call(ctx, "t1", "alice", "x", nil, "i")
	}()
	answer(t, ws, map[string]any{"ok": false, "code": "nope"})

	_ = ws.Close(1000, "bye")
	waitFor(t, func() bool { c, _ := obs.snapshot(); return c == 0 })
	waitFor(t, func() bool { _, m := obs.snapshot(); return m["ok"] == 1 && m["denied"] == 1 && m["offline"] == 1 })
}
