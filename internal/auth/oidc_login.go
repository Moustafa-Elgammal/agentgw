package auth

import (
	"context"
	"errors"
	"net/http"
	"net/url"

	"github.com/Moustafa-Elgammal/agentgw/internal/auth/oidc"
)

// Paths of the OIDC sign-in at the consent page. Register OIDCCallbackPath, on the gateway's
// public URL, as the redirect URI at the identity provider.
const (
	OIDCStartPath    = "/authorize/oidc"
	OIDCCallbackPath = "/authorize/oidc/callback"
)

// OIDCLogin is what the consent page needs from a sign-in with the studio's identity provider.
// *oidc.Flow implements it. See docs/design/oidc-consent.md.
type OIDCLogin interface {
	// Begin starts a sign-in for the given authorization request and returns the IdP URL.
	Begin(ctx context.Context, w http.ResponseWriter, payload map[string]string) (string, error)
	// Complete finishes it from the callback request.
	Complete(ctx context.Context, w http.ResponseWriter, r *http.Request) (oidc.Result, error)
	// AuthOrigin is the origin of the IdP's authorization endpoint, for the page's form-action
	// policy. ok is false when the IdP cannot be reached, and then the page hides the button.
	AuthOrigin(ctx context.Context) (origin string, ok bool)
}

// payloadKeys are the authorization request fields a sign-in carries through the IdP and back.
var payloadKeys = []string{"client_id", "redirect_uri", "response_type", "code_challenge", "code_challenge_method", "state", "resource", "scope", "scope_choice"}

func (s *Server) oidcEnabled(w http.ResponseWriter) bool {
	if s.cfg.OIDC == nil {
		http.Error(w, "sign in with an identity provider is not enabled", http.StatusNotFound)
		return false
	}
	return true
}

// oidcStart takes the consent form, checks it exactly as the link code path does, and sends the
// player to the identity provider. No grant is made yet: that happens at the callback, for the
// player the provider vouches for.
func (s *Server) oidcStart(w http.ResponseWriter, r *http.Request) {
	if !s.oidcEnabled(w) {
		return
	}
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
	scope := Scope(r.PostForm.Get("scope_choice"))
	if !scopeAllowed(req, scope) {
		s.render(w, r, http.StatusBadRequest, req, "Choose what the agent may do.")
		return
	}
	if !s.attempts.allow(s.clientAddr(r)) {
		w.Header().Set("Retry-After", "60")
		s.render(w, r, http.StatusTooManyRequests, req, "Too many attempts. Wait a minute and try again.")
		return
	}
	payload := map[string]string{}
	for _, k := range payloadKeys {
		if v := r.PostForm.Get(k); v != "" {
			payload[k] = v
		}
	}
	to, err := s.cfg.OIDC.Begin(r.Context(), w, payload)
	if err != nil {
		s.render(w, r, http.StatusServiceUnavailable, req, "Signing in is not available right now. Use a link code instead, or try again later.")
		return
	}
	securityHeaders(w, "")
	http.Redirect(w, r, to, http.StatusSeeOther) //nolint:gosec // the target is built from the operator's configured IdP endpoint, never from the request
}

// oidcCallback is where the identity provider sends the player back. The only things trusted
// here are the sealed session (made by Begin, in the player's own browser) and the verified ID
// token. Everything the client asked for is validated again from the session, so a callback can
// never send the player anywhere a registered redirect URI does not allow.
func (s *Server) oidcCallback(w http.ResponseWriter, r *http.Request) {
	if !s.oidcEnabled(w) {
		return
	}
	if !s.attempts.allow(s.clientAddr(r)) {
		w.Header().Set("Retry-After", "60")
		securityHeaders(w, "")
		http.Error(w, "Too many attempts. Wait a minute and try again.", http.StatusTooManyRequests)
		return
	}
	res, err := s.cfg.OIDC.Complete(r.Context(), w, r)
	if res.Payload == nil {
		// No trustworthy session: nothing is known about who asked, so nothing is redirected.
		securityHeaders(w, "")
		http.Error(w, "Cannot continue: the sign-in could not be completed. Start again from your agent.", http.StatusBadRequest)
		return
	}
	q := url.Values{}
	for k, v := range res.Payload {
		q.Set(k, v)
	}
	req, verified, code, desc := s.parseAuthz(r, q)
	if code != "" {
		s.fail(w, r, req, verified, code, desc)
		return
	}
	switch {
	case errors.Is(err, oidc.ErrDenied):
		s.redirectBack(w, r, req, url.Values{"error": {"access_denied"}, "error_description": {"the player declined"}})
		return
	case errors.Is(err, oidc.ErrExchange), errors.Is(err, oidc.ErrDiscovery):
		s.cfg.Log.Warn("oidc sign-in failed", "kind", "identity provider unavailable", "err", err)
		s.redirectBack(w, r, req, url.Values{"error": {"temporarily_unavailable"}, "error_description": {"sign-in is not available right now"}})
		return
	case err != nil || res.Player == "":
		// A token that fails verification says nothing about the player; the reason is for logs.
		s.cfg.Log.Warn("oidc sign-in failed", "kind", "token rejected", "err", err)
		s.redirectBack(w, r, req, url.Values{"error": {"access_denied"}, "error_description": {"sign-in failed"}})
		return
	}
	scope := Scope(res.Payload["scope_choice"])
	if !scopeAllowed(req, scope) {
		s.redirectBack(w, r, req, url.Values{"error": {"invalid_scope"}, "error_description": {"scope not allowed"}})
		return
	}
	plain, gerr := s.grantAndCode(r.Context(), req, s.cfg.Tenant, res.Player, scope)
	if gerr != nil {
		s.redirectBack(w, r, req, url.Values{"error": {"server_error"}, "error_description": {"could not record the grant"}})
		return
	}
	s.redirectBack(w, r, req, url.Values{"code": {plain}})
}

func scopeAllowed(req *authzRequest, scope Scope) bool {
	for _, a := range req.allowedScopes {
		if a == scope {
			return true
		}
	}
	return false
}
