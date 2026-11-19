package main

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"

	"github.com/Moustafa-Elgammal/agentgw/internal/auth"
)

// e2eIdP is a small identity provider with real keys. Its token endpoint behaves like a real one
// for the parts the gateway depends on: it checks the client secret and the PKCE verifier before
// it hands out an ID token, so a broken code exchange fails here instead of passing silently.
type e2eIdP struct {
	srv *httptest.Server
	key *rsa.PrivateKey

	mu        sync.Mutex
	challenge string // code_challenge the gateway sent to /authorize
	idToken   string
	tokenHits int
}

func newE2EIdP(t *testing.T) *e2eIdP {
	t.Helper()
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	p := &e2eIdP{key: k}
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issuer": p.srv.URL, "authorization_endpoint": p.srv.URL + "/authorize", "token_endpoint": p.srv.URL + "/token",
			"jwks_uri": p.srv.URL + "/jwks", "code_challenge_methods_supported": []string{"S256"},
			"id_token_signing_alg_values_supported": []string{"RS256"},
		})
	})
	mux.HandleFunc("/jwks", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(jose.JSONWebKeySet{Keys: []jose.JSONWebKey{
			{Key: &k.PublicKey, KeyID: "k1", Algorithm: "RS256", Use: "sig"},
		}})
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		id, secret, basic := r.BasicAuth()
		if !basic {
			id, secret = r.PostForm.Get("client_id"), r.PostForm.Get("client_secret")
		}
		sum := sha256.Sum256([]byte(r.PostForm.Get("code_verifier")))
		p.mu.Lock()
		defer p.mu.Unlock()
		p.tokenHits++
		if id != "gw-client" || secret != "idp-client-secret" ||
			base64.RawURLEncoding.EncodeToString(sum[:]) != p.challenge {
			http.Error(w, `{"error":"invalid_grant"}`, http.StatusBadRequest)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"id_token": p.idToken, "access_token": "unused", "token_type": "Bearer"})
	})
	p.srv = httptest.NewServer(mux)
	t.Cleanup(p.srv.Close)
	return p
}

// issue signs an ID token with the given key and stores it for the next token request.
func (p *e2eIdP) issue(t *testing.T, key *rsa.PrivateKey, nonce, player string) {
	t.Helper()
	sg, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: jose.JSONWebKey{Key: key, KeyID: "k1"}},
		(&jose.SignerOptions{}).WithType("JWT"))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	payload, _ := json.Marshal(map[string]any{
		"iss": p.srv.URL, "sub": player, "aud": "gw-client", "nonce": nonce,
		"iat": now.Unix(), "exp": now.Add(5 * time.Minute).Unix(),
	})
	jws, err := sg.Sign(payload)
	if err != nil {
		t.Fatal(err)
	}
	tok, err := jws.CompactSerialize()
	if err != nil {
		t.Fatal(err)
	}
	p.mu.Lock()
	p.idToken = tok
	p.mu.Unlock()
}

type gatewayRig struct {
	t     *testing.T
	h     http.Handler
	store *auth.Memory
	idp   *e2eIdP
}

const (
	e2eResource = "https://gw.example/mcp"
	e2eRedirect = "https://app.example/cb"
)

// newGatewayRig builds the sign-in the way main does (buildOIDC from flags and environment) and
// mounts it on a real auth server.
func newGatewayRig(t *testing.T) *gatewayRig {
	t.Helper()
	idp := newE2EIdP(t)
	t.Setenv(envOIDCClientSecret, "idp-client-secret")
	t.Setenv(envOIDCSessionSecret, goodSession)
	login, err := buildOIDC(context.Background(), slog.New(slog.NewTextHandler(&strings.Builder{}, nil)), oidcOptions{
		Issuer: idp.srv.URL, ClientID: "gw-client", PlayerClaim: "sub", Scopes: "openid", Algs: "RS256", AllowHTTP: true,
	}, "https://gw.example")
	if err != nil || login == nil {
		t.Fatalf("buildOIDC: %v", err)
	}
	st := auth.NewMemory(time.Now)
	s := auth.NewServer(auth.ServerConfig{
		Store: st, Issuer: "https://gw.example", Resource: e2eResource, Tenant: "t1",
		StudioKeyID: "studio-1", StudioSecret: []byte("studio-secret"), OIDC: login,
	})
	return &gatewayRig{t, s.Handler(), st, idp}
}

func (g *gatewayRig) do(r *http.Request) *httptest.ResponseRecorder {
	r.RemoteAddr = "198.51.100.7:5555"
	w := httptest.NewRecorder()
	g.h.ServeHTTP(w, r)
	return w
}

