// Package bodylimit caps request bodies at the front door, before any handler or SDK reads
// them, and answers an oversized request with a clean 413 in the project's JSON error shape.
package bodylimit

import (
	"bytes"
	"io"
	"net/http"
	"strings"
)

// Default is the largest body most endpoints accept.
const Default = 1 << 20

// Rule gives requests whose path starts with Prefix their own cap. Max 0 exempts the prefix:
// its handler enforces its own limit (manifest upload does).
type Rule struct {
	Prefix string
	Max    int64
}

// Middleware refuses a request whose body is larger than its cap with
// 413 {"error":"too_large"}. The longest matching Rule decides the cap, otherwise def (Default
// when def <= 0). A declared Content-Length over the cap is refused without reading anything.
// Otherwise the body is read once, up to the cap, and handed to the next handler from memory,
// so a handler never sees a half-read oversized body and a client that lies about its length
// is still stopped. Bodies are small JSON or form documents, so buffering is cheap.
func Middleware(next http.Handler, def int64, rules ...Rule) http.Handler {
	if def <= 0 {
		def = Default
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		max := def
		best := -1
		for _, rule := range rules {
			if strings.HasPrefix(r.URL.Path, rule.Prefix) && len(rule.Prefix) > best {
				best, max = len(rule.Prefix), rule.Max
			}
		}
		if max == 0 || r.Body == nil || r.Body == http.NoBody {
			next.ServeHTTP(w, r)
			return
		}
		if r.ContentLength > max {
			tooLarge(w)
			return
		}
		data, err := io.ReadAll(io.LimitReader(r.Body, max+1))
		_ = r.Body.Close()
		if err != nil {
			http.Error(w, "could not read the request body", http.StatusBadRequest)
			return
		}
		if int64(len(data)) > max {
			tooLarge(w)
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(data))
		r.ContentLength = int64(len(data))
		next.ServeHTTP(w, r)
	})
}

func tooLarge(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Connection", "close")
	w.WriteHeader(http.StatusRequestEntityTooLarge)
	_, _ = w.Write([]byte(`{"error":"too_large"}` + "\n"))
}

// Small is the cap for OAuth and studio endpoints. They carry tiny JSON or form documents and
// their handlers already enforce it, so the front door turns a breach into the same clean 413.
const Small = 64 << 10

// FrontDoor applies the gateway's body limits to its whole mux: max (Default when <= 0) for
// everything, Small or lower for the OAuth endpoints and the studio API, and no cap here for the
// manifest upload, whose handler enforces its own larger one.
func FrontDoor(next http.Handler, max int64) http.Handler {
	if max <= 0 {
		max = Default
	}
	small := min(max, Small)
	return Middleware(next, max,
		Rule{Prefix: "/v1/studio/", Max: small},
		Rule{Prefix: "/v1/studio/manifest", Max: 0},
		Rule{Prefix: "/register", Max: small},
		Rule{Prefix: "/token", Max: small},
		Rule{Prefix: "/authorize", Max: small},
	)
}
