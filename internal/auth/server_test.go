package auth_test

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Moustafa-Elgammal/agentgw/internal/auth"
	"github.com/Moustafa-Elgammal/agentgw/pkg/agentsig"
)

const (
	testIssuer   = "https://gw.example"
	testResource = "https://gw.example/mcp"
	studioKey    = "studio-1"
	cb           = "https://app.example/cb"
)

var studioSecret = []byte("studio-secret")

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) add(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

type env struct {
	t     *testing.T
	h     http.Handler
	store *auth.Memory
	clk   *clock
}

func newEnv(t *testing.T) *env {
	t.Helper()
	clk := &clock{t: time.Now()}
	st := auth.NewMemory(clk.now)
	s := auth.NewServer(auth.ServerConfig{
		Store: st, Issuer: testIssuer, Resource: testResource, Tenant: "t1",
		StudioKeyID: studioKey, StudioSecret: studioSecret, Now: clk.now,
	})
	return &env{t, s.Handler(), st, clk}
}

func (e *env) do(r *http.Request) *httptest.ResponseRecorder {
	r.RemoteAddr = "198.51.100.1:5555"
	w := httptest.NewRecorder()
	e.h.ServeHTTP(w, r)
	return w
}

func (e *env) register(body string) (*httptest.ResponseRecorder, map[string]any) {
	r := httptest.NewRequest("POST", "/register", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	w := e.do(r)
	var m map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &m)
	return w, m
}

func (e *env) client(name string, uris ...string) string {
	e.t.Helper()
	b, _ := json.Marshal(map[string]any{"client_name": name, "redirect_uris": uris})
	w, m := e.register(string(b))
	if w.Code != 201 {
		e.t.Fatalf("register: %d %s", w.Code, w.Body)
	}
	return m["client_id"].(string)
}

func (e *env) ticket(player string) string {
	e.t.Helper()
	return e.ticketReq(player, "idem-"+auth.NewID(), 201)
}

func (e *env) ticketReq(player, idem string, want int) string {
	e.t.Helper()
	r := httptest.NewRequest("POST", "/v1/studio/link-tickets", nil)
	agentsig.Sign(r, studioKey, studioSecret, player, idem, nil, e.clk.now())
	w := e.do(r)
	if w.Code != want {
		e.t.Fatalf("ticket: %d %s, want %d", w.Code, w.Body, want)
	}
	var m map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &m)
	s, _ := m["ticket"].(string)
	return s
}

func pkce() (verifier, challenge string) {
	verifier = strings.Repeat("v", 20) + auth.NewID() // 52 chars
	sum := sha256.Sum256([]byte(verifier))
	return verifier, base64.RawURLEncoding.EncodeToString(sum[:])
}

func authzParams(clientID, redirect, challenge string) url.Values {
	return url.Values{
		"client_id": {clientID}, "redirect_uri": {redirect}, "response_type": {"code"},
		"code_challenge": {challenge}, "code_challenge_method": {"S256"},
		"state": {"st8"}, "resource": {testResource},
	}
}

func (e *env) authzGet(v url.Values) *httptest.ResponseRecorder {
	return e.do(httptest.NewRequest("GET", "/authorize?"+v.Encode(), nil))
}

