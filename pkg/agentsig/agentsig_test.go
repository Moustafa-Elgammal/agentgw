package agentsig

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

var (
	secret = []byte("test-secret")
	now    = time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	look   = func(id string) []byte {
		if id == "k1" {
			return secret
		}
		return nil
	}
)

func signed(body string) (*http.Request, []byte) {
	req := httptest.NewRequest("POST", "/api/v1/market/sell?x=1", strings.NewReader(body))
	Sign(req, "k1", secret, "player-7", "idem-1", []byte(body), now)
	return req, []byte(body)
}

func TestRoundTrip(t *testing.T) {
	req, body := signed(`{"quantity":5}`)
	player, err := Verify(req, body, look, now.Add(time.Second), 0)
	if err != nil || player != "player-7" {
		t.Fatalf("got %q, %v", player, err)
	}
}

func TestTamperingIsRejected(t *testing.T) {
	req, body := signed(`{"quantity":5}`)

	if _, err := Verify(req, []byte(`{"quantity":500}`), look, now, 0); !errors.Is(err, ErrBadSignature) {
		t.Errorf("changed body: %v", err)
	}
	req.Header.Set(HeaderPlayer, "player-8")
	if _, err := Verify(req, body, look, now, 0); !errors.Is(err, ErrBadSignature) {
		t.Errorf("changed player: %v", err)
	}
	req2, body2 := signed(`{}`)
	req2.URL.Path = "/api/v1/admin"
	if _, err := Verify(req2, body2, look, now, 0); !errors.Is(err, ErrBadSignature) {
		t.Errorf("changed path: %v", err)
	}
}

func TestSkewUnknownKeyAndMissingHeaders(t *testing.T) {
	req, body := signed(`{}`)
	if _, err := Verify(req, body, look, now.Add(10*time.Minute), 0); !errors.Is(err, ErrBadTimestamp) {
		t.Errorf("old timestamp: %v", err)
	}
	if _, err := Verify(req, body, func(string) []byte { return nil }, now, 0); !errors.Is(err, ErrBadSignature) {
		t.Errorf("unknown key: %v", err)
	}
	req.Header.Del(HeaderSignature)
	if _, err := Verify(req, body, look, now, 0); !errors.Is(err, ErrMissingHeader) {
		t.Errorf("missing header: %v", err)
	}
}
