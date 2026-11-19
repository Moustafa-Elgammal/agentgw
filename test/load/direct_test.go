package main

import (
	"context"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/Moustafa-Elgammal/agentgw/pkg/agentsig"
)

func TestDirectRequestIsSignedLikeTheGateway(t *testing.T) {
	d := directSigner{keyID: "dev-1", secret: []byte("s3cret")}
	tests := []struct {
		name, tool, args, method, path, body string
	}{
		{"read", "get_city_state", `{}`, http.MethodGet, "/api/v1/city", ""},
		{"action with body", "sell_resource", `{"resource":"food","quantity":1}`, http.MethodPost, "/api/v1/market/sell", `{"resource":"food","quantity":1}`},
		{"post without arguments", "collect_taxes", `{}`, http.MethodPost, "/api/v1/taxes/collect", `{}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req, err := d.request(context.Background(), "http://backend:9090/", "load-7", tt.tool, tt.args, "idem-1")
			if err != nil {
				t.Fatal(err)
			}
			if req.Method != tt.method || req.URL.Path != tt.path || req.URL.Host != "backend:9090" {
				t.Fatalf("request = %s %s", req.Method, req.URL)
			}
			var body []byte
			if req.Body != nil {
				body, _ = io.ReadAll(req.Body)
			}
			if string(body) != tt.body {
				t.Fatalf("body = %q, want %q", body, tt.body)
			}
			// The backend must accept it, and refuse the same request under another secret.
			secretFor := func(id string) []byte {
				if id == "dev-1" {
					return []byte("s3cret")
				}
				return nil
			}
			player, err := agentsig.Verify(req, body, secretFor, time.Now(), 0)
			if err != nil || player != "load-7" {
				t.Fatalf("verify: %q %v", player, err)
			}
			if _, err := agentsig.Verify(req, body, func(string) []byte { return []byte("other") }, time.Now(), 0); err == nil {
				t.Fatal("a wrong secret must fail")
			}
		})
	}
}

func TestDirectRequestUnknownTool(t *testing.T) {
	if _, err := (directSigner{keyID: "k", secret: []byte("s")}).request(context.Background(), "http://x", "p", "nope", `{}`, "i"); err == nil {
		t.Fatal("an unknown tool must be refused")
	}
}
