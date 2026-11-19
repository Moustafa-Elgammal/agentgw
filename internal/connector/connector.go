// Package connector calls a studio's backend for a tool. Level 0 maps a tool onto
// an existing REST endpoint; every request is signed with pkg/agentsig.
package connector

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync/atomic"
	"time"

	"github.com/Moustafa-Elgammal/agentgw/internal/core"
	"github.com/Moustafa-Elgammal/agentgw/pkg/agentsig"
)

// Connector performs the backend call for a validated tool invocation.
type Connector interface {
	Call(ctx context.Context, p core.Principal, t core.Tool, args map[string]any, idem string) (core.Result, error)
}

// HTTP is the level 0 connector.
type HTTP struct {
	BaseURL string
	KeyID   string
	Secret  []byte
	Client  *http.Client
	Now     func() time.Time

	key atomic.Pointer[signKey] // set by SetKey; nil means KeyID and Secret
}

type signKey struct {
	id     string
	secret []byte
}

// SetKey replaces the key that signs backend calls. It is safe to call while calls are in
// flight: a call reads the key once, so it finishes with the key it started with. The id must be
// a safe header value and the secret must not be empty; otherwise the current key stays.
func (c *HTTP) SetKey(id string, secret []byte) error {
	if id == "" || !headerSafe(id) || len(id) > 128 {
		return errors.New("connector: signing key id is empty or not a safe header value")
	}
	if len(secret) == 0 {
		return errors.New("connector: signing key secret is empty")
	}
	c.key.Store(&signKey{id: id, secret: append([]byte(nil), secret...)})
	return nil
}

// CurrentKeyID returns the id of the key signing backend calls right now.
func (c *HTTP) CurrentKeyID() string {
	id, _ := c.signing()
	return id
}

func (c *HTTP) signing() (string, []byte) {
	if k := c.key.Load(); k != nil {
		return k.id, k.secret
	}
	return c.KeyID, c.Secret
}

const maxBody = 1 << 20 // 1 MiB

var placeholder = regexp.MustCompile(`\{([a-z][a-z0-9_]*)\}`)

// NewHTTP builds a connector with a 10 second timeout.
//
// By default the connector refuses to connect to loopback, private, link-local and other
// special-purpose addresses and never follows redirects (see ssrf.go). Pass AllowPrivate for a
// backend on a private network.
func NewHTTP(baseURL, keyID string, secret []byte, opts ...Option) *HTTP {
	c := &HTTP{
		BaseURL: strings.TrimRight(baseURL, "/"), KeyID: keyID, Secret: secret,
		Client: newClient(true), Now: time.Now,
	}
	for _, o := range opts {
		o(c)
	}
	return c
}

// Call sends the request. A transport failure is a Go error (the caller fails
// closed and releases its reservation). A backend 4xx is a normal denied Result.
func (c *HTTP) Call(ctx context.Context, p core.Principal, t core.Tool, args map[string]any, idem string) (core.Result, error) {
	// These values end up in signed headers. A bad one is refused before anything is signed.
	if !headerSafe(p.PlayerID) || !headerSafe(idem) {
		return core.Denied("invalid_principal", "the player or call identifier is not a safe header value"), nil
	}
	rest := map[string]any{}
	for k, v := range args {
		rest[k] = v
	}
	var badSegment string
	path := placeholder.ReplaceAllStringFunc(t.HTTP.URL, func(m string) string {
		name := m[1 : len(m)-1]
		v := fmt.Sprint(rest[name])
		delete(rest, name)
		// PathEscape leaves "." and ".." alone, and an empty value makes an empty segment.
		// Either would let an argument move the request to another path.
		if v == "" || v == "." || v == ".." {
			badSegment = name
		}
		return url.PathEscape(v)
	})
	if badSegment != "" {
		return core.Denied("invalid_arguments", fmt.Sprintf("argument %q cannot be used in a path (empty, \".\" or \"..\")", badSegment)), nil
	}

	var body []byte
	target := c.BaseURL + path
	switch t.HTTP.Method {
	case "GET", "DELETE":
		q := url.Values{}
		for k, v := range rest {
			q.Set(k, fmt.Sprint(v))
		}
		if len(q) > 0 {
			target += "?" + q.Encode() // Encode sorts keys, so the signed path is stable
		}
	default:
		b, err := json.Marshal(rest)
		if err != nil {
			return core.Result{}, err
		}
		body = b
	}

	req, err := http.NewRequestWithContext(ctx, t.HTTP.Method, target, bytes.NewReader(body))
	if err != nil {
		return core.Result{}, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json")
	keyID, secret := c.signing()
	agentsig.Sign(req, keyID, secret, p.PlayerID, idem, body, c.Now())

	resp, err := c.Client.Do(req)
	if err != nil {
		return core.Result{}, fmt.Errorf("backend unreachable: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return core.Result{}, fmt.Errorf("reading backend response: %w", err)
	}

	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		res := core.Result{OK: true}
		if json.Valid(data) {
			res.Data = data
		} else if len(data) > 0 {
			res.Data, _ = json.Marshal(string(data))
		}
		return res, nil
	case resp.StatusCode >= 400 && resp.StatusCode < 500:
		return core.Denied("backend_rejected", truncate(string(data), 500)), nil
	default:
		return core.Result{}, fmt.Errorf("backend error: status %d", resp.StatusCode)
	}
}

func truncate(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) > n {
		return s[:n] + "..."
	}
	return s
}

// headerSafe accepts printable ASCII only, at most 128 bytes, and not empty. That excludes CR,
// LF, NUL and every other control character, so a value cannot add or split a header.
func headerSafe(s string) bool {
	if s == "" || len(s) > 128 {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < 0x20 || s[i] > 0x7e {
			return false
		}
	}
	return true
}
