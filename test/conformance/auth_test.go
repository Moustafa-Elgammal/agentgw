package conformance

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/Moustafa-Elgammal/agentgw/internal/audit"
	"github.com/Moustafa-Elgammal/agentgw/internal/auth"
	"github.com/Moustafa-Elgammal/agentgw/internal/connector"
	"github.com/Moustafa-Elgammal/agentgw/internal/gateway"
	"github.com/Moustafa-Elgammal/agentgw/internal/manifest"
	"github.com/Moustafa-Elgammal/agentgw/internal/mcpserver"
	"github.com/Moustafa-Elgammal/agentgw/internal/policy"
	"github.com/Moustafa-Elgammal/agentgw/pkg/agentsig"
)

// This file is the M3 exit criterion: a scripted PKCE client completes the OAuth
// flow, and wrong-audience and revoked tokens are rejected.

const (
	studioKeyID  = "studio-1"
	oauthRedir   = "http://127.0.0.1/callback"
	oauthTenant  = "conf"
	metadataPath = "/.well-known/oauth-protected-resource/mcp"
)

var studioSecret = []byte("studio-secret")

type oauthRig struct {
	t     *testing.T
	url   string // issuer, also the gateway base URL
	b     *backend
	store *auth.Memory
	rs    *auth.ResourceServer
	http  *http.Client // does not follow redirects
}

func newOAuthRig(t *testing.T) *oauthRig {
	t.Helper()
	m, err := manifest.LoadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	b := newBackend(t)
	svc := gateway.New(m, policy.NewMemory(policy.DefaultLimits, nil),
		connector.NewHTTP(b.srv.URL, keyID, secret), &audit.Memory{})

	var h atomic.Value // the handler, set once the server URL is known
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.Load().(http.Handler).ServeHTTP(w, r)
	}))
	t.Cleanup(ts.Close)

	store := auth.NewMemory(nil)
	rs := &auth.ResourceServer{Store: store, Resource: ts.URL + "/mcp", MetadataURL: ts.URL + metadataPath, Tenant: oauthTenant}
	srv := auth.NewServer(auth.ServerConfig{
		Store: store, Issuer: ts.URL, Resource: rs.Resource, Tenant: oauthTenant,
		StudioKeyID: studioKeyID, StudioSecret: studioSecret,
	})
	mux := http.NewServeMux()
	mux.Handle("/mcp", mcpserver.Handler(svc, rs.Authenticate))
	mux.Handle("/.well-known/oauth-protected-resource", rs.MetadataHandler(ts.URL))
	mux.Handle(metadataPath, rs.MetadataHandler(ts.URL))
	mux.Handle("/", srv.Handler())
	h.Store(http.Handler(mux))

	return &oauthRig{t, ts.URL, b, store, rs, &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}
}

func (o *oauthRig) getJSON(path string) map[string]any {
	o.t.Helper()
	resp, err := o.http.Get(o.url + path)
	if err != nil {
		o.t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != 200 {
		o.t.Fatalf("GET %s: %d", path, resp.StatusCode)
	}
	var out map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		o.t.Fatal(err)
	}
	return out
}

