package oidc

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// Result is what a finished sign-in yields.
type Result struct {
	// Player is the studio's player id, from the verified ID token. Empty unless Complete
	// returned no error.
	Player string
	// Payload is the authorization request the player started, exactly as Begin received it.
	// It is set whenever the session was authentic and the state matched, also on ErrDenied
	// and other failures after that point, so the caller can send the player back to the
	// client with an error. It is nil when the session cannot be trusted (no cookie, tampered,
	// expired, wrong state, replayed): then the caller must not redirect anywhere.
	Payload map[string]string
}

// Flow runs OIDC logins against one provider. It is safe for concurrent use.
type Flow struct {
	cfg  Config
	v    Verifier
	seal *sealer
	prov *provider
	now  func() time.Time

	mu   sync.Mutex
	used map[string]time.Time // finished or attempted states, until their session would have expired
}

// maxUsed bounds the replay cache. At the cap new sign-ins are refused (fail closed) until
// entries expire, instead of the cache growing without limit.
const maxUsed = 50000

// NewFlow checks cfg and builds a Flow. hc may be nil (a client that never follows redirects,
// with a 10 second timeout). now may be nil.
func NewFlow(cfg Config, v Verifier, hc *http.Client, now func() time.Time) (*Flow, error) {
	if v == nil {
		return nil, errors.New("oidc: a Verifier is required")
	}
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	if cfg.PlayerClaim == "" {
		cfg.PlayerClaim = DefaultPlayerClaim
	}
	if cfg.SessionTTL <= 0 {
		cfg.SessionTTL = DefaultSessionTTL
	}
	hasOpenID := false
	for _, s := range cfg.Scopes {
		hasOpenID = hasOpenID || s == "openid"
	}
	if !hasOpenID {
		cfg.Scopes = append([]string{"openid"}, cfg.Scopes...)
	}
	if hc == nil {
		hc = newHTTPClient()
	}
	if now == nil {
		now = time.Now
	}
	s, err := newSealer(cfg.SessionSecret)
	if err != nil {
		return nil, err
	}
	return &Flow{cfg: cfg, v: v, seal: s, prov: &provider{cfg: cfg, hc: hc, now: now}, now: now, used: map[string]time.Time{}}, nil
}

// maxPayload bounds the authorization request kept in the session cookie.
const maxPayload = 4 << 10

// AuthOrigin returns the scheme and host of the IdP's authorization endpoint, which the consent
// page needs in its form-action policy (a browser applies that policy to the redirect that
// answers a form post). ok is false when discovery fails, and then the page must not offer
// the sign-in button.
func (f *Flow) AuthOrigin(ctx context.Context) (origin string, ok bool) {
	ep, err := f.prov.endpoints(ctx)
	if err != nil {
		return "", false
	}
	u, err := url.Parse(ep.auth)
	if err != nil || u.Host == "" {
		return "", false
	}
	return u.Scheme + "://" + u.Host, true
}