func (e *env) authzPost(v url.Values) *httptest.ResponseRecorder {
	r := httptest.NewRequest("POST", "/authorize", strings.NewReader(v.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return e.do(r)
}

func (e *env) consent(v url.Values, ticket string, scope auth.Scope) *httptest.ResponseRecorder {
	v = cloneValues(v)
	v.Set("ticket", ticket)
	v.Set("scope_choice", string(scope))
	v.Set("decision", "allow")
	return e.authzPost(v)
}

func cloneValues(v url.Values) url.Values {
	o := url.Values{}
	for k, x := range v {
		o[k] = append([]string(nil), x...)
	}
	return o
}

func location(t *testing.T, w *httptest.ResponseRecorder) *url.URL {
	t.Helper()
	if w.Code != http.StatusSeeOther {
		t.Fatalf("status %d, want 303: %s", w.Code, w.Body)
	}
	u, err := url.Parse(w.Header().Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func (e *env) tokenPost(v url.Values) (*httptest.ResponseRecorder, map[string]any) {
	r := httptest.NewRequest("POST", "/token", strings.NewReader(v.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := e.do(r)
	var m map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &m)
	return w, m
}

// obtainCode runs consent and returns the code and the PKCE verifier.
func (e *env) obtainCode(clientID, redirect, player string, scope auth.Scope) (code, verifier string) {
	e.t.Helper()
	verifier, ch := pkce()
	u := location(e.t, e.consent(authzParams(clientID, redirect, ch), e.ticket(player), scope))
	code = u.Query().Get("code")
	if code == "" {
		e.t.Fatalf("no code in %s", u)
	}
	return code, verifier
}

func (e *env) exchange(clientID, redirect, code, verifier string) map[string]any {
	e.t.Helper()
	w, m := e.tokenPost(url.Values{"grant_type": {"authorization_code"}, "client_id": {clientID},
		"redirect_uri": {redirect}, "code": {code}, "code_verifier": {verifier}, "resource": {testResource}})
	if w.Code != 200 {
		e.t.Fatalf("exchange: %d %s", w.Code, w.Body)
	}
	return m
}

func TestRegistration(t *testing.T) {
	many := make([]string, 11)
	for i := range many {
		many[i] = "https://app.example/cb" + string(rune('a'+i))
	}
	manyJSON, _ := json.Marshal(many)
	tests := []struct {
		name string
		body string
		want int
	}{
		{"https ok", `{"client_name":"A","redirect_uris":["https://app.example/cb"]}`, 201},
		{"loopback v4 ok", `{"client_name":"A","redirect_uris":["http://127.0.0.1:7000/cb"]}`, 201},
		{"loopback v6 ok", `{"client_name":"A","redirect_uris":["http://[::1]/cb"]}`, 201},
		{"http non loopback", `{"client_name":"A","redirect_uris":["http://app.example/cb"]}`, 400},
		{"localhost name rejected", `{"client_name":"A","redirect_uris":["http://localhost/cb"]}`, 400},
		{"fragment", `{"client_name":"A","redirect_uris":["https://app.example/cb#x"]}`, 400},
		{"userinfo", `{"client_name":"A","redirect_uris":["https://u:p@app.example/cb"]}`, 400},
		{"relative", `{"client_name":"A","redirect_uris":["/cb"]}`, 400},
		{"custom scheme", `{"client_name":"A","redirect_uris":["myapp://cb"]}`, 400},
		{"javascript scheme", `{"client_name":"A","redirect_uris":["javascript://x/%0aalert(1)"]}`, 400},
		{"none", `{"client_name":"A","redirect_uris":[]}`, 400},
		{"too many", `{"client_name":"A","redirect_uris":` + string(manyJSON) + `}`, 400},
		{"no name", `{"redirect_uris":["https://app.example/cb"]}`, 400},
		{"name too long", `{"client_name":"` + strings.Repeat("a", 101) + `","redirect_uris":["https://app.example/cb"]}`, 400},
		{"not json", `nope`, 400},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			w, m := newEnv(t).register(tc.body)
			if w.Code != tc.want {
				t.Fatalf("status %d, want %d: %s", w.Code, tc.want, w.Body)
			}
			if tc.want == 201 && (m["client_id"] == "" || m["token_endpoint_auth_method"] != "none") {
				t.Errorf("bad registration response %v", m)
			}
			if tc.want == 400 && m["error"] == nil {
				t.Errorf("want an RFC 7591 error body, got %s", w.Body)
			}
		})
	}
}

func TestRedirectMatching(t *testing.T) {
	tests := []struct {
		name       string
		registered string
		requested  string
		ok         bool
	}{
		{"exact https", "https://app.example/cb", "https://app.example/cb", true},
		{"different path", "https://app.example/cb", "https://app.example/other", false},
		{"https port is not flexible", "https://app.example/cb", "https://app.example:444/cb", false},
		{"https suffix host", "https://app.example/cb", "https://app.example.evil.test/cb", false},
		{"query added", "https://app.example/cb", "https://app.example/cb?x=1", false},
		{"loopback other port", "http://127.0.0.1/cb", "http://127.0.0.1:53211/cb", true},
		{"loopback v6 other port", "http://[::1]/cb", "http://[::1]:4000/cb", true},
		{"loopback wrong path", "http://127.0.0.1/cb", "http://127.0.0.1:4000/x", false},
		{"loopback other host", "http://127.0.0.1/cb", "http://127.0.0.2:4000/cb", false},
		{"loopback to external", "http://127.0.0.1/cb", "http://evil.test/cb", false},
		{"loopback userinfo trick", "http://127.0.0.1/cb", "http://127.0.0.1:80@evil.test/cb", false},
		{"loopback with userinfo", "http://127.0.0.1/cb", "http://u@127.0.0.1:80/cb", false},
		{"loopback to https", "http://127.0.0.1/cb", "https://127.0.0.1:4000/cb", false},
		{"https registered, loopback asked", "https://app.example/cb", "http://127.0.0.1:4000/cb", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			id := e.client("A", tc.registered)
			_, ch := pkce()
			w := e.authzGet(authzParams(id, tc.requested, ch))
			if tc.ok && w.Code != 200 {
				t.Fatalf("want the consent page, got %d %s", w.Code, w.Body)
			}
			if !tc.ok && (w.Code != 400 || w.Header().Get("Location") != "") {
				t.Fatalf("want a shown error and no redirect, got %d Location=%q", w.Code, w.Header().Get("Location"))
			}
		})
	}
}

func TestAuthorizeErrors(t *testing.T) {
	_, challenge := pkce()
	tests := []struct {
		name      string
		mutate    func(v url.Values)
		clientID  string // override
		wantShown bool   // shown to the user, never redirected
		wantErr   string
	}{
		{"unknown client", nil, "nope", true, ""},
		{"unregistered redirect", func(v url.Values) { v.Set("redirect_uri", "https://evil.test/cb") }, "", true, ""},
		{"missing redirect", func(v url.Values) { v.Del("redirect_uri") }, "", true, ""},
		{"bad response_type", func(v url.Values) { v.Set("response_type", "token") }, "", false, "unsupported_response_type"},
		{"no challenge", func(v url.Values) { v.Del("code_challenge"); v.Del("code_challenge_method") }, "", false, "invalid_request"},
		{"plain method", func(v url.Values) { v.Set("code_challenge_method", "plain") }, "", false, "invalid_request"},
		{"missing method", func(v url.Values) { v.Del("code_challenge_method") }, "", false, "invalid_request"},
		{"short challenge", func(v url.Values) { v.Set("code_challenge", "abc") }, "", false, "invalid_request"},
		{"challenge bad chars", func(v url.Values) { v.Set("code_challenge", strings.Repeat("!", 43)) }, "", false, "invalid_request"},
		{"missing resource", func(v url.Values) { v.Del("resource") }, "", false, "invalid_target"},
		{"other resource", func(v url.Values) { v.Set("resource", "https://other.example/mcp") }, "", false, "invalid_target"},
		{"resource prefix", func(v url.Values) { v.Set("resource", testResource+"/") }, "", false, "invalid_target"},
		{"unknown scope", func(v url.Values) { v.Set("scope", "admin") }, "", false, "invalid_scope"},
		{"long state", func(v url.Values) { v.Set("state", strings.Repeat("s", 600)) }, "", false, "invalid_request"},
	}
	for _, method := range []string{"GET", "POST"} {
		for _, tc := range tests {
			t.Run(method+" "+tc.name, func(t *testing.T) {
				e := newEnv(t)
				id := e.client("A", cb)
				if tc.clientID != "" {
					id = tc.clientID
				}
				v := authzParams(id, cb, challenge)
				if tc.mutate != nil {
					tc.mutate(v)
				}
				var w *httptest.ResponseRecorder
				if method == "GET" {
					w = e.authzGet(v)
				} else {
					v.Set("ticket", "AAAA-AAAA")
					v.Set("scope_choice", "coach")
					v.Set("decision", "allow")
					w = e.authzPost(v)
				}
				if tc.wantShown {
					if w.Code != 400 || w.Header().Get("Location") != "" {
						t.Fatalf("want a shown 400, got %d Location=%q", w.Code, w.Header().Get("Location"))
					}
					return
				}
				u := location(t, w)
				if u.Scheme+"://"+u.Host+u.Path != cb {
					t.Errorf("redirected to %s", u)
				}
				q := u.Query()
				if q.Get("error") != tc.wantErr {
					t.Errorf("error = %q, want %q", q.Get("error"), tc.wantErr)
				}
				if q.Get("iss") != testIssuer {
					t.Errorf("iss = %q", q.Get("iss"))
				}
				if tc.name != "long state" && q.Get("state") != "st8" {
					t.Errorf("state = %q", q.Get("state"))
				}
			})
		}
	}
}

func TestConsentPageScopes(t *testing.T) {
	e := newEnv(t)
	id := e.client("A", cb)
	_, ch := pkce()
	tests := []struct {
		name, scope string
		want, deny  []string
	}{
		{"default offers both", "", []string{`value="coach"`, `value="operator"`}, nil},
		{"coach only", "coach", []string{`value="coach"`}, []string{`value="operator"`}},
		{"operator only", "operator", []string{`value="operator"`}, []string{`value="coach"`}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			v := authzParams(id, cb, ch)
			if tc.scope != "" {
				v.Set("scope", tc.scope)
			}
			w := e.authzGet(v)
			if w.Code != 200 {
				t.Fatalf("status %d", w.Code)
			}
			body := w.Body.String()
			for _, s := range tc.want {
				if !strings.Contains(body, s) {
					t.Errorf("missing %s", s)
				}
			}
			for _, s := range tc.deny {
				if strings.Contains(body, s) {
					t.Errorf("must not offer %s", s)
				}
			}
		})
	}
}

func TestConsentPageHeadersAndEscaping(t *testing.T) {
	e := newEnv(t)
	hostile := `<script>alert(1)</script>"><img src=x onerror=alert(2)>`
	id := e.client(hostile, cb, "http://127.0.0.1/cb")
	_, ch := pkce()
	v := authzParams(id, cb, ch)
	v.Set("state", `"><script>alert(3)</script>`)
	w := e.authzGet(v)
	if w.Code != 200 {
		t.Fatalf("status %d", w.Code)
	}
	body := w.Body.String()
	for _, bad := range []string{"<script>alert", "<img src=x", `"><script`} {
		if strings.Contains(body, bad) {
			t.Errorf("unescaped content %q in page", bad)
		}
	}
	if !strings.Contains(body, "&lt;script&gt;alert(1)") {
		t.Errorf("client name should appear escaped")
	}
	h := w.Header()
	if h.Get("X-Frame-Options") != "DENY" {
		t.Errorf("X-Frame-Options = %q", h.Get("X-Frame-Options"))
	}
	csp := h.Get("Content-Security-Policy")
	for _, want := range []string{"frame-ancestors 'none'", "default-src 'none'", "form-action 'self' https://app.example", "base-uri 'none'"} {
		if !strings.Contains(csp, want) {
			t.Errorf("CSP %q lacks %q", csp, want)
		}
	}
	if strings.Contains(csp, "script-src") {
		t.Errorf("no scripts may be allowed: %q", csp)
	}
	for k, want := range map[string]string{"Cache-Control": "no-store", "X-Content-Type-Options": "nosniff", "Referrer-Policy": "no-referrer"} {
		if h.Get(k) != want {
			t.Errorf("%s = %q, want %q", k, h.Get(k), want)
		}
	}
	// A loopback redirect target allows any port in form-action.
	w = e.authzGet(authzParams(id, "http://127.0.0.1:9999/cb", ch))
	if !strings.Contains(w.Header().Get("Content-Security-Policy"), "form-action 'self' http://127.0.0.1:*") {
		t.Errorf("CSP %q", w.Header().Get("Content-Security-Policy"))
	}
}

func TestConsentSubmit(t *testing.T) {
	e := newEnv(t)
	id := e.client("A", cb)
	_, ch := pkce()
	base := authzParams(id, cb, ch)

	t.Run("deny redirects with access_denied", func(t *testing.T) {
		v := cloneValues(base)
		v.Set("decision", "deny")
		q := location(t, e.authzPost(v)).Query()
		if q.Get("error") != "access_denied" || q.Get("state") != "st8" || q.Get("iss") != testIssuer {
			t.Errorf("got %v", q)
		}
	})
	t.Run("missing decision is a denial", func(t *testing.T) {
		v := cloneValues(base)
		v.Set("ticket", e.ticket("p1"))
		v.Set("scope_choice", "coach")
		if q := location(t, e.authzPost(v)).Query(); q.Get("error") != "access_denied" || q.Get("code") != "" {
			t.Errorf("got %v", q)
		}
	})
	t.Run("scope outside the request is refused", func(t *testing.T) {
		v := cloneValues(base)
		v.Set("scope", "coach")
		w := e.consent(v, e.ticket("p1"), auth.ScopeOperator)
		if w.Code != 400 || w.Header().Get("Location") != "" {
			t.Fatalf("player must not escalate beyond the requested scope: %d", w.Code)
		}
	})
	t.Run("junk scope refused", func(t *testing.T) {
		if w := e.consent(base, e.ticket("p1"), "root"); w.Code != 400 {
			t.Fatalf("status %d", w.Code)
		}
	})
	t.Run("requested subset is granted", func(t *testing.T) {
		v := cloneValues(base)
		v.Set("scope", "coach")
		u := location(t, e.consent(v, e.ticket("p1"), auth.ScopeCoach))
		if u.Query().Get("code") == "" || u.Query().Get("state") != "st8" || u.Query().Get("iss") != testIssuer {
			t.Fatalf("got %s", u)
		}
	})
}

func TestTickets(t *testing.T) {
	e := newEnv(t)
	id := e.client("A", cb)
	_, ch := pkce()
	v := authzParams(id, cb, ch)

	t.Run("format and normalisation", func(t *testing.T) {
		tk := e.ticket("p1")
		if !regexp.MustCompile(`^[A-HJ-NP-Z2-9]{4}-[A-HJ-NP-Z2-9]{4}$`).MatchString(tk) {
			t.Fatalf("ticket %q", tk)
		}
		typed := " " + strings.ToLower(strings.ReplaceAll(tk, "-", " ")) + " "
		if q := location(t, e.consent(v, typed, auth.ScopeCoach)).Query(); q.Get("code") == "" {
			t.Fatalf("a sloppily typed ticket must work: %v", q)
		}
	})
	t.Run("single use", func(t *testing.T) {
		tk := e.ticket("p1")
		if q := location(t, e.consent(v, tk, auth.ScopeCoach)).Query(); q.Get("code") == "" {
			t.Fatal("first use must work")
		}
		if w := e.consent(v, tk, auth.ScopeCoach); w.Code != 401 || w.Header().Get("Location") != "" {
			t.Fatalf("reuse: %d", w.Code)
		}
	})
	t.Run("bad tickets look alike", func(t *testing.T) {
		var bodies []string
		for _, tk := range []string{"", "AAAA-AAAA", "nonsense", "0000-0000"} {
			w := e.consent(v, tk, auth.ScopeCoach)
			if w.Code != 401 {
				t.Fatalf("ticket %q: %d", tk, w.Code)
			}
			bodies = append(bodies, w.Body.String())
		}
		for _, b := range bodies[1:] {
			if b != bodies[0] {
				t.Error("error pages must not reveal why a ticket failed")
			}
		}
	})
	t.Run("expired", func(t *testing.T) {
		tk := e.ticket("p1")
		e.clk.add(auth.TicketTTL + time.Second)
		if w := e.consent(v, tk, auth.ScopeCoach); w.Code != 401 {
			t.Fatalf("status %d", w.Code)
		}
	})
}

func TestTicketGuessingIsRateLimited(t *testing.T) {
	e := newEnv(t)
	id := e.client("A", cb)
	_, ch := pkce()
	v := authzParams(id, cb, ch)
	var last *httptest.ResponseRecorder
	for i := 0; i < 11; i++ {
		last = e.consent(v, "AAAA-AAAA", auth.ScopeCoach)
		if i < 10 && last.Code != 401 {
			t.Fatalf("attempt %d: %d", i, last.Code)
		}
	}
	if last.Code != 429 || last.Header().Get("Retry-After") == "" {
		t.Fatalf("11th attempt: %d", last.Code)
	}
	// Even a valid ticket is refused while limited, then works after the window.
	tk := e.ticket("p1")
	if w := e.consent(v, tk, auth.ScopeCoach); w.Code != 429 {
		t.Fatalf("limited: %d", w.Code)
	}
	e.clk.add(61 * time.Second)
	if w := e.consent(v, tk, auth.ScopeCoach); w.Code != 303 {
		t.Fatalf("after the window: %d", w.Code)
	}
}

func TestStudioTicketEndpoint(t *testing.T) {
	e := newEnv(t)
	tests := []struct {
		name   string
		player string
		secret []byte
		keyID  string
		want   int
	}{
		{"valid", "p1", studioSecret, studioKey, 201},
		{"wrong secret", "p1", []byte("other"), studioKey, 401},
		{"unknown key", "p1", studioSecret, "nope", 401},
		{"no player", "", studioSecret, studioKey, 401},
		{"unsigned", "p1", nil, studioKey, 401},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest("POST", "/v1/studio/link-tickets", nil)
			if tc.secret != nil {
				agentsig.Sign(r, tc.keyID, tc.secret, tc.player, "i-"+auth.NewID(), nil, e.clk.now())
			}
			w := e.do(r)
			if w.Code != tc.want {
				t.Fatalf("status %d: %s", w.Code, w.Body)
			}
			if tc.want == 201 && w.Header().Get("Cache-Control") != "no-store" {
				t.Error("tickets must not be cached")
			}
		})
	}
	t.Run("stale timestamp", func(t *testing.T) {
		r := httptest.NewRequest("POST", "/v1/studio/link-tickets", nil)
		agentsig.Sign(r, studioKey, studioSecret, "p1", "stale", nil, e.clk.now().Add(-time.Hour))
		if w := e.do(r); w.Code != 401 {
			t.Fatalf("status %d", w.Code)
		}
	})
	t.Run("replayed request", func(t *testing.T) {
		e.ticketReq("p1", "same-idem", 201)
		e.ticketReq("p1", "same-idem", 401)
	})
	t.Run("tampered body", func(t *testing.T) {
		r := httptest.NewRequest("POST", "/v1/studio/link-tickets", strings.NewReader("x"))
		agentsig.Sign(r, studioKey, studioSecret, "p1", "tamper", []byte("y"), e.clk.now())
		if w := e.do(r); w.Code != 401 {
			t.Fatalf("status %d", w.Code)
		}
	})
}

