// Package gateway wires manifest, policy, connector and audit into one pipeline.
//
// Order of a call, and why:
//
//  1. look up the tool
//  2. Admit    (rate limit, tier switch)  - counts the attempt even if invalid
//  3. validate arguments against the manifest
//  4. Confirm  (tools marked confirm need the player's approval; claims it)
//  5. Reserve  (cooldown, daily caps)
//  6. call the backend
//  7. Settle   (keep or release the reservation and the claimed approval)
//  8. audit    (every outcome, including denials)
package gateway

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/Moustafa-Elgammal/agentgw/internal/audit"
	"github.com/Moustafa-Elgammal/agentgw/internal/confirm"
	"github.com/Moustafa-Elgammal/agentgw/internal/connector"
	"github.com/Moustafa-Elgammal/agentgw/internal/core"
	"github.com/Moustafa-Elgammal/agentgw/internal/manifest"
	"github.com/Moustafa-Elgammal/agentgw/internal/policy"
)

// Service executes tool calls.
type Service struct {
	tools   map[string]core.Tool
	policy  policy.Policy
	conn    connector.Connector
	audit   audit.Auditor
	confirm confirm.Store // nil: tools that need confirmation fail closed
}

// Option customizes a Service.
type Option func(*Service)

// WithConfirmations sets where player confirmations live. Without it, tools
// marked confirm are refused with confirmation_unavailable.
func WithConfirmations(c confirm.Store) Option { return func(s *Service) { s.confirm = c } }

// New builds a Service from a validated manifest.
func New(m *manifest.Manifest, p policy.Policy, c connector.Connector, a audit.Auditor, opts ...Option) *Service {
	tools := make(map[string]core.Tool, len(m.Tools))
	for _, t := range m.Tools {
		tools[t.Name] = t
	}
	s := &Service{tools: tools, policy: p, conn: c, audit: a}
	for _, o := range opts {
		o(s)
	}
	return s
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

	// Confirmation gate: a call that needs the player's approval stops here until it has one.
	claim := ""
	if t.Confirm {
		var stop *core.Result
		claim, stop = s.gate(ctx, p, t, args)
		if stop != nil {
			record(t, stopOutcome(stop.Code), stop.Code)
			return *stop
		}
	}
	settleClaim := func(ok bool) {
		if claim != "" {
			_ = s.confirm.Settle(ctx, claim, ok) // a failed call returns the approval to the player
		}
	}

	res, rd, err := s.policy.Reserve(ctx, p, t, amount)
	if err != nil {
		settleClaim(false)
		record(t, "error", "policy_unavailable")
		return core.Denied("policy_unavailable", "safety checks are unavailable, try again later")
	}
	if !rd.Allowed {
		settleClaim(false)
		record(t, "denied", rd.Code)
		return core.Denied(rd.Code, rd.Message)
	}

	result, err := s.conn.Call(ctx, p, t, args, newIdempotencyKey())
	if err != nil {
		_ = s.policy.Settle(ctx, res, false)
		settleClaim(false)
		record(t, "error", "backend_unavailable")
		return core.Denied("backend_unavailable", "the game could not be reached, nothing was changed")
	}
	_ = s.policy.Settle(ctx, res, result.OK)
	settleClaim(result.OK)
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

// gate runs the confirmation check. It returns the claim id when the call may proceed,
// or a Result that stops it.
func (s *Service) gate(ctx context.Context, p core.Principal, t core.Tool, args map[string]any) (string, *core.Result) {
	if s.confirm == nil {
		return "", deniedPtr("confirmation_unavailable", "this action needs the player's approval, but approvals are not available. Nothing was changed.")
	}
	g, err := s.confirm.Gate(ctx, p, t.Name, argsDigest(args))
	if err != nil {
		return "", deniedPtr("confirmation_unavailable", "approvals are unavailable right now, try again later. Nothing was changed.")
	}
	if !g.Status.Valid() { // a store bug must never read as an approval
		return "", deniedPtr("confirmation_unavailable", "approvals are unavailable right now, try again later. Nothing was changed.")
	}
	switch g.Status {
	case confirm.StatusClaimed:
		return g.ID, nil
	case confirm.StatusDenied:
		return "", deniedPtr("confirmation_denied", fmt.Sprintf("the player denied this exact %s call. Do not repeat it. Ask the player what they want instead.", t.Name))
	case confirm.StatusInUse:
		return "", deniedPtr("confirmation_in_use", "this approved call is already running. Wait for it to finish instead of repeating it.")
	}
	// Created or Pending: waiting for the player.
	r := core.Denied("confirmation_required", fmt.Sprintf(
		"%s needs the player's approval. Ask the player to approve confirmation %s in the game, then repeat this exact call. It expires at %s.",
		t.Name, g.ID, g.ExpiresAt.UTC().Format("15:04 UTC")))
	r.Data, _ = json.Marshal(map[string]any{"confirmation_id": g.ID, "expires_at": g.ExpiresAt.UTC()})
	return "", &r
}

func deniedPtr(code, msg string) *core.Result {
	r := core.Denied(code, msg)
	return &r
}

// stopOutcome maps a stopped call to an audit outcome: infrastructure trouble is an
// error, everything else is a denial.
func stopOutcome(code string) string {
	if code == "confirmation_unavailable" {
		return "error"
	}
	return "denied"
}

// argsDigest hashes validated arguments. json.Marshal sorts map keys, so key order
// and whitespace in the agent's request do not change the digest.
func argsDigest(args map[string]any) string {
	b, _ := json.Marshal(args)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