// studioTicket does what a studio backend does for a logged-in player.
func (o *oauthRig) studioTicket(player string) string {
	o.t.Helper()
	req, _ := http.NewRequest("POST", o.url+"/v1/studio/link-tickets", nil)
	agentsig.Sign(req, studioKeyID, studioSecret, player, "idem-"+auth.NewID(), nil, time.Now())
	resp, err := o.http.Do(req)
	if err != nil {
		o.t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var out struct {
		Ticket string `json:"ticket"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&out)
	if resp.StatusCode != 201 || out.Ticket == "" {
		o.t.Fatalf("mint ticket: %d", resp.StatusCode)
	}
	return out.Ticket
}

func (o *oauthRig) registerClient() string {
	o.t.Helper()
	resp, err := o.http.Post(o.url+"/register", "application/json",
		strings.NewReader(`{"client_name":"scripted agent","redirect_uris":["`+oauthRedir+`"]}`))
	if err != nil {
		o.t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var out struct {
		ClientID string `json:"client_id"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&out)
	if resp.StatusCode != 201 || out.ClientID == "" {
		o.t.Fatalf("register: %d", resp.StatusCode)
	}
	return out.ClientID
}

type tokens struct {
	Access  string `json:"access_token"`
	Refresh string `json:"refresh_token"`
	Scope   string `json:"scope"`
	Type    string `json:"token_type"`
}

// link runs the whole PKCE flow for one player and returns the tokens.
func (o *oauthRig) link(clientID, player string, scope auth.Scope) tokens {
	o.t.Helper()
	sum := sha256.Sum256([]byte(strings.Repeat("verifier", 8)))
	verifier := strings.Repeat("verifier", 8)
	challenge := base64.RawURLEncoding.EncodeToString(sum[:])
	q := url.Values{
		"client_id": {clientID}, "redirect_uri": {oauthRedir + ""}, "response_type": {"code"},
		"code_challenge": {challenge}, "code_challenge_method": {"S256"},
		"state": {"xyz"}, "resource": {o.rs.Resource},
	}
	// The browser step: GET shows the consent page.
	resp, err := o.http.Get(o.url + "/authorize?" + q.Encode())
	if err != nil {
		o.t.Fatal(err)
	}
	page, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != 200 || !strings.Contains(string(page), "scripted agent") {
		o.t.Fatalf("consent page: %d", resp.StatusCode)
	}
	// The player types the ticket the game showed and picks a scope.
	form := cloneQuery(q)
	form.Set("ticket", o.studioTicket(player))
	form.Set("scope_choice", string(scope))
	form.Set("decision", "allow")
	resp, err = o.http.PostForm(o.url+"/authorize", form)
	if err != nil {
		o.t.Fatal(err)
	}
	_ = resp.Body.Close()
	loc, err := url.Parse(resp.Header.Get("Location"))
	if err != nil || resp.StatusCode != http.StatusSeeOther {
		o.t.Fatalf("authorize: %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	if loc.Query().Get("state") != "xyz" || loc.Query().Get("iss") != o.url || loc.Query().Get("code") == "" {
		o.t.Fatalf("bad redirect %s", loc)
	}
	return o.token(url.Values{
		"grant_type": {"authorization_code"}, "code": {loc.Query().Get("code")}, "client_id": {clientID},
		"redirect_uri": {oauthRedir}, "code_verifier": {verifier}, "resource": {o.rs.Resource},
	}, 200)
}

func cloneQuery(v url.Values) url.Values {
	o := url.Values{}
	for k, x := range v {
		o[k] = append([]string(nil), x...)
	}
	return o
}

func (o *oauthRig) token(form url.Values, want int) tokens {
	o.t.Helper()
	resp, err := o.http.PostForm(o.url+"/token", form)
	if err != nil {
		o.t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != want {
		o.t.Fatalf("token: %d, want %d", resp.StatusCode, want)
	}
	var tk tokens
	_ = json.NewDecoder(resp.Body).Decode(&tk)
	return tk
}

type bearerRT struct{ token string }

func (b bearerRT) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+b.token)
	return http.DefaultTransport.RoundTrip(r)
}

// mcpStatus POSTs an initialize request with the given token and returns the status and header.
func (o *oauthRig) mcpStatus(token string) (int, string) {
	o.t.Helper()
	req, _ := http.NewRequest("POST", o.url+"/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := o.http.Do(req)
	if err != nil {
		o.t.Fatal(err)
	}
	_ = resp.Body.Close()
	return resp.StatusCode, resp.Header.Get("WWW-Authenticate")
}

func (o *oauthRig) session(token string) *mcp.ClientSession {
	o.t.Helper()
	c := mcp.NewClient(&mcp.Implementation{Name: "oauth-conformance", Version: "0"}, nil)
	cs, err := c.Connect(context.Background(), &mcp.StreamableClientTransport{
		Endpoint: o.url + "/mcp", HTTPClient: &http.Client{Transport: bearerRT{token}},
	}, nil)
	if err != nil {
		o.t.Fatalf("connect: %v", err)
	}
	o.t.Cleanup(func() { _ = cs.Close() })
	return cs
}

func toolNames(t *testing.T, cs *mcp.ClientSession) map[string]bool {
	t.Helper()
	res, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]bool{}
	for _, tl := range res.Tools {
		out[tl.Name] = true
	}
	return out
}

func TestOAuthDiscovery(t *testing.T) {
	o := newOAuthRig(t)
	for _, p := range []string{"/.well-known/oauth-protected-resource", metadataPath} {
		prm := o.getJSON(p)
		as, _ := prm["authorization_servers"].([]any)
		if prm["resource"] != o.rs.Resource || len(as) != 1 || as[0] != o.url {
			t.Errorf("%s: %v", p, prm)
		}
	}
	asm := o.getJSON("/.well-known/oauth-authorization-server")
	if asm["issuer"] != o.url || asm["registration_endpoint"] != o.url+"/register" {
		t.Errorf("%v", asm)
	}

	status, www := o.mcpStatus("")
	if status != 401 || !strings.Contains(www, `resource_metadata="`+o.url+metadataPath+`"`) {
		t.Fatalf("no token: %d %q", status, www)
	}
	status, www = o.mcpStatus("not-a-token")
	if status != 401 || !strings.Contains(www, "resource_metadata=") || !strings.Contains(www, `error="invalid_token"`) {
		t.Fatalf("bad token: %d %q", status, www)
	}
}

func TestOAuthCoachGrantIsReadOnly(t *testing.T) {
	o := newOAuthRig(t)
	tk := o.link(o.registerClient(), "alice", auth.ScopeCoach)
	if tk.Type != "Bearer" || tk.Scope != "coach" || tk.Refresh == "" {
		t.Fatalf("%+v", tk)
	}
	cs := o.session(tk.Access)
	names := toolNames(t, cs)
	if len(names) != 1 || !names["get_city_state"] {
		t.Fatalf("a coach lists only read tools, got %v", names)
	}
	if body, isErr := call(t, cs, "get_city_state", nil); isErr {
		t.Fatalf("read must work: %v", body)
	}
	// Guessing the name of an action tool must not work.
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "sell_resource", Arguments: sell(1)})
	if err == nil && !res.IsError {
		t.Fatal("a coach grant must not run action tools")
	}
	for _, c := range o.b.got() {
		if c.Path == "/api/v1/market/sell" {
			t.Fatal("the backend must never see the action")
		}
	}
	if calls := o.b.got(); len(calls) != 1 || calls[0].Player != "alice" {
		t.Fatalf("the player comes from the grant: %+v", calls)
	}
}

