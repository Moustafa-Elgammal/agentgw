package oidc_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
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

	"github.com/go-jose/go-jose/v4"

	"github.com/Moustafa-Elgammal/agentgw/internal/auth/oidc"
)

// signingIdP is a fake identity provider that publishes real keys and signs real ID tokens, so the
// go-oidc verifier is tested against genuine signatures and a genuine JWKS fetch.
type signingIdP struct {
	srv *httptest.Server

	mu       sync.Mutex
	keys     []jose.JSONWebKey // public keys published at the JWKS endpoint
	jwksHits int
	issuer   string // what discovery claims (empty: the server URL)
	idToken  string // what the token endpoint returns
}

func newSigningIdP(t *testing.T) *signingIdP {
	t.Helper()
	p := &signingIdP{}
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		p.mu.Lock()
		iss := p.issuer
		p.mu.Unlock()
		if iss == "" {
			iss = p.srv.URL
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issuer": iss, "authorization_endpoint": p.srv.URL + "/authorize", "token_endpoint": p.srv.URL + "/token",
			"jwks_uri": p.srv.URL + "/jwks", "code_challenge_methods_supported": []string{"S256"},
			"id_token_signing_alg_values_supported": []string{"RS256", "ES256", "HS256"},
		})
	})
	mux.HandleFunc("/jwks", func(w http.ResponseWriter, _ *http.Request) {
		p.mu.Lock()
		defer p.mu.Unlock()
		p.jwksHits++
		_ = json.NewEncoder(w).Encode(jose.JSONWebKeySet{Keys: p.keys})
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, _ *http.Request) {
		p.mu.Lock()
		tok := p.idToken
		p.mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]string{"id_token": tok, "access_token": "unused", "token_type": "Bearer"})
	})
	p.srv = httptest.NewServer(mux)
	t.Cleanup(p.srv.Close)
	return p
}

func (p *signingIdP) publish(keys ...jose.JSONWebKey) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.keys = keys
}

func (p *signingIdP) hits() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.jwksHits
}

type signer struct {
	alg  jose.SignatureAlgorithm
	kid  string
	priv any
	pub  jose.JSONWebKey
}

func newRSA(t *testing.T, kid string) signer {
	t.Helper()
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	return signer{jose.RS256, kid, k, jose.JSONWebKey{Key: &k.PublicKey, KeyID: kid, Algorithm: "RS256", Use: "sig"}}
}

func newEC(t *testing.T, kid string) signer {
	t.Helper()
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return signer{jose.ES256, kid, k, jose.JSONWebKey{Key: &k.PublicKey, KeyID: kid, Algorithm: "ES256", Use: "sig"}}
}

