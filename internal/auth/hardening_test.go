package auth_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Moustafa-Elgammal/agentgw/internal/auth"
	"github.com/Moustafa-Elgammal/agentgw/pkg/agentsig"
)

func newHardEnv(t *testing.T, mod func(*auth.ServerConfig)) (*auth.Server, *clock) {
	t.Helper()
	clk := &clock{t: time.Now()}
	cfg := auth.ServerConfig{
		Store: auth.NewMemory(clk.now), Issuer: testIssuer, Resource: testResource, Tenant: "t1",
		StudioKeyID: studioKey, StudioSecret: studioSecret, Now: clk.now,
	}
	if mod != nil {
		mod(&cfg)
	}
	return auth.NewServer(cfg), clk
}

func regBody(name string, uris ...string) string {
	b, _ := json.Marshal(map[string]any{"client_name": name, "redirect_uris": uris})
	return string(b)
}

func postRegister(h http.Handler, remote string, hdr map[string]string, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest("POST", "/register", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	r.RemoteAddr = remote
	for k, v := range hdr {
		r.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestRegisterValidation(t *testing.T) {
	long := "https://app.example/" + strings.Repeat("a", 2048)
	exact := "https://app.example/" + strings.Repeat("a", 2048-len("https://app.example/"))
	tests := []struct {
		name string
		body string
		want int
	}{
		{"ok", regBody("App", cb), 201},
		{"two distinct", regBody("App", cb, "https://app.example/cb2"), 201},
		{"duplicate redirect", regBody("App", cb, cb), 400},
		{"redirect of exactly 2048 bytes", regBody("App", exact), 201},
		{"redirect over 2048 bytes", regBody("App", long), 400},
		{"newline in name", regBody("App\nEvil", cb), 400},
		{"tab in name", regBody("App\tEvil", cb), 400},
		{"nul in name", regBody("App\x00", cb), 400},
		{"bidi override in name", regBody("App"+string(rune(0x202e))+"evil", cb), 400},
		{"unicode name is fine", regBody("Ünï App", cb), 201},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s, _ := newHardEnv(t, nil)
			w := postRegister(s.Handler(), "198.51.100.1:1", nil, tc.body)
			if w.Code != tc.want {
				t.Fatalf("status %d, want %d: %s", w.Code, tc.want, w.Body)
			}
		})
	}
}

func TestRegisterRateLimit(t *testing.T) {
	tests := []struct {
		name    string
		perHour int
		sent    int
		wantOK  int
	}{
		{"default is 10", 0, 12, 10},
		{"custom 3", 3, 5, 3},
		{"disabled", -1, 30, 30},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s, clk := newHardEnv(t, func(c *auth.ServerConfig) { c.RegisterPerHour = tc.perHour })
			h := s.Handler()
			ok := 0
			var last *httptest.ResponseRecorder
			for i := 0; i < tc.sent; i++ {
				last = postRegister(h, "198.51.100.1:1", nil, regBody("App", cb))
				if last.Code == 201 {
					ok++
				}
			}
			if ok != tc.wantOK {
				t.Fatalf("%d registrations succeeded, want %d", ok, tc.wantOK)
			}
			if tc.sent > tc.wantOK {
				if last.Code != 429 {
					t.Fatalf("limited status %d", last.Code)
				}
				ra, err := strconv.Atoi(last.Header().Get("Retry-After"))
				if err != nil || ra < 1 || ra > 3600 {
					t.Fatalf("Retry-After %q", last.Header().Get("Retry-After"))
				}
				var m map[string]string
				_ = json.Unmarshal(last.Body.Bytes(), &m)
				if m["error"] == "" || m["error_description"] == "" {
					t.Fatalf("error body %s", last.Body)
				}
				// Another address is unaffected. After the window this one works again.
				if w := postRegister(h, "198.51.100.2:1", nil, regBody("App", cb)); w.Code != 201 {
					t.Fatalf("other address: %d", w.Code)
				}
				clk.add(time.Hour + time.Second)
				if w := postRegister(h, "198.51.100.1:1", nil, regBody("App", cb)); w.Code != 201 {
					t.Fatalf("after the window: %d", w.Code)
				}
			}
		})
	}
}

