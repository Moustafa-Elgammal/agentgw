package studiosig_test

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Moustafa-Elgammal/agentgw/internal/studiosig"
	"github.com/Moustafa-Elgammal/agentgw/pkg/agentsig"
)

const keyID = "studio-1"

var secret = []byte("studio-secret")

type clock struct{ t atomic.Int64 }

func newClock() *clock {
	c := &clock{}
	c.t.Store(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC).Unix())
	return c
}
func (c *clock) now() time.Time          { return time.Unix(c.t.Load(), 0) }
func (c *clock) advance(d time.Duration) { c.t.Add(int64(d.Seconds())) }

func newVerifier(c *clock, max int) *studiosig.Verifier {
	return studiosig.New(studiosig.Config{KeyID: keyID, Secret: secret, Now: c.now, MaxEntries: max})
}

func req(method, player, idem string, at time.Time) *http.Request {
	r := httptest.NewRequest(method, "/v1/studio/x", nil)
	agentsig.Sign(r, keyID, secret, player, idem, nil, at)
	return r
}

func verify(v *studiosig.Verifier, r *http.Request) (string, error) {
	p, _, err := v.Verify(httptest.NewRecorder(), r)
	return p, err
}

func TestReplayTable(t *testing.T) {
	tests := []struct {
		name   string
		second func(c *clock) *http.Request
		want   error
	}{
		{"same request again", func(c *clock) *http.Request { return req("POST", "alice", "k1", c.now()) }, studiosig.ErrReplay},
		{"same key other method", func(c *clock) *http.Request { return req("GET", "alice", "k1", c.now()) }, studiosig.ErrReplay},
		{"same key other player", func(c *clock) *http.Request { return req("POST", "bob", "k1", c.now()) }, studiosig.ErrReplay},
		{"same key later timestamp", func(c *clock) *http.Request { return req("POST", "alice", "k1", c.now().Add(time.Minute)) }, studiosig.ErrReplay},
		{"fresh key", func(c *clock) *http.Request { return req("POST", "alice", "k2", c.now()) }, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := newClock()
			v := newVerifier(c, 0)
			if p, err := verify(v, req("POST", "alice", "k1", c.now())); err != nil || p != "alice" {
				t.Fatalf("first: %q %v", p, err)
			}
			if _, err := verify(v, tt.second(c)); !errors.Is(err, tt.want) {
				t.Fatalf("got %v, want %v", err, tt.want)
			}
		})
	}
}

func TestBadSignatureNeverEntersCache(t *testing.T) {
	c := newClock()
	v := newVerifier(c, 1)
	bad := req("POST", "alice", "k1", c.now())
	bad.Header.Set(agentsig.HeaderPlayer, "mallory") // signature no longer matches
	if _, err := verify(v, bad); !errors.Is(err, studiosig.ErrUnauthorized) {
		t.Fatalf("got %v", err)
	}
	// The same key, properly signed, still works: the failed attempt left no trace.
	if _, err := verify(v, req("POST", "alice", "k1", c.now())); err != nil {
		t.Fatalf("legit request refused after bad one: %v", err)
	}
}

func TestMissingPlayerAndLargeBody(t *testing.T) {
	c := newClock()
	v := newVerifier(c, 0)
	r := req("POST", "alice", "k1", c.now())
	r.Header.Del(agentsig.HeaderPlayer)
	if _, err := verify(v, r); !errors.Is(err, studiosig.ErrUnauthorized) {
		t.Fatalf("missing player: %v", err)
	}
	big := strings.Repeat("a", studiosig.MaxBody+1)
	r = httptest.NewRequest("POST", "/x", strings.NewReader(big))
	agentsig.Sign(r, keyID, secret, "alice", "k2", []byte(big), c.now())
	if _, err := verify(v, r); !errors.Is(err, studiosig.ErrTooLarge) {
		t.Fatalf("large body: %v", err)
	}
	if _, err := verify(v, req("POST", "alice", "k2", c.now())); err != nil {
		t.Fatalf("a refused request must not burn its key: %v", err)
	}
}

