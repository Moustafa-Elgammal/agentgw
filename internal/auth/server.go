package auth

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/Moustafa-Elgammal/agentgw/internal/studiosig"
)

// ServerConfig configures the embedded authorization server.
type ServerConfig struct {
	Store    Store
	Issuer   string // public base URL, no trailing slash, e.g. https://gw.example
	Resource string // the MCP URL tokens are bound to, e.g. https://gw.example/mcp
	Tenant   string // the tenant this process serves
	// MaxScope caps what a player may grant. Coach hides the operator choice on the consent
	// page, rejects scope=operator, and clamps tokens and refreshes to coach. Empty means no cap.
	MaxScope Scope

	// The studio's signing key authenticates the request that mints link tickets,
	// the same way the studio's backend verifies the gateway's calls.
	StudioKeyID  string
	StudioSecret []byte
	// Verifier verifies studio-signed requests and refuses replays. Pass one shared
	// with the confirmations handler so a request cannot be replayed across
	// endpoints. Nil builds one from StudioKeyID and StudioSecret.
	Verifier *studiosig.Verifier

	// RegisterPerHour limits POST /register per client address. 0 means 10, negative disables.
	RegisterPerHour int
	// MaxClients caps how many clients this process will register. 0 means 10000, negative
	// disables. The count is process local (see Server.registered), so it resets on restart.
	MaxClients int

	// ClientIPHeader names a header, such as X-Forwarded-For or X-Real-IP, whose first value is
	// used as the client address by the ticket limiter and the registration limiter. Empty means
	// use the connection's RemoteAddr.
	//
	// SECURITY: set this ONLY when the gateway sits behind a reverse proxy that overwrites the
	// header on every request. If clients can reach the gateway directly, or the proxy appends to
	// a client supplied value, any client can choose its own address and evade every limit.
	// A missing or unparsable value falls back to RemoteAddr.
	ClientIPHeader string

	Now func() time.Time // nil means time.Now
}

// Server is the embedded authorization server.
type Server struct {
	cfg      ServerConfig
	attempts *limiter // ticket guessing, per client address
	regLimit *limiter // POST /register, per client address; nil when disabled

	// registered counts clients registered by this process. auth.Store has no count method and
	// must not grow one for this, so the cap is process local: it resets on restart and each
	// replica counts alone. It bounds growth per process, it is not a global quota.
	regMu      sync.Mutex
	registered int
}

// NewServer builds the authorization server.
func NewServer(cfg ServerConfig) *Server {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	cfg.Issuer = strings.TrimRight(cfg.Issuer, "/")
	if cfg.RegisterPerHour == 0 {
		cfg.RegisterPerHour = 10
	}
	if cfg.MaxClients == 0 {
		cfg.MaxClients = 10000
	}
	if cfg.Verifier == nil {
		cfg.Verifier = studiosig.New(studiosig.Config{KeyID: cfg.StudioKeyID, Secret: cfg.StudioSecret, Now: cfg.Now})
	}
	s := &Server{cfg: cfg, attempts: newLimiter(10, time.Minute, cfg.Now)}
	if cfg.RegisterPerHour > 0 {
		s.regLimit = newLimiter(cfg.RegisterPerHour, time.Hour, cfg.Now)
	}
	return s
}

// Handler serves the authorization server endpoints.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.Handle("GET /.well-known/oauth-authorization-server", cors("", http.HandlerFunc(s.metadata)))
	mux.Handle("OPTIONS /.well-known/oauth-authorization-server", preflight("GET", ""))
	// The protected resource metadata itself is served by ResourceServer.MetadataHandler, which
	// the caller mounts for GET. Preflights for it land here because this handler is mounted at "/".
	mux.Handle("OPTIONS /.well-known/oauth-protected-resource", preflight("GET", ""))
	mux.Handle("OPTIONS /.well-known/oauth-protected-resource/mcp", preflight("GET", ""))
	mux.Handle("POST /register", cors("Retry-After", http.HandlerFunc(s.register)))
	mux.Handle("OPTIONS /register", preflight("POST", "Content-Type"))
	mux.HandleFunc("GET /authorize", s.authorizeForm)
	mux.HandleFunc("POST /authorize", s.authorizeSubmit)
	mux.Handle("POST /token", cors("", http.HandlerFunc(s.token)))
	mux.Handle("OPTIONS /token", preflight("POST", "Content-Type"))
	mux.HandleFunc("POST /v1/studio/link-tickets", s.mintTicket)
	return mux
}

const maxBody = 64 << 10

// scopesUpTo lists the scopes a player may choose under the cap.
func scopesUpTo(max Scope) []Scope {
	if max == ScopeCoach {
		return []Scope{ScopeCoach}
	}
	return []Scope{ScopeCoach, ScopeOperator}
}

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
		"scopes_supported":                               scopesUpTo(s.cfg.MaxScope),
		"authorization_response_iss_parameter_supported": true,
	})
}