// mint signs claims as a JWT.
func (s signer) mint(t *testing.T, claims map[string]any) string {
	t.Helper()
	sg, err := jose.NewSigner(jose.SigningKey{Algorithm: s.alg, Key: jose.JSONWebKey{Key: s.priv, KeyID: s.kid}},
		(&jose.SignerOptions{}).WithType("JWT"))
	if err != nil {
		t.Fatal(err)
	}
	payload, _ := json.Marshal(claims)
	jws, err := sg.Sign(payload)
	if err != nil {
		t.Fatal(err)
	}
	out, err := jws.CompactSerialize()
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// goodClaims is a clean token for the given issuer, valid right now.
func goodClaims(iss, nonce string) map[string]any {
	now := time.Now()
	return map[string]any{
		"iss": iss, "sub": "player-7", "aud": clientID, "nonce": nonce,
		"iat": now.Unix(), "exp": now.Add(5 * time.Minute).Unix(),
	}
}

func newVerifier(t *testing.T, p *signingIdP, algs []string) *oidc.GoOIDCVerifier {
	t.Helper()
	v, err := oidc.NewGoOIDCVerifier(context.Background(), oidc.Config{Issuer: p.srv.URL, ClientID: clientID, AllowInsecureHTTP: true}, algs, nil, nil)
	if err != nil {
		t.Fatalf("NewGoOIDCVerifier: %v", err)
	}
	return v
}

func b64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

func TestGoOIDCVerifier(t *testing.T) {
	p := newSigningIdP(t)
	rsaKey, ecKey := newRSA(t, "rsa-1"), newEC(t, "ec-1")
	p.publish(rsaKey.pub, ecKey.pub)
	v := newVerifier(t, p, nil)
	iss := p.srv.URL

	with := func(edit func(map[string]any)) map[string]any {
		c := goodClaims(iss, "n-1")
		edit(c)
		return c
	}
	tests := []struct {
		name    string
		token   func() string
		nonce   string
		wantErr bool
		check   func(t *testing.T, c oidc.Claims)
	}{
		{"RS256", func() string { return rsaKey.mint(t, goodClaims(iss, "n-1")) }, "n-1", false, func(t *testing.T, c oidc.Claims) {
			if c.Subject != "player-7" || c.Issuer != iss || c.Nonce != "n-1" || len(c.Audience) != 1 {
				t.Errorf("claims = %+v", c)
			}
		}},
		{"ES256", func() string { return ecKey.mint(t, goodClaims(iss, "n-1")) }, "n-1", false, nil},
		{"azp, nbf and a numeric claim are mapped", func() string {
			return rsaKey.mint(t, with(func(c map[string]any) {
				c["azp"] = clientID
				c["nbf"] = time.Now().Add(-time.Minute).Unix()
				c["uid"] = 9007199254740993 // beyond float64 precision
			}))
		}, "n-1", false, func(t *testing.T, c oidc.Claims) {
			if c.AuthorizedParty != clientID || c.NotBefore.IsZero() {
				t.Errorf("azp/nbf not mapped: %+v", c)
			}
			id, err := oidc.PlayerID(c, "uid")
			if err != nil || id != "9007199254740993" {
				t.Errorf("numeric claim = %q, %v", id, err)
			}
		}},
		{"wrong issuer", func() string {
			return rsaKey.mint(t, with(func(c map[string]any) { c["iss"] = "https://evil.example" }))
		}, "n-1", true, nil},
		{"wrong audience", func() string { return rsaKey.mint(t, with(func(c map[string]any) { c["aud"] = "another-client" })) }, "n-1", true, nil},
		{"expired", func() string {
			return rsaKey.mint(t, with(func(c map[string]any) { c["exp"] = time.Now().Add(-time.Hour).Unix() }))
		}, "n-1", true, nil},
		{"nonce mismatch", func() string { return rsaKey.mint(t, goodClaims(iss, "other")) }, "n-1", true, nil},
		{"empty nonce", func() string { return rsaKey.mint(t, goodClaims(iss, "")) }, "", true, nil},
		{"alg none", func() string {
			h := b64([]byte(`{"alg":"none","typ":"JWT"}`))
			pl, _ := json.Marshal(goodClaims(iss, "n-1"))
			return h + "." + b64(pl) + "."
		}, "n-1", true, nil},
		{"HS256 signed with a shared secret", func() string {
			sg, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.HS256, Key: []byte("0123456789abcdef0123456789abcdef")},
				(&jose.SignerOptions{}).WithType("JWT"))
			if err != nil {
				t.Fatal(err)
			}
			pl, _ := json.Marshal(goodClaims(iss, "n-1"))
			jws, _ := sg.Sign(pl)
			out, _ := jws.CompactSerialize()
			return out
		}, "n-1", true, nil},
		{"tampered payload", func() string {
			parts := strings.Split(rsaKey.mint(t, goodClaims(iss, "n-1")), ".")
			pl, _ := json.Marshal(with(func(c map[string]any) { c["sub"] = "admin" }))
			return parts[0] + "." + b64(pl) + "." + parts[2]
		}, "n-1", true, nil},
		{"tampered signature", func() string {
			parts := strings.Split(rsaKey.mint(t, goodClaims(iss, "n-1")), ".")
			sig := []byte(parts[2])
			if sig[10] == 'A' {
				sig[10] = 'B'
			} else {
				sig[10] = 'A'
			}
			return parts[0] + "." + parts[1] + "." + string(sig)
		}, "n-1", true, nil},
		{"signed by a key the provider does not publish", func() string { return newRSA(t, "rsa-1").mint(t, goodClaims(iss, "n-1")) }, "n-1", true, nil},
		{"not a JWT", func() string { return "not.a.jwt" }, "n-1", true, nil},
		{"empty", func() string { return "" }, "n-1", true, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, err := v.Verify(context.Background(), tt.token(), tt.nonce)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if tt.wantErr && c.Subject != "" {
				t.Errorf("claims returned with an error: %+v", c)
			}
			if tt.check != nil && err == nil {
				tt.check(t, c)
			}
		})
	}
}

