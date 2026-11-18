package httpapi

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Moustafa-Elgammal/agentgw/internal/audit"
	"github.com/Moustafa-Elgammal/agentgw/internal/confirm"
	"github.com/Moustafa-Elgammal/agentgw/internal/connector"
	"github.com/Moustafa-Elgammal/agentgw/internal/gateway"
	"github.com/Moustafa-Elgammal/agentgw/internal/manifest"
	"github.com/Moustafa-Elgammal/agentgw/internal/policy"
)

func server(t *testing.T) *httptest.Server {
	t.Helper()
	be := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"coins":100}`))
	}))
	t.Cleanup(be.Close)
	m, err := manifest.Parse(strings.NewReader(`{"version":1,"tools":[
	 {"name":"get_state","description":"State.","tier":"read","http":{"method":"GET","url":"/state"}}]}`))
	if err != nil {
		t.Fatal(err)
	}
	svc := gateway.New(m, policy.NewMemory(policy.DefaultLimits, nil), connector.NewHTTP(be.URL, "k", []byte("s")), &audit.Memory{})
	srv := httptest.NewServer(New(svc, "demo", nil))
	t.Cleanup(srv.Close)
	return srv
}

func do(t *testing.T, method, url, player, body string) (int, string) {
	t.Helper()
	req, _ := http.NewRequest(method, url, strings.NewReader(body))
	if player != "" {
		req.Header.Set("X-Dev-Player", player)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func TestEndpoints(t *testing.T) {
	s := server(t)

	if code, _ := do(t, "GET", s.URL+"/healthz", "", ""); code != 200 {
		t.Errorf("healthz = %d", code)
	}
	if code, body := do(t, "GET", s.URL+"/v1/tools", "", ""); code != 200 || !strings.Contains(body, "get_state") {
		t.Errorf("tools = %d %s", code, body)
	}
	if code, body := do(t, "POST", s.URL+"/v1/tools/get_state/call", "p1", `{"arguments":{}}`); code != 200 || !strings.Contains(body, `"ok":true`) {
		t.Errorf("call = %d %s", code, body)
	}
	if code, body := do(t, "POST", s.URL+"/v1/tools/get_state/call", "", `{}`); code != 401 || !strings.Contains(body, "no_player") {
		t.Errorf("no player = %d %s", code, body)
	}
	if code, body := do(t, "POST", s.URL+"/v1/tools/nope/call", "p1", `{}`); code != 422 || !strings.Contains(body, "unknown_tool") {
		t.Errorf("unknown = %d %s", code, body)
	}
	if code, _ := do(t, "POST", s.URL+"/v1/tools/get_state/call", "p1", `not json`); code != 400 {
		t.Errorf("bad json = %d", code)
	}
}

func TestConfirmationEndpoints(t *testing.T) {
	be := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`{"built":true}`)) }))
	defer be.Close()
	m, err := manifest.Parse(strings.NewReader(`{"version":1,"tools":[
	 {"name":"build","description":"Build.","tier":"medium","confirm":true,"http":{"method":"POST","url":"/build"}}]}`))
	if err != nil {
		t.Fatal(err)
	}
	store := confirm.NewMemory(nil)
	svc := gateway.New(m, policy.NewMemory(policy.DefaultLimits, nil), connector.NewHTTP(be.URL, "k", []byte("s")), &audit.Memory{}, gateway.WithConfirmations(store))
	srv := httptest.NewServer(New(svc, "demo", store))
	defer srv.Close()

	// The agent asks and is told to wait for the player.
	code, body := do(t, "POST", srv.URL+"/v1/tools/build/call", "alice", `{"arguments":{}}`)
	if code != 422 || !strings.Contains(body, "confirmation_required") {
		t.Fatalf("call = %d %s", code, body)
	}
	// The player sees it, and only the player.
	code, body = do(t, "GET", srv.URL+"/v1/confirmations", "alice", "")
	if code != 200 || !strings.Contains(body, `"tool":"build"`) {
		t.Fatalf("list = %d %s", code, body)
	}
	var list struct {
		C []struct{ ID string } `json:"confirmations"`
	}
	if err := json.Unmarshal([]byte(body), &list); err != nil || len(list.C) != 1 {
		t.Fatalf("decode: %v %s", err, body)
	}
	id := list.C[0].ID
	if _, body := do(t, "GET", srv.URL+"/v1/confirmations", "bob", ""); strings.Contains(body, id) {
		t.Fatal("bob must not see alice's confirmation")
	}
	if code, _ := do(t, "POST", srv.URL+"/v1/confirmations/"+id+"/approve", "bob", ""); code != 404 {
		t.Errorf("bob approving = %d, want 404", code)
	}
	if code, _ := do(t, "POST", srv.URL+"/v1/confirmations/"+id+"/approve", "", ""); code != 401 {
		t.Errorf("no player = %d, want 401", code)
	}
	if code, body := do(t, "POST", srv.URL+"/v1/confirmations/"+id+"/approve", "alice", ""); code != 200 {
		t.Fatalf("approve = %d %s", code, body)
	}
	// The agent repeats the call and it runs.
	if code, body := do(t, "POST", srv.URL+"/v1/tools/build/call", "alice", `{"arguments":{}}`); code != 200 || !strings.Contains(body, `"ok":true`) {
		t.Fatalf("approved call = %d %s", code, body)
	}
	if code, _ := do(t, "POST", srv.URL+"/v1/confirmations/"+id+"/deny", "alice", ""); code != 409 {
		t.Errorf("denying a consumed confirmation = %d, want 409", code)
	}
}

func TestConfirmationEndpointsOffWithoutStore(t *testing.T) {
	s := server(t)
	if code, _ := do(t, "GET", s.URL+"/v1/confirmations", "p1", ""); code != 501 {
		t.Errorf("code = %d, want 501", code)
	}
}
