// Package core holds the domain types shared by every other package.
package core

import "encoding/json"

// Tier classifies how risky a tool is. It decides which policy controls apply.
type Tier string

const (
	TierRead      Tier = "read"      // state and prices; rate limit only
	TierSafe      Tier = "safe"      // same rules as a human player; cooldowns and rate limit
	TierMedium    Tier = "medium"    // spends in-game currency; daily caps
	TierSensitive Tier = "sensitive" // paid currency, gifts, deletion; disabled in v1
)

// Tiers lists every valid tier, from least to most risky.
var Tiers = []Tier{TierRead, TierSafe, TierMedium, TierSensitive}

// Valid reports whether t is a known tier.
func (t Tier) Valid() bool {
	switch t {
	case TierRead, TierSafe, TierMedium, TierSensitive:
		return true
	}
	return false
}

// IsAction reports whether tools of this tier change game state.
func (t Tier) IsAction() bool { return t != TierRead }

// Scope is what a player's grant allows the agent to do.
type Scope string

const (
	// ScopeCoach allows read tools only.
	ScopeCoach Scope = "coach"
	// ScopeOperator allows read tools and action tools.
	ScopeOperator Scope = "operator"
)

// Valid reports whether s is a known scope.
func (s Scope) Valid() bool { return s == ScopeCoach || s == ScopeOperator }

// Cap returns s limited to at most max. It is how an operator turns every grant into coach
// during a pilot: Cap(ScopeCoach) lowers operator, and the empty (not scope-limited) scope,
// to coach. An empty or operator max changes nothing. An unknown s stays as it is, so a corrupt
// grant still allows nothing.
func (s Scope) Cap(max Scope) Scope {
	if max != ScopeCoach {
		return s
	}
	switch s {
	case "", ScopeOperator:
		return ScopeCoach
	}
	return s
}

// Allows reports whether a grant with scope s may call a tool of tier t. The empty
// scope means the caller is not scope-limited (dev auth, tests). Any other unknown
// value allows nothing, so a corrupt grant fails closed.
func (s Scope) Allows(t Tier) bool {
	switch s {
	case "", ScopeOperator:
		return true
	case ScopeCoach:
		return !t.IsAction()
	}
	return false
}

// Principal identifies who a call is made for. The gateway derives it from the
// player's authorization, never from tool arguments.
type Principal struct {
	TenantID string
	PlayerID string
	ClientID string
	Scope    Scope // what the player's grant allows. Empty means not scope-limited (dev auth).
}

// Caps are optional per-tool limits. Zero means "not set".
type Caps struct {
	PerDayCalls     int    `json:"per_day_calls,omitempty"`
	PerDayAmount    int64  `json:"per_day_amount,omitempty"`
	AmountParam     string `json:"amount_param,omitempty"` // integer argument that is added to the daily amount
	CooldownSeconds int    `json:"cooldown_seconds,omitempty"`
}

// HTTPBinding describes how a tool maps onto a backend REST endpoint (level 0).
type HTTPBinding struct {
	Method string `json:"method"`
	URL    string `json:"url"` // path, may contain {param} placeholders
}

// Param describes one tool argument.
type Param struct {
	Type     string   `json:"type"` // string | integer | number | boolean
	Enum     []string `json:"enum,omitempty"`
	Min      *int64   `json:"min,omitempty"`
	Max      *int64   `json:"max,omitempty"`
	Optional bool     `json:"optional,omitempty"`
}

// Backend names how a tool reaches the game. The zero value means BackendHTTP.
const (
	BackendHTTP  = "http"  // signed REST call to the studio's backend (levels 0 and 1)
	BackendRelay = "relay" // call sent over the game client's WebSocket (M5)
)

// Tool is one capability a player's agent may call.
type Tool struct {
	Name        string           `json:"name"`
	Description string           `json:"description"`
	Tier        Tier             `json:"tier"`
	Caps        Caps             `json:"caps,omitempty"`
	Confirm     bool             `json:"confirm,omitempty"` // every call needs the player's approval
	Backend     string           `json:"backend,omitempty"` // BackendHTTP (default) or BackendRelay
	HTTP        HTTPBinding      `json:"http"`
	Params      map[string]Param `json:"params,omitempty"`
}

// Result is what a call returns to the caller (and later to the agent).
// Expected failures are results with OK=false and a machine-readable Code, so
// the agent can correct itself; they are not Go errors.
type Result struct {
	OK      bool            `json:"ok"`
	Code    string          `json:"code,omitempty"`
	Message string          `json:"message,omitempty"`
	Data    json.RawMessage `json:"data,omitempty"`
}

// Denied builds a failed Result.
func Denied(code, message string) Result {
	return Result{OK: false, Code: code, Message: message}
}
