package oidc_test

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Moustafa-Elgammal/agentgw/internal/auth/oidc"
)

const (
	clientID = "gw-client"
	secret32 = "0123456789abcdef0123456789abcdef" //nolint:gosec // a fixed test key
)

var epoch = time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)

// fakeIdP serves a discovery document and a token endpoint, and records what the flow sent.
type fakeIdP struct {
	srv *httptest.Server

	mu          sync.Mutex
	issuer      string // what the discovery document claims
	tokenStatus int
	tokenBody   string
	form        url.Values
	authHeader  string
	tokenCalls  int
}

func newIdP(t *testing.T) *fakeIdP {
	t.Helper()
	f := &fakeIdP{tokenStatus: 200, tokenBody: `{"id_token":"raw.jwt.value"}`} //nolint:gosec // a fake token, not a credential
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		iss := f.issuer
		if iss == "" {
			iss = f.srv.URL
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issuer": iss, "authorization_endpoint": f.srv.URL + "/authorize", "token_endpoint": f.srv.URL + "/token",
			"code_challenge_methods_supported": []string{"S256"},
		})
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		f.mu.Lock()
		f.form, f.authHeader = r.PostForm, r.Header.Get("Authorization")
		f.tokenCalls++
		status, body := f.tokenStatus, f.tokenBody
		f.mu.Unlock()
		if status >= 300 && status < 400 {
			http.Redirect(w, r, "http://127.0.0.1:1/elsewhere", status)
			return
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

// verifier is a fake Verifier. By default it vouches for a clean token for player "p-42" and
// echoes the nonce it was given; tests change claims through mutate.
type verifier struct {
	issuer string
	mutate func(*oidc.Claims)
	err    error
	gotRaw string
	gotNon string
}

func (v *verifier) Verify(_ context.Context, raw, nonce string) (oidc.Claims, error) {
	v.gotRaw, v.gotNon = raw, nonce
	if v.err != nil {
		return oidc.Claims{}, v.err
	}
	c := oidc.Claims{
		Issuer: v.issuer, Subject: "p-42", Audience: []string{clientID}, Nonce: nonce,
		Expiry: epoch.Add(5 * time.Minute), IssuedAt: epoch.Add(-time.Minute),
		Raw: map[string]any{"sub": "p-42", "player_id": "pid-7"},
	}
	if v.mutate != nil {
		v.mutate(&c)
	}
	return c, nil
}

type rig struct {
	t    *testing.T
	idp  *fakeIdP
	v    *verifier
	flow *oidc.Flow
	now  time.Time
}

func newRig(t *testing.T, edit func(*oidc.Config)) *rig {
	t.Helper()
	r := &rig{t: t, idp: newIdP(t), now: epoch}
	r.v = &verifier{issuer: r.idp.srv.URL}
	cfg := oidc.Config{
		Issuer: r.idp.srv.URL, ClientID: clientID, ClientSecret: "s3cret",
		RedirectURL: "http://127.0.0.1:8080/authorize/oidc/callback", SessionSecret: []byte(secret32),
		AllowInsecureHTTP: true,
	}
	if edit != nil {
		edit(&cfg)
	}
	f, err := oidc.NewFlow(cfg, r.v, nil, func() time.Time { return r.now })
	if err != nil {
		t.Fatal(err)
	}
	r.flow = f
	return r
}

var payload = map[string]string{"client_id": "c1", "redirect_uri": "http://127.0.0.1:5555/cb", "state": "client-state", "scope_choice": "coach"}

// begin starts a sign-in and returns the IdP URL and the session cookie.
func (r *rig) begin() (*url.URL, *http.Cookie) {
	r.t.Helper()
	w := httptest.NewRecorder()
	loc, err := r.flow.Begin(context.Background(), w, payload)
	if err != nil {
		r.t.Fatal(err)
	}
	u, _ := url.Parse(loc)
	cs := w.Result().Cookies()
	if len(cs) != 1 {
		r.t.Fatalf("want one cookie, got %v", cs)
	}
	return u, cs[0]
}

// callback plays the IdP's redirect back to the gateway.
func (r *rig) callback(cookie *http.Cookie, q url.Values) (oidc.Result, *httptest.ResponseRecorder, error) {
	r.t.Helper()
	req := httptest.NewRequest("GET", "/authorize/oidc/callback?"+q.Encode(), nil)
	if cookie != nil {
		req.AddCookie(cookie)
	}
	w := httptest.NewRecorder()
	res, err := r.flow.Complete(context.Background(), w, req)
	return res, w, err
}

func TestHappyPath(t *testing.T) {
	r := newRig(t, nil)
	u, cookie := r.begin()
	q := u.Query()
	for k, want := range map[string]string{
		"response_type": "code", "client_id": clientID, "code_challenge_method": "S256",
		"redirect_uri": "http://127.0.0.1:8080/authorize/oidc/callback", "scope": "openid",
	} {
		if q.Get(k) != want {
			t.Errorf("%s = %q, want %q", k, q.Get(k), want)
		}
	}
	if q.Get("state") == "" || q.Get("nonce") == "" || q.Get("code_challenge") == "" {
		t.Fatalf("missing state, nonce or challenge: %v", q)
	}
	if !strings.HasPrefix(u.String(), r.idp.srv.URL+"/authorize?") {
		t.Fatalf("redirect goes to %s", u)
	}
	if cookie.HttpOnly != true || cookie.SameSite != http.SameSiteLaxMode || cookie.Path != "/authorize/oidc/callback" {
		t.Errorf("cookie attributes: %+v", cookie)
	}

	res, w, err := r.callback(cookie, url.Values{"state": {q.Get("state")}, "code": {"the-code"}, "iss": {r.idp.srv.URL}})
	if err != nil {
		t.Fatal(err)
	}
	if res.Player != "p-42" || res.Payload["client_id"] != "c1" || res.Payload["state"] != "client-state" {
		t.Fatalf("result = %+v", res)
	}
	// The code exchange proves possession of the PKCE verifier and sends the client secret.
	form := r.idp.form
	sum := sha256.Sum256([]byte(form.Get("code_verifier")))
	if got := base64.RawURLEncoding.EncodeToString(sum[:]); got != q.Get("code_challenge") {
		t.Errorf("code_verifier does not match the challenge")
	}
	if form.Get("code") != "the-code" || form.Get("grant_type") != "authorization_code" ||
		form.Get("redirect_uri") != q.Get("redirect_uri") {
		t.Errorf("token request = %v", form)
	}
	if !strings.HasPrefix(r.idp.authHeader, "Basic ") {
		t.Errorf("client secret not sent as Basic auth: %q", r.idp.authHeader)
	}
	if r.v.gotRaw != "raw.jwt.value" || r.v.gotNon != q.Get("nonce") {
		t.Errorf("verifier saw %q with nonce %q", r.v.gotRaw, r.v.gotNon)
	}
	// The session cookie is cleared by the callback.
	cleared := false
	for _, c := range w.Result().Cookies() {
		cleared = cleared || (c.Name == cookie.Name && c.MaxAge < 0)
	}
	if !cleared {
		t.Error("session cookie was not cleared")
	}
}

func TestPublicClientSendsNoSecret(t *testing.T) {
	r := newRig(t, func(c *oidc.Config) { c.ClientSecret = "" })
	u, cookie := r.begin()
	if _, _, err := r.callback(cookie, url.Values{"state": {u.Query().Get("state")}, "code": {"c"}}); err != nil {
		t.Fatal(err)
	}
	if r.idp.authHeader != "" {
		t.Errorf("sent credentials for a public client: %q", r.idp.authHeader)
	}
}

func withValue(c *http.Cookie, v string) *http.Cookie { //nolint:gosec // a test double for a tampered cookie
	c.Value = v
	return c
}

func TestCompleteFailures(t *testing.T) {
	tests := []struct {
		name        string
		setup       func(r *rig)
		cookie      func(c *http.Cookie) *http.Cookie
		query       func(state string) url.Values
		want        error
		wantPayload bool // the caller may send the player back to the client
	}{
		{name: "no cookie", cookie: func(*http.Cookie) *http.Cookie { return nil }, want: oidc.ErrSession},
		{name: "tampered cookie", cookie: func(c *http.Cookie) *http.Cookie {
			return withValue(c, c.Value[:len(c.Value)-3]+"AAA")
		}, want: oidc.ErrSession},
		{name: "garbage cookie", cookie: func(c *http.Cookie) *http.Cookie { return withValue(c, "not-base64!!") }, want: oidc.ErrSession},
		{name: "huge cookie", cookie: func(c *http.Cookie) *http.Cookie { return withValue(c, strings.Repeat("A", 20000)) }, want: oidc.ErrSession},
		{name: "state mismatch", query: func(string) url.Values { return url.Values{"state": {"forged"}, "code": {"c"}} }, want: oidc.ErrState},
		{name: "missing state", query: func(string) url.Values { return url.Values{"code": {"c"}} }, want: oidc.ErrState},
		{name: "session expired", setup: func(r *rig) { r.now = r.now.Add(11 * time.Minute) }, want: oidc.ErrSession},
		{
			name: "provider refused", wantPayload: true, want: oidc.ErrDenied,
			query: func(s string) url.Values { return url.Values{"state": {s}, "error": {"access_denied"}} },
		},
		{
			name: "wrong issuer in the response", wantPayload: true, want: oidc.ErrIssuer,
			query: func(s string) url.Values {
				return url.Values{"state": {s}, "code": {"c"}, "iss": {"https://evil.example"}}
			},
		},
		{name: "no code", wantPayload: true, want: oidc.ErrExchange, query: func(s string) url.Values { return url.Values{"state": {s}} }},
		{name: "token endpoint fails", wantPayload: true, want: oidc.ErrExchange, setup: func(r *rig) { r.idp.tokenStatus = 500 }},
		{name: "token endpoint redirects", wantPayload: true, want: oidc.ErrExchange, setup: func(r *rig) { r.idp.tokenStatus = 302 }},
		{name: "no id_token", wantPayload: true, want: oidc.ErrExchange, setup: func(r *rig) { r.idp.tokenBody = `{"access_token":"x"}` }},
		{name: "verifier rejects the token", wantPayload: true, want: oidc.ErrToken, setup: func(r *rig) { r.v.err = errors.New("bad signature") }},
		{name: "wrong audience", wantPayload: true, want: oidc.ErrAudience, setup: func(r *rig) {
			r.v.mutate = func(c *oidc.Claims) { c.Audience = []string{"someone-else"} }
		}},
		{name: "nonce mismatch", wantPayload: true, want: oidc.ErrNonce, setup: func(r *rig) {
			r.v.mutate = func(c *oidc.Claims) { c.Nonce = "replayed-from-another-login" }
		}},
		{name: "token expired", wantPayload: true, want: oidc.ErrExpired, setup: func(r *rig) {
			r.v.mutate = func(c *oidc.Claims) { c.Expiry = epoch.Add(-5 * time.Minute) }
		}},
		{name: "wrong issuer in the token", wantPayload: true, want: oidc.ErrIssuer, setup: func(r *rig) {
			r.v.mutate = func(c *oidc.Claims) { c.Issuer = "https://evil.example" }
		}},
		{name: "player claim missing", wantPayload: true, want: oidc.ErrPlayerClaim, setup: func(r *rig) {
			r.v.mutate = func(c *oidc.Claims) { delete(c.Raw, "sub"); c.Subject = "" }
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newRig(t, nil)
			u, cookie := r.begin()
			if tt.setup != nil {
				tt.setup(r)
			}
			if tt.cookie != nil {
				cookie = tt.cookie(cookie)
			}
			q := url.Values{"state": {u.Query().Get("state")}, "code": {"c"}}
			if tt.query != nil {
				q = tt.query(u.Query().Get("state"))
			}
			res, _, err := r.callback(cookie, q)
			if !errors.Is(err, tt.want) {
				t.Fatalf("err = %v, want %v", err, tt.want)
			}
			if res.Player != "" {
				t.Errorf("a failed sign-in produced player %q", res.Player)
			}
			if (res.Payload != nil) != tt.wantPayload {
				t.Errorf("payload present = %v, want %v", res.Payload != nil, tt.wantPayload)
			}
		})
	}
}

// A callback that was already used is refused even though its cookie still opens.
func TestReplayedCallback(t *testing.T) {
	r := newRig(t, nil)
	u, cookie := r.begin()
	q := url.Values{"state": {u.Query().Get("state")}, "code": {"c"}}
	if _, _, err := r.callback(cookie, q); err != nil {
		t.Fatal(err)
	}
	if r.idp.tokenCalls != 1 {
		t.Fatalf("token endpoint called %d times", r.idp.tokenCalls)
	}
	res, _, err := r.callback(cookie, q)
	if !errors.Is(err, oidc.ErrReplay) || res.Payload != nil || res.Player != "" {
		t.Fatalf("replay: res %+v err %v", res, err)
	}
	if r.idp.tokenCalls != 1 {
		t.Fatal("a replayed callback reached the token endpoint")
	}
}

// Two sign-ins in flight do not mix: a session answers only its own state.
func TestSessionsAreIndependent(t *testing.T) {
	r := newRig(t, nil)
	u1, c1 := r.begin()
	u2, c2 := r.begin()
	if _, _, err := r.callback(c1, url.Values{"state": {u2.Query().Get("state")}, "code": {"c"}}); !errors.Is(err, oidc.ErrState) {
		t.Fatalf("cross-session state = %v", err)
	}
	if _, _, err := r.callback(c2, url.Values{"state": {u2.Query().Get("state")}, "code": {"c"}}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := r.callback(c1, url.Values{"state": {u1.Query().Get("state")}, "code": {"c"}}); err != nil {
		t.Fatalf("first session should still work: %v", err)
	}
}

// A cookie sealed with another secret is useless (another deployment, or a forged one).
func TestCookieFromAnotherSecret(t *testing.T) {
	a := newRig(t, nil)
	b := newRig(t, func(c *oidc.Config) { c.SessionSecret = []byte("ffffffffffffffffffffffffffffffff") })
	u, cookie := a.begin()
	if _, _, err := b.callback(cookie, url.Values{"state": {u.Query().Get("state")}, "code": {"c"}}); !errors.Is(err, oidc.ErrSession) {
		t.Fatalf("err = %v", err)
	}
}

func TestPlayerClaimConfig(t *testing.T) {
	r := newRig(t, func(c *oidc.Config) { c.PlayerClaim = "player_id" })
	u, cookie := r.begin()
	res, _, err := r.callback(cookie, url.Values{"state": {u.Query().Get("state")}, "code": {"c"}})
	if err != nil || res.Player != "pid-7" {
		t.Fatalf("player = %q, err %v", res.Player, err)
	}
}

func TestBegin(t *testing.T) {
	t.Run("payload limits", func(t *testing.T) {
		r := newRig(t, nil)
		for name, p := range map[string]map[string]string{
			"empty": nil, "too large": {"state": strings.Repeat("x", 5000)},
		} {
			if _, err := r.flow.Begin(context.Background(), httptest.NewRecorder(), p); err == nil {
				t.Errorf("%s payload accepted", name)
			}
		}
	})
	t.Run("scopes always include openid", func(t *testing.T) {
		r := newRig(t, func(c *oidc.Config) { c.Scopes = []string{"profile", "email"} })
		u, _ := r.begin()
		if got := u.Query().Get("scope"); got != "openid profile email" {
			t.Fatalf("scope = %q", got)
		}
	})
	t.Run("discovery issuer mismatch", func(t *testing.T) {
		r := newRig(t, nil)
		r.idp.issuer = "https://someone-else.example"
		_, err := r.flow.Begin(context.Background(), httptest.NewRecorder(), payload)
		if !errors.Is(err, oidc.ErrDiscovery) {
			t.Fatalf("err = %v", err)
		}
		if _, ok := r.flow.AuthOrigin(context.Background()); ok {
			t.Error("AuthOrigin ok after failed discovery")
		}
	})
	t.Run("auth origin", func(t *testing.T) {
		r := newRig(t, nil)
		got, ok := r.flow.AuthOrigin(context.Background())
		if !ok || got != r.idp.srv.URL {
			t.Fatalf("origin = %q %v, want %q", got, ok, r.idp.srv.URL)
		}
	})
	t.Run("explicit endpoints skip discovery", func(t *testing.T) {
		r := newRig(t, func(c *oidc.Config) {
			c.AuthURL, c.TokenURL = "http://127.0.0.1:1/a", "http://127.0.0.1:1/t"
		})
		u, _ := r.begin()
		if u.Host != "127.0.0.1:1" {
			t.Fatalf("redirect = %s", u)
		}
	})
}

func TestNewFlowRejectsBadConfig(t *testing.T) {
	base := oidc.Config{
		Issuer: "https://idp.example", ClientID: "c", RedirectURL: "https://gw.example/authorize/oidc/callback",
		SessionSecret: []byte(secret32),
	}
	tests := []struct {
		name string
		edit func(*oidc.Config)
	}{
		{"no client id", func(c *oidc.Config) { c.ClientID = "" }},
		{"short session secret", func(c *oidc.Config) { c.SessionSecret = []byte("short") }},
		{"http issuer", func(c *oidc.Config) { c.Issuer = "http://idp.example" }},
		{"http callback", func(c *oidc.Config) { c.RedirectURL = "http://gw.example/cb" }},
		{"insecure allowed but not loopback", func(c *oidc.Config) { c.AllowInsecureHTTP = true; c.Issuer = "http://idp.example" }},
		{"credentials in issuer", func(c *oidc.Config) { c.Issuer = "https://u:p@idp.example" }},
		{"relative issuer", func(c *oidc.Config) { c.Issuer = "/idp" }},
		{"auth url without token url", func(c *oidc.Config) { c.AuthURL = "https://idp.example/a" }},
		{"http token url", func(c *oidc.Config) { c.AuthURL, c.TokenURL = "https://idp.example/a", "http://idp.example/t" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := base
			tt.edit(&c)
			if _, err := oidc.NewFlow(c, &verifier{}, nil, nil); err == nil {
				t.Fatal("accepted")
			}
		})
	}
	if _, err := oidc.NewFlow(base, nil, nil, nil); err == nil {
		t.Error("accepted a nil Verifier")
	}
	if _, err := oidc.NewFlow(base, &verifier{}, nil, nil); err != nil {
		t.Errorf("good config refused: %v", err)
	}
}

func TestSecureCookieNameOverHTTPS(t *testing.T) {
	f, err := oidc.NewFlow(oidc.Config{ //nolint:gosec // a fixed test key
		Issuer: "https://idp.example", ClientID: "c", RedirectURL: "https://gw.example/authorize/oidc/callback",
		SessionSecret: []byte(secret32), AuthURL: "https://idp.example/a", TokenURL: "https://idp.example/t",
	}, &verifier{}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	if _, err := f.Begin(context.Background(), w, payload); err != nil {
		t.Fatal(err)
	}
	c := w.Result().Cookies()[0]
	if c.Name != "__Secure-agw_oidc" || !c.Secure {
		t.Fatalf("cookie = %+v", c)
	}
}

func TestCheckClaims(t *testing.T) {
	cfg := oidc.Config{Issuer: "https://idp.example", ClientID: "me"}
	good := func() oidc.Claims {
		return oidc.Claims{
			Issuer: cfg.Issuer, Audience: []string{"me"}, Nonce: "n1",
			Expiry: epoch.Add(5 * time.Minute), IssuedAt: epoch.Add(-time.Minute),
		}
	}
	tests := []struct {
		name string
		edit func(*oidc.Claims)
		want error
	}{
		{"good", func(*oidc.Claims) {}, nil},
		{"no iat is fine", func(c *oidc.Claims) { c.IssuedAt = time.Time{} }, nil},
		{"several audiences with azp", func(c *oidc.Claims) { c.Audience = []string{"me", "x"}; c.AuthorizedParty = "me" }, nil},
		{"several audiences without azp", func(c *oidc.Claims) { c.Audience = []string{"me", "x"} }, oidc.ErrAudience},
		{"azp is someone else", func(c *oidc.Claims) { c.AuthorizedParty = "x" }, oidc.ErrAudience},
		{"not our audience", func(c *oidc.Claims) { c.Audience = []string{"x"} }, oidc.ErrAudience},
		{"no audience", func(c *oidc.Claims) { c.Audience = nil }, oidc.ErrAudience},
		{"issuer differs by a slash", func(c *oidc.Claims) { c.Issuer += "/" }, oidc.ErrIssuer},
		{"no issuer", func(c *oidc.Claims) { c.Issuer = "" }, oidc.ErrIssuer},
		{"nonce differs", func(c *oidc.Claims) { c.Nonce = "n2" }, oidc.ErrNonce},
		{"nonce missing", func(c *oidc.Claims) { c.Nonce = "" }, oidc.ErrNonce},
		{"expired beyond skew", func(c *oidc.Claims) { c.Expiry = epoch.Add(-2 * time.Minute) }, oidc.ErrExpired},
		{"expired within skew", func(c *oidc.Claims) { c.Expiry = epoch.Add(-30 * time.Second) }, nil},
		{"no exp", func(c *oidc.Claims) { c.Expiry = time.Time{} }, oidc.ErrExpired},
		{"not yet valid", func(c *oidc.Claims) { c.NotBefore = epoch.Add(10 * time.Minute) }, oidc.ErrExpired},
		{"nbf within skew", func(c *oidc.Claims) { c.NotBefore = epoch.Add(30 * time.Second) }, nil},
		{"issued in the future", func(c *oidc.Claims) { c.IssuedAt = epoch.Add(10 * time.Minute) }, oidc.ErrExpired},
		{"issued too long ago", func(c *oidc.Claims) { c.IssuedAt = epoch.Add(-time.Hour) }, oidc.ErrExpired},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := good()
			tt.edit(&c)
			if err := oidc.CheckClaims(c, cfg, "n1", epoch); (tt.want == nil && err != nil) || (tt.want != nil && !errors.Is(err, tt.want)) {
				t.Fatalf("err = %v, want %v", err, tt.want)
			}
		})
	}
	if err := oidc.CheckClaims(good(), cfg, "", epoch); !errors.Is(err, oidc.ErrNonce) {
		t.Errorf("empty expected nonce accepted: %v", err)
	}
}

func TestPlayerID(t *testing.T) {
	tests := []struct {
		name  string
		claim string
		raw   map[string]any
		sub   string
		want  string
		err   bool
	}{
		{name: "default is sub from the subject field", sub: "abc", want: "abc"},
		{name: "default is sub from raw", raw: map[string]any{"sub": "raw-sub"}, want: "raw-sub"},
		{name: "custom string claim", claim: "pid", raw: map[string]any{"pid": "p-1"}, want: "p-1"},
		{name: "integral number", claim: "pid", raw: map[string]any{"pid": float64(42)}, want: "42"},
		{name: "json number", claim: "pid", raw: map[string]any{"pid": json.Number("7")}, want: "7"},
		{name: "fraction", claim: "pid", raw: map[string]any{"pid": 1.5}, err: true},
		{name: "missing", claim: "pid", raw: map[string]any{}, err: true},
		{name: "empty", claim: "pid", raw: map[string]any{"pid": ""}, err: true},
		{name: "space", claim: "pid", raw: map[string]any{"pid": "a b"}, err: true},
		{name: "crlf", claim: "pid", raw: map[string]any{"pid": "a\r\nX-Evil: 1"}, err: true},
		{name: "non ascii", claim: "pid", raw: map[string]any{"pid": "josé"}, err: true},
		{name: "nul", claim: "pid", raw: map[string]any{"pid": "a\x00b"}, err: true},
		{name: "too long", claim: "pid", raw: map[string]any{"pid": strings.Repeat("a", 129)}, err: true},
		{name: "longest allowed", claim: "pid", raw: map[string]any{"pid": strings.Repeat("a", 128)}, want: strings.Repeat("a", 128)},
		{name: "array", claim: "pid", raw: map[string]any{"pid": []any{"a"}}, err: true},
		{name: "bool", claim: "pid", raw: map[string]any{"pid": true}, err: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := oidc.PlayerID(oidc.Claims{Subject: tt.sub, Raw: tt.raw}, tt.claim)
			if (err != nil) != tt.err || got != tt.want {
				t.Fatalf("got %q, %v; want %q, err=%v", got, err, tt.want, tt.err)
			}
		})
	}
}
