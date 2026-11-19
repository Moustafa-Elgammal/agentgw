package main

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const goodSession = "0123456789abcdef0123456789abcdef-session" //nolint:gosec // a fixed test value

func fakeProvider(t *testing.T, issuerOverride string) *httptest.Server {
	t.Helper()
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/.well-known/openid-configuration" {
			http.NotFound(w, r)
			return
		}
		iss := issuerOverride
		if iss == "" {
			iss = srv.URL
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issuer": iss, "authorization_endpoint": srv.URL + "/authorize", "token_endpoint": srv.URL + "/token",
			"jwks_uri": srv.URL + "/jwks", "code_challenge_methods_supported": []string{"S256"},
		})
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestBuildOIDC(t *testing.T) {
	good := fakeProvider(t, "")
	mismatch := fakeProvider(t, "https://somebody-else.example")
	tests := []struct {
		name       string
		opts       func(issuer string) oidcOptions
		issuer     string
		clientSec  string
		sessionSec string
		wantNil    bool
		wantErr    []string // all of these must appear in the error
	}{
		{"off by default", func(string) oidcOptions { return oidcOptions{} }, "", "", "", true, nil},
		{"working provider", func(i string) oidcOptions {
			return oidcOptions{Issuer: i, ClientID: "gw", Scopes: "openid profile", AllowHTTP: true}
		}, good.URL, "client-secret", goodSession, false, nil},
		{"missing client id", func(i string) oidcOptions { return oidcOptions{Issuer: i, AllowHTTP: true} }, good.URL, "client-secret", goodSession, true, []string{"--oidc-client-id"}},
		{"missing client secret", func(i string) oidcOptions { return oidcOptions{Issuer: i, ClientID: "gw", AllowHTTP: true} }, good.URL, "", goodSession, true, []string{"AGENTGW_OIDC_CLIENT_SECRET"}},
		{"missing session secret", func(i string) oidcOptions { return oidcOptions{Issuer: i, ClientID: "gw", AllowHTTP: true} }, good.URL, "client-secret", "", true, []string{"AGENTGW_OIDC_SESSION_SECRET", "32 bytes"}},
		{"short session secret", func(i string) oidcOptions { return oidcOptions{Issuer: i, ClientID: "gw", AllowHTTP: true} }, good.URL, "client-secret", "too-short", true, []string{"AGENTGW_OIDC_SESSION_SECRET"}},
		{"all problems at once", func(i string) oidcOptions { return oidcOptions{Issuer: i, AllowHTTP: true} }, good.URL, "", "", true, []string{"--oidc-client-id", "AGENTGW_OIDC_CLIENT_SECRET", "AGENTGW_OIDC_SESSION_SECRET"}},
		{"alg none is refused", func(i string) oidcOptions {
			return oidcOptions{Issuer: i, ClientID: "gw", Algs: "none", AllowHTTP: true}
		}, good.URL, "client-secret", goodSession, true, []string{"not allowed"}},
		{"HS256 is refused", func(i string) oidcOptions {
			return oidcOptions{Issuer: i, ClientID: "gw", Algs: "RS256,HS256", AllowHTTP: true}
		}, good.URL, "client-secret", goodSession, true, []string{"HS256"}},
		{"issuer differs from its own document", func(i string) oidcOptions { return oidcOptions{Issuer: i, ClientID: "gw", AllowHTTP: true} }, mismatch.URL, "client-secret", goodSession, true, []string{"discovery", "will not start"}},
		{"unreachable issuer", func(i string) oidcOptions { return oidcOptions{Issuer: i, ClientID: "gw", AllowHTTP: true} }, "http://127.0.0.1:1", "client-secret", goodSession, true, []string{"discovery"}},
		{"http issuer without the dev switch", func(i string) oidcOptions { return oidcOptions{Issuer: i, ClientID: "gw"} }, good.URL, "client-secret", goodSession, true, []string{"issuer"}},
		{"settings without an issuer", func(string) oidcOptions { return oidcOptions{ClientID: "gw"} }, "", "", "", true, []string{"--oidc-issuer"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv(envOIDCClientSecret, tt.clientSec)
			t.Setenv(envOIDCSessionSecret, tt.sessionSec)
			var logs bytes.Buffer
			log := slog.New(slog.NewTextHandler(&logs, nil))
			got, err := buildOIDC(context.Background(), log, tt.opts(tt.issuer), "http://127.0.0.1:8080")
			if len(tt.wantErr) == 0 && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(tt.wantErr) > 0 {
				if err == nil {
					t.Fatalf("expected an error containing %v", tt.wantErr)
				}
				for _, w := range tt.wantErr {
					if !strings.Contains(err.Error(), w) {
						t.Errorf("error %q does not mention %q", err, w)
					}
				}
			}
			if (got == nil) != tt.wantNil {
				t.Errorf("login nil = %v, want %v", got == nil, tt.wantNil)
			}
			for _, secret := range []string{"client-secret", goodSession} {
				if tt.clientSec != "" && strings.Contains(logs.String()+errString(err), secret) {
					t.Errorf("a secret leaked into the log or the error")
				}
			}
		})
	}
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
