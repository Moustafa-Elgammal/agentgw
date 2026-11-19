package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Moustafa-Elgammal/agentgw/internal/audit"
	"github.com/Moustafa-Elgammal/agentgw/internal/connector"
	"github.com/Moustafa-Elgammal/agentgw/internal/manifest"
	"github.com/Moustafa-Elgammal/agentgw/internal/policy"
)

// brokenSink is an audit store whose batch writes always fail, standing in for a Postgres outage.
type brokenSink struct {
	audit.Ring
	writes atomic.Int32
}

func (b *brokenSink) WriteBatch(context.Context, []audit.Event) error {
	b.writes.Add(1)
	return errors.New("database unavailable")
}

// A batching audit store that cannot write must not turn a good call into an error, slow it down,
// or change its result: the same rule as a failed single INSERT. The loss is counted instead.
func TestBatchedAuditFailureNeverFailsACall(t *testing.T) {
	m, err := manifest.Parse(strings.NewReader(doc))
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`{}`)) }))
	defer srv.Close()

	sink := &brokenSink{}
	var onErr atomic.Int32
	b, err := audit.NewBatcher(sink, audit.BatcherConfig{
		Queue: 4, BatchSize: 2, FlushInterval: time.Millisecond, Overflow: audit.Drop,
		OnError: func(error) { onErr.Add(1) },
	})
	if err != nil {
		t.Fatal(err)
	}
	svc := New(m, policy.NewMemory(policy.Limits{ActionsPerMinute: 1 << 20, ReadsPerMinute: 1 << 20}, nil),
		connector.NewHTTP(srv.URL, "k1", secret, connector.AllowPrivate()), b)

	start := time.Now()
	for i := 0; i < 200; i++ {
		res := svc.Call(context.Background(), player, "get_state", json.RawMessage(`{}`))
		if !res.OK {
			t.Fatalf("call %d failed because audit is down: %+v", i, res)
		}
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Errorf("200 calls took %v with a broken audit store", d)
	}
	_ = b.Close(context.Background())
	if sink.writes.Load() == 0 || onErr.Load() == 0 {
		t.Errorf("the failure was not reported: writes=%d OnError=%d", sink.writes.Load(), onErr.Load())
	}
	if b.Dropped() == 0 {
		t.Error("lost events were not counted")
	}
}