func TestAuthorizationServerMetadata(t *testing.T) {
	w := newEnv(t).do(httptest.NewRequest("GET", "/.well-known/oauth-authorization-server", nil))
	var m map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &m); err != nil {
		t.Fatal(err)
	}
	if m["issuer"] != testIssuer || m["token_endpoint"] != testIssuer+"/token" ||
		m["registration_endpoint"] != testIssuer+"/register" || m["authorization_endpoint"] != testIssuer+"/authorize" {
		t.Errorf("%v", m)
	}
	if m["authorization_response_iss_parameter_supported"] != true {
		t.Error("must advertise iss support")
	}
	if cm, _ := m["code_challenge_methods_supported"].([]any); len(cm) != 1 || cm[0] != "S256" {
		t.Errorf("code_challenge_methods_supported = %v", m["code_challenge_methods_supported"])
	}
}

type tokenSetup struct {
	e                         *env
	id, other, code, verifier string
}

func (s tokenSetup) form() url.Values {
	return url.Values{"grant_type": {"authorization_code"}, "client_id": {s.id}, "redirect_uri": {cb},
		"code": {s.code}, "code_verifier": {s.verifier}, "resource": {testResource}}
}

func newTokenSetup(t *testing.T) tokenSetup {
	e := newEnv(t)
	id := e.client("A", cb)
	other := e.client("B", "https://b.example/cb")
	code, v := e.obtainCode(id, cb, "p1", auth.ScopeOperator)
	return tokenSetup{e, id, other, code, v}
}

