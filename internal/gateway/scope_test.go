package gateway

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/Moustafa-Elgammal/agentgw/internal/core"
	"github.com/Moustafa-Elgammal/agentgw/internal/policy"
)

func TestScopeLimitsWhatACoachCanCall(t *testing.T) {
	f := setup(t, policy.DefaultLimits) // doc: get_state read, sell_resource medium, collect safe, gift sensitive
	coach := core.Principal{TenantID: "t", PlayerID: "p1", Scope: core.ScopeCoach}
	operator := core.Principal{TenantID: "t", PlayerID: "p1", Scope: core.ScopeOperator}
	rogue := core.Principal{TenantID: "t", PlayerID: "p1", Scope: core.Scope("admin")}
	tests := []struct {
		name string
		who  core.Principal
		tool string
		args string
		want string // "" means success
	}{
		{"coach reads", coach, "get_state", `{}`, ""},
		{"coach cannot sell", coach, "sell_resource", `{"resource":"food","quantity":1}`, "scope_insufficient"},
		{"coach cannot collect", coach, "collect", `{}`, "scope_insufficient"},
		{"operator sells", operator, "sell_resource", `{"resource":"food","quantity":1}`, ""},
		{"unscoped is not limited", player, "sell_resource", `{"resource":"food","quantity":1}`, ""},
		{"an unknown scope allows nothing", rogue, "get_state", `{}`, "scope_insufficient"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			before := *f.hits
			r := f.svc.Call(context.Background(), tc.who, tc.tool, json.RawMessage(tc.args))
			if tc.want == "" {
				if !r.OK {
					t.Fatalf("want success, got %+v", r)
				}
				return
			}
			if r.OK || r.Code != tc.want || *f.hits != before {
				t.Fatalf("want %s without reaching the backend, got %+v (hits %d to %d)", tc.want, r, before, *f.hits)
			}
		})
	}
}