func TestRegisterRateLimitCountsInvalidAttempts(t *testing.T) {
	s, _ := newHardEnv(t, func(c *auth.ServerConfig) { c.RegisterPerHour = 2 })
	h := s.Handler()
	for i := 0; i < 2; i++ {
		if w := postRegister(h, "198.51.100.1:1", nil, `{}`); w.Code != 400 {
			t.Fatalf("invalid: %d", w.Code)
		}
	}
	if w := postRegister(h, "198.51.100.1:1", nil, regBody("App", cb)); w.Code != 429 {
		t.Fatalf("valid after invalid floods: %d", w.Code)
	}
}

func TestRegisterRetryAfterShrinks(t *testing.T) {
	s, clk := newHardEnv(t, func(c *auth.ServerConfig) { c.RegisterPerHour = 1 })
	h := s.Handler()
	postRegister(h, "198.51.100.1:1", nil, regBody("App", cb))
	clk.add(30 * time.Minute)
	w := postRegister(h, "198.51.100.1:1", nil, regBody("App", cb))
	if got := w.Header().Get("Retry-After"); got != "1800" {
		t.Fatalf("Retry-After %q, want 1800", got)
	}
}

func TestRegisterTotalCap(t *testing.T) {
	s, _ := newHardEnv(t, func(c *auth.ServerConfig) { c.MaxClients = 3; c.RegisterPerHour = -1 })
	h := s.Handler()
	for i := 0; i < 3; i++ {
		if w := postRegister(h, fmt.Sprintf("198.51.100.%d:1", i), nil, regBody("App", cb)); w.Code != 201 {
			t.Fatalf("registration %d: %d", i, w.Code)
		}
	}
	w := postRegister(h, "198.51.100.9:1", nil, regBody("App", cb))
	if w.Code != 503 || !strings.Contains(w.Body.String(), "temporarily_unavailable") {
		t.Fatalf("over the cap: %d %s", w.Code, w.Body)
	}
	// A rejected request does not consume a slot: invalid ones never reached the cap.
	s2, _ := newHardEnv(t, func(c *auth.ServerConfig) { c.MaxClients = 1; c.RegisterPerHour = -1 })
	h2 := s2.Handler()
	if w := postRegister(h2, "198.51.100.1:1", nil, `{}`); w.Code != 400 {
		t.Fatal(w.Code)
	}
	if w := postRegister(h2, "198.51.100.1:1", nil, regBody("App", cb)); w.Code != 201 {
		t.Fatalf("invalid request used a slot: %d", w.Code)
	}
}

