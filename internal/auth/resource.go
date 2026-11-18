package auth

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/Moustafa-Elgammal/agentgw/internal/core"
)

// ResourceServer validates bearer tokens for the gateway's MCP endpoint.
type ResourceServer struct {
	Store       Store
	Resource    string // this gateway's MCP URL. Tokens must be bound to exactly this audience.
	MetadataURL string // where the protected resource metadata is served
	Tenant      string // the tenant this process serves
}

// Challenge is the error Authenticate returns. It tells the HTTP layer what to put in
// WWW-Authenticate so an MCP client can find the authorization server.
type Challenge struct {
	header string
	msg    string
}

func (c *Challenge) Error() string           { return c.msg }
func (c *Challenge) WWWAuthenticate() string { return c.header }

func (rs *ResourceServer) challenge(code, desc string) *Challenge {
	h := fmt.Sprintf(`Bearer resource_metadata=%q`, rs.MetadataURL)
	msg := "bearer token required"
	if code != "" {
		h += fmt.Sprintf(`, error=%q, error_description=%q`, code, desc)
		msg = desc
	}
	return &Challenge{header: h, msg: msg}
}

// Authenticate resolves the principal behind a request's bearer token. It matches
// mcpserver.Authenticator. Every failure is a *Challenge and carries no detail about
// why a token was rejected beyond what RFC 6750 allows.
func (rs *ResourceServer) Authenticate(r *http.Request) (core.Principal, error) {
	h := r.Header.Get("Authorization")
	if h == "" {
		return core.Principal{}, rs.challenge("", "")
	}
	scheme, tok, ok := strings.Cut(h, " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") || strings.TrimSpace(tok) == "" {
		return core.Principal{}, rs.challenge("invalid_request", "send the token as Authorization: Bearer <token>")
	}
	g, err := rs.Store.Access(r.Context(), HashToken(strings.TrimSpace(tok)))
	if err != nil {
		// Unknown, expired and revoked look the same on purpose.
		return core.Principal{}, rs.challenge("invalid_token", "the token is invalid, expired or revoked")
	}
	if g.Resource != rs.Resource || g.Tenant != rs.Tenant || !g.Scope.Valid() {
		// A token issued for another resource must never work here (confused deputy).
		return core.Principal{}, rs.challenge("invalid_token", "the token was not issued for this resource")
	}
	return core.Principal{TenantID: g.Tenant, PlayerID: g.Player, ClientID: g.ClientID, Scope: g.Scope}, nil
}

// MetadataHandler serves the OAuth protected resource metadata (RFC 9728), which
// tells a client which authorization server to use for this resource.
func (rs *ResourceServer) MetadataHandler(issuer string) http.Handler {
	doc, _ := json.Marshal(map[string]any{
		"resource":                 rs.Resource,
		"authorization_servers":    []string{issuer},
		"bearer_methods_supported": []string{"header"},
		"scopes_supported":         []Scope{ScopeCoach, ScopeOperator},
		"resource_name":            "agentgw game gateway",
	})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Public metadata, readable by browser based clients. No credentials are involved.
		w.Header().Set("Access-Control-Allow-Origin", "*")
		if r.Method == http.MethodOptions {
			preflight("GET", "").ServeHTTP(w, r)
			return
		}
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write(doc)
	})
}
