// Package mcpserver is the MCP front door. It turns the gateway's tools into
// MCP tools and sends every call through gateway.Service.Call, so the MCP
// transport gets the same rate limits, caps, signing and audit as any other
// entry point.
//
// The server runs the streamable HTTP transport in stateless mode: each request
// is authenticated on its own and gets a server built for that principal.
// Identity is therefore never carried by a session.
package mcpserver

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/Moustafa-Elgammal/agentgw/internal/core"
	"github.com/Moustafa-Elgammal/agentgw/internal/gateway"
)

// Authenticator resolves the principal of an incoming request. A non-nil error
// means the request is rejected with 401 before any MCP handling.
type Authenticator func(*http.Request) (core.Principal, error)

// DevAuth trusts the X-Dev-Player header. Development only: pair it with a
// loopback listener. Real token validation arrives with M3.
func DevAuth(tenant string) Authenticator {
	return func(r *http.Request) (core.Principal, error) {
		player := r.Header.Get("X-Dev-Player")
		if player == "" {
			return core.Principal{}, errNoPlayer
		}
		return core.Principal{TenantID: tenant, PlayerID: player}, nil
	}
}

type authError string

func (e authError) Error() string { return string(e) }

const errNoPlayer = authError("set the X-Dev-Player header")

type ctxKey struct{}

// Handler returns the MCP endpoint handler.
func Handler(svc *gateway.Service, auth Authenticator) http.Handler {
	mcpHandler := mcp.NewStreamableHTTPHandler(func(r *http.Request) *mcp.Server {
		p, _ := r.Context().Value(ctxKey{}).(core.Principal)
		return newServer(svc, p)
	}, &mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true})

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, err := auth(r)
		if err != nil {
			http.Error(w, err.Error(), http.StatusUnauthorized)
			return
		}
		mcpHandler.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxKey{}, p)))
	})
}

// newServer builds the tool set one principal may see. Sensitive tools are not
// listed: they are disabled in v1, and an agent should not be offered them.
func newServer(svc *gateway.Service, p core.Principal) *mcp.Server {
	s := mcp.NewServer(&mcp.Implementation{Name: "agentgw", Version: "0.1.0"}, nil)
	for _, t := range svc.Tools() {
		if t.Tier == core.TierSensitive {
			continue
		}
		name := t.Name
		s.AddTool(&mcp.Tool{
			Name:        name,
			Description: t.Description,
			InputSchema: InputSchema(t),
			Annotations: &mcp.ToolAnnotations{ReadOnlyHint: !t.Tier.IsAction()},
		}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return toResult(svc.Call(ctx, p, name, req.Params.Arguments)), nil
		})
	}
	return s
}

// toResult maps a gateway result onto MCP. Expected failures become tool
// errors (isError) with a machine-readable code, so the agent can read them and
// correct itself. Backend data is passed through as text and stays untrusted.
func toResult(r core.Result) *mcp.CallToolResult {
	body := map[string]any{"ok": r.OK}
	if r.Code != "" {
		body["code"] = r.Code
	}
	if r.Message != "" {
		body["message"] = r.Message
	}
	if len(r.Data) > 0 {
		body["data"] = json.RawMessage(r.Data)
	}
	text, _ := json.Marshal(body)
	return &mcp.CallToolResult{
		IsError: !r.OK,
		Content: []mcp.Content{&mcp.TextContent{Text: string(text)}},
	}
}
