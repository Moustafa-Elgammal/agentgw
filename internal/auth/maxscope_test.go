package auth_test

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/Moustafa-Elgammal/agentgw/internal/auth"
)

// coachEnv is newEnv with the gateway capped at coach, as in a first pilot.
func coachEnv(t *testing.T, max auth.Scope) *env {
	t.Helper()
	clk := &clock{t: time.Now()}
	st := auth.NewMemory(clk.now)
	s := auth.NewServer(auth.ServerConfig{
		Store: st, Issuer: testIssuer, Resource: testResource, Tenant: "t1",
		StudioKeyID: studioKey, StudioSecret: studioSecret, Now: clk.now, MaxScope: max,
	})
	return &env{t, s.Handler(), st, clk}
}

func TestMaxScopeConsentPage(t *testing.T) {
	tests := []struct {
		name  string
		max   auth.Scope
		query string // the client's scope parameter
		want  int
		offer []string
		deny  []string
	}{
		{"no cap offers both", "", "", 200, []string{`value="coach"`, `value="operator"`}, nil},
		{"operator cap offers both", auth.ScopeOperator, "", 200, []string{`value="coach"`, `value="operator"`}, nil},
		{"coach cap offers coach only", auth.ScopeCoach, "", 200, []string{`value="coach"`}, []string{`value="operator"`}},
		{"coach cap, client asks coach", auth.ScopeCoach, "coach", 200, []string{`value="coach"`}, []string{`value="operator"`}},
		// A client that insists on operator is told no, it is not quietly downgraded.
		{"coach cap, client asks operator", auth.ScopeCoach, "operator", 303, nil, nil},
		{"coach cap, client asks both", auth.ScopeCoach, "coach operator", 303, nil, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := coachEnv(t, tt.max)
			id := e.client("A", cb)
			_, ch := pkce()
			v := authzParams(id, cb, ch)
			if tt.query != "" {
				v.Set("scope", tt.query)
			}
			w := e.authzGet(v)
			if w.Code != tt.want {
				t.Fatalf("status %d, want %d: %s", w.Code, tt.want, w.Body)
			}
			if tt.want == 303 {
				if got := location(t, w).Query().Get("error"); got != "invalid_scope" {
					t.Fatalf("error = %q, want invalid_scope", got)
				}
				return
			}
			for _, s := range tt.offer {
				if !strings.Contains(w.Body.String(), s) {
					t.Errorf("missing %s", s)
				}
			}
			for _, s := range tt.deny {
				if strings.Contains(w.Body.String(), s) {
					t.Errorf("must not offer %s", s)
				}
			}
		})
	}
}

// A form posted by hand with scope_choice=operator is refused under the cap, even though the
// page would never have offered it.
func TestMaxScopeRefusesOperatorSubmit(t *testing.T) {
	e := coachEnv(t, auth.ScopeCoach)
	id := e.client("A", cb)
	_, ch := pkce()
	w := e.consent(authzParams(id, cb, ch), e.ticket("p1"), auth.ScopeOperator)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status %d, want 400: %s", w.Code, w.Body)
	}
}

func TestMaxScopeClampsTokensAndRefresh(t *testing.T) {
	// The grant is stored as operator (made before the cap was set), then the gateway restarts
	// with --max-scope coach. Exchanging a code and refreshing must both report coach.
	e := coachEnv(t, auth.ScopeCoach)
	id := e.client("A", cb)
	verifier, ch := pkce()
	// Mint the code the way an older, uncapped gateway would have: operator in the code.
	code, hash := auth.NewToken()
	if err := e.store.PutCode(context.Background(), auth.Code{
		Hash: hash, ClientID: id, RedirectURI: cb, Challenge: ch, Resource: testResource,
		Scope: auth.ScopeOperator, Tenant: "t1", Player: "p1", Expires: e.clk.now().Add(time.Minute),
	}); err != nil {
		t.Fatal(err)
	}
	tok := e.exchange(id, cb, code, verifier)
	if tok["scope"] != "coach" {
		t.Fatalf("exchange scope = %v, want coach", tok["scope"])
	}
	grants, _ := e.store.Grants(context.Background(), "t1", "p1")
	if len(grants) != 1 || grants[0].Scope != auth.ScopeCoach {
		t.Fatalf("stored grant = %+v, want one coach grant", grants)
	}
	w, m := e.tokenPost(url.Values{"grant_type": {"refresh_token"}, "client_id": {id}, "refresh_token": {tok["refresh_token"].(string)}})
	if w.Code != 200 || m["scope"] != "coach" {
		t.Fatalf("refresh = %d %v, want 200 coach", w.Code, m)
	}
}

func TestMaxScopeAtCallTime(t *testing.T) {
	tests := []struct {
		name  string
		grant auth.Scope
		max   auth.Scope
		want  auth.Scope
	}{
		{"operator grant, no cap", auth.ScopeOperator, "", auth.ScopeOperator},
		{"operator grant, operator cap", auth.ScopeOperator, auth.ScopeOperator, auth.ScopeOperator},
		{"operator grant, coach cap", auth.ScopeOperator, auth.ScopeCoach, auth.ScopeCoach},
		{"coach grant, coach cap", auth.ScopeCoach, auth.ScopeCoach, auth.ScopeCoach},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rs, st, _ := rsWith(t)
			g, err := st.UpsertGrant(context.Background(), auth.Grant{Tenant: "game", Player: "bob", ClientID: "c1", Resource: resource, Scope: tt.grant})
			if err != nil {
				t.Fatal(err)
			}
			rs.MaxScope = tt.max
			p, err := rs.Authenticate(authReq("Bearer " + issue(t, st, g)))
			if err != nil {
				t.Fatal(err)
			}
			if p.Scope != tt.want {
				t.Fatalf("principal scope = %q, want %q", p.Scope, tt.want)
			}
		})
	}
}
