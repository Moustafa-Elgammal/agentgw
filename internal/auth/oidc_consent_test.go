package auth_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/Moustafa-Elgammal/agentgw/internal/auth"
	"github.com/Moustafa-Elgammal/agentgw/internal/auth/oidc"
)

const idpURL = "https://idp.example/authorize?client_id=gw"

// fakeLogin stands in for the OIDC flow: tests decide what the identity provider "said".
type fakeLogin struct {
	beginErr  error
	began     []map[string]string
	result    oidc.Result
	completeE error
	completed int
	noOrigin  bool
}

func (f *fakeLogin) Begin(_ context.Context, w http.ResponseWriter, p map[string]string) (string, error) {
	if f.beginErr != nil {
		return "", f.beginErr
	}
	f.began = append(f.began, p)
	http.SetCookie(w, &http.Cookie{Name: "agw_oidc", Value: "sealed", Path: auth.OIDCCallbackPath, HttpOnly: true}) //nolint:gosec // a stand-in cookie for a fake flow
	return idpURL, nil
}

func (f *fakeLogin) Complete(context.Context, http.ResponseWriter, *http.Request) (oidc.Result, error) {
	f.completed++
	return f.result, f.completeE
}

func (f *fakeLogin) AuthOrigin(context.Context) (string, bool) {
	return "https://idp.example", !f.noOrigin
}

func oidcEnv(t *testing.T, l auth.OIDCLogin, max auth.Scope) *env {
	t.Helper()
	clk := &clock{t: time.Now()}
	st := auth.NewMemory(clk.now)
	s := auth.NewServer(auth.ServerConfig{
		Store: st, Issuer: testIssuer, Resource: testResource, Tenant: "t1",
		StudioKeyID: studioKey, StudioSecret: studioSecret, Now: clk.now, OIDC: l, MaxScope: max,
	})
	return &env{t, s.Handler(), st, clk}
}

func (e *env) oidcStart(v url.Values, scope auth.Scope) *httptest.ResponseRecorder {
	v = cloneValues(v)
	v.Set("scope_choice", string(scope))
	r := httptest.NewRequest("POST", "/authorize/oidc", strings.NewReader(v.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return e.do(r)
}

func (e *env) oidcCallback() *httptest.ResponseRecorder {
	return e.do(httptest.NewRequest("GET", "/authorize/oidc/callback?state=x&code=y", nil))
}

// payloadFor is what the flow hands back after a sign-in that started from authzParams.
func payloadFor(id, redirect, challenge string, scope auth.Scope) map[string]string {
	p := map[string]string{"scope_choice": string(scope)}
	for k, v := range authzParams(id, redirect, challenge) {
		p[k] = v[0]
	}
	return p
}

func TestConsentPageOffersOIDCOnlyWhenEnabled(t *testing.T) {
	tests := []struct {
		name      string
		login     auth.OIDCLogin
		wantBtn   bool
		wantOrig  bool
		ticketReq bool
	}{
		{"no oidc", nil, false, false, true},
		{"oidc on", &fakeLogin{}, true, true, false},
		{"idp unreachable hides the button and keeps the code required", &fakeLogin{noOrigin: true}, false, false, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := oidcEnv(t, tt.login, "")
			id := e.client("A", cb)
			_, ch := pkce()
			w := e.authzGet(authzParams(id, cb, ch))
			if w.Code != 200 {
				t.Fatalf("status %d", w.Code)
			}
			body, csp := w.Body.String(), w.Header().Get("Content-Security-Policy")
			if got := strings.Contains(body, `formaction="/authorize/oidc"`); got != tt.wantBtn {
				t.Errorf("button present = %v, want %v", got, tt.wantBtn)
			}
			if got := strings.Contains(csp, "https://idp.example"); got != tt.wantOrig {
				t.Errorf("idp origin in form-action = %v, want %v (csp %q)", got, tt.wantOrig, csp)
			}
			if got := strings.Contains(body, `placeholder="XXXX-XXXX" required`); got != tt.ticketReq {
				t.Errorf("link code required = %v, want %v", got, tt.ticketReq)
			}
			// The link code form still works exactly as before in every configuration.
			if !strings.Contains(body, `name="ticket"`) {
				t.Error("link code input missing")
			}
		})
	}
}

func TestOIDCRoutesAre404WhenDisabled(t *testing.T) {
	e := newEnv(t)
	id := e.client("A", cb)
	_, ch := pkce()
	if w := e.oidcStart(authzParams(id, cb, ch), auth.ScopeCoach); w.Code != 404 {
		t.Errorf("start = %d", w.Code)
	}
	if w := e.oidcCallback(); w.Code != 404 {
		t.Errorf("callback = %d", w.Code)
	}
}

func TestOIDCStart(t *testing.T) {
	_, ch := pkce()
	tests := []struct {
		name     string
		max      auth.Scope
		mutate   func(v url.Values, id string)
		scope    auth.Scope
		beginErr error
		want     int
		began    bool
	}{
		{name: "ok", scope: auth.ScopeCoach, want: 303, began: true},
		{name: "operator ok without a cap", scope: auth.ScopeOperator, want: 303, began: true},
		{name: "operator refused under a coach cap", max: auth.ScopeCoach, scope: auth.ScopeOperator, want: 400},
		{name: "no scope choice", scope: "", want: 400},
		{name: "unknown scope", scope: "admin", want: 400},
		{name: "unregistered redirect uri", scope: auth.ScopeCoach, want: 400,
			mutate: func(v url.Values, _ string) { v.Set("redirect_uri", "https://evil.example/cb") }},
		{name: "unknown client", scope: auth.ScopeCoach, want: 400,
			mutate: func(v url.Values, _ string) { v.Set("client_id", "nope") }},
		{name: "pkce missing", scope: auth.ScopeCoach, want: 303, // parseAuthz errors go back to the client
			mutate: func(v url.Values, _ string) { v.Del("code_challenge") }},
		{name: "wrong resource", scope: auth.ScopeCoach, want: 303,
			mutate: func(v url.Values, _ string) { v.Set("resource", "https://other.example/mcp") }},
		{name: "idp unavailable", scope: auth.ScopeCoach, beginErr: errors.New("down"), want: 503},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			l := &fakeLogin{beginErr: tt.beginErr}
			e := oidcEnv(t, l, tt.max)
			id := e.client("A", cb)
			v := authzParams(id, cb, ch)
			if tt.mutate != nil {
				tt.mutate(v, id)
			}
			w := e.oidcStart(v, tt.scope)
			if w.Code != tt.want {
				t.Fatalf("status %d, want %d: %s", w.Code, tt.want, w.Body)
			}
			if (len(l.began) == 1) != tt.began {
				t.Fatalf("Begin calls = %d, want began=%v", len(l.began), tt.began)
			}
			if tt.name == "ok" {
				if got := w.Header().Get("Location"); got != idpURL {
					t.Errorf("redirect = %q", got)
				}
				p := l.began[0]
				if p["client_id"] != id || p["redirect_uri"] != cb || p["scope_choice"] != "coach" || p["state"] != "st8" || p["code_challenge"] != ch {
					t.Errorf("payload = %v", p)
				}
				if !strings.Contains(w.Header().Get("Set-Cookie"), "agw_oidc") {
					t.Error("session cookie not passed through")
				}
			}
			if tt.want == 400 || tt.want == 503 {
				// A refused start must not send the browser anywhere.
				if loc := w.Header().Get("Location"); loc != "" {
					t.Errorf("redirected to %q", loc)
				}
			}
		})
	}
}

