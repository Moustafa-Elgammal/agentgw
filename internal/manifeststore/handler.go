package manifeststore

import (
	"encoding/json"
	"errors"
	"mime"
	"net/http"
	"strconv"

	"github.com/Moustafa-Elgammal/agentgw/internal/studiosig"
)

// MaxUpload is the largest manifest an upload may carry. Give the Verifier passed to
// Handler at least this MaxBody.
const MaxUpload = 1 << 20

// Handler serves the studio's manifest endpoints. Requests are signed (agentsig) with the
// ADMIN key, which should differ from the key that signs player calls: whoever holds it
// decides which tools agents may call. The signed X-AG-Player names the acting admin and is
// recorded on the version. Mount it at /v1/studio/manifest and /v1/studio/manifest/.
//
//	PUT  /v1/studio/manifest                       upload and activate (JSON, or YAML with Content-Type application/yaml)
//	GET  /v1/studio/manifest                       the active manifest
//	GET  /v1/studio/manifest/versions              all versions, newest first
//	POST /v1/studio/manifest/versions/{id}/activate  roll back (or forward) to a version
func Handler(m *Manager, v *studiosig.Verifier) http.Handler {
	h := &handler{m: m, v: v}
	mux := http.NewServeMux()
	mux.HandleFunc("PUT /v1/studio/manifest", h.upload)
	mux.HandleFunc("GET /v1/studio/manifest", h.active)
	mux.HandleFunc("GET /v1/studio/manifest/versions", h.list)
	mux.HandleFunc("POST /v1/studio/manifest/versions/{id}/activate", h.activate)
	return mux
}

type handler struct {
	m *Manager
	v *studiosig.Verifier
}

func reply(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func fail(w http.ResponseWriter, status int, code string) {
	reply(w, status, map[string]string{"error": code})
}

// verify checks the admin signature and returns the acting admin and the body.
func (h *handler) verify(w http.ResponseWriter, r *http.Request) (string, []byte, bool) {
	actor, body, err := h.v.Verify(w, r)
	switch {
	case err == nil:
		return actor, body, true
	case errors.Is(err, studiosig.ErrBusy):
		fail(w, http.StatusServiceUnavailable, "temporarily_unavailable")
	case errors.Is(err, studiosig.ErrTooLarge):
		fail(w, http.StatusRequestEntityTooLarge, "too_large")
	default:
		fail(w, http.StatusUnauthorized, "invalid_client")
	}
	return "", nil, false
}

func (h *handler) upload(w http.ResponseWriter, r *http.Request) {
	actor, body, ok := h.verify(w, r)
	if !ok {
		return
	}
	f := JSON
	if mt, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type")); mt == "application/yaml" || mt == "application/x-yaml" || mt == "text/yaml" {
		f = YAML
	}
	v, err := h.m.Upload(r.Context(), actor, body, f)
	switch {
	case err == nil:
		reply(w, http.StatusCreated, v)
	case errors.Is(err, ErrInvalid):
		reply(w, http.StatusUnprocessableEntity, map[string]string{"error": "invalid_manifest", "detail": err.Error()})
	default:
		fail(w, http.StatusServiceUnavailable, "temporarily_unavailable")
	}
}

func (h *handler) active(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := h.verify(w, r); !ok {
		return
	}
	v, doc, err := h.m.store.Active(r.Context(), h.m.tenant)
	switch {
	case err == nil:
		reply(w, http.StatusOK, map[string]any{"version": v, "manifest": json.RawMessage(doc)})
	case errors.Is(err, ErrNotFound):
		fail(w, http.StatusNotFound, "not_found")
	default:
		fail(w, http.StatusServiceUnavailable, "temporarily_unavailable")
	}
}

func (h *handler) list(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := h.verify(w, r); !ok {
		return
	}
	vs, err := h.m.store.List(r.Context(), h.m.tenant)
	if err != nil {
		fail(w, http.StatusServiceUnavailable, "temporarily_unavailable")
		return
	}
	reply(w, http.StatusOK, map[string]any{"versions": vs})
}

func (h *handler) activate(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := h.verify(w, r); !ok {
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		fail(w, http.StatusNotFound, "not_found")
		return
	}
	v, err := h.m.Rollback(r.Context(), id)
	switch {
	case err == nil:
		reply(w, http.StatusOK, v)
	case errors.Is(err, ErrNotFound):
		fail(w, http.StatusNotFound, "not_found")
	case errors.Is(err, ErrInvalid):
		reply(w, http.StatusUnprocessableEntity, map[string]string{"error": "invalid_manifest", "detail": err.Error()})
	default:
		fail(w, http.StatusServiceUnavailable, "temporarily_unavailable")
	}
}
