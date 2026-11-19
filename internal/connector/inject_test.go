package connector

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/Moustafa-Elgammal/agentgw/internal/core"
)

type seen struct {
	rawPath, rawQuery string
	header            http.Header
}

func capture(t *testing.T) (*HTTP, func() seen) {
	t.Helper()
	var mu sync.Mutex
	var got seen
	srv := backend(t, func(w http.ResponseWriter, r *http.Request, _ []byte, _ string) {
		mu.Lock()
		got = seen{rawPath: r.URL.EscapedPath(), rawQuery: r.URL.RawQuery, header: r.Header.Clone()}
		mu.Unlock()
		_, _ = w.Write([]byte(`{}`))
	})
	t.Cleanup(srv.Close)
	return NewHTTP(srv.URL, "k1", secret, AllowPrivate()), func() seen {
		mu.Lock()
		defer mu.Unlock()
		return got
	}
}

func TestPathPlaceholderCannotChangeThePath(t *testing.T) {
	tool := core.Tool{Name: "b", HTTP: core.HTTPBinding{Method: "GET", URL: "/api/buildings/{id}/info"}}
	tests := []struct {
		name, value string
		wantPath    string // empty: the call must be refused
	}{
		{"plain", "12", "/api/buildings/12/info"},
		{"slash", "a/b", "/api/buildings/a%2Fb/info"},
		{"dot dot slash", "../admin", "/api/buildings/..%2Fadmin/info"},
		{"encoded slash", "a%2fb", "/api/buildings/a%252fb/info"},
		{"encoded dots", "%2e%2e", "/api/buildings/%252e%252e/info"},
		{"backslash", `..\admin`, "/api/buildings/..%5Cadmin/info"},
		{"question mark", "1?admin=1", "/api/buildings/1%3Fadmin=1/info"},
		{"hash", "1#frag", "/api/buildings/1%23frag/info"},
		{"crlf", "1\r\nX-Admin: 1", "/api/buildings/1%0D%0AX-Admin:%201/info"},
		{"null byte", "1\x00", "/api/buildings/1%00/info"},
		{"unicode", "é☃", "/api/buildings/%C3%A9%E2%98%83/info"},
		{"space", "a b", "/api/buildings/a%20b/info"},
		{"single dot", ".", ""},
		{"double dot", "..", ""},
		{"empty", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, last := capture(t)
			res, err := c.Call(context.Background(), player, tool, map[string]any{"id": tt.value}, "i1")
			if err != nil {
				t.Fatal(err)
			}
			if tt.wantPath == "" {
				if res.OK || res.Code != "invalid_arguments" {
					t.Fatalf("not refused: %+v", res)
				}
				return
			}
			if !res.OK {
				t.Fatalf("refused: %+v", res)
			}
			got := last()
			if got.rawPath != tt.wantPath {
				t.Fatalf("path = %q, want %q", got.rawPath, tt.wantPath)
			}
			if got.header.Get("X-Admin") != "" {
				t.Fatal("a header was injected")
			}
		})
	}
}

func TestQueryValuesStayValues(t *testing.T) {
	tool := core.Tool{Name: "q", HTTP: core.HTTPBinding{Method: "GET", URL: "/api/search"}}
	tests := []struct{ name, value string }{
		{"ampersand", "a&admin=1"}, {"equals", "a=b"}, {"hash", "a#b"}, {"percent", "100%"},
		{"encoded ampersand", "a%26admin=1"}, {"crlf", "a\r\nX-Admin: 1"}, {"plus", "a+b"}, {"semicolon", "a;admin=1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, last := capture(t)
			if res, err := c.Call(context.Background(), player, tool, map[string]any{"term": tt.value}, "i1"); err != nil || !res.OK {
				t.Fatalf("res=%+v err=%v", res, err)
			}
			got := last()
			q, err := url.ParseQuery(got.rawQuery)
			if err != nil {
				t.Fatal(err)
			}
			if len(q) != 1 || q.Get("term") != tt.value {
				t.Fatalf("query = %q parsed to %v, want one param term=%q", got.rawQuery, q, tt.value)
			}
			if got.header.Get("X-Admin") != "" {
				t.Fatal("a header was injected")
			}
		})
	}
}

func TestUnsafeIdentifiersAreRefusedBeforeSigning(t *testing.T) {
	tests := []struct {
		name, player, idem string
		ok                 bool
	}{
		{"normal", "alice", "abc123", true},
		{"max length", strings.Repeat("a", 128), "i", true},
		{"crlf player", "alice\r\nX-Admin: 1", "i", false},
		{"lf player", "alice\nX", "i", false},
		{"null player", "al\x00ice", "i", false},
		{"tab player", "al\tice", "i", false},
		{"del player", "al\x7fice", "i", false},
		{"non ascii player", "alicé", "i", false},
		{"empty player", "", "i", false},
		{"long player", strings.Repeat("a", 129), "i", false},
		{"crlf idem", "alice", "i\r\nX: 1", false},
		{"empty idem", "alice", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, last := capture(t)
			p := core.Principal{TenantID: "t", PlayerID: tt.player}
			res, err := c.Call(context.Background(), p, core.Tool{Name: "x", HTTP: core.HTTPBinding{Method: "GET", URL: "/x"}}, nil, tt.idem)
			if err != nil {
				t.Fatal(err)
			}
			if res.OK != tt.ok {
				t.Fatalf("res=%+v, want ok=%v", res, tt.ok)
			}
			if !tt.ok && (res.Code != "invalid_principal" || last().header != nil) {
				t.Fatalf("refused call still reached the backend or wrong code: %+v", res)
			}
		})
	}
}

func TestBodyArgsAreOnlyData(t *testing.T) {
	var body string
	srv := backend(t, func(w http.ResponseWriter, _ *http.Request, b []byte, _ string) {
		body = string(b)
		_, _ = w.Write([]byte(`{}`))
	})
	defer srv.Close()
	c := NewHTTP(srv.URL, "k1", secret, AllowPrivate())
	tool := core.Tool{Name: "p", HTTP: core.HTTPBinding{Method: "POST", URL: "/api/{id}"}}
	evil := `"}, "admin": true, "x": "`
	if res, err := c.Call(context.Background(), player, tool, map[string]any{"id": "7", "note": evil}, "i1"); err != nil || !res.OK {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	if strings.Contains(body, `"admin":true`) || !strings.Contains(body, `\"}, \"admin\": true`) {
		t.Fatalf("body = %s", body)
	}
}
