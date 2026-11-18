package auth

import (
	"crypto/subtle"
	"html/template"
	"net/http"
	"net/url"
	"regexp"
	"strings"
)

// authzRequest is a validated authorization request.
type authzRequest struct {
	client        Client
	redirectURI   string
	challenge     string
	state         string
	resource      string
	allowedScopes []Scope
}

// maxState bounds the opaque value we echo back in a redirect and in the consent page.
const maxState = 512

var challengeRe = regexp.MustCompile(`^[A-Za-z0-9_-]{43,128}$`)

// parseAuthz validates the parameters of an authorization request. The returned
// bool says whether the client and redirect URI were verified, which decides whether
// errors may be sent to the redirect URI or must be shown to the user instead.
func (s *Server) parseAuthz(r *http.Request, q url.Values) (*authzRequest, bool, string, string) {
	c, err := s.cfg.Store.Client(r.Context(), q.Get("client_id"))
	if err != nil {
		return nil, false, "invalid_request", "unknown client_id"
	}
	redirect := q.Get("redirect_uri")
	ok := false
	for _, reg := range c.RedirectURIs {
		if redirectMatches(reg, redirect) {
			ok = true
			break
		}
	}
	if !ok {
		return nil, false, "invalid_request", "redirect_uri does not match a registered one"
	}
	req := &authzRequest{client: c, redirectURI: redirect, state: q.Get("state"), challenge: q.Get("code_challenge"), resource: q.Get("resource")}
	if len(req.state) > maxState {
		return req, true, "invalid_request", "state is too long"
	}
	if q.Get("response_type") != "code" {
		return req, true, "unsupported_response_type", "only response_type=code is supported"
	}
	if q.Get("code_challenge_method") != "S256" || !challengeRe.MatchString(req.challenge) {
		return req, true, "invalid_request", "PKCE is required: code_challenge with code_challenge_method=S256"
	}
	if req.resource != s.cfg.Resource {
		return req, true, "invalid_target", "resource must be " + s.cfg.Resource
	}
	req.allowedScopes = []Scope{ScopeCoach, ScopeOperator}
	if raw := strings.TrimSpace(q.Get("scope")); raw != "" {
		req.allowedScopes = nil
		for _, f := range strings.Fields(raw) {
			sc := Scope(f)
			if !sc.Valid() {
				return req, true, "invalid_scope", "scope must be coach and/or operator"
			}
			req.allowedScopes = append(req.allowedScopes, sc)
		}
	}
	return req, true, "", ""
}

func (s *Server) authorizeForm(w http.ResponseWriter, r *http.Request) {
	req, verified, code, desc := s.parseAuthz(r, r.URL.Query())
	if code != "" {
		s.fail(w, r, req, verified, code, desc)
		return
	}
	s.render(w, http.StatusOK, req, "")
}

func (s *Server) authorizeSubmit(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxBody)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	req, verified, code, desc := s.parseAuthz(r, r.PostForm)
	if code != "" {
		s.fail(w, r, req, verified, code, desc)
		return
	}
	// Only an explicit allow proceeds. Anything else, including a missing decision, is a denial.
	if r.PostForm.Get("decision") != "allow" {
		s.redirectBack(w, r, req, url.Values{"error": {"access_denied"}, "error_description": {"the player declined"}})
		return
	}
	scope := Scope(r.PostForm.Get("scope_choice"))
	allowed := false
	for _, a := range req.allowedScopes {
		if a == scope {
			allowed = true
		}
	}
	if !allowed {
		s.render(w, http.StatusBadRequest, req, "Choose what the agent may do.")
		return
	}
	// Guessing a ticket is the only attack on this form, so every submission counts.
	if !s.attempts.allow(clientIP(r)) {
		w.Header().Set("Retry-After", "60")
		s.render(w, http.StatusTooManyRequests, req, "Too many attempts. Wait a minute and try again.")
		return
	}
	ticket := normalizeTicket(r.PostForm.Get("ticket"))
	var tenant, player string
	var err error
	if ticket != "" {
		tenant, player, err = s.cfg.Store.TakeTicket(r.Context(), HashToken(ticket))
	}
	if ticket == "" || err != nil || tenant != s.cfg.Tenant {
		// Wrong, used and expired tickets look the same.
		s.render(w, http.StatusUnauthorized, req, "That link code did not work. Ask the game for a new one.")
		return
	}
	g, err := s.cfg.Store.UpsertGrant(r.Context(), Grant{Tenant: tenant, Player: player, ClientID: req.client.ID, Resource: req.resource, Scope: scope})
	if err != nil {
		s.render(w, http.StatusServiceUnavailable, req, "Something went wrong. Try again.")
		return
	}
	plain, hash := NewToken()
	if err := s.cfg.Store.PutCode(r.Context(), Code{
		Hash: hash, ClientID: req.client.ID, RedirectURI: req.redirectURI, Challenge: req.challenge,
		Resource: req.resource, Scope: g.Scope, Tenant: tenant, Player: player, Expires: s.cfg.Now().Add(CodeTTL),
	}); err != nil {
		s.render(w, http.StatusServiceUnavailable, req, "Something went wrong. Try again.")
		return
	}
	s.redirectBack(w, r, req, url.Values{"code": {plain}})
}

