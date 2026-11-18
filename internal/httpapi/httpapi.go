// Package httpapi is the M0 front door: a small JSON API around the gateway
// pipeline for local development and tests.
//
// It is NOT the production entry point. M1 adds the MCP transport and M2 adds
// OAuth 2.1; both end in the same gateway.Service.Call. Here the player is taken
// from the X-Dev-Player header, so never expose this listener publicly.
package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/Moustafa-Elgammal/agentgw/internal/confirm"
	"github.com/Moustafa-Elgammal/agentgw/internal/core"
	"github.com/Moustafa-Elgammal/agentgw/internal/gateway"
)

const maxRequest = 64 << 10

// New returns the handler. tenant is fixed for the dev server. confirmations may be
// nil, which turns the confirmation endpoints off (501).
func New(svc *gateway.Service, tenant string, confirmations confirm.Store) http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})

	mux.HandleFunc("GET /v1/tools", func(w http.ResponseWriter, _ *http.Request) {
		type item struct {
			Name        string            `json:"name"`
			Description string            `json:"description"`
			Tier        core.Tier         `json:"tier"`
			Params      map[string]string `json:"params,omitempty"`
		}
		var out []item
		for _, t := range svc.Tools() {
			it := item{Name: t.Name, Description: t.Description, Tier: t.Tier}
			if len(t.Params) > 0 {
				it.Params = map[string]string{}
				for n, p := range t.Params {
					it.Params[n] = p.Type
				}
			}
			out = append(out, it)
		}
		writeJSON(w, http.StatusOK, map[string]any{"tools": out})
	})

	mux.HandleFunc("POST /v1/tools/{name}/call", func(w http.ResponseWriter, r *http.Request) {
		player := r.Header.Get("X-Dev-Player")
		if player == "" {
			writeJSON(w, http.StatusUnauthorized, core.Denied("no_player", "set the X-Dev-Player header"))
			return
		}
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxRequest))
		if err != nil {
			writeJSON(w, http.StatusRequestEntityTooLarge, core.Denied("too_large", "request body too large"))
			return
		}
		var req struct {
			Arguments json.RawMessage `json:"arguments"`
		}
		if len(body) > 0 {
			if err := json.Unmarshal(body, &req); err != nil {
				writeJSON(w, http.StatusBadRequest, core.Denied("bad_request", "body must be {\"arguments\": {...}}"))
				return
			}
		}
		res := svc.Call(r.Context(), core.Principal{TenantID: tenant, PlayerID: player}, r.PathValue("name"), req.Arguments)
		status := http.StatusOK
		if !res.OK {
			status = http.StatusUnprocessableEntity
		}
		writeJSON(w, status, res)
	})

	// The player's side of confirmations. In production this is the authenticated
	// player API (M4). Here the player is whoever X-Dev-Player says.
	needStore := func(w http.ResponseWriter, r *http.Request) (core.Principal, bool) {
		if confirmations == nil {
			writeJSON(w, http.StatusNotImplemented, core.Denied("not_configured", "confirmations are not enabled"))
			return core.Principal{}, false
		}
		player := r.Header.Get("X-Dev-Player")
		if player == "" {
			writeJSON(w, http.StatusUnauthorized, core.Denied("no_player", "set the X-Dev-Player header"))
			return core.Principal{}, false
		}
		return core.Principal{TenantID: tenant, PlayerID: player}, true
	}

	mux.HandleFunc("GET /v1/confirmations", func(w http.ResponseWriter, r *http.Request) {
		p, ok := needStore(w, r)
		if !ok {
			return
		}
		list, err := confirmations.Pending(r.Context(), p)
		if err != nil {
			writeJSON(w, http.StatusServiceUnavailable, core.Denied("confirmation_unavailable", "try again later"))
			return
		}
		if list == nil {
			list = []confirm.Confirmation{}
		}
		writeJSON(w, http.StatusOK, map[string]any{"confirmations": list})
	})

	decide := func(act func(context.Context, core.Principal, string) error, verb string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			p, ok := needStore(w, r)
			if !ok {
				return
			}
			switch err := act(r.Context(), p, r.PathValue("id")); {
			case err == nil:
				writeJSON(w, http.StatusOK, map[string]any{"ok": true, "result": verb})
			case errors.Is(err, confirm.ErrNotFound):
				writeJSON(w, http.StatusNotFound, core.Denied("not_found", "no such confirmation for this player, or it expired"))
			case errors.Is(err, confirm.ErrState):
				writeJSON(w, http.StatusConflict, core.Denied("wrong_state", "this confirmation can no longer be "+verb))
			default:
				writeJSON(w, http.StatusServiceUnavailable, core.Denied("confirmation_unavailable", "try again later"))
			}
		}
	}
	mux.HandleFunc("POST /v1/confirmations/{id}/approve", decide(func(ctx context.Context, p core.Principal, id string) error {
		return confirmations.Approve(ctx, p, id)
	}, "approved"))
	mux.HandleFunc("POST /v1/confirmations/{id}/deny", decide(func(ctx context.Context, p core.Principal, id string) error {
		return confirmations.Deny(ctx, p, id)
	}, "denied"))
	return mux
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