func TestEntriesExpire(t *testing.T) {
	tests := []struct {
		name    string
		advance time.Duration
		want    error
	}{
		{"inside window", agentsig.DefaultMaxSkew, studiosig.ErrReplay},
		{"just before the end", 2*agentsig.DefaultMaxSkew - time.Second, studiosig.ErrReplay},
		{"after window", 2 * agentsig.DefaultMaxSkew, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := newClock()
			// A cap of 1 proves the expired entry was dropped, not just ignored.
			v := newVerifier(c, 1)
			first := req("POST", "alice", "k1", c.now().Add(agentsig.DefaultMaxSkew)) // future dated, still valid
			if _, err := verify(v, first); err != nil {
				t.Fatal(err)
			}
			c.advance(tt.advance)
			// Replaying the original after the clock moves: signature timestamp must still be in
			// skew for the replay to reach the cache, so re-sign with the same key at the new time.
			if _, err := verify(v, req("POST", "alice", "k1", c.now())); !errors.Is(err, tt.want) {
				t.Fatalf("got %v, want %v", err, tt.want)
			}
		})
	}
}

func TestCacheStaysBounded(t *testing.T) {
	c := newClock()
	v := newVerifier(c, 3)
	for i := 0; i < 3; i++ {
		if _, err := verify(v, req("POST", "alice", "k"+strconv.Itoa(i), c.now())); err != nil {
			t.Fatal(err)
		}
	}
	// At the cap: fail closed, and never evict an existing entry to make room.
	if _, err := verify(v, req("POST", "alice", "k9", c.now())); !errors.Is(err, studiosig.ErrBusy) {
		t.Fatalf("got %v, want ErrBusy", err)
	}
	if _, err := verify(v, req("POST", "alice", "k0", c.now())); !errors.Is(err, studiosig.ErrReplay) {
		t.Fatalf("an entry was evicted early: %v", err)
	}
	// Once entries expire, capacity returns.
	c.advance(2 * agentsig.DefaultMaxSkew)
	if _, err := verify(v, req("POST", "alice", "k9", c.now())); err != nil {
		t.Fatalf("no capacity after expiry: %v", err)
	}
}

func TestUnsignedFloodCannotEvictOrFill(t *testing.T) {
	c := newClock()
	v := newVerifier(c, 2)
	if _, err := verify(v, req("POST", "alice", "legit", c.now())); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 1000; i++ {
		r := httptest.NewRequest("POST", "/x", nil)
		agentsig.Sign(r, keyID, []byte("guess"), "alice", "f"+strconv.Itoa(i), nil, c.now())
		if _, err := verify(v, r); !errors.Is(err, studiosig.ErrUnauthorized) {
			t.Fatalf("flood request: %v", err)
		}
	}
	if _, err := verify(v, req("POST", "alice", "legit", c.now())); !errors.Is(err, studiosig.ErrReplay) {
		t.Fatalf("legit entry lost: %v", err)
	}
	if _, err := verify(v, req("POST", "alice", "second", c.now())); err != nil {
		t.Fatalf("flood consumed capacity: %v", err)
	}
}

func TestConcurrentIdenticalRequestsOneWins(t *testing.T) {
	c := newClock()
	v := newVerifier(c, 0)
	const n = 64
	var wins, replays atomic.Int32
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		r := req("POST", "alice", "same", c.now())
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			switch _, err := verify(v, r); {
			case err == nil:
				wins.Add(1)
			case errors.Is(err, studiosig.ErrReplay):
				replays.Add(1)
			}
		}()
	}
	close(start)
	wg.Wait()
	if wins.Load() != 1 || replays.Load() != n-1 {
		t.Fatalf("wins=%d replays=%d", wins.Load(), replays.Load())
	}
}

