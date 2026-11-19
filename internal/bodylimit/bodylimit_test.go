package bodylimit_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Moustafa-Elgammal/agentgw/internal/bodylimit"
)

func TestMiddleware(t *testing.T) {
	var got int
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got = len(b)
		w.WriteHeader(http.StatusOK)
	})
	h := bodylimit.Middleware(next, 100,
		bodylimit.Rule{Prefix: "/token", Max: 10},
		bodylimit.Rule{Prefix: "/v1/studio/", Max: 20},
		bodylimit.Rule{Prefix: "/v1/studio/manifest", Max: 0},
	)
	tests := []struct {
		name, path string
		size       int
		chunked    bool // hide the length so only the read cap can stop it
		want       int
	}{
		{"default ok", "/mcp", 100, false, 200},
		{"default over", "/mcp", 101, false, 413},
		{"default over, no length", "/mcp", 101, true, 413},
		{"token ok", "/token", 10, false, 200},
		{"token over", "/token", 11, false, 413},
		{"studio over", "/v1/studio/grants", 21, false, 413},
		{"manifest exempt", "/v1/studio/manifest", 5000, false, 200},
		{"empty body", "/mcp", 0, false, 200},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got = -1
			var body io.Reader = strings.NewReader(strings.Repeat("a", tt.size))
			if tt.chunked {
				body = io.MultiReader(body) // not a *strings.Reader, so ContentLength is unknown
			}
			r := httptest.NewRequest("POST", tt.path, body)
			if tt.chunked {
				r.ContentLength = -1
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != tt.want {
				t.Fatalf("status %d, want %d", w.Code, tt.want)
			}
			if tt.want == 413 {
				if got != -1 {
					t.Error("handler ran for an oversized body")
				}
				if ct := w.Header().Get("Content-Type"); ct != "application/json" || !strings.Contains(w.Body.String(), `"too_large"`) {
					t.Errorf("not the JSON error shape: %q %q", ct, w.Body.String())
				}
			} else if got != tt.size {
				t.Errorf("handler read %d bytes, want %d", got, tt.size)
			}
		})
	}
}