func TestClientAddressHeader(t *testing.T) {
	tests := []struct {
		name   string
		header string // configured header, empty means RemoteAddr
		hdr    map[string]string
		// two requests from the same RemoteAddr: do they share one budget?
		hdr2   map[string]string
		shared bool
	}{
		{"unset trusts RemoteAddr only", "", map[string]string{"X-Forwarded-For": "203.0.113.1"}, map[string]string{"X-Forwarded-For": "203.0.113.2"}, true},
		{"set and distinct", "X-Forwarded-For", map[string]string{"X-Forwarded-For": "203.0.113.1"}, map[string]string{"X-Forwarded-For": "203.0.113.2"}, false},
		{"set and same", "X-Forwarded-For", map[string]string{"X-Forwarded-For": "203.0.113.1"}, map[string]string{"X-Forwarded-For": "203.0.113.1"}, true},
		{"first of several values", "X-Forwarded-For", map[string]string{"X-Forwarded-For": "203.0.113.1, 10.0.0.1"}, map[string]string{"X-Forwarded-For": "203.0.113.1, 10.0.0.2"}, true},
		{"later values are ignored", "X-Forwarded-For", map[string]string{"X-Forwarded-For": "203.0.113.1, 10.0.0.1"}, map[string]string{"X-Forwarded-For": "203.0.113.2, 10.0.0.1"}, false},
		{"missing falls back", "X-Real-IP", nil, nil, true},
		{"garbage falls back", "X-Real-IP", map[string]string{"X-Real-IP": "not-an-ip"}, map[string]string{"X-Real-IP": "also garbage"}, true},
		{"empty falls back", "X-Real-IP", map[string]string{"X-Real-IP": ""}, map[string]string{"X-Real-IP": " "}, true},
		{"header with port is garbage", "X-Real-IP", map[string]string{"X-Real-IP": "203.0.113.1:80"}, map[string]string{"X-Real-IP": "203.0.113.2:80"}, true},
		{"ipv6", "X-Real-IP", map[string]string{"X-Real-IP": "2001:db8::1"}, map[string]string{"X-Real-IP": "2001:db8::2"}, false},
		{"ipv4 mapped ipv6 equals ipv4", "X-Real-IP", map[string]string{"X-Real-IP": "::ffff:203.0.113.1"}, map[string]string{"X-Real-IP": "203.0.113.1"}, true},
	}
	for _, tc := range tests {
		t.Run("register "+tc.name, func(t *testing.T) {
			s, _ := newHardEnv(t, func(c *auth.ServerConfig) { c.RegisterPerHour = 1; c.ClientIPHeader = tc.header })
			h := s.Handler()
			if w := postRegister(h, "10.9.9.9:1", tc.hdr, regBody("App", cb)); w.Code != 201 {
				t.Fatalf("first: %d", w.Code)
			}
			w := postRegister(h, "10.9.9.9:2", tc.hdr2, regBody("App", cb))
			if tc.shared && w.Code != 429 || !tc.shared && w.Code != 201 {
				t.Fatalf("second: %d, shared budget = %v", w.Code, tc.shared)
			}
		})
		t.Run("ticket "+tc.name, func(t *testing.T) {
			clk := &clock{t: time.Now()}
			st := auth.NewMemory(clk.now)
			s := auth.NewServer(auth.ServerConfig{
				Store: st, Issuer: testIssuer, Resource: testResource, Tenant: "t1",
				StudioKeyID: studioKey, StudioSecret: studioSecret, Now: clk.now, ClientIPHeader: tc.header,
			})
			h := s.Handler()
			cl := auth.Client{ID: "c1", Name: "A", RedirectURIs: []string{cb}}
			_ = st.RegisterClient(t.Context(), cl)
			_, ch := pkce()
			v := authzParams("c1", cb, ch)
			v.Set("ticket", "AAAA-AAAA")
			v.Set("scope_choice", "coach")
			v.Set("decision", "allow")
			post := func(hdr map[string]string) int {
				r := httptest.NewRequest("POST", "/authorize", strings.NewReader(v.Encode()))
				r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
				r.RemoteAddr = "10.9.9.9:1"
				for k, x := range hdr {
					r.Header.Set(k, x)
				}
				w := httptest.NewRecorder()
				h.ServeHTTP(w, r)
				return w.Code
			}
			for i := 0; i < 10; i++ {
				if c := post(tc.hdr); c != 401 {
					t.Fatalf("attempt %d: %d", i, c)
				}
			}
			c := post(tc.hdr2)
			if tc.shared && c != 429 || !tc.shared && c != 401 {
				t.Fatalf("11th attempt: %d, shared budget = %v", c, tc.shared)
			}
		})
	}
}

