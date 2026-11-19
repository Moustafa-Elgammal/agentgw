package bodylimit_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Moustafa-Elgammal/agentgw/internal/audit"
	"github.com/Moustafa-Elgammal/agentgw/internal/auth"
	"github.com/Moustafa-Elgammal/agentgw/internal/bodylimit"
	"github.com/Moustafa-Elgammal/agentgw/internal/confirm"
	"github.com/Moustafa-Elgammal/agentgw/internal/gateway"
	"github.com/Moustafa-Elgammal/agentgw/internal/httpapi"
	"github.com/Moustafa-Elgammal/agentgw/internal/manifest"
	"github.com/Moustafa-Elgammal/agentgw/internal/manifeststore"
	"github.com/Moustafa-Elgammal/agentgw/internal/mcpserver"
	"github.com/Moustafa-Elgammal/agentgw/internal/policy"
	"github.com/Moustafa-Elgammal/agentgw/internal/studiosig"
)

const doc = `{"version":1,"tools":[{"name":"get_state","description":"State.","tier":"read","http":{"method":"GET","url":"/state"}}]}`

// The real front doors behind the real middleware: an oversized body gets a clean 413 on each
// one, a normal request still reaches its handler, and the manifest upload keeps its larger cap.
func TestFrontDoorsRefuseOversizedBodies(t *testing.T) {
	m, err := manifest.Parse(strings.NewReader(doc))
	if err != nil {
		t.Fatal(err)
	}
	svc := gateway.New(m, policy.NewMemory(policy.DefaultLimits, nil), nil, &audit.Memory{})
	as := auth.NewServer(auth.ServerConfig{Store: auth.NewMemory(nil), Issuer: "https://gw.example", Resource: "https://gw.example/mcp",
		Tenant: "t", StudioKeyID: "s1", StudioSecret: []byte("secret")})
	mgr := manifeststore.NewManager(manifeststore.NewMemory(nil), "t", svc)
	admin := studiosig.New(studiosig.Config{KeyID: "a1", Secret: []byte("admin"), MaxBody: manifeststore.MaxUpload})

	mux := http.NewServeMux()
	mux.Handle("/mcp", mcpserver.Handler(svc, mcpserver.DevAuth("t")))
	mux.Handle("/v1/studio/manifest", manifeststore.Handler(mgr, admin))
	mux.Handle("/v1/studio/confirmations", confirm.StudioHandler(confirm.StudioConfig{Store: confirm.NewMemory(nil), Tenant: "t", KeyID: "s1", Secret: []byte("secret")}))
	mux.Handle("/", as.Handler())
	mux.Handle("POST /v1/tools/", httpapi.New(svc, httpapi.Config{Tenant: "t"}))
	srv := httptest.NewServer(bodylimit.FrontDoor(mux, 1<<20))
	defer srv.Close()

	post := func(path, ctype string, size int) int {
		method := "POST"
		if path == "/v1/studio/manifest" {
			method = "PUT"
		}
		req, _ := http.NewRequest(method, srv.URL+path, strings.NewReader(strings.Repeat("a", size)))
		req.Header.Set("Content-Type", ctype)
		req.Header.Set("X-Dev-Player", "p1")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		return resp.StatusCode
	}
	tests := []struct {
		name, path, ctype string
		size              int
		want413           bool
	}{
		{"mcp over", "/mcp", "application/json", 1<<20 + 1, true},
		{"mcp normal", "/mcp", "application/json", 100, false},
		{"dev api over", "/v1/tools/get_state/call", "application/json", 1<<20 + 1, true},
		{"dev api normal", "/v1/tools/get_state/call", "application/json", 10, false},
		{"register over", "/register", "application/json", bodylimit.Small + 1, true},
		{"register normal", "/register", "application/json", 50, false},
		{"token over", "/token", "application/x-www-form-urlencoded", bodylimit.Small + 1, true},
		{"token normal", "/token", "application/x-www-form-urlencoded", 20, false},
		{"authorize over", "/authorize", "application/x-www-form-urlencoded", bodylimit.Small + 1, true},
		{"authorize normal", "/authorize", "application/x-www-form-urlencoded", 20, false},
		{"studio over", "/v1/studio/confirmations", "application/json", bodylimit.Small + 1, true},
		{"studio normal (unsigned, so 401 not 413)", "/v1/studio/confirmations", "application/json", 10, false},
		// The upload cap is the handler's own (1 MiB), well above the 64 KiB studio cap.
		{"manifest 200 KiB gets past the front door", "/v1/studio/manifest", "application/json", 200 << 10, false},
		{"manifest over its own cap", "/v1/studio/manifest", "application/json", manifeststore.MaxUpload + 1, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			status := post(tt.path, tt.ctype, tt.size)
			if tt.want413 && status != http.StatusRequestEntityTooLarge {
				t.Fatalf("status %d, want 413", status)
			}
			if !tt.want413 && status == http.StatusRequestEntityTooLarge {
				t.Fatalf("a normal request was refused with 413")
			}
		})
	}
}
