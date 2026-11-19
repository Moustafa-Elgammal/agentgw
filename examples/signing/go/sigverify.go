// Package sigverify is a small, complete example of a backend that accepts the gateway's
// signed calls. It checks the signature and the timestamp with pkg/agentsig, and it makes a
// repeated idempotency key return the stored result instead of running the handler again.
//
// Copy it into your backend. It has no dependencies beyond pkg/agentsig.
package sigverify

import (
	"bytes"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/Moustafa-Elgammal/agentgw/pkg/agentsig"
)

// MaxBody is the largest request body the middleware reads.
const MaxBody = 1 << 20

// Handler is what your code implements. player is authenticated: the signature covered it.
type Handler func(w http.ResponseWriter, r *http.Request, player string, body []byte)

// Middleware verifies every request and replays stored answers for repeated idempotency keys.
type Middleware struct {
	// SecretFor returns the secret for a key id, or nil for an unknown key. Return the old
	// and the new secret during a rotation.
	SecretFor func(keyID string) []byte
	// Now is the clock. Nil means time.Now.
	Now func() time.Time

	mu      sync.Mutex
	results map[string]*stored
}

type stored struct {
	done    chan struct{} // closed when the first call has finished
	status  int
	body    []byte
	expires time.Time
}

func (m *Middleware) now() time.Time {
	if m.Now != nil {
		return m.Now()
	}
	return time.Now()
}

// Wrap returns an http.Handler that runs h for verified requests.
func (m *Middleware) Wrap(h Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(io.LimitReader(r.Body, MaxBody))
		if err != nil {
			http.Error(w, "bad body", http.StatusBadRequest)
			return
		}
		// 1. Signature and timestamp. Verify compares in constant time and rejects a
		//    timestamp more than five minutes from our clock.
		player, err := agentsig.Verify(r, body, m.SecretFor, m.now(), agentsig.DefaultMaxSkew)
		if err != nil {
			http.Error(w, "unauthorized", http.StatusUnauthorized) // say nothing about why
			return
		}

		// 2. Idempotency. The key is only trusted after the signature checked out.
		key := r.Header.Get(agentsig.HeaderKeyID) + "/" + r.Header.Get(agentsig.HeaderIdempotency)
		entry, first := m.claim(key)
		if !first {
			<-entry.done // a concurrent first call is still running: wait for its answer
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("X-Idempotent-Replay", "true")
			w.WriteHeader(entry.status)
			_, _ = w.Write(entry.body)
			return
		}

		// 3. Run the handler once, recording what it answered.
		rec := &recorder{ResponseWriter: w, status: http.StatusOK}
		h(rec, r, player, body)
		m.finish(key, entry, rec)
	})
}

// claim returns the entry for key and whether the caller is the first to use it.
func (m *Middleware) claim(key string) (*stored, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.results == nil {
		m.results = map[string]*stored{}
	}
	now := m.now()
	for k, e := range m.results { // keep the map bounded: drop answers past the window
		select {
		case <-e.done:
			if now.After(e.expires) {
				delete(m.results, k)
			}
		default:
		}
	}
	if e, ok := m.results[key]; ok {
		return e, false
	}
	e := &stored{done: make(chan struct{})}
	m.results[key] = e
	return e, true
}

func (m *Middleware) finish(key string, e *stored, rec *recorder) {
	m.mu.Lock()
	defer m.mu.Unlock()
	// Keep a result at least as long as a signature stays valid in either direction.
	e.status, e.body, e.expires = rec.status, rec.buf.Bytes(), m.now().Add(2*agentsig.DefaultMaxSkew)
	if rec.status >= 500 {
		delete(m.results, key) // a server error did not apply the action: let a retry run
	}
	close(e.done)
}

// recorder copies the answer so a repeat can send the same bytes.
type recorder struct {
	http.ResponseWriter
	status int
	buf    bytes.Buffer
}

func (r *recorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

func (r *recorder) Write(b []byte) (int, error) {
	r.buf.Write(b)
	return r.ResponseWriter.Write(b)
}
