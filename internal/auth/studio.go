package auth

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/Moustafa-Elgammal/agentgw/pkg/agentsig"
)

// StudioHandler serves the studio-signed endpoints that let a game's own UI show a
// player their linked agents and revoke them. Players have no gateway login: the
// studio backend knows the player and signs each request, with the player in
// X-AG-Player. Mount it next to Handler().
func (s *Server) StudioHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/studio/grants", s.studioList)
	mux.HandleFunc("DELETE /v1/studio/grants/{id}", s.studioRevokeOne)
	mux.HandleFunc("DELETE /v1/studio/grants", s.studioRevokeAll)
	return mux
}

// studioPlayer verifies the studio signature and returns the signed player. On
// failure it writes the response and returns false.
func (s *Server) studioPlayer(w http.ResponseWriter, r *http.Request) (string, bool) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBody))
	if err != nil {
		studioError(w, http.StatusUnauthorized, "invalid_client")
		return "", false
	}
	player, err := agentsig.Verify(r, body, func(id string) []byte {
		if id == s.cfg.StudioKeyID && len(s.cfg.StudioSecret) > 0 {
			return s.cfg.StudioSecret
		}
		return nil
	}, s.cfg.Now(), 0)
	if err != nil || player == "" {
		studioError(w, http.StatusUnauthorized, "invalid_client")
		return "", false
	}
	return player, true
}

func studioError(w http.ResponseWriter, status int, code string) {
	studioJSON(w, status, map[string]string{"error": code})
}

func studioJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

type studioGrant struct {
	ID         string    `json:"id"`
	ClientID   string    `json:"client_id"`
	ClientName string    `json:"client_name"`
	Scope      Scope     `json:"scope"`
	Resource   string    `json:"resource"`
	CreatedAt  time.Time `json:"created_at"`
}

func (s *Server) studioList(w http.ResponseWriter, r *http.Request) {
	player, ok := s.studioPlayer(w, r)
	if !ok {
		return
	}
	grants, err := s.cfg.Store.Grants(r.Context(), s.cfg.Tenant, player)
	if err != nil {
		studioError(w, http.StatusServiceUnavailable, "temporarily_unavailable")
		return
	}
	out := make([]studioGrant, 0, len(grants))
	for _, g := range grants {
		c, err := s.cfg.Store.Client(r.Context(), g.ClientID)
		if err != nil && !errors.Is(err, ErrNotFound) {
			studioError(w, http.StatusServiceUnavailable, "temporarily_unavailable")
			return
		}
		out = append(out, studioGrant{ID: g.ID, ClientID: g.ClientID, ClientName: c.Name,
			Scope: g.Scope, Resource: g.Resource, CreatedAt: g.CreatedAt.UTC()})
	}
	studioJSON(w, http.StatusOK, map[string]any{"grants": out})
}

func (s *Server) studioRevokeOne(w http.ResponseWriter, r *http.Request) {
	player, ok := s.studioPlayer(w, r)
	if !ok {
		return
	}
	err := s.cfg.Store.RevokeGrant(r.Context(), s.cfg.Tenant, player, r.PathValue("id"))
	switch {
	case err == nil:
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusNoContent)
	case errors.Is(err, ErrNotFound):
		studioError(w, http.StatusNotFound, "not_found")
	default:
		studioError(w, http.StatusServiceUnavailable, "temporarily_unavailable")
	}
}

func (s *Server) studioRevokeAll(w http.ResponseWriter, r *http.Request) {
	player, ok := s.studioPlayer(w, r)
	if !ok {
		return
	}
	grants, err := s.cfg.Store.Grants(r.Context(), s.cfg.Tenant, player)
	if err != nil {
		studioError(w, http.StatusServiceUnavailable, "temporarily_unavailable")
		return
	}
	n := 0
	for _, g := range grants {
		err := s.cfg.Store.RevokeGrant(r.Context(), s.cfg.Tenant, player, g.ID)
		if errors.Is(err, ErrNotFound) {
			continue // revoked concurrently, nothing left to do
		}
		if err != nil {
			// Fail closed: report no count. Revoking is idempotent, so the caller can retry.
			studioError(w, http.StatusServiceUnavailable, "temporarily_unavailable")
			return
		}
		n++
	}
	studioJSON(w, http.StatusOK, map[string]int{"revoked": n})
}