func TestCORS(t *testing.T) {
	s, _ := newHardEnv(t, nil)
	h := s.Handler()
	signed := httptest.NewRequest("POST", "/v1/studio/link-tickets", nil)
	agentsig.Sign(signed, studioKey, studioSecret, "p1", "idem-cors", nil, time.Now())

	type tc struct {
		name    string
		method  string
		path    string
		allowed bool   // CORS expected
		methods string // preflight: Access-Control-Allow-Methods
		headers string // preflight: Access-Control-Allow-Headers
		simple  bool   // also check the real (non preflight) response
	}
	tests := []tc{
		{"as metadata", "GET", "/.well-known/oauth-authorization-server", true, "GET", "", true},
		{"resource metadata", "GET", "/.well-known/oauth-protected-resource", true, "GET", "", false},
		{"resource metadata mcp", "GET", "/.well-known/oauth-protected-resource/mcp", true, "GET", "", false},
		{"register", "POST", "/register", true, "POST", "Content-Type", true},
		{"token", "POST", "/token", true, "POST", "Content-Type", true},
		{"authorize get", "GET", "/authorize", false, "", "", true},
		{"authorize post", "POST", "/authorize", false, "", "", true},
		{"studio tickets", "POST", "/v1/studio/link-tickets", false, "", "", true},
	}
	for _, c := range tests {
		t.Run("preflight "+c.name, func(t *testing.T) {
			r := httptest.NewRequest("OPTIONS", c.path, nil)
			r.Header.Set("Origin", "https://browser.example")
			r.Header.Set("Access-Control-Request-Method", c.method)
			r.Header.Set("Access-Control-Request-Headers", "content-type")
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			got := w.Header()
			if !c.allowed {
				for k := range got {
					if strings.HasPrefix(k, "Access-Control-") {
						t.Fatalf("%s must not get CORS headers, got %s", c.path, k)
					}
				}
				return
			}
			if w.Code != 204 || got.Get("Access-Control-Allow-Origin") != "*" ||
				got.Get("Access-Control-Allow-Methods") != c.methods ||
				got.Get("Access-Control-Allow-Headers") != c.headers ||
				got.Get("Access-Control-Max-Age") == "" {
				t.Fatalf("preflight: %d %v", w.Code, got)
			}
			if got.Get("Access-Control-Allow-Credentials") != "" {
				t.Fatal("credentials must never be allowed")
			}
		})
		if !c.simple {
			continue
		}
		t.Run("response "+c.name, func(t *testing.T) {
			var r *http.Request
			switch c.path {
			case "/register":
				r = httptest.NewRequest("POST", c.path, strings.NewReader(regBody("App", cb)))
				r.Header.Set("Content-Type", "application/json")
			case "/token", "/authorize":
				r = httptest.NewRequest(c.method, c.path, strings.NewReader(url.Values{"grant_type": {"nope"}}.Encode()))
				r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			case "/v1/studio/link-tickets":
				r = signed
			default:
				r = httptest.NewRequest(c.method, c.path, nil)
			}
			r.Header.Set("Origin", "https://browser.example")
			r.RemoteAddr = "198.51.100.1:1"
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			got := w.Header().Get("Access-Control-Allow-Origin")
			if c.allowed && got != "*" || !c.allowed && got != "" {
				t.Fatalf("Allow-Origin %q (status %d), allowed = %v", got, w.Code, c.allowed)
			}
			if w.Header().Get("Access-Control-Allow-Credentials") != "" {
				t.Fatal("credentials must never be allowed")
			}
		})
	}
}

func TestCORSOnRateLimitedAndErrorResponses(t *testing.T) {
	s, _ := newHardEnv(t, func(c *auth.ServerConfig) { c.RegisterPerHour = 1 })
	h := s.Handler()
	postRegister(h, "198.51.100.1:1", nil, regBody("App", cb))
	for _, body := range []string{regBody("App", cb), `{}`} {
		w := postRegister(h, "198.51.100.1:1", nil, body)
		if w.Header().Get("Access-Control-Allow-Origin") != "*" || w.Header().Get("Access-Control-Expose-Headers") != "Retry-After" {
			t.Fatalf("status %d needs CORS so a browser can read the error: %v", w.Code, w.Header())
		}
	}
}
