package auth

import (
	"crypto/rand"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/Moustafa-Elgammal/agentgw/pkg/agentsig"
)

// ServerConfig configures the embedded authorization server.
type ServerConfig struct {
	Store    Store
	Issuer   string // public base URL, no trailing slash, e.g. https://gw.example
	Resource string // the MCP URL tokens are bound to, e.g. https://gw.example/mcp
	Tenant   string // the tenant this process serves

	// The studio's signing key authenticates the request that mints link tickets,
	// the same way the studio's backend verifies the gateway's calls.
	StudioKeyID  string
	StudioSecret []byte

	Now func() time.Time // nil means time.Now
}

// Server is the embedded authorization server.
type Server struct {
	cfg      ServerConfig
	attempts *limiter

	seenMu sync.Mutex
	seen   map[string]time.Time // signed ticket requests already honoured, to refuse replays
}

// NewServer builds the authorization server.
func NewServer(cfg ServerConfig) *Server {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	cfg.Issuer = strings.TrimRight(cfg.Issuer, "/")
	return &Server{cfg: cfg, attempts: newLimiter(10, time.Minute, cfg.Now), seen: map[string]time.Time{}}
}

// Handler serves the authorization server endpoints.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /.well-known/oauth-authorization-server", s.metadata)
	mux.HandleFunc("POST /register", s.register)
	mux.HandleFunc("GET /authorize", s.authorizeForm)
	mux.HandleFunc("POST /authorize", s.authorizeSubmit)
	mux.HandleFunc("POST /token", s.token)
	mux.HandleFunc("POST /v1/studio/link-tickets", s.mintTicket)
	return mux
}

const maxBody = 64 << 10

func (s *Server) metadata(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"issuer":                                         s.cfg.Issuer,
		"authorization_endpoint":                         s.cfg.Issuer + "/authorize",
		"token_endpoint":                                 s.cfg.Issuer + "/token",
		"registration_endpoint":                          s.cfg.Issuer + "/register",
		"response_types_supported":                       []string{"code"},
		"grant_types_supported":                          []string{"authorization_code", "refresh_token"},
		"code_challenge_methods_supported":               []string{"S256"},
		"token_endpoint_auth_methods_supported":          []string{"none"},
		"scopes_supported":                               []Scope{ScopeCoach, ScopeOperator},
		"authorization_response_iss_parameter_supported": true,
	})
}

// register is dynamic client registration (RFC 7591) for public clients.
func (s *Server) register(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ClientName   string   `json:"client_name"`
		RedirectURIs []string `json:"redirect_uris"`
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBody))
	if err := dec.Decode(&req); err != nil {
		oauthError(w, http.StatusBadRequest, "invalid_client_metadata", "body must be JSON with client_name and redirect_uris")
		return
	}
	name := strings.TrimSpace(req.ClientName)
	if name == "" || len(name) > 100 {
		oauthError(w, http.StatusBadRequest, "invalid_client_metadata", "client_name is required, at most 100 characters")
		return
	}
	if len(req.RedirectURIs) == 0 || len(req.RedirectURIs) > 10 {
		oauthError(w, http.StatusBadRequest, "invalid_redirect_uri", "give between 1 and 10 redirect_uris")
		return
	}
	for _, u := range req.RedirectURIs {
		if err := validRedirectURI(u); err != nil {
			oauthError(w, http.StatusBadRequest, "invalid_redirect_uri", err.Error())
			return
		}
	}
	c := Client{ID: NewID(), Name: name, RedirectURIs: req.RedirectURIs, CreatedAt: s.cfg.Now()}
	if err := s.cfg.Store.RegisterClient(r.Context(), c); err != nil {
		oauthError(w, http.StatusServiceUnavailable, "temporarily_unavailable", "try again later")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"client_id": c.ID, "client_name": c.Name, "redirect_uris": c.RedirectURIs,
		"token_endpoint_auth_method": "none",
		"grant_types":                []string{"authorization_code", "refresh_token"},
		"response_types":             []string{"code"},
	})
}

// validRedirectURI accepts https URLs and http URLs on a literal loopback address
// (RFC 8252). It rejects fragments, user info and everything else.
func validRedirectURI(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || !u.IsAbs() || u.Host == "" {
		return errString("redirect_uris must be absolute URLs")
	}
	if u.Fragment != "" || u.User != nil {
		return errString("redirect_uris must not contain a fragment or user info")
	}
	switch u.Scheme {
	case "https":
		return nil
	case "http":
		if isLoopback(u.Hostname()) {
			return nil
		}
		return errString("http redirect_uris are only allowed on 127.0.0.1 or [::1]")
	}
	return errString("redirect_uris must use https (or http on loopback)")
}

