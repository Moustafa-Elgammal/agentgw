package auth

import (
	"net/http"
	"strconv"
)

// corsMaxAge is how long a browser may cache a preflight answer, in seconds.
const corsMaxAge = 600

// cors adds the deliberately narrow CORS policy to a public endpoint: any origin may read
// the response, but never with credentials. Access-Control-Allow-Credentials is never set.
//
// This is only safe on endpoints that carry no ambient credentials (no cookies, no
// Authorization needed to be useful): public metadata, dynamic registration and the token
// endpoint, whose public clients are protected by PKCE. Do not wrap /authorize, the studio
// endpoints or the MCP endpoint with it. See docs/DECISIONS.md, "Public endpoint hardening".
func cors(expose string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		if expose != "" {
			w.Header().Set("Access-Control-Expose-Headers", expose)
		}
		next.ServeHTTP(w, r)
	})
}

// preflight answers an OPTIONS preflight for one endpoint with exactly the method and
// request headers that endpoint needs.
func preflight(method, headers string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		h := w.Header()
		h.Set("Access-Control-Allow-Origin", "*")
		h.Set("Access-Control-Allow-Methods", method)
		if headers != "" {
			h.Set("Access-Control-Allow-Headers", headers)
		}
		h.Set("Access-Control-Max-Age", strconv.Itoa(corsMaxAge))
		w.WriteHeader(http.StatusNoContent)
	})
}