func TestAlgorithmAllowList(t *testing.T) {
	p := newSigningIdP(t)
	ecKey := newEC(t, "ec-1")
	p.publish(ecKey.pub)
	// Only RS256 is accepted: a perfectly valid ES256 token is refused.
	v := newVerifier(t, p, []string{"RS256"})
	if _, err := v.Verify(context.Background(), ecKey.mint(t, goodClaims(p.srv.URL, "n")), "n"); err == nil {
		t.Fatal("ES256 token accepted with an RS256-only allow-list")
	}
	v = newVerifier(t, p, []string{"ES256"})
	if _, err := v.Verify(context.Background(), ecKey.mint(t, goodClaims(p.srv.URL, "n")), "n"); err != nil {
		t.Fatalf("ES256 refused with ES256 allowed: %v", err)
	}
}

func TestParseAlgs(t *testing.T) {
	tests := []struct {
		in   string
		want []string
		ok   bool
	}{
		{"", []string{"RS256", "ES256"}, true},
		{"  ", []string{"RS256", "ES256"}, true},
		{"ES256", []string{"ES256"}, true},
		{"RS256, ES384,EdDSA", []string{"RS256", "ES384", "EdDSA"}, true},
		{"none", nil, false},
		{"HS256", nil, false},
		{"RS256,HS512", nil, false},
		{"rs256", nil, false}, // names are case sensitive in JOSE
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			got, err := oidc.ParseAlgs(tt.in)
			if (err == nil) != tt.ok {
				t.Fatalf("err = %v, ok %v", err, tt.ok)
			}
			if tt.ok && strings.Join(got, ",") != strings.Join(tt.want, ",") {
				t.Errorf("got %v, want %v", got, tt.want)
			}
		})
	}
	if _, err := oidc.NewGoOIDCVerifier(context.Background(), oidc.Config{Issuer: "https://idp.example", ClientID: clientID}, []string{"none"}, nil, nil); err == nil {
		t.Error("NewGoOIDCVerifier accepted alg none")
	}
}

func TestKeyRotation(t *testing.T) {
	p := newSigningIdP(t)
	oldKey, newKey := newRSA(t, "k-old"), newRSA(t, "k-new")
	p.publish(oldKey.pub)
	v := newVerifier(t, p, nil)
	ctx := context.Background()

	if _, err := v.Verify(ctx, oldKey.mint(t, goodClaims(p.srv.URL, "n")), "n"); err != nil {
		t.Fatalf("old key: %v", err)
	}
	before := p.hits()
	if _, err := v.Verify(ctx, oldKey.mint(t, goodClaims(p.srv.URL, "n")), "n"); err != nil {
		t.Fatal(err)
	}
	if p.hits() != before {
		t.Errorf("keys were fetched again for a known kid (%d to %d hits): the cache is not used", before, p.hits())
	}

	// The provider rotates. A token with an unknown kid makes the verifier fetch the JWKS again.
	p.publish(newKey.pub)
	if _, err := v.Verify(ctx, newKey.mint(t, goodClaims(p.srv.URL, "n")), "n"); err != nil {
		t.Fatalf("new key after rotation: %v", err)
	}
	if p.hits() <= before {
		t.Error("the JWKS was not refetched for an unknown kid")
	}
	// The retired key no longer verifies once the provider stopped publishing it.
	if _, err := v.Verify(ctx, oldKey.mint(t, goodClaims(p.srv.URL, "n")), "n"); err == nil {
		t.Error("a token signed by a retired key is still accepted")
	}
}

