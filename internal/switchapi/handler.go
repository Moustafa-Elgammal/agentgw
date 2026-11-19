// Package switchapi is the studio's signed kill switch API. A studio turns a tool, a player
// or the whole game off during an incident, and back on after, without a restart. Requests
// are signed (agentsig) with the ADMIN key, the same one that guards manifest uploads.
package switchapi

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"regexp"
	"time"
	"unicode/utf8"

	"github.com/Moustafa-Elgammal/agentgw/internal/audit"
	"github.com/Moustafa-Elgammal/agentgw/internal/policy"
	"github.com/Moustafa-Elgammal/agentgw/internal/studiosig"
)

// MaxBody bounds a request body. Give the Verifier passed to Handler at least this MaxBody.
const MaxBody = 4 << 10

// Audit naming for the admin activity feed. Free text (the reason) is never stored in the
// audit event, only a hash of the target.
const (
	AuditTierAdmin = "admin"
	AuditToolOn    = "admin.switch.on"
	AuditToolOff   = "admin.switch.off"
)

const maxReason = 200

var (
	toolRe   = regexp.MustCompile(`^[a-z][a-z0-9_]{1,63}$`)
	playerRe = regexp.MustCompile(`^[\x21-\x7e]{1,128}$`)
)

// Handler serves the switch endpoints for one tenant. The tenant is fixed by the gateway:
// a request never names another tenant, and an empty tool and player switches off the whole
// game. The signed X-AG-Player names the acting admin and goes into the audit event.
//
//	GET    /v1/studio/switches   the switches that are on
//	PUT    /v1/studio/switches   turn one on   {"tool":"","player":"","reason":""}
//	DELETE /v1/studio/switches   turn one off  (same body)
//
// Turning a switch on or off twice is not an error. A switch blocks every tier it matches,
// reads included.
func Handler(sb policy.Switchboard, v *studiosig.Verifier, tenant string, a audit.Auditor) http.Handler {
	h := &handler{sb: sb, v: v, tenant: tenant, audit: a, now: time.Now}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/studio/switches", h.list)
	mux.HandleFunc("PUT /v1/studio/switches", func(w http.ResponseWriter, r *http.Request) { h.set(w, r, true) })
	mux.HandleFunc("DELETE /v1/studio/switches", func(w http.ResponseWriter, r *http.Request) { h.set(w, r, false) })
	return mux
}

type handler struct {
	sb     policy.Switchboard
	v      *studiosig.Verifier
	tenant string
	audit  audit.Auditor
	now    func() time.Time
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

type request struct {
	Tool   string `json:"tool"`
	Player string `json:"player"`
	Reason string `json:"reason"`
}

func invalid(w http.ResponseWriter, detail string) {
	reply(w, http.StatusBadRequest, map[string]string{"error": "invalid_request", "detail": detail})
}

// parse reads and checks the body. An empty body means "the whole game".
func parse(body []byte) (request, string) {
	var req request
	if len(body) > 0 {
		dec := json.NewDecoder(bytes.NewReader(body))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&req); err != nil {
			return req, `body must be {"tool":"","player":"","reason":""}`
		}
		if dec.More() {
			return req, "unexpected data after the body"
		}
	}
	if req.Tool != "" && !toolRe.MatchString(req.Tool) {
		return req, "tool must be a tool name (lowercase letters, digits, underscore)"
	}
	if req.Player != "" && !playerRe.MatchString(req.Player) {
		return req, "player must be 1 to 128 printable ASCII characters without spaces"
	}
	if utf8.RuneCountInString(req.Reason) > maxReason || !utf8.ValidString(req.Reason) {
		return req, "reason must be at most 200 characters"
	}
	for _, c := range req.Reason {
		if c < 0x20 || c == 0x7f {
			return req, "reason must not contain control characters"
		}
	}
	return req, ""
}

func (h *handler) list(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := h.verify(w, r); !ok {
		return
	}
	list, err := h.sb.Switches(r.Context())
	if err != nil {
		fail(w, http.StatusServiceUnavailable, "temporarily_unavailable")
		return
	}
	if list == nil {
		list = []policy.Switch{}
	}
	reply(w, http.StatusOK, map[string]any{"switches": list})
}

func (h *handler) set(w http.ResponseWriter, r *http.Request, on bool) {
	actor, body, ok := h.verify(w, r)
	if !ok {
		return
	}
	req, problem := parse(body)
	if problem != "" {
		invalid(w, problem)
		return
	}
	sw := policy.Switch{Tenant: h.tenant, Tool: req.Tool, Player: req.Player}
	if err := h.sb.SetSwitch(r.Context(), sw, on); err != nil {
		slog.Error("kill switch store failed", "actor", actor, "on", on, "err", err)
		fail(w, http.StatusServiceUnavailable, "temporarily_unavailable")
		return
	}
	h.record(r.Context(), actor, sw, on, req.Reason)
	reply(w, http.StatusOK, map[string]any{"switch": sw, "on": on})
}

func (h *handler) record(ctx context.Context, actor string, sw policy.Switch, on bool, reason string) {
	tool := AuditToolOff
	if on {
		tool = AuditToolOn
	}
	slog.Warn("kill switch changed", "actor", actor, "on", on, "tenant", sw.Tenant, "tool", sw.Tool, "player", sw.Player, "reason", reason)
	if h.audit == nil {
		return
	}
	target, _ := json.Marshal(sw)
	sum := sha256.Sum256(target)
	h.audit.Record(ctx, audit.Event{
		Time: h.now().UTC(), Tenant: h.tenant, Player: actor, Tool: tool, Tier: AuditTierAdmin,
		Outcome: "ok", ArgsSHA: hex.EncodeToString(sum[:]),
	})
}
