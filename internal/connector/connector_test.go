package connector

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Moustafa-Elgammal/agentgw/internal/core"
	"github.com/Moustafa-Elgammal/agentgw/pkg/agentsig"
)

var (
	secret = []byte("s3cret")
	player = core.Principal{TenantID: "t", PlayerID: "p1"}
)

func backend(t *testing.T, h func(w http.ResponseWriter, r *http.Request, body []byte, player string)) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		pl, err := agentsig.Verify(r, body, func(id string) []byte {
			if id == "k1" {
				return secret
			}
			return nil
		}, time.Now(), 0)
		if err != nil {
			http.Error(w, err.Error(), http.StatusUnauthorized)
			return
		}
		h(w, r, body, pl)
	}))
}

func TestPostIsSignedAndCarriesJSON(t *testing.T) {
	srv := backend(t, func(w http.ResponseWriter, r *http.Request, body []byte, pl string) {
		if r.Method != "POST" || r.URL.Path != "/api/sell" || pl != "p1" {
			t.Errorf("unexpected request %s %s player=%s", r.Method, r.URL.Path, pl)
		}
		if string(body) != `{"quantity":5,"resource":"food"}` {
			t.Errorf("body = %s", body)
		}
		_, _ = w.Write([]byte(`{"earned":10}`))
	})
	defer srv.Close()

	c := NewHTTP(srv.URL, "k1", secret, AllowPrivate())
	tool := core.Tool{Name: "sell", HTTP: core.HTTPBinding{Method: "POST", URL: "/api/sell"}}
	res, err := c.Call(context.Background(), player, tool, map[string]any{"resource": "food", "quantity": 5}, "idem-1")
	if err != nil || !res.OK || string(res.Data) != `{"earned":10}` {
		t.Fatalf("res=%+v err=%v", res, err)
	}
}

func TestGetUsesPathPlaceholderAndQuery(t *testing.T) {
	srv := backend(t, func(w http.ResponseWriter, r *http.Request, _ []byte, _ string) {
		if r.URL.Path != "/api/buildings/12" || r.URL.Query().Get("verbose") != "true" {
			t.Errorf("url = %s", r.URL)
		}
		_, _ = w.Write([]byte(`{"id":12}`))
	})
	defer srv.Close()

	c := NewHTTP(srv.URL, "k1", secret, AllowPrivate())
	tool := core.Tool{Name: "get_building", HTTP: core.HTTPBinding{Method: "GET", URL: "/api/buildings/{id}"}}
	res, err := c.Call(context.Background(), player, tool, map[string]any{"id": 12, "verbose": true}, "i")
	if err != nil || !res.OK {
		t.Fatalf("res=%+v err=%v", res, err)
	}
}

func TestBackendStatusMapping(t *testing.T) {
	status := 0
	srv := backend(t, func(w http.ResponseWriter, _ *http.Request, _ []byte, _ string) {
		w.WriteHeader(status)
		_, _ = w.Write([]byte("nope"))
	})
	defer srv.Close()
	c := NewHTTP(srv.URL, "k1", secret, AllowPrivate())
	tool := core.Tool{Name: "x", HTTP: core.HTTPBinding{Method: "POST", URL: "/a"}}

	status = 422
	res, err := c.Call(context.Background(), player, tool, nil, "i")
	if err != nil || res.OK || res.Code != "backend_rejected" || res.Message != "nope" {
		t.Errorf("4xx: res=%+v err=%v", res, err)
	}
	status = 500
	if _, err := c.Call(context.Background(), player, tool, nil, "i"); err == nil {
		t.Error("5xx must be a Go error so the caller fails closed")
	}
}

func TestUnreachableBackendIsAnError(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL
	srv.Close()
	c := NewHTTP(url, "k1", secret, AllowPrivate())
	tool := core.Tool{Name: "x", HTTP: core.HTTPBinding{Method: "GET", URL: "/a"}}
	if _, err := c.Call(context.Background(), player, tool, nil, "i"); err == nil {
		t.Fatal("expected error")
	}
}
