package httpapi

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Moustafa-Elgammal/agentgw/internal/audit"
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
	srv := httptest.NewServer(New(svc, "demo"))
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