func isLoopback(host string) bool {
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

type errString string

func (e errString) Error() string { return string(e) }

// redirectMatches compares a requested redirect_uri with a registered one. They must be
// identical, except that a registered loopback URI matches any port (RFC 8252 section 7.3).
func redirectMatches(registered, requested string) bool {
	if registered == requested {
		return true
	}
	a, err1 := url.Parse(registered)
	b, err2 := url.Parse(requested)
	if err1 != nil || err2 != nil || a.Scheme != "http" || b.Scheme != "http" {
		return false
	}
	if b.User != nil || b.Fragment != "" {
		return false
	}
	return isLoopback(a.Hostname()) && a.Hostname() == b.Hostname() && a.Path == b.Path && a.RawQuery == b.RawQuery
}

const ticketAlphabet = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789" // no 0, O, 1, I

// newTicketCode returns a code a player can type: XXXX-XXXX, 40 bits.
func newTicketCode() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	out := make([]byte, 0, 9)
	for i, v := range b {
		if i == 4 {
			out = append(out, '-')
		}
		out = append(out, ticketAlphabet[int(v)%len(ticketAlphabet)])
	}
	return string(out)
}

func normalizeTicket(s string) string {
	s = strings.ToUpper(strings.NewReplacer("-", "", " ", "").Replace(strings.TrimSpace(s)))
	if len(s) != 8 {
		return ""
	}
	for _, c := range s {
		if !strings.ContainsRune(ticketAlphabet, c) {
			return ""
		}
	}
	return s[:4] + "-" + s[4:]
}

// mintTicket lets the studio's backend create a link ticket for a logged-in player.
// The request is signed like every studio call, with the player in X-AG-Player.
func (s *Server) mintTicket(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBody))
	if err != nil {
		oauthError(w, http.StatusRequestEntityTooLarge, "invalid_request", "body too large")
		return
	}
	player, err := agentsig.Verify(r, body, func(id string) []byte {
		if id == s.cfg.StudioKeyID {
			return s.cfg.StudioSecret
		}
		return nil
	}, s.cfg.Now(), 0)
	if err != nil || player == "" {
		oauthError(w, http.StatusUnauthorized, "invalid_client", "a valid studio signature is required")
		return
	}
	if s.replayed(r.Header.Get(agentsig.HeaderKeyID) + "|" + r.Header.Get(agentsig.HeaderIdempotency)) {
		oauthError(w, http.StatusUnauthorized, "invalid_client", "this signed request was already used")
		return
	}
	code := newTicketCode()
	exp := s.cfg.Now().Add(TicketTTL)
	if err := s.cfg.Store.PutTicket(r.Context(), HashToken(normalizeTicket(code)), s.cfg.Tenant, player, exp); err != nil {
		oauthError(w, http.StatusServiceUnavailable, "temporarily_unavailable", "try again later")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"ticket": code, "expires_at": exp.UTC()})
}

// replayed records a signed request's key id and idempotency key and reports whether it was
// seen before. Entries live as long as a timestamp is accepted (agentsig.DefaultMaxSkew).
func (s *Server) replayed(id string) bool {
	s.seenMu.Lock()
	defer s.seenMu.Unlock()
	now := s.cfg.Now()
	for k, t := range s.seen {
		if now.Sub(t) > agentsig.DefaultMaxSkew {
			delete(s.seen, k)
		}
	}
	if _, dup := s.seen[id]; dup {
		return true
	}
	s.seen[id] = now
	return false
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// oauthError writes an RFC 6749 error response.
func oauthError(w http.ResponseWriter, status int, code, desc string) {
	writeJSON(w, status, map[string]string{"error": code, "error_description": desc})
}

// limiter is a small sliding window counter keyed by client address. It slows
// down guessing of link tickets. Process-local: M6 moves it to Redis.
type limiter struct {
	mu     sync.Mutex
	max    int
	window time.Duration
	now    func() time.Time
	hits   map[string][]time.Time
}

func newLimiter(max int, window time.Duration, now func() time.Time) *limiter {
	return &limiter{max: max, window: window, now: now, hits: map[string][]time.Time{}}
}

// allow records an attempt and reports whether it is within the budget.
func (l *limiter) allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	kept := l.hits[key][:0]
	for _, t := range l.hits[key] {
		if now.Sub(t) < l.window {
			kept = append(kept, t)
		}
	}
	if len(kept) >= l.max {
		l.hits[key] = kept
		return false
	}
	l.hits[key] = append(kept, now)
	if len(l.hits) > 10000 { // bound memory: drop idle keys
		for k, v := range l.hits {
			if len(v) == 0 || now.Sub(v[len(v)-1]) >= l.window {
				delete(l.hits, k)
			}
		}
	}
	return true
}

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
