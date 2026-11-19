// Package activity serves the activity feed: what agents did, read back from the audit
// store. Two studio-signed endpoints share one implementation. The player feed is signed
// with the studio key and shows a player only their own events. The admin feed is signed
// with the admin key and can look at any player. Rows carry the argument hash, never the
// arguments.
package activity

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/Moustafa-Elgammal/agentgw/internal/audit"
	"github.com/Moustafa-Elgammal/agentgw/internal/studiosig"
)

// PlayerPath and AdminPath are the routes.
const (
	PlayerPath = "/v1/studio/activity"
	AdminPath  = "/v1/studio/admin/activity"
)

// PlayerHandler serves GET /v1/studio/activity. The signed X-AG-Player is forced as the
// query's player and tenant is the gateway's, so a player only ever sees their own events.
func PlayerHandler(store audit.Store, tenant string, v *studiosig.Verifier) http.Handler {
	return handler(store, tenant, v, PlayerPath, false)
}

// AdminHandler serves GET /v1/studio/admin/activity. Sign with the admin key. The signed
// X-AG-Player names the acting admin and is not a filter; use the player parameter for that.
func AdminHandler(store audit.Store, tenant string, v *studiosig.Verifier) http.Handler {
	return handler(store, tenant, v, AdminPath, true)
}

func handler(store audit.Store, tenant string, v *studiosig.Verifier, path string, admin bool) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET "+path, func(w http.ResponseWriter, r *http.Request) {
		signed, _, err := v.Verify(w, r)
		switch {
		case err == nil:
		case errors.Is(err, studiosig.ErrBusy):
			reply(w, http.StatusServiceUnavailable, map[string]string{"error": "temporarily_unavailable"})
			return
		default:
			reply(w, http.StatusUnauthorized, map[string]string{"error": "invalid_client"})
			return
		}
		q, ok := parse(r)
		if !ok {
			reply(w, http.StatusBadRequest, map[string]string{"error": "invalid_request"})
			return
		}
		q.Tenant = tenant
		if admin {
			q.Player = r.URL.Query().Get("player")
		} else {
			q.Player = signed // never taken from the URL
		}
		limit := q.Clamp()
		q.Limit = limit + 1 // one extra row tells us whether there is another page
		rows, err := store.Query(r.Context(), q)
		if err != nil {
			reply(w, http.StatusServiceUnavailable, map[string]string{"error": "temporarily_unavailable"})
			return
		}
		out := map[string]any{}
		if len(rows) > limit {
			rows = rows[:limit]
			out["next_before"] = rows[len(rows)-1].ID
		}
		events := make([]row, 0, len(rows))
		for _, e := range rows {
			rw := row{ID: e.ID, Time: e.Time.UTC(), Tool: e.Tool, Tier: e.Tier, Outcome: e.Outcome, Code: e.Code, ArgsSHA: e.ArgsSHA}
			if admin {
				rw.Player = e.Player
			}
			events = append(events, rw)
		}
		out["events"] = events
		reply(w, http.StatusOK, out)
	})
	return mux
}

type row struct {
	ID      int64     `json:"id"`
	Time    time.Time `json:"time"`
	Player  string    `json:"player,omitempty"` // admin feed only
	Tool    string    `json:"tool"`
	Tier    string    `json:"tier"`
	Outcome string    `json:"outcome"`
	Code    string    `json:"code,omitempty"`
	ArgsSHA string    `json:"args_sha256"`
}

// parse reads the filters. Unknown parameters are ignored; malformed known ones are refused.
func parse(r *http.Request) (audit.Query, bool) {
	v := r.URL.Query()
	q := audit.Query{Tool: v.Get("tool"), Outcome: v.Get("outcome")}
	switch q.Outcome {
	case "", "ok", "denied", "error":
	default:
		return q, false
	}
	if s := v.Get("since"); s != "" {
		t, err := time.Parse(time.RFC3339, s)
		if err != nil {
			return q, false
		}
		q.Since = t
	}
	if s := v.Get("limit"); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n < 0 {
			return q, false
		}
		q.Limit = n
	}
	if s := v.Get("before"); s != "" {
		n, err := strconv.ParseInt(s, 10, 64)
		if err != nil || n < 0 {
			return q, false
		}
		q.Before = n
	}
	return q, true
}

func reply(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