// register is dynamic client registration (RFC 7591) for public clients.
func (s *Server) register(w http.ResponseWriter, r *http.Request) {
	// Every attempt counts, valid or not, like the ticket limiter.
	if s.regLimit != nil {
		if ok, wait := s.regLimit.allowRetry(s.clientAddr(r)); !ok {
			w.Header().Set("Retry-After", strconv.Itoa(int((wait+time.Second-1)/time.Second)))
			oauthError(w, http.StatusTooManyRequests, "temporarily_unavailable", "too many registrations from this address, try again later")
			return
		}
	}
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
	if !printableText(name) {
		oauthError(w, http.StatusBadRequest, "invalid_client_metadata", "client_name must be printable text without control characters")
		return
	}
	if len(req.RedirectURIs) == 0 || len(req.RedirectURIs) > 10 {
		oauthError(w, http.StatusBadRequest, "invalid_redirect_uri", "give between 1 and 10 redirect_uris")
		return
	}
	seen := map[string]bool{}
	for _, u := range req.RedirectURIs {
		if len(u) > maxRedirectURI {
			oauthError(w, http.StatusBadRequest, "invalid_redirect_uri", "a redirect_uri is longer than 2048 bytes")
			return
		}
		if err := validRedirectURI(u); err != nil {
			oauthError(w, http.StatusBadRequest, "invalid_redirect_uri", err.Error())
			return
		}
		if seen[u] {
			oauthError(w, http.StatusBadRequest, "invalid_redirect_uri", "redirect_uris must not contain duplicates")
			return
		}
		seen[u] = true
	}
	if !s.reserveClient() {
		oauthError(w, http.StatusServiceUnavailable, "temporarily_unavailable", "this server is not accepting new client registrations")
		return
	}
	c := Client{ID: NewID(), Name: name, RedirectURIs: req.RedirectURIs, CreatedAt: s.cfg.Now()}
	if err := s.cfg.Store.RegisterClient(r.Context(), c); err != nil {
		s.releaseClient()
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

const maxRedirectURI = 2048

// printableText reports whether s is valid UTF-8 made only of printable characters
// (no control, format or bidi override characters).
func printableText(s string) bool {
	if !utf8.ValidString(s) {
		return false
	}
	for _, r := range s {
		if !unicode.IsPrint(r) {
			return false
		}
	}
	return true
}

// reserveClient takes one slot of the client cap. See Server.registered for its limits.
func (s *Server) reserveClient() bool {
	s.regMu.Lock()
	defer s.regMu.Unlock()
	if s.cfg.MaxClients > 0 && s.registered >= s.cfg.MaxClients {
		return false
	}
	s.registered++
	return true
}

func (s *Server) releaseClient() {
	s.regMu.Lock()
	s.registered--
	s.regMu.Unlock()
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
	player, _, err := s.cfg.Verifier.Verify(w, r)
	switch {
	case err == nil:
	case errors.Is(err, studiosig.ErrTooLarge):
		oauthError(w, http.StatusRequestEntityTooLarge, "invalid_request", "body too large")
		return
	case errors.Is(err, studiosig.ErrReplay):
		oauthError(w, http.StatusUnauthorized, "invalid_client", "this signed request was already used")
		return
	case errors.Is(err, studiosig.ErrBusy):
		oauthError(w, http.StatusServiceUnavailable, "temporarily_unavailable", "try again later")
		return
	default:
		oauthError(w, http.StatusUnauthorized, "invalid_client", "a valid studio signature is required")
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
	ok, _ := l.allowRetry(key)
	return ok
}

// allowRetry is allow, and when the attempt is refused it also returns how long until the
// oldest counted attempt leaves the window.
func (l *limiter) allowRetry(key string) (bool, time.Duration) {
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
		return false, kept[0].Add(l.window).Sub(now)
	}
	l.hits[key] = append(kept, now)
	if len(l.hits) > 10000 { // bound memory: drop idle keys
		for k, v := range l.hits {
			if len(v) == 0 || now.Sub(v[len(v)-1]) >= l.window {
				delete(l.hits, k)
			}
		}
	}
	return true, 0
}

// clientAddr is the address the limiters key on. With ClientIPHeader unset it is the host of
// RemoteAddr. With it set, it is the first comma separated value of that header, if it parses as
// an IP address, else RemoteAddr. See ServerConfig.ClientIPHeader for the trust requirement.
func (s *Server) clientAddr(r *http.Request) string {
	if h := s.cfg.ClientIPHeader; h != "" {
		first, _, _ := strings.Cut(r.Header.Get(h), ",")
		if a, err := netip.ParseAddr(strings.TrimSpace(first)); err == nil {
			return a.Unmap().WithZone("").String()
		}
	}
	return clientIP(r)
}

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
