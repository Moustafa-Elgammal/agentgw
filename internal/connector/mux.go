package connector

import (
	"context"
	"fmt"

	"github.com/Moustafa-Elgammal/agentgw/internal/core"
)

// Mux sends a tool to the connector for its backend. Tools with no backend use HTTP.
// A tool whose backend has no connector configured is a transport error, so the call fails
// closed and its reservation is released.
type Mux struct {
	HTTP  Connector
	Relay Connector
}

// Call implements Connector.
func (m Mux) Call(ctx context.Context, p core.Principal, t core.Tool, args map[string]any, idem string) (core.Result, error) {
	var c Connector
	switch t.Backend {
	case "", core.BackendHTTP:
		c = m.HTTP
	case core.BackendRelay:
		c = m.Relay
	}
	if c == nil {
		return core.Result{}, fmt.Errorf("connector: no connector for backend %q (tool %s)", t.Backend, t.Name)
	}
	return c.Call(ctx, p, t, args, idem)
}