func randString(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// cookie returns the name and the common attributes of the session cookie. It is sent only to the
// callback path, is HttpOnly, and is SameSite=Lax because the callback is a top level navigation
// from the IdP (Strict would drop it).
func (f *Flow) cookie(value string, maxAge int) *http.Cookie {
	u, _ := url.Parse(f.cfg.RedirectURL)
	secure := u != nil && u.Scheme == "https"
	name := "agw_oidc"
	if secure {
		name = "__Secure-agw_oidc"
	}
	path := "/"
	if u != nil && u.Path != "" {
		path = u.Path
	}
	// Secure follows the callback's scheme: https in production, plain http only for a loopback IdP.
	return &http.Cookie{Name: name, Value: value, Path: path, MaxAge: maxAge, HttpOnly: true, Secure: secure, SameSite: http.SameSiteLaxMode} //nolint:gosec // see above
}

// Begin starts a sign-in. payload is the authorization request the player is answering (the
// caller has already validated it); it comes back from Complete. Begin sets the sealed session
// cookie on w and returns the IdP URL to send the player to.
func (f *Flow) Begin(ctx context.Context, w http.ResponseWriter, payload map[string]string) (string, error) {
	size := 0
	for k, v := range payload {
		size += len(k) + len(v)
	}
	if len(payload) == 0 || size > maxPayload {
		return "", errors.New("oidc: the authorization request is empty or too large")
	}
	ep, err := f.prov.endpoints(ctx)
	if err != nil {
		return "", err
	}
	state, err1 := randString(16)
	nonce, err2 := randString(16)
	verifier, err3 := randString(32)
	if err := errors.Join(err1, err2, err3); err != nil {
		return "", err
	}
	sealed, err := f.seal.seal(session{
		State: state, Nonce: nonce, Verifier: verifier, Payload: payload,
		Expires: f.now().Add(f.cfg.SessionTTL).Unix(),
	})
	if err != nil {
		return "", err
	}
	http.SetCookie(w, f.cookie(sealed, int(f.cfg.SessionTTL.Seconds())))

	sum := sha256.Sum256([]byte(verifier))
	u, err := url.Parse(ep.auth)
	if err != nil {
		return "", ErrDiscovery
	}
	q := u.Query()
	q.Set("response_type", "code")
	q.Set("client_id", f.cfg.ClientID)
	q.Set("redirect_uri", f.cfg.RedirectURL)
	q.Set("scope", strings.Join(f.cfg.Scopes, " "))
	q.Set("state", state)
	q.Set("nonce", nonce)
	q.Set("code_challenge", base64.RawURLEncoding.EncodeToString(sum[:]))
	q.Set("code_challenge_method", "S256")
	u.RawQuery = q.Encode()
	return u.String(), nil
}

// Complete finishes a sign-in from the callback request. It reads and clears the session cookie,
// checks the state, makes sure the callback is used only once, exchanges the code, has the
// Verifier authenticate the ID token, re-checks its claims and reads the player id.
func (f *Flow) Complete(ctx context.Context, w http.ResponseWriter, r *http.Request) (Result, error) {
	c, err := r.Cookie(f.cookie("", 0).Name)
	if err != nil {
		return Result{}, ErrSession
	}
	http.SetCookie(w, f.cookie("", -1)) // the session is single use, whatever happens next
	now := f.now()
	sess, err := f.seal.open(c.Value, now)
	if err != nil {
		return Result{}, err
	}
	q := r.URL.Query()
	if subtle.ConstantTimeCompare([]byte(q.Get("state")), []byte(sess.State)) != 1 {
		return Result{}, ErrState
	}
	if !f.markUsed(sess.State, now) {
		return Result{}, ErrReplay
	}
	res := Result{Payload: sess.Payload}
	if q.Get("error") != "" {
		return res, ErrDenied
	}
	// RFC 9207: when the IdP says who answered, it must be the IdP we asked.
	if iss := q.Get("iss"); iss != "" && iss != f.cfg.Issuer {
		return res, ErrIssuer
	}
	code := q.Get("code")
	if code == "" {
		return res, fmt.Errorf("%w: no code", ErrExchange)
	}
	ep, err := f.prov.endpoints(ctx)
	if err != nil {
		return res, err
	}
	raw, err := f.prov.exchange(ctx, ep.token, code, sess.Verifier)
	if err != nil {
		return res, err
	}
	claims, err := f.v.Verify(ctx, raw, sess.Nonce)
	if err != nil {
		return res, fmt.Errorf("%w: %v", ErrToken, err)
	}
	if err := CheckClaims(claims, f.cfg, sess.Nonce, now); err != nil {
		return res, err
	}
	player, err := PlayerID(claims, f.cfg.PlayerClaim)
	if err != nil {
		return res, err
	}
	res.Player = player
	return res, nil
}

// markUsed records a state and reports whether it was new. Entries live as long as a session
// could, so a replayed callback is refused for as long as its cookie could still open.
func (f *Flow) markUsed(state string, now time.Time) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.used) >= maxUsed/2 {
		for k, exp := range f.used {
			if !now.Before(exp) {
				delete(f.used, k)
			}
		}
	}
	if _, seen := f.used[state]; seen || len(f.used) >= maxUsed {
		return false
	}
	f.used[state] = now.Add(f.cfg.SessionTTL + time.Minute)
	return true
}
