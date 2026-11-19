package connector_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Moustafa-Elgammal/agentgw/internal/connector"
	"github.com/Moustafa-Elgammal/agentgw/internal/core"
	"github.com/Moustafa-Elgammal/agentgw/pkg/agentsig"
)

func testNow() time.Time { return time.Now() }

var rotTool = core.Tool{Name: "get_state", HTTP: core.HTTPBinding{Method: "GET", URL: "/state"}}

func TestSetKeyChangesTheSigningKey(t *testing.T) {
	secrets := map[string][]byte{"old": []byte("so"), "new": []byte("sn")}
	var lastID atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, err := agentsig.Verify(r, nil, func(id string) []byte { return secrets[id] }, testNow(), 0)
		if err != nil {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		lastID.Store(r.Header.Get(agentsig.HeaderKeyID))
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()
	c := connector.NewHTTP(srv.URL, "old", secrets["old"], connector.AllowPrivate())
	c.Now = testNow

	call := func() core.Result {
		res, err := c.Call(context.Background(), core.Principal{PlayerID: "p1"}, rotTool, nil, "idem-"+t.Name())
		if err != nil {
			t.Fatal(err)
		}
		return res
	}
	steps := []struct {
		name   string
		act    func() error
		wantID string
		wantOK bool
	}{
		{"starts on the constructor key", func() error { return nil }, "old", true},
		{"rotates to the new key", func() error { return c.SetKey("new", secrets["new"]) }, "new", true},
		{"an empty secret is refused and the key stays", func() error {
			if c.SetKey("x", nil) == nil {
				t.Error("empty secret accepted")
			}
			return nil
		}, "new", true},
		{"an unsafe id is refused and the key stays", func() error {
			if c.SetKey("bad\r\nid", []byte("s")) == nil {
				t.Error("unsafe id accepted")
			}
			if c.SetKey("", []byte("s")) == nil {
				t.Error("empty id accepted")
			}
			return nil
		}, "new", true},
	}
	for _, s := range steps {
		t.Run(s.name, func(t *testing.T) {
			if err := s.act(); err != nil {
				t.Fatal(err)
			}
			if got := call(); got.OK != s.wantOK {
				t.Fatalf("result = %+v", got)
			}
			if got := lastID.Load(); got != s.wantID {
				t.Fatalf("signed with %v, want %s", got, s.wantID)
			}
			if c.CurrentKeyID() != s.wantID {
				t.Fatalf("CurrentKeyID = %s, want %s", c.CurrentKeyID(), s.wantID)
			}
		})
	}
}

// Calls running while the key is swapped must all be signed with a complete key (an id and its
// own secret), never a mix. The backend accepts both keys, so any torn pair would be a 401.
func TestSetKeyIsSafeUnderConcurrentCalls(t *testing.T) {
	secrets := map[string][]byte{"k1": []byte("s1"), "k2": []byte("s2")}
	var bad atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := agentsig.Verify(r, nil, func(id string) []byte { return secrets[id] }, testNow(), 0); err != nil {
			bad.Add(1)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()
	c := connector.NewHTTP(srv.URL, "k1", secrets["k1"], connector.AllowPrivate())
	c.Now = testNow

	var wg sync.WaitGroup
	stop := make(chan struct{})
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
					_, _ = c.Call(context.Background(), core.Principal{PlayerID: "p"}, rotTool, nil, "i")
				}
			}
		}()
	}
	for i := 0; i < 200; i++ {
		id := "k1"
		if i%2 == 1 {
			id = "k2"
		}
		if err := c.SetKey(id, secrets[id]); err != nil {
			t.Fatal(err)
		}
	}
	close(stop)
	wg.Wait()
	if n := bad.Load(); n != 0 {
		t.Fatalf("%d calls were signed with a mismatched key id and secret", n)
	}
}
