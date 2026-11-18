// Package studiosig is the one place the gateway verifies requests signed by the
// studio's backend (agentsig, key id plus secret). Beyond the signature it enforces
// the replay rule: a (key id, idempotency key) pair is accepted once within the
// timestamp window, on any endpoint that shares the Verifier. See docs/PROTOCOL.md.
package studiosig

import (
	"errors"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/Moustafa-Elgammal/agentgw/pkg/agentsig"
)

// MaxBody is the largest request body a studio call may carry.
const MaxBody = 64 << 10

// DefaultMaxEntries bounds the replay cache.
const DefaultMaxEntries = 100_000

// Errors returned by Verify. Callers map them to their own response shape.
var (
	ErrTooLarge     = errors.New("studiosig: body too large")
	ErrUnauthorized = errors.New("studiosig: missing or invalid studio signature")
	ErrReplay       = errors.New("studiosig: signed request already used")
	ErrBusy         = errors.New("studiosig: replay cache full, try again later")
)

// Config configures a Verifier.
type Config struct {
	KeyID  string
	Secret []byte
	// Now is the clock. Nil means time.Now.
	Now func() time.Time
	// MaxSkew is how far a request timestamp may be from the clock. Zero means
	// agentsig.DefaultMaxSkew.
	MaxSkew time.Duration
	// MaxEntries caps the replay cache. Zero means DefaultMaxEntries. At the cap,
	// after expired entries are dropped, new requests fail closed with ErrBusy.
	// Existing entries are never evicted early, because that would reopen replay.
	MaxEntries int
}

type entry struct {
	key    string
	expiry time.Time
}

// Verifier verifies studio-signed requests and refuses replays. It is safe for
// concurrent use. Share one instance between every handler that uses the same key.
type Verifier struct {
	keyID   string
	secret  []byte
	now     func() time.Time
	skew    time.Duration
	max     int
	mu      sync.Mutex
	seen    map[string]struct{}
	pending []entry // FIFO by insertion time, so expiry is non-decreasing
}

// New builds a Verifier.
func New(cfg Config) *Verifier {
	v := &Verifier{keyID: cfg.KeyID, secret: cfg.Secret, now: cfg.Now, skew: cfg.MaxSkew,
		max: cfg.MaxEntries, seen: map[string]struct{}{}}
	if v.now == nil {
		v.now = time.Now
	}
	if v.skew <= 0 {
		v.skew = agentsig.DefaultMaxSkew
	}
	if v.max <= 0 {
		v.max = DefaultMaxEntries
	}
	return v
}

// Verify reads the body (at most MaxBody), checks the signature, requires a
// player, and refuses a (key id, idempotency key) pair already accepted. It
// returns the signed player and the body. Every method is replay checked,
// including GET: a replayed read would hand the captured response to whoever
// replays it. The pair is recorded only after the signature verified, so a caller
// who cannot sign cannot fill or poison the cache.
func (v *Verifier) Verify(w http.ResponseWriter, r *http.Request) (player string, body []byte, err error) {
	body, err = io.ReadAll(http.MaxBytesReader(w, r.Body, MaxBody))
	if err != nil {
		return "", nil, ErrTooLarge
	}
	now := v.now()
	player, err = agentsig.Verify(r, body, func(id string) []byte {
		if id == v.keyID && len(v.secret) > 0 {
			return v.secret
		}
		return nil
	}, now, v.skew)
	if err != nil || player == "" {
		return "", nil, ErrUnauthorized
	}
	if err := v.claim(r.Header.Get(agentsig.HeaderKeyID)+"|"+r.Header.Get(agentsig.HeaderIdempotency), now); err != nil {
		return "", nil, err
	}
	return player, body, nil
}

// claim records the pair. A timestamp up to skew in the future is accepted, so an
// entry must outlive now+skew+skew: it is kept for twice the skew from insertion.
func (v *Verifier) claim(k string, now time.Time) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	i := 0
	for i < len(v.pending) && !v.pending[i].expiry.After(now) {
		delete(v.seen, v.pending[i].key)
		i++
	}
	v.pending = v.pending[i:] // append reallocates as the head advances, so old slots are freed
	if _, dup := v.seen[k]; dup {
		return ErrReplay
	}
	if len(v.seen) >= v.max {
		return ErrBusy
	}
	v.seen[k] = struct{}{}
	v.pending = append(v.pending, entry{key: k, expiry: now.Add(2 * v.skew)})
	return nil
}
