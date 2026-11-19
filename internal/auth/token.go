package auth

import (
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"net/http"
)

func (s *Server) token(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxBody)
	if err := r.ParseForm(); err != nil {
		oauthError(w, http.StatusBadRequest, "invalid_request", "send an application/x-www-form-urlencoded body")
		return
	}
	switch r.PostForm.Get("grant_type") {
	case "authorization_code":
		s.codeGrant(w, r)
	case "refresh_token":
		s.refreshGrant(w, r)
	default:
		oauthError(w, http.StatusBadRequest, "unsupported_grant_type", "use authorization_code or refresh_token")
	}
}

// pkceOK checks code_verifier against the S256 challenge from the authorization request.
func pkceOK(verifier, challenge string) bool {
	if len(verifier) < 43 || len(verifier) > 128 {
		return false
	}
	sum := sha256.Sum256([]byte(verifier))
	return constantTimeEqual(base64.RawURLEncoding.EncodeToString(sum[:]), challenge)
}

func (s *Server) codeGrant(w http.ResponseWriter, r *http.Request) {
	f := r.PostForm
	// The code is consumed first, so a failed attempt cannot be retried with other parameters.
	code, err := s.cfg.Store.TakeCode(r.Context(), HashToken(f.Get("code")))
	if err != nil {
		oauthError(w, http.StatusBadRequest, "invalid_grant", "the code is invalid, expired or already used")
		return
	}
	switch {
	case f.Get("client_id") != code.ClientID:
		oauthError(w, http.StatusBadRequest, "invalid_grant", "the code was issued to another client")
		return
	case f.Get("redirect_uri") != code.RedirectURI:
		oauthError(w, http.StatusBadRequest, "invalid_grant", "redirect_uri differs from the authorization request")
		return
	case !pkceOK(f.Get("code_verifier"), code.Challenge):
		oauthError(w, http.StatusBadRequest, "invalid_grant", "code_verifier does not match the code_challenge")
		return
	case f.Get("resource") != "" && f.Get("resource") != code.Resource:
		oauthError(w, http.StatusBadRequest, "invalid_target", "resource differs from the authorization request")
		return
	}
	g, err := s.cfg.Store.UpsertGrant(r.Context(), Grant{Tenant: code.Tenant, Player: code.Player, ClientID: code.ClientID, Resource: code.Resource, Scope: code.Scope.Cap(s.cfg.MaxScope)})
	if err != nil {
		oauthError(w, http.StatusServiceUnavailable, "temporarily_unavailable", "try again later")
		return
	}
	s.issue(w, r, g, "")
}

func (s *Server) refreshGrant(w http.ResponseWriter, r *http.Request) {
	f := r.PostForm
	newRefresh, newHash := NewToken()
	g, err := s.cfg.Store.RotateRefresh(r.Context(), HashToken(f.Get("refresh_token")), newHash, s.cfg.Now().Add(RefreshTTL))
	switch {
	case errors.Is(err, ErrReuse):
		oauthError(w, http.StatusBadRequest, "invalid_grant", "this refresh token was already used, so the grant was revoked. The player must link again")
		return
	case errors.Is(err, ErrNotFound):
		oauthError(w, http.StatusBadRequest, "invalid_grant", "the refresh token is invalid, expired or revoked")
		return
	case err != nil:
		oauthError(w, http.StatusServiceUnavailable, "temporarily_unavailable", "try again later")
		return
	}
	if f.Get("client_id") != g.ClientID {
		// The token was rotated by someone else's client. The real client's next use
		// will trip reuse detection and revoke the grant, which is the safe outcome.
		oauthError(w, http.StatusBadRequest, "invalid_grant", "the refresh token was issued to another client")
		return
	}
	s.issue(w, r, g, newRefresh)
}

// issue mints an access token and, when refresh is empty, a first refresh token.
func (s *Server) issue(w http.ResponseWriter, r *http.Request, g Grant, refresh string) {
	access, accessHash := NewToken()
	if err := s.cfg.Store.PutAccess(r.Context(), accessHash, g.ID, s.cfg.Now().Add(AccessTTL)); err != nil {
		oauthError(w, http.StatusServiceUnavailable, "temporarily_unavailable", "try again later")
		return
	}
	if refresh == "" {
		var hash string
		refresh, hash = NewToken()
		if err := s.cfg.Store.PutRefresh(r.Context(), hash, g.ID, s.cfg.Now().Add(RefreshTTL)); err != nil {
			oauthError(w, http.StatusServiceUnavailable, "temporarily_unavailable", "try again later")
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"access_token": access, "token_type": "Bearer", "expires_in": int(AccessTTL.Seconds()),
		"refresh_token": refresh, "scope": string(g.Scope.Cap(s.cfg.MaxScope)),
	})
}