func TestTokenEndpoint(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(s tokenSetup, f url.Values)
		want   string
	}{
		{"ok", func(tokenSetup, url.Values) {}, ""},
		{"ok without resource", func(s tokenSetup, f url.Values) { f.Del("resource") }, ""},
		{"wrong verifier", func(s tokenSetup, f url.Values) { v, _ := pkce(); f.Set("code_verifier", v) }, "invalid_grant"},
		{"missing verifier", func(s tokenSetup, f url.Values) { f.Del("code_verifier") }, "invalid_grant"},
		{"short verifier", func(s tokenSetup, f url.Values) { f.Set("code_verifier", "short") }, "invalid_grant"},
		{"wrong client", func(s tokenSetup, f url.Values) { f.Set("client_id", s.other) }, "invalid_grant"},
		{"missing client", func(s tokenSetup, f url.Values) { f.Del("client_id") }, "invalid_grant"},
		{"wrong redirect", func(s tokenSetup, f url.Values) { f.Set("redirect_uri", "https://app.example/other") }, "invalid_grant"},
		{"missing redirect", func(s tokenSetup, f url.Values) { f.Del("redirect_uri") }, "invalid_grant"},
		{"resource mismatch", func(s tokenSetup, f url.Values) { f.Set("resource", "https://other.example/mcp") }, "invalid_target"},
		{"unknown code", func(s tokenSetup, f url.Values) { f.Set("code", "bogus") }, "invalid_grant"},
		{"bad grant type", func(s tokenSetup, f url.Values) { f.Set("grant_type", "password") }, "unsupported_grant_type"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := newTokenSetup(t)
			f := s.form()
			tc.mutate(s, f)
			w, m := s.e.tokenPost(f)
			if tc.want == "" {
				if w.Code != 200 || m["access_token"] == "" || m["refresh_token"] == "" || m["token_type"] != "Bearer" || m["scope"] != "operator" {
					t.Fatalf("%d %s", w.Code, w.Body)
				}
				if w.Header().Get("Cache-Control") != "no-store" {
					t.Error("token responses must not be cached")
				}
				return
			}
			if w.Code != 400 || m["error"] != tc.want {
				t.Fatalf("%d %s, want %s", w.Code, w.Body, tc.want)
			}
			if m["access_token"] != nil {
				t.Error("no token on failure")
			}
		})
	}

	t.Run("a failed attempt burns the code", func(t *testing.T) {
		s := newTokenSetup(t)
		bad := s.form()
		bad.Set("code_verifier", strings.Repeat("x", 50))
		s.e.tokenPost(bad)
		if w, m := s.e.tokenPost(s.form()); w.Code != 400 || m["error"] != "invalid_grant" {
			t.Fatalf("%d %s", w.Code, w.Body)
		}
	})
	t.Run("replay", func(t *testing.T) {
		s := newTokenSetup(t)
		if w, _ := s.e.tokenPost(s.form()); w.Code != 200 {
			t.Fatal("first exchange must work")
		}
		if w, m := s.e.tokenPost(s.form()); w.Code != 400 || m["error"] != "invalid_grant" {
			t.Fatalf("%d %s", w.Code, w.Body)
		}
	})
	t.Run("expired code", func(t *testing.T) {
		s := newTokenSetup(t)
		s.e.clk.add(auth.CodeTTL + time.Second)
		if w, _ := s.e.tokenPost(s.form()); w.Code != 400 {
			t.Fatalf("%d", w.Code)
		}
	})
	t.Run("get is not allowed", func(t *testing.T) {
		if w := newEnv(t).do(httptest.NewRequest("GET", "/token", nil)); w.Code != 405 {
			t.Fatalf("%d", w.Code)
		}
	})
}