func TestOIDCCallback(t *testing.T) {
	_, ch := pkce()
	tests := []struct {
		name      string
		max       auth.Scope
		result    func(id string) oidc.Result
		err       error
		wantCode  int
		wantErr   string // error parameter on the redirect back to the client
		wantGrant bool
	}{
		{name: "signed in", wantCode: 303, wantGrant: true,
			result: func(id string) oidc.Result {
				return oidc.Result{Player: "p-42", Payload: payloadFor(id, cb, ch, auth.ScopeOperator)}
			}},
		{name: "player declined at the idp", wantCode: 303, wantErr: "access_denied", err: oidc.ErrDenied,
			result: func(id string) oidc.Result { return oidc.Result{Payload: payloadFor(id, cb, ch, auth.ScopeCoach)} }},
		{name: "idp token endpoint down", wantCode: 303, wantErr: "temporarily_unavailable", err: oidc.ErrExchange,
			result: func(id string) oidc.Result { return oidc.Result{Payload: payloadFor(id, cb, ch, auth.ScopeCoach)} }},
		{name: "bad id token", wantCode: 303, wantErr: "access_denied", err: oidc.ErrToken,
			result: func(id string) oidc.Result { return oidc.Result{Payload: payloadFor(id, cb, ch, auth.ScopeCoach)} }},
		{name: "wrong audience", wantCode: 303, wantErr: "access_denied", err: oidc.ErrAudience,
			result: func(id string) oidc.Result { return oidc.Result{Payload: payloadFor(id, cb, ch, auth.ScopeCoach)} }},
		{name: "nonce mismatch", wantCode: 303, wantErr: "access_denied", err: oidc.ErrNonce,
			result: func(id string) oidc.Result { return oidc.Result{Payload: payloadFor(id, cb, ch, auth.ScopeCoach)} }},
		{name: "expired token", wantCode: 303, wantErr: "access_denied", err: oidc.ErrExpired,
			result: func(id string) oidc.Result { return oidc.Result{Payload: payloadFor(id, cb, ch, auth.ScopeCoach)} }},
		{name: "missing player claim", wantCode: 303, wantErr: "access_denied", err: oidc.ErrPlayerClaim,
			result: func(id string) oidc.Result { return oidc.Result{Payload: payloadFor(id, cb, ch, auth.ScopeCoach)} }},
		// Fail closed: a flow that reports success but no player must not mint anything.
		{name: "success without a player", wantCode: 303, wantErr: "access_denied",
			result: func(id string) oidc.Result { return oidc.Result{Payload: payloadFor(id, cb, ch, auth.ScopeCoach)} }},
		// No trustworthy session: CSRF, a replayed callback, a state mismatch, an expired cookie.
		{name: "no session (CSRF)", wantCode: 400, err: oidc.ErrSession, result: func(string) oidc.Result { return oidc.Result{} }},
		{name: "state mismatch", wantCode: 400, err: oidc.ErrState, result: func(string) oidc.Result { return oidc.Result{} }},
		{name: "replayed callback", wantCode: 400, err: oidc.ErrReplay, result: func(string) oidc.Result { return oidc.Result{} }},
		// The session is authentic but its request no longer validates: never redirect to it.
		{name: "redirect uri no longer registered (open redirect)", wantCode: 400,
			result: func(id string) oidc.Result {
				return oidc.Result{Player: "p-42", Payload: payloadFor(id, "https://evil.example/cb", ch, auth.ScopeCoach)}
			}},
		{name: "unknown client in the session", wantCode: 400,
			result: func(string) oidc.Result {
				return oidc.Result{Player: "p-42", Payload: payloadFor("nope", cb, ch, auth.ScopeCoach)}
			}},
		{name: "operator under a coach cap", max: auth.ScopeCoach, wantCode: 303, wantErr: "invalid_scope",
			result: func(id string) oidc.Result {
				return oidc.Result{Player: "p-42", Payload: payloadFor(id, cb, ch, auth.ScopeOperator)}
			}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			l := &fakeLogin{completeE: tt.err}
			e := oidcEnv(t, l, tt.max)
			id := e.client("A", cb)
			l.result = tt.result(id)
			w := e.oidcCallback()
			if w.Code != tt.wantCode {
				t.Fatalf("status %d, want %d: %s", w.Code, tt.wantCode, w.Body)
			}
			gs, _ := e.store.Grants(context.Background(), "t1", "p-42")
			if (len(gs) == 1) != tt.wantGrant {
				t.Fatalf("grants = %d, wantGrant %v", len(gs), tt.wantGrant)
			}
			if tt.wantCode == 400 {
				if w.Header().Get("Location") != "" {
					t.Fatalf("redirected on an untrusted callback: %s", w.Header().Get("Location"))
				}
				return
			}
			u := location(t, w)
			if !strings.HasPrefix(u.String(), cb) {
				t.Fatalf("redirect went to %s, not the registered %s", u, cb)
			}
			q := u.Query()
			if q.Get("state") != "st8" || q.Get("iss") != testIssuer {
				t.Errorf("state or iss missing: %v", q)
			}
			if got := q.Get("error"); got != tt.wantErr {
				t.Fatalf("error = %q, want %q", got, tt.wantErr)
			}
			if tt.wantErr != "" && q.Get("code") != "" {
				t.Fatal("a failed sign-in produced a code")
			}
		})
	}
}

