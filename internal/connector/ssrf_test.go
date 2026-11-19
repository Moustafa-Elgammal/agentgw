package connector

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Moustafa-Elgammal/agentgw/internal/core"
)

func TestBlockedIP(t *testing.T) {
	tests := []struct {
		ip      string
		blocked bool
	}{
		{"127.0.0.1", true}, {"127.1.2.3", true}, {"::1", true},
		{"10.0.0.1", true}, {"172.16.5.5", true}, {"172.31.255.255", true}, {"192.168.1.1", true},
		{"fd00::1", true}, {"fc00::1", true},
		{"169.254.169.254", true}, {"169.254.0.1", true}, {"fe80::1", true},
		{"0.0.0.0", true}, {"0.1.2.3", true}, {"::", true},
		{"224.0.0.1", true}, {"ff02::1", true},
		{"100.64.0.1", true}, {"100.127.255.255", true},
		{"192.0.0.1", true}, {"198.18.0.1", true}, {"240.0.0.1", true}, {"255.255.255.255", true},
		{"::ffff:127.0.0.1", true}, {"::ffff:10.0.0.1", true}, {"::ffff:169.254.169.254", true},
		{"64:ff9b::a00:1", true}, {"2002:a00:1::1", true}, {"2001:db8::1", true},
		{"fe80::1%eth0", true},
		{"8.8.8.8", false}, {"1.1.1.1", false}, {"172.32.0.1", false}, {"100.63.255.255", false},
		{"100.128.0.1", false}, {"93.184.216.34", false}, {"::ffff:8.8.8.8", false},
		{"2606:4700:4700::1111", false},
	}
	for _, tt := range tests {
		t.Run(tt.ip, func(t *testing.T) {
			ip, err := netip.ParseAddr(tt.ip)
			if err != nil {
				t.Fatal(err)
			}
			if got := BlockedIP(ip); got != tt.blocked {
				t.Fatalf("BlockedIP(%s) = %v, want %v", tt.ip, got, tt.blocked)
			}
		})
	}
}

var getTool = core.Tool{Name: "x", HTTP: core.HTTPBinding{Method: "GET", URL: "/x"}}

func TestDefaultConnectorRefusesPrivateBackends(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { hits.Add(1) }))
	defer srv.Close() // listens on 127.0.0.1

	tests := []struct {
		name string
		url  string
	}{
		{"loopback", srv.URL},
		{"localhost name", strings.Replace(srv.URL, "127.0.0.1", "localhost", 1)},
		{"mapped v6 loopback", strings.Replace(srv.URL, "127.0.0.1", "[::ffff:127.0.0.1]", 1)},
		{"metadata address", "http://169.254.169.254"},
		{"private", "http://10.0.0.5:8080"},
		{"unspecified", "http://0.0.0.0:80"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := NewHTTP(tt.url, "k1", secret)
			_, err := c.Call(context.Background(), player, getTool, nil, "i1")
			if !errors.Is(err, ErrBlockedAddress) {
				t.Fatalf("err = %v, want ErrBlockedAddress", err)
			}
		})
	}
	if hits.Load() != 0 {
		t.Fatalf("the backend was reached %d times", hits.Load())
	}
}

func TestAllowPrivateReachesLoopback(t *testing.T) {
	srv := backend(t, func(w http.ResponseWriter, _ *http.Request, _ []byte, _ string) { _, _ = w.Write([]byte(`{}`)) })
	defer srv.Close()
	res, err := NewHTTP(srv.URL, "k1", secret, AllowPrivate()).Call(context.Background(), player, getTool, nil, "i1")
	if err != nil || !res.OK {
		t.Fatalf("res=%+v err=%v", res, err)
	}
}

// Redirects are never followed, with or without AllowPrivate: the signed request must not be
// replayed against a host the operator did not configure.
func TestRedirectsAreNotFollowed(t *testing.T) {
	var target atomic.Int32
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { target.Add(1) }))
	defer other.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL+"/steal", http.StatusFound)
	}))
	defer srv.Close()
	res, err := NewHTTP(srv.URL, "k1", secret, AllowPrivate()).Call(context.Background(), player, getTool, nil, "i1")
	if err == nil && res.OK {
		t.Fatalf("a redirect was treated as success: %+v", res)
	}
	if target.Load() != 0 {
		t.Fatal("the redirect target was contacted")
	}
}