func TestDiscoveryFailures(t *testing.T) {
	good := newSigningIdP(t)
	good.publish(newRSA(t, "k").pub)

	mismatch := newSigningIdP(t)
	mismatch.issuer = "https://someone-else.example"

	broken := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "nope", http.StatusInternalServerError) }))
	t.Cleanup(broken.Close)
	notJSON := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("<html>")) }))
	t.Cleanup(notJSON.Close)
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, good.srv.URL+r.URL.Path, http.StatusFound)
	}))
	t.Cleanup(redirect.Close)
	down := httptest.NewServer(http.NotFoundHandler())
	downURL := down.URL
	down.Close()

	tests := []struct {
		name   string
		cfg    oidc.Config
		wantOK bool
	}{
		{"working provider", oidc.Config{Issuer: good.srv.URL, ClientID: clientID, AllowInsecureHTTP: true}, true},
		{"issuer in the document differs", oidc.Config{Issuer: mismatch.srv.URL, ClientID: clientID, AllowInsecureHTTP: true}, false},
		{"server error", oidc.Config{Issuer: broken.URL, ClientID: clientID, AllowInsecureHTTP: true}, false},
		{"not JSON", oidc.Config{Issuer: notJSON.URL, ClientID: clientID, AllowInsecureHTTP: true}, false},
		{"redirects are not followed", oidc.Config{Issuer: redirect.URL, ClientID: clientID, AllowInsecureHTTP: true}, false},
		{"unreachable", oidc.Config{Issuer: downURL, ClientID: clientID, AllowInsecureHTTP: true}, false},
		{"http without the insecure switch", oidc.Config{Issuer: good.srv.URL, ClientID: clientID}, false},
		{"no client id", oidc.Config{Issuer: good.srv.URL, AllowInsecureHTTP: true}, false},
		{"issuer is not a URL", oidc.Config{Issuer: "idp", ClientID: clientID}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_, err := oidc.NewGoOIDCVerifier(ctx, tt.cfg, nil, nil, nil)
			if (err == nil) != tt.wantOK {
				t.Fatalf("err = %v, wantOK %v", err, tt.wantOK)
			}
			if err != nil && tt.cfg.ClientID != "" && strings.HasPrefix(tt.cfg.Issuer, "http") && !errors.Is(err, oidc.ErrDiscovery) &&
				!strings.Contains(err.Error(), "insecure") && !strings.Contains(err.Error(), "must be https") {
				t.Errorf("a discovery failure should wrap ErrDiscovery: %v", err)
			}
		})
	}
}

// flowRig runs the whole sign-in against the signing fake: Begin, the IdP's redirect, the code
// exchange, signature verification, claim checks and the mapping to a player.
type flowRig struct {
	t    *testing.T
	idp  *signingIdP
	flow *oidc.Flow
}