// fail reports an authorization error. Until the client and redirect URI are verified
// the error is shown to the user, never sent to a URL an attacker may have chosen.
func (s *Server) fail(w http.ResponseWriter, r *http.Request, req *authzRequest, verified bool, code, desc string) {
	if !verified {
		securityHeaders(w, "")
		http.Error(w, "Cannot continue: "+desc, http.StatusBadRequest)
		return
	}
	s.redirectBack(w, r, req, url.Values{"error": {code}, "error_description": {desc}})
}

func (s *Server) redirectBack(w http.ResponseWriter, r *http.Request, req *authzRequest, params url.Values) {
	u, err := url.Parse(req.redirectURI)
	if err != nil {
		http.Error(w, "bad redirect", http.StatusBadRequest)
		return
	}
	if req.state != "" {
		params.Set("state", req.state)
	}
	params.Set("iss", s.cfg.Issuer) // RFC 9207: lets a client detect a mix-up
	q := u.Query()
	for k, v := range params {
		q[k] = v
	}
	u.RawQuery = q.Encode()
	securityHeaders(w, "")
	http.Redirect(w, r, u.String(), http.StatusSeeOther) //nolint:gosec // target matched a registered redirect URI; 303 so the browser never re-POSTs
}

// securityHeaders sets the headers every page of the authorization server carries.
// formTarget is the redirect URI of the request. Browsers apply form-action to the
// redirect that answers a form post, so the consent page must allow that one origin.
func securityHeaders(w http.ResponseWriter, formTarget string) {
	h := w.Header()
	h.Set("Cache-Control", "no-store")
	h.Set("X-Frame-Options", "DENY") // the consent page must never be framed (clickjacking)
	formAction := "'self'"
	if u, err := url.Parse(formTarget); err == nil && u.Host != "" {
		host := u.Host
		if isLoopback(u.Hostname()) { // loopback clients may use any port
			host = u.Hostname() + ":*"
			if strings.Contains(u.Hostname(), ":") {
				host = "[" + u.Hostname() + "]:*"
			}
		}
		formAction += " " + u.Scheme + "://" + host
	}
	h.Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; form-action "+formAction+"; frame-ancestors 'none'; base-uri 'none'")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Referrer-Policy", "no-referrer")
}

var consentTmpl = template.Must(template.New("consent").Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>Link your agent</title>
<style>body{font:16px system-ui,sans-serif;max-width:30rem;margin:3rem auto;padding:0 1rem}
fieldset{border:1px solid #888;border-radius:6px;margin:1rem 0}label{display:block;margin:.5rem 0}
input[type=text]{font:1.2rem monospace;letter-spacing:.1em;width:10rem}
.err{color:#a00;font-weight:600}button{font:inherit;padding:.5rem 1rem;margin-right:.5rem}</style></head>
<body><h1>Link an AI agent</h1>
<p><strong>{{.ClientName}}</strong> wants to act in the game for you. It will be sent back to <code>{{.RedirectHost}}</code>.</p>
{{if .Error}}<p class="err">{{.Error}}</p>{{end}}
<form method="post" action="/authorize">
<input type="hidden" name="client_id" value="{{.ClientID}}">
<input type="hidden" name="redirect_uri" value="{{.RedirectURI}}">
<input type="hidden" name="response_type" value="code">
<input type="hidden" name="code_challenge" value="{{.Challenge}}">
<input type="hidden" name="code_challenge_method" value="S256">
<input type="hidden" name="state" value="{{.State}}">
<input type="hidden" name="resource" value="{{.Resource}}">
<input type="hidden" name="scope" value="{{.ScopeParam}}">
<fieldset><legend>What may it do?</legend>
{{range $i, $s := .Scopes}}<label><input type="radio" name="scope_choice" value="{{$s.Value}}" {{if eq $i 0}}checked{{end}}> <strong>{{$s.Label}}</strong>: {{$s.Help}}</label>{{end}}
</fieldset>
<label>Link code from the game <input type="text" name="ticket" autocomplete="off" autocapitalize="characters" maxlength="9" placeholder="XXXX-XXXX" required></label>
<p><button type="submit" name="decision" value="allow">Allow</button><button type="submit" name="decision" value="deny" formnovalidate>Deny</button></p>
</form></body></html>`))

type scopeChoice struct{ Value, Label, Help string }

func (s *Server) render(w http.ResponseWriter, status int, req *authzRequest, errMsg string) {
	var scopes []scopeChoice
	var scopeParam []string
	for _, sc := range req.allowedScopes {
		scopeParam = append(scopeParam, string(sc))
		switch sc {
		case ScopeCoach:
			scopes = append(scopes, scopeChoice{string(sc), "Coach", "read only: look at your game and give advice"})
		case ScopeOperator:
			scopes = append(scopes, scopeChoice{string(sc), "Operator", "also take actions for you, within the game's limits"})
		}
	}
	host := req.redirectURI
	if u, err := url.Parse(req.redirectURI); err == nil {
		host = u.Host
	}
	securityHeaders(w, req.redirectURI)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_ = consentTmpl.Execute(w, map[string]any{
		"ClientName": req.client.Name, "RedirectHost": host, "ClientID": req.client.ID,
		"RedirectURI": req.redirectURI, "Challenge": req.challenge, "State": req.state,
		"Resource": req.resource, "ScopeParam": strings.Join(scopeParam, " "), "Scopes": scopes, "Error": errMsg,
	})
}

// constantTimeEqual compares two strings without leaking where they differ.
func constantTimeEqual(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}
