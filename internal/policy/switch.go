package policy

import (
	"context"
	"sort"

	"github.com/Moustafa-Elgammal/agentgw/internal/core"
)

// Switch is a kill switch. Empty fields are wildcards, so a Switch matches a
// call when every non-empty field equals the call's value:
//
//	Switch{}                          everything
//	Switch{Tenant: "g"}               one game
//	Switch{Tenant: "g", Tool: "buy"}  one tool in one game
//	Switch{Tenant: "g", Player: "p"}  one player in one game
//
// A switch blocks every tier it matches, reads included. To keep reads working,
// switch off the action tools by name.
type Switch struct {
	Tenant string `json:"tenant,omitempty"`
	Tool   string `json:"tool,omitempty"`
	Player string `json:"player,omitempty"`
}

func (s Switch) matches(p core.Principal, t core.Tool) bool {
	return (s.Tenant == "" || s.Tenant == p.TenantID) &&
		(s.Tool == "" || s.Tool == t.Name) &&
		(s.Player == "" || s.Player == p.PlayerID)
}

// Switchboard stores kill switches. Admit consults it before anything else, and
// a store error makes Admit fail closed.
type Switchboard interface {
	// SetSwitch turns a switch on or off. Setting an already-set state is not an error.
	SetSwitch(ctx context.Context, s Switch, on bool) error
	// Switches lists the switches that are on, in a stable order.
	Switches(ctx context.Context) ([]Switch, error)
}

// SetSwitch implements Switchboard.
func (m *Memory) SetSwitch(_ context.Context, s Switch, on bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if on {
		m.switches[s] = true
	} else {
		delete(m.switches, s)
	}
	return nil
}

// Switches implements Switchboard.
func (m *Memory) Switches(_ context.Context) ([]Switch, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Switch, 0, len(m.switches))
	for s := range m.switches {
		out = append(out, s)
	}
	SortSwitches(out)
	return out, nil
}

// SortSwitches orders switches by tenant, tool, player. Every store lists in this order.
func SortSwitches(out []Switch) {
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Tenant != b.Tenant {
			return a.Tenant < b.Tenant
		}
		if a.Tool != b.Tool {
			return a.Tool < b.Tool
		}
		return a.Player < b.Player
	})
}

// killed reports whether any switch matches. Caller holds m.mu.
func (m *Memory) killed(p core.Principal, t core.Tool) bool {
	for s := range m.switches {
		if s.matches(p, t) {
			return true
		}
	}
	return false
}
