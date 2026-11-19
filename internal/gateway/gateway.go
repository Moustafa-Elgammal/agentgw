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
	"errors"
	"fmt"
	"net"
	"sort"
	"sync/atomic"
	"time"

	"github.com/Moustafa-Elgammal/agentgw/internal/audit"
	"github.com/Moustafa-Elgammal/agentgw/internal/confirm"
	"github.com/Moustafa-Elgammal/agentgw/internal/connector"
	"github.com/Moustafa-Elgammal/agentgw/internal/core"
	"github.com/Moustafa-Elgammal/agentgw/internal/manifest"
	"github.com/Moustafa-Elgammal/agentgw/internal/policy"
)

// Service executes tool calls.
type Service struct {
	tools    atomic.Pointer[map[string]core.Tool] // swapped whole by SetManifest, never mutated
	policy   policy.Policy
	conn     connector.Connector
	audit    audit.Auditor
	confirm  confirm.Store // nil: tools that need confirmation fail closed
	obs      Observer      // nil: no metrics
	maxScope core.Scope    // caps every principal's scope; empty means no cap
}

// Observer is told about every finished call. It must not block. tool is the manifest name,
// or "unknown" when the name is not in the manifest (so it is safe as a metric label).
// outcome is "ok" or the result's denial code.
type Observer interface {
	ObserveCall(tool, outcome string, d time.Duration)
}

// WithObserver reports every call to o. It does not change what a call does.
func WithObserver(o Observer) Option { return func(s *Service) { s.obs = o } }

// Option customizes a Service.
type Option func(*Service)

// WithMaxScope caps what any principal may do, whatever its grant says. ScopeCoach makes the
// gateway read only for everyone, which is how a pilot starts. Empty or ScopeOperator changes
// nothing.
func WithMaxScope(max core.Scope) Option { return func(s *Service) { s.maxScope = max } }

// Capped returns p with its scope limited by WithMaxScope. Front doors call it before they
// decide which tools to list, so the list matches what Call will allow.
func (s *Service) Capped(p core.Principal) core.Principal {
	p.Scope = p.Scope.Cap(s.maxScope)
	return p
}

// WithConfirmations sets where player confirmations live. Without it, tools
// marked confirm are refused with confirmation_unavailable.
func WithConfirmations(c confirm.Store) Option { return func(s *Service) { s.confirm = c } }

// New builds a Service from a validated manifest.
func New(m *manifest.Manifest, p policy.Policy, c connector.Connector, a audit.Auditor, opts ...Option) *Service {
	s := &Service{policy: p, conn: c, audit: a}
	s.SetManifest(m)
	for _, o := range opts {
		o(s)
	}
	return s
}

// SetManifest replaces the tool set. It is safe to call while calls are running: a call that
// already looked up its tool finishes with the version it started with, and the next call
// sees the new set. The manifest must already be validated.
func (s *Service) SetManifest(m *manifest.Manifest) {
	tools := make(map[string]core.Tool, len(m.Tools))
	for _, t := range m.Tools {
		tools[t.Name] = t
	}
	s.tools.Store(&tools)
}

// Tools lists the tools sorted by name.
func (s *Service) Tools() []core.Tool {
	tools := *s.tools.Load()
	out := make([]core.Tool, 0, len(tools))
	for _, t := range tools {
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Call runs one tool call for a principal. It never returns a Go error for an
// expected failure; those come back as a Result the agent can read.
func (s *Service) Call(ctx context.Context, p core.Principal, name string, raw json.RawMessage) core.Result {
	p = s.Capped(p)
	if s.obs == nil {
		return s.call(ctx, p, name, raw)
	}
	start := time.Now()
	res := s.call(ctx, p, name, raw)
	tool := name
	if _, ok := (*s.tools.Load())[name]; !ok {
		tool = "unknown"
	}
	outcome := "ok"
	if !res.OK {
		outcome = res.Code
	}
	s.obs.ObserveCall(tool, outcome, time.Since(start))
	return res
}

func (s *Service) call(ctx context.Context, p core.Principal, name string, raw json.RawMessage) core.Result {
	argsHash := audit.HashArgs(raw)
	// Bookkeeping must survive the caller going away: a cancelled request still has to release
	// its reservation and leave its audit event. It keeps the values of ctx but not its
	// cancellation, and has its own deadline so a stuck store cannot hang the call forever.
	bg, stop := context.WithTimeout(context.WithoutCancel(ctx), bookkeepingTimeout)
	defer stop()
	record := func(t core.Tool, outcome, code string) {
		s.audit.Record(bg, audit.Event{
			Time: nowUTC(), Tenant: p.TenantID, Player: p.PlayerID, Tool: name,
			Tier: string(t.Tier), Outcome: outcome, Code: code, ArgsSHA: argsHash,
		})
	}

	t, ok := (*s.tools.Load())[name]
	if !ok {
		record(core.Tool{}, "denied", "unknown_tool")
		return core.Denied("unknown_tool", fmt.Sprintf("no tool named %q. List tools first.", name))
	}

	// A coach grant cannot call action tools, whatever the agent was shown. Checked
	// before Admit: it is a cheap, permanent answer, not a budgeted attempt.
	if !p.Scope.Allows(t.Tier) {
		record(t, "denied", "scope_insufficient")
		return core.Denied("scope_insufficient", fmt.Sprintf("%s changes the game, but the player only allowed read-only coaching. Ask the player to grant operator access.", name))
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
			_ = s.confirm.Settle(bg, claim, ok) // a failed call returns the approval to the player
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
		_ = s.policy.Settle(bg, res, false)
		settleClaim(false)
		if unknown := outcomeUnknown(ctx, err); unknown {
			record(t, "error", "outcome_unknown")
			return core.Denied("outcome_unknown", "the call was cut off before the game answered (cancelled or timed out). "+
				"The game may or may not have applied it. Check the game state before trying again.")
		}
		record(t, "error", "backend_unavailable")
		return core.Denied("backend_unavailable", "the game could not be reached, nothing was changed")
	}
	_ = s.policy.Settle(bg, res, result.OK)
	settleClaim(result.OK)
	if result.OK {
		record(t, "ok", "")
	} else {
		record(t, "denied", result.Code)
	}
	return result
}

// bookkeepingTimeout bounds release, settle and audit work done after the call itself.
const bookkeepingTimeout = 5 * time.Second

// outcomeUnknown reports whether a connector error happened after the request may have
// reached the game: the caller cancelled, or the request timed out in flight. A refused
// connection is not ambiguous, nothing was sent.
func outcomeUnknown(ctx context.Context, err error) bool {
	if ctx.Err() != nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var ne net.Error
	return errors.As(err, &ne) && ne.Timeout()
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