func TestKeyRotation(t *testing.T) {
	c := newClock()
	v := studiosig.New(studiosig.Config{Keys: map[string][]byte{"old": []byte("s-old")}, Now: c.now})
	sign := func(id string, sec []byte, idem string) *http.Request {
		r := httptest.NewRequest("GET", "/v1/studio/x", nil)
		agentsig.Sign(r, id, sec, "alice", idem, nil, c.now())
		return r
	}
	if _, err := verify(v, sign("old", []byte("s-old"), "a1")); err != nil {
		t.Fatalf("old key before rotation: %v", err)
	}
	// Overlap: both keys accepted.
	v.SetKeys(map[string][]byte{"old": []byte("s-old"), "new": []byte("s-new")})
	if _, err := verify(v, sign("new", []byte("s-new"), "a2")); err != nil {
		t.Fatalf("new key during overlap: %v", err)
	}
	if _, err := verify(v, sign("old", []byte("s-old"), "a3")); err != nil {
		t.Fatalf("old key during overlap: %v", err)
	}
	// A captured request is still a replay after rotation.
	if _, err := verify(v, sign("new", []byte("s-new"), "a2")); !errors.Is(err, studiosig.ErrReplay) {
		t.Fatalf("replay across SetKeys = %v", err)
	}
	// Retire the old key.
	v.SetKeys(map[string][]byte{"new": []byte("s-new")})
	if _, err := verify(v, sign("old", []byte("s-old"), "a4")); !errors.Is(err, studiosig.ErrUnauthorized) {
		t.Fatalf("retired key = %v", err)
	}
	// Right id, wrong secret, and empty sets accept nothing.
	if _, err := verify(v, sign("new", []byte("nope"), "a5")); !errors.Is(err, studiosig.ErrUnauthorized) {
		t.Fatalf("wrong secret = %v", err)
	}
	v.SetKeys(nil)
	if _, err := verify(v, sign("new", []byte("s-new"), "a6")); !errors.Is(err, studiosig.ErrUnauthorized) {
		t.Fatalf("empty key set = %v", err)
	}
}

func TestLoadKeys(t *testing.T) {
	dir := t.TempDir()
	write := func(body string) string {
		p := dir + "/k.json"
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	tests := []struct {
		name, body string
		ok         bool
	}{
		{"two keys", `{"a":"x","b":"y"}`, true},
		{"empty object", `{}`, false},
		{"empty secret", `{"a":""}`, false},
		{"empty id", `{"":"x"}`, false},
		{"not json", `a=b`, false},
		{"wrong shape", `{"a":1}`, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := studiosig.LoadKeys(write(tt.body))
			if (err == nil) != tt.ok {
				t.Errorf("err = %v, want ok=%v", err, tt.ok)
			}
		})
	}
	if _, err := studiosig.LoadKeys(dir + "/missing.json"); err == nil {
		t.Error("missing file must error")
	}
}

func TestLoadKeyPair(t *testing.T) {
	dir := t.TempDir()
	write := func(body string) string {
		p := dir + "/pair.json"
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	tests := []struct {
		name, body string
		ok         bool
	}{
		{"valid", `{"id":"backend-2","secret":"s3cret"}`, true},
		{"empty id", `{"id":"","secret":"x"}`, false},
		{"empty secret", `{"id":"a","secret":""}`, false},
		{"missing fields", `{}`, false},
		{"unknown field", `{"id":"a","secret":"b","extra":1}`, false},
		{"not json", `id=a`, false},
		{"wrong shape", `{"a":"b"}`, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			kp, err := studiosig.LoadKeyPair(write(tt.body))
			if (err == nil) != tt.ok {
				t.Fatalf("err = %v, want ok=%v", err, tt.ok)
			}
			if tt.ok && (kp.ID != "backend-2" || string(kp.Secret) != "s3cret") {
				t.Fatalf("pair = %+v", kp)
			}
		})
	}
	if _, err := studiosig.LoadKeyPair(dir + "/missing.json"); err == nil {
		t.Error("missing file must error")
	}
}

func TestDiffIDs(t *testing.T) {
	set := func(ids ...string) map[string]struct{} {
		m := map[string]struct{}{}
		for _, id := range ids {
			m[id] = struct{}{}
		}
		return m
	}
	tests := []struct {
		name          string
		prev, next    map[string]struct{}
		added, remove string
	}{
		{"first load", set(), set("a", "b"), "a,b", ""},
		{"add one", set("a"), set("a", "b"), "b", ""},
		{"retire one", set("a", "b"), set("b"), "", "a"},
		{"swap", set("a"), set("b"), "b", "a"},
		{"no change", set("a"), set("a"), "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a, r := studiosig.DiffIDs(tt.prev, tt.next)
			if got := strings.Join(a, ","); got != tt.added {
				t.Errorf("added = %q, want %q", got, tt.added)
			}
			if got := strings.Join(r, ","); got != tt.remove {
				t.Errorf("removed = %q, want %q", got, tt.remove)
			}
		})
	}
}
