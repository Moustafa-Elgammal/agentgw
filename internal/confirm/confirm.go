// Package confirm holds player confirmations: a tool marked confirm needs the
// player's approval for each call, and the agent cannot approve for itself.
//
// Lifecycle of one confirmation (see docs/DECISIONS.md, Confirmations):
//
//	pending --Approve--> approved --Gate--> claimed --Settle(ok)--> consumed
//	   |                    |                  |
//	   +------Deny----------+                  +--Settle(!ok)--> approved
//
// A confirmation is bound to (tenant, player, tool, hash of the validated
// arguments) and expires after TTL.
package confirm

import (
	"context"
	"errors"
	"time"

	"github.com/Moustafa-Elgammal/agentgw/internal/core"
)

// TTL is how long a confirmation stays usable after it is requested.
const TTL = 10 * time.Minute

// State is where a confirmation is in its lifecycle.
type State string

const (
	StatePending  State = "pending"
	StateApproved State = "approved"
	StateClaimed  State = "claimed"
	StateConsumed State = "consumed"
	StateDenied   State = "denied"
)

// Confirmation is one approval request.
type Confirmation struct {
	ID        string    `json:"id"`
	Tenant    string    `json:"tenant"`
	Player    string    `json:"player"`
	Tool      string    `json:"tool"`
	ArgsSHA   string    `json:"args_sha256"`
	State     State     `json:"state"`
	CreatedAt time.Time `json:"created_at"`
	ExpiresAt time.Time `json:"expires_at"`
}

// Status is what the gateway learns from Gate.
type Status int

const (
	// statusInvalid is the zero value. A Gate that was never filled in, for example
	// returned alongside an error, must not read as an approval.
	statusInvalid Status = iota
	// StatusClaimed means an approval was found and claimed. The caller proceeds and
	// must call Settle with the returned ID.
	StatusClaimed
	// StatusCreated means no live confirmation existed and a pending one was made.
	StatusCreated
	// StatusPending means the same call is already waiting for the player.
	StatusPending
	// StatusDenied means the player denied this exact call and the denial has not expired.
	StatusDenied
	// StatusInUse means an approved call is executing right now.
	StatusInUse
)

// Valid reports whether s is one of the defined statuses, not the zero value.
func (s Status) Valid() bool { return s > statusInvalid && s <= StatusInUse }

// Gate is the result of checking a call against the confirmations.
type Gate struct {
	Status    Status
	ID        string
	ExpiresAt time.Time
}

// Errors from Approve and Deny.
var (
	// ErrNotFound covers "no such confirmation", "belongs to another player" and "expired".
	// It is one error on purpose, so a player cannot probe other players' ids.
	ErrNotFound = errors.New("confirmation not found")
	// ErrState means the confirmation exists but cannot make that move.
	ErrState = errors.New("confirmation is not in a state that allows this")
)

// Store keeps confirmations. Every method must be safe for concurrent use,
// across processes for shared stores.
type Store interface {
	// Gate checks a call. It atomically claims an approved confirmation, reports a
	// waiting, denied or in-use one, or creates a pending one.
	Gate(ctx context.Context, p core.Principal, tool, argsSHA string) (Gate, error)
	// Settle finishes a claim: ok consumes it, a failed call returns it to approved.
	// Settling something that is not claimed is a no-op.
	Settle(ctx context.Context, id string, ok bool) error
	// Approve moves a pending confirmation to approved. Only the owning player can.
	// Approving an approved one is a no-op.
	Approve(ctx context.Context, p core.Principal, id string) error
	// Deny moves a pending or approved confirmation to denied. Only the owning player can.
	// Denying a denied one is a no-op.
	Deny(ctx context.Context, p core.Principal, id string) error
	// Pending lists the player's live pending confirmations, oldest first.
	Pending(ctx context.Context, p core.Principal) ([]Confirmation, error)
}
