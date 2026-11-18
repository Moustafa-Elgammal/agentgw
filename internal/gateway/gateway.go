// Package gateway wires manifest, policy, connector and audit into one pipeline.
//
// Order of a call, and why:
//
//  1. look up the tool
//  2. Admit    (rate limit, tier switch)  - counts the attempt even if invalid
//  3. validate arguments against the manifest
//  4. Reserve  (cooldown, daily caps)
//  5. call the backend
//  6. Settle   (keep or release the reservation)
//  7. audit    (every outcome, including denials)
package gateway

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/Moustafa-Elgammal/agentgw/internal/audit"
	"github.com/Moustafa-Elgammal/agentgw/internal/connector"
	"github.com/Moustafa-Elgammal/agentgw/internal/core"
	"github.com/Moustafa-Elgammal/agentgw/internal/manifest"
	"github.com/Moustafa-Elgammal/agentgw/internal/policy"
)

// Service executes tool calls.
type Service struct {
	tools  map[string]core.Tool
	policy policy.Policy
	conn   connector.Connector
	audit  audit.Auditor
}

// New builds a Service from a validated manifest.
func New(m *manifest.Manifest, p policy.Policy, c connector.Connector, a audit.Auditor) *Service {
	tools := make(map[string]core.Tool, len(m.Tools))
	for _, t := range m.Tools {
		tools[t.Name] = t
	}
	return &Service{tools: tools, policy: p, conn: c, audit: a}
}

// Tools lists the tools sorted by name.
func (s *Service) Tools() []core.Tool {
	out := make([]core.Tool, 0, len(s.tools))
	for _, t := range s.tools {
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Call runs one tool call for a principal. It never returns a Go error for an
// expected failure; those come back as a Result the agent can read.
func (s *Service) Call(ctx context.Context, p core.Principal, name string, raw json.RawMessage) core.Result {
	argsHash := audit.HashArgs(raw)
	record := func(t core.Tool, outcome, code string) {
		s.audit.Record(ctx, audit.Event{
			Time: nowUTC(), Tenant: p.TenantID, Player: p.PlayerID, Tool: name,
			Tier: string(t.Tier), Outcome: outcome, Code: code, ArgsSHA: argsHash,
		})
	}

	t, ok := s.tools[name]
	if !ok {
		record(core.Tool{}, "denied", "unknown_tool")
		return core.Denied("unknown_tool", fmt.Sprintf("no tool named %q. List tools first.", name))
	}

	d, err := s.policy.Admit(ctx, p, t)
	if err != nil { // policy store down: fail closed for actions, and for reads too in M0
		record(t, "error", "policy_unavailable")
		return core.Denied("policy_unavailable", "safety checks are unavailable, try again later")
	}
	if !d.Allowed {
		record(t, "denied", d.Code)
		return core.Denied(d.Code, d.Message)
	}

	args, amount, verr := Validate(t, raw)
	if verr != nil {
		record(t, "denied", "invalid_arguments")
		return core.Denied("invalid_arguments", verr.Error())
	}

	res, rd, err := s.policy.Reserve(ctx, p, t, amount)
	if err != nil {
		record(t, "error", "policy_unavailable")
		return core.Denied("policy_unavailable", "safety checks are unavailable, try again later")
	}
	if !rd.Allowed {
		record(t, "denied", rd.Code)
		return core.Denied(rd.Code, rd.Message)
	}

	result, err := s.conn.Call(ctx, p, t, args, newIdempotencyKey())
	if err != nil {
		_ = s.policy.Settle(ctx, res, false)
		record(t, "error", "backend_unavailable")
		return core.Denied("backend_unavailable", "the game could not be reached, nothing was changed")
	}
	_ = s.policy.Settle(ctx, res, result.OK)
	if result.OK {
		record(t, "ok", "")
	} else {
		record(t, "denied", result.Code)
	}
	return result
}

func newIdempotencyKey() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