func TestOAuthOperatorGrantCanAct(t *testing.T) {
	o := newOAuthRig(t)
	tk := o.link(o.registerClient(), "bob", auth.ScopeOperator)
	cs := o.session(tk.Access)
	names := toolNames(t, cs)
	for _, want := range []string{"get_city_state", "collect_taxes", "build_structure", "sell_resource"} {
		if !names[want] {
			t.Errorf("operator must list %s, got %v", want, names)
		}
	}
	if body, isErr := call(t, cs, "sell_resource", sell(5)); isErr {
		t.Fatalf("operator action: %v", body)
	}
	calls := o.b.got()
	if len(calls) != 1 || calls[0].Player != "bob" || calls[0].Path != "/api/v1/market/sell" {
		t.Fatalf("backend calls %+v", calls)
	}
}

func TestOAuthRefreshRotationReuseAndRevocation(t *testing.T) {
	o := newOAuthRig(t)
	id := o.registerClient()
	first := o.link(id, "carol", auth.ScopeOperator)
	refresh := func(rt string, want int) tokens {
		return o.token(url.Values{"grant_type": {"refresh_token"}, "client_id": {id}, "refresh_token": {rt}}, want)
	}

	second := refresh(first.Refresh, 200)
	if second.Refresh == first.Refresh || second.Access == first.Access {
		t.Fatal("refresh must rotate both tokens")
	}
	if status, _ := o.mcpStatus(second.Access); status != 200 {
		t.Fatalf("rotated access token: %d", status)
	}
	if body, isErr := call(t, o.session(second.Access), "get_city_state", nil); isErr {
		t.Fatalf("%v", body)
	}

	// Replaying the retired refresh token means it leaked: the whole grant dies.
	refresh(first.Refresh, 400)
	for name, tok := range map[string]string{"first access": first.Access, "second access": second.Access} {
		if status, www := o.mcpStatus(tok); status != 401 || !strings.Contains(www, "invalid_token") {
			t.Errorf("%s after reuse: %d %q", name, status, www)
		}
	}
	refresh(second.Refresh, 400)
}