func (g *gatewayRig) form(method, path string, v url.Values) *http.Request {
	r := httptest.NewRequest(method, path, strings.NewReader(v.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return r
}

// startSignIn registers an MCP client and plays its authorization request up to the redirect to
// the identity provider. It returns what the provider was sent and the cookie the gateway set.
func (g *gatewayRig) startSignIn() (clientID, verifier string, idpReq *url.URL, cookies []*http.Cookie) {
	g.t.Helper()
	body, _ := json.Marshal(map[string]any{"client_name": "agent", "redirect_uris": []string{e2eRedirect}})
	reg := httptest.NewRequest("POST", "/register", strings.NewReader(string(body)))
	reg.Header.Set("Content-Type", "application/json")
	w := g.do(reg)
	var m map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &m)
	if w.Code != 201 {
		g.t.Fatalf("register: %d %s", w.Code, w.Body)
	}
	clientID = m["client_id"].(string)

	verifier = strings.Repeat("v", 20) + auth.NewID()
	sum := sha256.Sum256([]byte(verifier))
	w = g.do(g.form("POST", "/authorize/oidc", url.Values{
		"client_id": {clientID}, "redirect_uri": {e2eRedirect}, "response_type": {"code"},
		"code_challenge": {base64.RawURLEncoding.EncodeToString(sum[:])}, "code_challenge_method": {"S256"},
		"state": {"agent-state"}, "resource": {e2eResource}, "scope_choice": {"coach"},
	}))
	if w.Code != http.StatusSeeOther {
		g.t.Fatalf("start sign-in: %d %s", w.Code, w.Body)
	}
	idpReq, err := url.Parse(w.Header().Get("Location"))
	if err != nil || !strings.HasPrefix(idpReq.String(), g.idp.srv.URL+"/authorize") {
		g.t.Fatalf("redirect = %q, want the identity provider's authorize endpoint", w.Header().Get("Location"))
	}
	g.idp.mu.Lock()
	g.idp.challenge = idpReq.Query().Get("code_challenge")
	g.idp.mu.Unlock()
	return clientID, verifier, idpReq, w.Result().Cookies()
}

func (g *gatewayRig) callback(idpReq *url.URL, cookies []*http.Cookie) *httptest.ResponseRecorder {
	g.t.Helper()
	r := httptest.NewRequest("GET", "/authorize/oidc/callback?"+url.Values{
		"code": {"idp-code"}, "state": {idpReq.Query().Get("state")},
	}.Encode(), nil)
	for _, c := range cookies {
		r.AddCookie(c)
	}
	return g.do(r)
}

// The whole sign-in as an agent and a player live it: dynamic client registration, the redirect
// to the identity provider, a signed ID token back, and a PKCE token exchange that ends in a
// grant for the player the token names.
func TestOIDCEndToEnd(t *testing.T) {
	g := newGatewayRig(t)
	clientID, verifier, idpReq, cookies := g.startSignIn()
	g.idp.issue(t, g.idp.key, idpReq.Query().Get("nonce"), "player-e2e")

	w := g.callback(idpReq, cookies)
	if w.Code != http.StatusSeeOther {
		t.Fatalf("callback: %d %s", w.Code, w.Body)
	}
	back, _ := url.Parse(w.Header().Get("Location"))
	code := back.Query().Get("code")
	if !strings.HasPrefix(back.String(), e2eRedirect) || code == "" || back.Query().Get("state") != "agent-state" {
		t.Fatalf("callback redirect = %s, want the agent's redirect URI with a code and its own state", back)
	}

	w = g.do(g.form("POST", "/token", url.Values{
		"grant_type": {"authorization_code"}, "client_id": {clientID}, "redirect_uri": {e2eRedirect},
		"code": {code}, "code_verifier": {verifier}, "resource": {e2eResource},
	}))
	var tok map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &tok)
	if w.Code != 200 || tok["access_token"] == "" || tok["scope"] != "coach" {
		t.Fatalf("token: %d %s", w.Code, w.Body)
	}
	gs, _ := g.store.Grants(context.Background(), "t1", "player-e2e")
	if len(gs) != 1 || gs[0].Scope != auth.ScopeCoach || gs[0].Resource != e2eResource {
		t.Fatalf("grants for the player the ID token named = %+v", gs)
	}
	if g.idp.tokenHits != 1 {
		t.Errorf("identity provider token endpoint hit %d times, want 1", g.idp.tokenHits)
	}
}

// A token signed by a key the provider does not publish never becomes a grant, and the agent only
// learns that access was denied.
func TestOIDCEndToEndRejectsForgedToken(t *testing.T) {
	g := newGatewayRig(t)
	_, _, idpReq, cookies := g.startSignIn()
	other, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	g.idp.issue(t, other, idpReq.Query().Get("nonce"), "player-forged")

	w := g.callback(idpReq, cookies)
	loc := w.Header().Get("Location")
	if w.Code != http.StatusSeeOther || !strings.Contains(loc, "error=access_denied") || strings.Contains(loc, "code=") {
		t.Fatalf("forged token: %d Location=%q, want a redirect with error=access_denied and no code", w.Code, loc)
	}
	if gs, _ := g.store.Grants(context.Background(), "t1", "player-forged"); len(gs) != 0 {
		t.Fatalf("a forged token produced grants: %+v", gs)
	}
}

// The sealed cookie ties the callback to the browser that started the sign-in: the same callback
// without it is refused and nothing is granted.
func TestOIDCEndToEndRequiresTheStartingBrowser(t *testing.T) {
	g := newGatewayRig(t)
	_, _, idpReq, _ := g.startSignIn()
	g.idp.issue(t, g.idp.key, idpReq.Query().Get("nonce"), "player-csrf")

	w := g.callback(idpReq, nil)
	if w.Code == http.StatusSeeOther && strings.Contains(w.Header().Get("Location"), "code=") {
		t.Fatalf("callback without the session cookie returned a code: %s", w.Header().Get("Location"))
	}
	if gs, _ := g.store.Grants(context.Background(), "t1", "player-csrf"); len(gs) != 0 {
		t.Fatalf("login CSRF produced grants: %+v", gs)
	}
}