// A sign-in through the identity provider ends in the same place as a link code: a grant for the
// mapped player and a code the client can exchange (with PKCE) for tokens.
func TestOIDCSignInYieldsAWorkingGrant(t *testing.T) {
	l := &fakeLogin{}
	e := oidcEnv(t, l, "")
	id := e.client("A", cb)
	verifier, ch := pkce()
	l.result = oidc.Result{Player: "p-42", Payload: payloadFor(id, cb, ch, auth.ScopeCoach)}
	code := location(t, e.oidcCallback()).Query().Get("code")
	if code == "" {
		t.Fatal("no code")
	}
	m := e.exchange(id, cb, code, verifier)
	if m["access_token"] == "" || m["scope"] != "coach" {
		t.Fatalf("token response = %v", m)
	}
	gs, _ := e.store.Grants(context.Background(), "t1", "p-42")
	if len(gs) != 1 || gs[0].Scope != auth.ScopeCoach || gs[0].Resource != testResource {
		t.Fatalf("grants = %+v", gs)
	}
}

func TestOIDCCallbackIsRateLimited(t *testing.T) {
	l := &fakeLogin{completeE: oidc.ErrSession}
	e := oidcEnv(t, l, "")
	var last int
	for i := 0; i < 12; i++ {
		last = e.oidcCallback().Code
	}
	if last != http.StatusTooManyRequests {
		t.Fatalf("12th callback = %d, want 429", last)
	}
	if l.completed > 10 {
		t.Fatalf("Complete ran %d times, the limiter should stop it at 10", l.completed)
	}
}
