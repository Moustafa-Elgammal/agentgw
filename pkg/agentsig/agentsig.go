// Package agentsig signs and verifies the requests the gateway sends to a game
// backend (level 1 and level 0 integration). Backends written in Go can import
// it directly; every other language implements the same few lines, specified in
// docs/PROTOCOL.md.
//
// Signed string (fields joined by "\n"):
//
//	version "v1", unix timestamp, key id, HTTP method, request path with query,
//	player id, idempotency key, hex SHA-256 of the body.
package agentsig

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Header names carried on every gateway request.
const (
	HeaderKeyID       = "X-AG-Key-Id"
	HeaderTimestamp   = "X-AG-Timestamp"
	HeaderPlayer      = "X-AG-Player"
	HeaderIdempotency = "X-AG-Idempotency-Key"
	HeaderSignature   = "X-AG-Signature"
)

// DefaultMaxSkew is how far a timestamp may be from the verifier's clock.
const DefaultMaxSkew = 5 * time.Minute

// Errors returned by Verify.
var (
	ErrMissingHeader = errors.New("agentsig: missing header")
	ErrBadTimestamp  = errors.New("agentsig: timestamp outside allowed skew")
	ErrBadSignature  = errors.New("agentsig: signature mismatch")
)

func canonical(ts int64, keyID, method, pathQuery, player, idem string, body []byte) string {
	sum := sha256.Sum256(body)
	return strings.Join([]string{
		"v1", strconv.FormatInt(ts, 10), keyID, strings.ToUpper(method),
		pathQuery, player, idem, hex.EncodeToString(sum[:]),
	}, "\n")
}

func mac(secret []byte, s string) string {
	h := hmac.New(sha256.New, secret)
	h.Write([]byte(s))
	return hex.EncodeToString(h.Sum(nil))
}

// Sign sets the five headers on req. The body must be the exact bytes that will be sent.
func Sign(req *http.Request, keyID string, secret []byte, player, idem string, body []byte, now time.Time) {
	ts := now.Unix()
	pq := req.URL.RequestURI()
	req.Header.Set(HeaderKeyID, keyID)
	req.Header.Set(HeaderTimestamp, strconv.FormatInt(ts, 10))
	req.Header.Set(HeaderPlayer, player)
	req.Header.Set(HeaderIdempotency, idem)
	req.Header.Set(HeaderSignature, mac(secret, canonical(ts, keyID, req.Method, pq, player, idem, body)))
}

// Verify checks the signature on an incoming request and returns the player id.
// secretFor maps a key id to its secret; return nil for an unknown key.
func Verify(req *http.Request, body []byte, secretFor func(keyID string) []byte, now time.Time, maxSkew time.Duration) (string, error) {
	h := req.Header
	keyID, tsStr, player, idem, sig := h.Get(HeaderKeyID), h.Get(HeaderTimestamp), h.Get(HeaderPlayer), h.Get(HeaderIdempotency), h.Get(HeaderSignature)
	if keyID == "" || tsStr == "" || player == "" || idem == "" || sig == "" {
		return "", ErrMissingHeader
	}
	ts, err := strconv.ParseInt(tsStr, 10, 64)
	if err != nil {
		return "", ErrBadTimestamp
	}
	if maxSkew <= 0 {
		maxSkew = DefaultMaxSkew
	}
	if d := now.Sub(time.Unix(ts, 0)); d > maxSkew || d < -maxSkew {
		return "", ErrBadTimestamp
	}
	secret := secretFor(keyID)
	if secret == nil {
		return "", ErrBadSignature
	}
	want := mac(secret, canonical(ts, keyID, req.Method, req.URL.RequestURI(), player, idem, body))
	if !hmac.Equal([]byte(want), []byte(sig)) {
		return "", ErrBadSignature
	}
	return player, nil
}
