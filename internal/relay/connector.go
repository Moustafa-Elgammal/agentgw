package relay

import (
	"context"

	"github.com/Moustafa-Elgammal/agentgw/internal/connector"
	"github.com/Moustafa-Elgammal/agentgw/internal/core"
)

type relayConnector struct{ h *Hub }

// NewConnector returns a connector that runs tools on the player's connected game client.
// A player with no socket gets a denied result (client_offline); a lost socket or a timeout
// is a Go error, so the gateway fails closed.
func NewConnector(h *Hub) connector.Connector { return relayConnector{h} }

// Call implements connector.Connector.
func (c relayConnector) Call(ctx context.Context, p core.Principal, t core.Tool, args map[string]any, idem string) (core.Result, error) {
	return c.h.Call(ctx, p.TenantID, p.PlayerID, t.Name, args, idem)
}
