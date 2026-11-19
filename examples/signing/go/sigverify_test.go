package sigverify_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Moustafa-Elgammal/agentgw/examples/signing/go"
	"github.com/Moustafa-Elgammal/agentgw/pkg/agentsig"
)

type vec struct {
	KeyID       string `json:"key_id"`
	Secret      string `json:"secret"`
	Timestamp   int64  `json:"timestamp"`
	Method      string `json:"method"`
	Path        string `json:"path"`
	Player      string `json:"player"`
	Idempotency string `json:"idempotency_key"`
	Body        string `json:"body"`
	Signature   string `json:"signature"`
}

func load(t *testing.T) vec {
	t.Helper()
	b, err := os.ReadFile("../vector.json")
	if err != nil {
		t.Fatal(err)
	}
	var v vec
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatal(err)
	}
	return v
}

// request rebuilds the published request, with the signature taken from the vector file.
func request(v vec) *http.Request {
	r := httptest.NewRequest(v.Method, v.Path, strings.NewReader(v.Body))
	r.Header.Set(agentsig.HeaderKeyID, v.KeyID)
	r.Header.Set(agentsig.HeaderTimestamp, "1767268800")
	r.Header.Set(agentsig.HeaderPlayer, v.Player)
	r.Header.Set(agentsig.HeaderIdempotency, v.Idempotency)
	r.Header.Set(agentsig.HeaderSignature, v.Signature)
	return r
}

func setup(t *testing.T, v vec, now time.Time) (http.Handler, *atomic.Int32) {
	t.Helper()
	var calls atomic.Int32
	m := &sigverify.Middleware{
		SecretFor: func(id string) []byte {
			if id == v.KeyID {
				return []byte(v.Secret)
			}
			return nil
		},
		Now: func() time.Time { return now },
	}
	return m.Wrap(func(w http.ResponseWriter, _ *http.Request, player string, _ []byte) {
		n := calls.Add(1)
		_, _ = w.Write([]byte(`{"player":"` + player + `","run":` + string(rune('0'+n)) + `}`))
	}), &calls
}

func serve(h http.Handler, r *http.Request) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestPublishedVector(t *testing.T) {
	v := load(t)
	h, calls := setup(t, v, time.Unix(v.Timestamp, 0))
	w := serve(h, request(v))
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"player":"player-7"`) || calls.Load() != 1 {
		t.Fatalf("vector refused: %d %s", w.Code, w.Body)
	}
}

func TestRejections(t *testing.T) {
	v := load(t)
	tests := []struct {
		name   string
		change func(r *http.Request)
		now    time.Time
	}{
		{"tampered signature", func(r *http.Request) { r.Header.Set(agentsig.HeaderSignature, strings.Repeat("0", 64)) }, time.Unix(v.Timestamp, 0)},
		{"tampered player", func(r *http.Request) { r.Header.Set(agentsig.HeaderPlayer, "mallory") }, time.Unix(v.Timestamp, 0)},
		{"tampered path", func(r *http.Request) { r.URL.RawQuery = "src=other" }, time.Unix(v.Timestamp, 0)},
		{"unknown key", func(r *http.Request) { r.Header.Set(agentsig.HeaderKeyID, "nope") }, time.Unix(v.Timestamp, 0)},
		{"missing header", func(r *http.Request) { r.Header.Del(agentsig.HeaderIdempotency) }, time.Unix(v.Timestamp, 0)},
		{"six minutes late", func(*http.Request) {}, time.Unix(v.Timestamp, 0).Add(6 * time.Minute)},
		{"six minutes early", func(*http.Request) {}, time.Unix(v.Timestamp, 0).Add(-6 * time.Minute)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, calls := setup(t, v, tt.now)
			r := request(v)
			tt.change(r)
			if w := serve(h, r); w.Code != http.StatusUnauthorized || calls.Load() != 0 {
				t.Fatalf("got %d, handler ran %d times", w.Code, calls.Load())
			}
		})
	}
}

func TestTamperedBody(t *testing.T) {
	v := load(t)
	h, calls := setup(t, v, time.Unix(v.Timestamp, 0))
	v.Body = strings.Replace(v.Body, `"amount":2`, `"amount":9`, 1)
	if w := serve(h, request(v)); w.Code != http.StatusUnauthorized || calls.Load() != 0 {
		t.Fatalf("tampered body accepted: %d", w.Code)
	}
}

func TestReplayedIdempotencyKeyReturnsStoredResult(t *testing.T) {
	v := load(t)
	h, calls := setup(t, v, time.Unix(v.Timestamp, 0))
	first := serve(h, request(v))
	second := serve(h, request(v))
	if calls.Load() != 1 {
		t.Fatalf("handler ran %d times for one idempotency key", calls.Load())
	}
	if first.Body.String() != second.Body.String() || second.Header().Get("X-Idempotent-Replay") != "true" {
		t.Fatalf("repeat differs: %q vs %q", first.Body, second.Body)
	}
}