func newFlowRig(t *testing.T, edit func(*oidc.Config)) (*flowRig, signer) {
	t.Helper()
	p := newSigningIdP(t)
	key := newRSA(t, "k1")
	p.publish(key.pub)
	cfg := oidc.Config{
		Issuer: p.srv.URL, ClientID: clientID, ClientSecret: "s3cret",
		RedirectURL: "http://127.0.0.1:8080/authorize/oidc/callback", SessionSecret: []byte(secret32), AllowInsecureHTTP: true,
	}
	if edit != nil {
		edit(&cfg)
	}
	v, err := oidc.NewGoOIDCVerifier(context.Background(), cfg, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	f, err := oidc.NewFlow(cfg, v, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	return &flowRig{t, p, f}, key
}

// signIn plays one login. mutate edits the claims the IdP puts in the ID token.
func (r *flowRig) signIn(key signer, mutate func(map[string]any)) (oidc.Result, error) {
	r.t.Helper()
	w := httptest.NewRecorder()
	loc, err := r.flow.Begin(context.Background(), w, map[string]string{"client_id": "c1", "state": "client-state"})
	if err != nil {
		r.t.Fatal(err)
	}
	u, _ := url.Parse(loc)
	claims := goodClaims(r.idp.srv.URL, u.Query().Get("nonce"))
	if mutate != nil {
		mutate(claims)
	}
	r.idp.mu.Lock()
	r.idp.idToken = key.mint(r.t, claims)
	r.idp.mu.Unlock()

	req := httptest.NewRequest("GET", "/authorize/oidc/callback?"+url.Values{"code": {"c"}, "state": {u.Query().Get("state")}}.Encode(), nil)
	for _, c := range w.Result().Cookies() {
		req.AddCookie(c)
	}
	return r.flow.Complete(context.Background(), httptest.NewRecorder(), req)
}

func TestFlowWithRealSignatures(t *testing.T) {
	tests := []struct {
		name    string
		edit    func(*oidc.Config)
		mutate  func(map[string]any)
		want    string
		wantErr error
	}{
		{"happy path maps sub to the player", nil, nil, "player-7", nil},
		{"custom player claim", func(c *oidc.Config) { c.PlayerClaim = "game_id" }, func(c map[string]any) { c["game_id"] = "g-123" }, "g-123", nil},
		{"missing player claim", func(c *oidc.Config) { c.PlayerClaim = "game_id" }, nil, "", oidc.ErrPlayerClaim},
		{"wrong issuer", nil, func(c map[string]any) { c["iss"] = "https://evil.example" }, "", oidc.ErrToken},
		{"wrong audience", nil, func(c map[string]any) { c["aud"] = "someone-else" }, "", oidc.ErrToken},
		{"expired", nil, func(c map[string]any) { c["exp"] = time.Now().Add(-time.Hour).Unix() }, "", oidc.ErrToken},
		{"nonce of another sign-in", nil, func(c map[string]any) { c["nonce"] = "replayed-from-elsewhere" }, "", oidc.ErrToken},
		{"issued too long ago", nil, func(c map[string]any) {
			c["iat"] = time.Now().Add(-time.Hour).Unix()
			c["exp"] = time.Now().Add(time.Hour).Unix()
		}, "", oidc.ErrExpired},
		{"two audiences without azp", nil, func(c map[string]any) { c["aud"] = []string{clientID, "other"} }, "", oidc.ErrAudience},
		{"two audiences with azp", nil, func(c map[string]any) { c["aud"] = []string{clientID, "other"}; c["azp"] = clientID }, "player-7", nil},
		{"azp of another client", nil, func(c map[string]any) { c["azp"] = "other" }, "", oidc.ErrAudience},
		{"player id with a space", nil, func(c map[string]any) { c["sub"] = "bad id" }, "", oidc.ErrPlayerClaim},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r, key := newFlowRig(t, tt.edit)
			res, err := r.signIn(key, tt.mutate)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("err = %v, want %v", err, tt.wantErr)
			}
			if res.Player != tt.want {
				t.Errorf("player = %q, want %q", res.Player, tt.want)
			}
		})
	}
}

func TestFlowEndToEndES256AndRotation(t *testing.T) {
	r, rsaKey := newFlowRig(t, nil)
	ec := newEC(t, "ec-2")
	r.idp.publish(rsaKey.pub, ec.pub)
	if res, err := r.signIn(ec, nil); err != nil || res.Player != "player-7" {
		t.Fatalf("ES256 sign-in: %v %+v", err, res)
	}
	// Rotation: the provider moves to a new RSA key between two sign-ins, nothing restarts.
	next := newRSA(t, "k2")
	r.idp.publish(next.pub)
	if res, err := r.signIn(next, nil); err != nil || res.Player != "player-7" {
		t.Fatalf("sign-in after rotation: %v %+v", err, res)
	}
	if _, err := r.signIn(rsaKey, nil); err == nil {
		t.Fatal("a token from the retired key was accepted")
	}
}