func TestRefresh(t *testing.T) {
	setup := func(t *testing.T) (*env, string, map[string]any) {
		e := newEnv(t)
		id := e.client("A", cb)
		code, v := e.obtainCode(id, cb, "p1", auth.ScopeCoach)
		return e, id, e.exchange(id, cb, code, v)
	}
	refresh := func(e *env, id, rt string) (*httptest.ResponseRecorder, map[string]any) {
		return e.tokenPost(url.Values{"grant_type": {"refresh_token"}, "client_id": {id}, "refresh_token": {rt}})
	}
	access := func(t *testing.T, e *env, tok any) error {
		_, err := e.store.Access(t.Context(), auth.HashToken(tok.(string)))
		return err
	}

	t.Run("rotation", func(t *testing.T) {
		e, id, first := setup(t)
		w, second := refresh(e, id, first["refresh_token"].(string))
		if w.Code != 200 || second["refresh_token"] == first["refresh_token"] || second["access_token"] == first["access_token"] || second["scope"] != "coach" {
			t.Fatalf("%d %s", w.Code, w.Body)
		}
		if err := access(t, e, second["access_token"]); err != nil {
			t.Errorf("new access token: %v", err)
		}
		if w, _ = refresh(e, id, second["refresh_token"].(string)); w.Code != 200 {
			t.Errorf("chained refresh: %d", w.Code)
		}
	})
	t.Run("reuse revokes the grant", func(t *testing.T) {
		e, id, first := setup(t)
		_, second := refresh(e, id, first["refresh_token"].(string))
		w, m := refresh(e, id, first["refresh_token"].(string))
		if w.Code != 400 || m["error"] != "invalid_grant" {
			t.Fatalf("%d %s", w.Code, w.Body)
		}
		for name, tok := range map[string]any{"first access": first["access_token"], "second access": second["access_token"]} {
			if access(t, e, tok) == nil {
				t.Errorf("%s must be revoked", name)
			}
		}
		if w, _ = refresh(e, id, second["refresh_token"].(string)); w.Code != 400 {
			t.Errorf("the rotated chain must be dead: %d", w.Code)
		}
	})
	t.Run("other client", func(t *testing.T) {
		e, id, first := setup(t)
		other := e.client("B", "https://b.example/cb")
		w, m := refresh(e, other, first["refresh_token"].(string))
		if w.Code != 400 || m["error"] != "invalid_grant" || m["access_token"] != nil {
			t.Fatalf("%d %s", w.Code, w.Body)
		}
		// The token is spent, so the owner's next use trips reuse detection (fail safe).
		if w, _ = refresh(e, id, first["refresh_token"].(string)); w.Code != 400 {
			t.Errorf("%d", w.Code)
		}
	})
	t.Run("unknown and missing", func(t *testing.T) {
		e, id, _ := setup(t)
		for _, rt := range []string{"", "bogus"} {
			if w, _ := refresh(e, id, rt); w.Code != 400 {
				t.Errorf("%q: %d", rt, w.Code)
			}
		}
	})
	t.Run("expired", func(t *testing.T) {
		e, id, first := setup(t)
		e.clk.add(auth.RefreshTTL + time.Second)
		if w, _ := refresh(e, id, first["refresh_token"].(string)); w.Code != 400 {
			t.Errorf("%d", w.Code)
		}
	})
	t.Run("revoked grant", func(t *testing.T) {
		e, id, first := setup(t)
		gs, _ := e.store.Grants(t.Context(), "t1", "p1")
		if err := e.store.RevokeGrant(t.Context(), "t1", "p1", gs[0].ID); err != nil {
			t.Fatal(err)
		}
		if w, _ := refresh(e, id, first["refresh_token"].(string)); w.Code != 400 {
			t.Errorf("%d", w.Code)
		}
		if access(t, e, first["access_token"]) == nil {
			t.Error("access token must die with the grant")
		}
	})
}