func TestOAuthRevokeGrantStopsTheNextCall(t *testing.T) {
	o := newOAuthRig(t)
	tk := o.link(o.registerClient(), "dave", auth.ScopeOperator)
	cs := o.session(tk.Access)
	if _, isErr := call(t, cs, "get_city_state", nil); isErr {
		t.Fatal("call before revocation must work")
	}
	gs, err := o.store.Grants(context.Background(), oauthTenant, "dave")
	if err != nil || len(gs) != 1 {
		t.Fatalf("%v %v", gs, err)
	}
	if err := o.store.RevokeGrant(context.Background(), oauthTenant, "dave", gs[0].ID); err != nil {
		t.Fatal(err)
	}
	if _, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "get_city_state"}); err == nil {
		t.Fatal("the next call on a revoked grant must fail")
	}
	if status, _ := o.mcpStatus(tk.Access); status != 401 {
		t.Fatalf("status %d", status)
	}
	// The refresh token died with the grant.
	o.token(url.Values{"grant_type": {"refresh_token"}, "client_id": {gs[0].ClientID}, "refresh_token": {tk.Refresh}}, 400)
}

func TestOAuthWrongAudienceIsRejected(t *testing.T) {
	o := newOAuthRig(t)
	ctx := context.Background()
	other, err := o.store.UpsertGrant(ctx, auth.Grant{Tenant: oauthTenant, Player: "eve", ClientID: "c1",
		Resource: "https://other.example/mcp", Scope: auth.ScopeOperator})
	if err != nil {
		t.Fatal(err)
	}
	plain, hash := auth.NewToken()
	if err := o.store.PutAccess(ctx, hash, other.ID, time.Now().Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if status, www := o.mcpStatus(plain); status != 401 || !strings.Contains(www, "invalid_token") {
		t.Fatalf("a token for another resource: %d %q", status, www)
	}
	// And a token for another tenant is rejected the same way.
	wt, err := o.store.UpsertGrant(ctx, auth.Grant{Tenant: "elsewhere", Player: "eve", ClientID: "c1",
		Resource: o.rs.Resource, Scope: auth.ScopeOperator})
	if err != nil {
		t.Fatal(err)
	}
	plain2, hash2 := auth.NewToken()
	_ = o.store.PutAccess(ctx, hash2, wt.ID, time.Now().Add(time.Minute))
	if status, _ := o.mcpStatus(plain2); status != 401 {
		t.Fatalf("a token for another tenant: %d", status)
	}
	// The authorization server will not even start a flow for another resource.
	c := o.registerClient()
	q := url.Values{"client_id": {c}, "redirect_uri": {oauthRedir}, "response_type": {"code"},
		"code_challenge": {strings.Repeat("a", 43)}, "code_challenge_method": {"S256"}, "resource": {"https://other.example/mcp"}}
	resp, err := o.http.Get(o.url + "/authorize?" + q.Encode())
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if !strings.Contains(resp.Header.Get("Location"), "error=invalid_target") {
		t.Fatalf("authorize for another resource: %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}
}

func TestOAuthStudioTicketNeedsSignature(t *testing.T) {
	o := newOAuthRig(t)
	resp, err := o.http.Post(o.url+"/v1/studio/link-tickets", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != 401 {
		t.Fatalf("unsigned ticket request: %d", resp.StatusCode)
	}
}
