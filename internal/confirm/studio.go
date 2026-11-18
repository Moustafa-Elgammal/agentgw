package confirm

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/Moustafa-Elgammal/agentgw/internal/core"
	"github.com/Moustafa-Elgammal/agentgw/internal/studiosig"
)

// StudioConfig configures StudioHandler.
type StudioConfig struct {
	Store  Store
	Tenant string
	KeyID  string
	Secret []byte
	Now    func() time.Time // nil means time.Now
	// Verifier verifies studio-signed requests and refuses replays. Pass one shared
	// with the authorization server so a request cannot be replayed across
	// endpoints. Nil builds one from KeyID and Secret.
	Verifier *studiosig.Verifier
}

type studio struct {
	cfg      StudioConfig
	verifier *studiosig.Verifier
}

// StudioHandler serves the studio-signed endpoints that let a game's own UI show a
// player their pending confirmations and approve or deny them. Players have no
// gateway login: the studio backend knows the player and signs each request, with
// the player in X-AG-Player. Every route verifies the studio signature, so the
// agent's bearer token can never reach an approval. Mount it at
// /v1/studio/confirmations and /v1/studio/confirmations/.
func StudioHandler(cfg StudioConfig) http.Handler {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	v := cfg.Verifier
	if v == nil {
		v = studiosig.New(studiosig.Config{KeyID: cfg.KeyID, Secret: cfg.Secret, Now: cfg.Now})
	}
	s := &studio{cfg: cfg, verifier: v}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/studio/confirmations", s.list)
	mux.HandleFunc("POST /v1/studio/confirmations/{id}/approve", s.decide(Store.Approve))
	mux.HandleFunc("POST /v1/studio/confirmations/{id}/deny", s.decide(Store.Deny))
	return mux
}

// player verifies the studio signature and returns the signed player. On failure it
// writes the response and returns false.
func (s *studio) player(w http.ResponseWriter, r *http.Request) (core.Principal, bool) {
	player, _, err := s.verifier.Verify(w, r)
	switch {
	case err == nil:
		return core.Principal{TenantID: s.cfg.Tenant, PlayerID: player}, true
	case errors.Is(err, studiosig.ErrBusy):
		studioError(w, http.StatusServiceUnavailable, "temporarily_unavailable")
	default:
		studioError(w, http.StatusUnauthorized, "invalid_client")
	}
	return core.Principal{}, false
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

type studioConfirmation struct {
	ID        string    `json:"id"`
	Tool      string    `json:"tool"`
	State     State     `json:"state"`
	CreatedAt time.Time `json:"created_at"`
	ExpiresAt time.Time `json:"expires_at"`
	ArgsSHA   string    `json:"args_sha256"`
}

func (s *studio) list(w http.ResponseWriter, r *http.Request) {
	p, ok := s.player(w, r)
	if !ok {
		return
	}
	list, err := s.cfg.Store.Pending(r.Context(), p)
	if err != nil {
		studioError(w, http.StatusServiceUnavailable, "temporarily_unavailable")
		return
	}
	out := make([]studioConfirmation, 0, len(list))
	for _, c := range list {
		out = append(out, studioConfirmation{ID: c.ID, Tool: c.Tool, State: c.State,
			CreatedAt: c.CreatedAt.UTC(), ExpiresAt: c.ExpiresAt.UTC(), ArgsSHA: c.ArgsSHA})
	}
	studioJSON(w, http.StatusOK, map[string]any{"confirmations": out})
}

func (s *studio) decide(move func(Store, context.Context, core.Principal, string) error) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p, ok := s.player(w, r)
		if !ok {
			return
		}
		err := move(s.cfg.Store, r.Context(), p, r.PathValue("id"))
		switch {
		case err == nil:
			w.Header().Set("Cache-Control", "no-store")
			w.WriteHeader(http.StatusNoContent)
		case errors.Is(err, ErrNotFound):
			studioError(w, http.StatusNotFound, "not_found")
		case errors.Is(err, ErrState):
			studioError(w, http.StatusConflict, "wrong_state")
		default:
			studioError(w, http.StatusServiceUnavailable, "temporarily_unavailable")
		}
	}
}
