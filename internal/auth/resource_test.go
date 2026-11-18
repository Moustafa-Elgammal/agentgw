package auth_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Moustafa-Elgammal/agentgw/internal/auth"
	"github.com/Moustafa-Elgammal/agentgw/internal/core"
)

const resource = "https://gw.example/mcp"

func rsWith(t *testing.T) (*auth.ResourceServer, auth.Store, auth.Grant) {
	t.Helper()
	st := auth.NewMemory(nil)
	g, err := st.UpsertGrant(context.Background(), auth.Grant{Tenant: "game", Player: "alice", ClientID: "c1", Resource: resource, Scope: auth.ScopeCoach})
	if err != nil {
		t.Fatal(err)
	}
	rs := &auth.ResourceServer{Store: st, Resource: resource, Tenant: "game",
		MetadataURL: "https://gw.example/.well-known/oauth-protected-resource/mcp"}
	return rs, st, g
}

func issue(t *testing.T, st auth.Store, g auth.Grant) string {
	t.Helper()
	plain, hash := auth.NewToken()
	if err := st.PutAccess(context.Background(), hash, g.ID, time.Now().Add(auth.AccessTTL)); err != nil {
		t.Fatal(err)
	}
	return plain
}

func authReq(h string) *http.Request {
	r := httptest.NewRequest("POST", "/mcp", nil)
	if h != "" {
		r.Header.Set("Authorization", h)
	}
	return r
}

func TestAuthenticate(t *testing.T) {
	rs, st, g := rsWith(t)
	tok := issue(t, st, g)

	// A token bound to another resource, and one for another tenant.
	other, _ := st.UpsertGrant(context.Background(), auth.Grant{Tenant: "game", Player: "alice", ClientID: "c1", Resource: "https://other.example/mcp", Scope: auth.ScopeOperator})
	wrongAud := issue(t, st, other)
	otherTenant, _ := st.UpsertGrant(context.Background(), auth.Grant{Tenant: "elsewhere", Player: "alice", ClientID: "c1", Resource: resource, Scope: auth.ScopeOperator})
	wrongTenant := issue(t, st, otherTenant)

	revoked := issue(t, st, g)
	rev, _ := st.UpsertGrant(context.Background(), auth.Grant{Tenant: "game", Player: "bob", ClientID: "c1", Resource: resource, Scope: auth.ScopeOperator})
	revTok := issue(t, st, rev)
	_ = st.RevokeGrant(context.Background(), "game", "bob", rev.ID)
	_ = revoked

	tests := []struct {
		name     string
		header   string
		wantErr  string // substring of the WWW-Authenticate header, "" means success
		wantAuth bool
	}{
		{"valid token", "Bearer " + tok, "", true},
		{"scheme is case insensitive", "bearer " + tok, "", true},
		{"no credentials gets a bare challenge", "", "resource_metadata=", false},
		{"wrong scheme", "Basic abc", `error="invalid_request"`, false},
		{"empty token", "Bearer ", `error="invalid_request"`, false},
		{"unknown token", "Bearer nope", `error="invalid_token"`, false},
		{"wrong audience", "Bearer " + wrongAud, `error="invalid_token"`, false},
		{"wrong tenant", "Bearer " + wrongTenant, `error="invalid_token"`, false},
		{"revoked grant", "Bearer " + revTok, `error="invalid_token"`, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p, err := rs.Authenticate(authReq(tc.header))
			if tc.wantAuth {
				if err != nil || p.PlayerID != "alice" || p.Scope != core.ScopeCoach || p.ClientID != "c1" || p.TenantID != "game" {
					t.Fatalf("principal = %+v, err %v", p, err)
				}
				return
			}
			ch, ok := err.(*auth.Challenge)
			if !ok {
				t.Fatalf("want a *Challenge, got %v", err)
			}
			if !strings.Contains(ch.WWWAuthenticate(), tc.wantErr) || !strings.Contains(ch.WWWAuthenticate(), "resource_metadata=") {
				t.Errorf("header = %q, want it to contain %q", ch.WWWAuthenticate(), tc.wantErr)
			}
			if p.PlayerID != "" {
				t.Errorf("no principal may be returned on failure: %+v", p)
			}
		})
	}
}

func TestExpiredTokenIsRejected(t *testing.T) {
	rs, st, g := rsWith(t)
	plain, hash := auth.NewToken()
	_ = st.PutAccess(context.Background(), hash, g.ID, time.Now().Add(-time.Second))
	if _, err := rs.Authenticate(authReq("Bearer " + plain)); err == nil {
		t.Fatal("expired token must be rejected")
	}
}

func TestMetadata(t *testing.T) {
	rs, _, _ := rsWith(t)
	srv := httptest.NewServer(rs.MetadataHandler("https://gw.example"))
	defer srv.Close()
	resp, err := http.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	for _, want := range []string{`"resource":"https://gw.example/mcp"`, `"authorization_servers":["https://gw.example"]`, `"coach"`, `"operator"`} {
		if !strings.Contains(string(b), want) {
			t.Errorf("metadata %s is missing %s", b, want)
		}
	}
	if resp, err := http.Post(srv.URL, "application/json", nil); err == nil {
		_ = resp.Body.Close()
		if resp.StatusCode != 405 {
			t.Errorf("POST = %d, want 405", resp.StatusCode)
		}
	}
}
