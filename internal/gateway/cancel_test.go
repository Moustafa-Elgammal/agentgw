package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/Moustafa-Elgammal/agentgw/internal/audit"
	"github.com/Moustafa-Elgammal/agentgw/internal/core"
	"github.com/Moustafa-Elgammal/agentgw/internal/manifest"
	"github.com/Moustafa-Elgammal/agentgw/internal/policy"
)

// ctxStrictPolicy behaves like a networked store: it refuses work on a cancelled context.
type ctxStrictPolicy struct {
	policy.Policy
	settleOK []bool
}

func (c *ctxStrictPolicy) Settle(ctx context.Context, r policy.Reservation, ok bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c.settleOK = append(c.settleOK, ok)
	return c.Policy.Settle(ctx, r, ok)
}

type ctxStrictAudit struct{ events []audit.Event }

func (a *ctxStrictAudit) Record(ctx context.Context, e audit.Event) {
	if ctx.Err() == nil {
		a.events = append(a.events, e)
	}
}

type errConn struct {
	cancel context.CancelFunc
	err    error
}

func (c errConn) Call(context.Context, core.Principal, core.Tool, map[string]any, string) (core.Result, error) {
	if c.cancel != nil {
		c.cancel()
	}
	return core.Result{}, c.err
}

type okConn struct{}

func (okConn) Call(context.Context, core.Principal, core.Tool, map[string]any, string) (core.Result, error) {
	return core.Result{OK: true}, nil
}

func TestConnectorErrorOutcomes(t *testing.T) {
	tests := []struct {
		name     string
		cancel   bool
		err      error
		wantCode string
		wantText string
	}{
		{"caller cancelled in flight", true, context.Canceled, "outcome_unknown", "Check the game state"},
		{"caller deadline in flight", true, context.DeadlineExceeded, "outcome_unknown", "Check the game state"},
		{"refused connection", false, errors.New("backend unreachable: connection refused"), "backend_unavailable", "nothing was changed"},
	}
	args := json.RawMessage(`{"resource":"food","quantity":100}`)
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m, err := manifest.Parse(strings.NewReader(doc))
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			conn := errConn{err: tc.err}
			if tc.cancel {
				conn.cancel = cancel
			}
			pol := &ctxStrictPolicy{Policy: policy.NewMemory(policy.DefaultLimits, nil)}
			aud := &ctxStrictAudit{}
			svc := New(m, pol, conn, aud)

			res := svc.Call(ctx, player, "sell_resource", args)
			if res.OK || res.Code != tc.wantCode || !strings.Contains(res.Message, tc.wantText) {
				t.Fatalf("result = %+v", res)
			}
			if len(pol.settleOK) != 1 || pol.settleOK[0] {
				t.Errorf("reservation must be released even when ctx is cancelled, settles = %v", pol.settleOK)
			}
			if len(aud.events) != 1 || aud.events[0].Code != tc.wantCode {
				t.Errorf("audit event must be written even when ctx is cancelled, got %+v", aud.events)
			}
			// The cap of 100 is free again.
			again := New(m, pol.Policy, okConn{}, &audit.Memory{}).Call(context.Background(), player, "sell_resource", args)
			if !again.OK {
				t.Errorf("cap must be released, got %+v", again)
			}
		})
	}
}
