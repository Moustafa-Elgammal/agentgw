package relay

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/coder/websocket"

	"github.com/Moustafa-Elgammal/agentgw/internal/studiosig"
)

// HandlerOption configures Handler.
type HandlerOption func(*websocket.AcceptOptions)

// WithOriginPatterns allows browser clients (for example Unity WebGL) from these origins.
// Without it, requests that carry an Origin header must be same origin. Native game
// clients send no Origin and are unaffected.
func WithOriginPatterns(p ...string) HandlerOption {
	return func(o *websocket.AcceptOptions) { o.OriginPatterns = p }
}

func reply(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// Handler serves GET /v1/relay/connect. The ticket comes from "Authorization: Bearer"
// (preferred) or the ?ticket= query parameter. It is checked before the upgrade.
func Handler(h *Hub, t *Tickets, opts ...HandlerOption) http.Handler {
	ao := &websocket.AcceptOptions{}
	for _, o := range opts {
		o(ao)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/relay/connect", func(w http.ResponseWriter, r *http.Request) {
		ticket := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
			ticket = r.URL.Query().Get("ticket")
		}
		tenant, player, err := t.Verify(ticket)
		if err != nil {
			reply(w, http.StatusUnauthorized, map[string]string{"error": "invalid_ticket"})
			return
		}
		ws, err := websocket.Accept(w, r, ao)
		if err != nil {
			return // Accept already wrote the response
		}
		h.Serve(r.Context(), tenant, player, ws)
	})
	return mux
}

// MintHandler serves POST /v1/studio/relay/tickets. The studio's backend signs the request
// (agentsig, studio key); the signed X-AG-Player is the player the ticket is for.
func MintHandler(t *Tickets, v *studiosig.Verifier, tenant string) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/studio/relay/tickets", func(w http.ResponseWriter, r *http.Request) {
		player, _, err := v.Verify(w, r)
		switch {
		case err == nil:
		case errors.Is(err, studiosig.ErrBusy):
			reply(w, http.StatusServiceUnavailable, map[string]string{"error": "temporarily_unavailable"})
			return
		case errors.Is(err, studiosig.ErrTooLarge):
			reply(w, http.StatusRequestEntityTooLarge, map[string]string{"error": "too_large"})
			return
		default:
			reply(w, http.StatusUnauthorized, map[string]string{"error": "invalid_client"})
			return
		}
		ticket, exp := t.Mint(tenant, player)
		reply(w, http.StatusCreated, map[string]any{"ticket": ticket, "expires_at": exp.UTC()})
	})
	return mux
}
