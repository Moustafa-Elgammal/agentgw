package oidc

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"time"
)

// session is what a sign-in remembers between the redirect to the IdP and the callback. It lives
// in an encrypted cookie in the player's own browser, so the gateway keeps no state for a
// sign-in that was never finished, and the cookie also binds the callback to the browser that
// started it (a login CSRF defense: an attacker cannot feed a victim a callback for the
// attacker's own session).
type session struct {
	State    string            `json:"s"`
	Nonce    string            `json:"n"`
	Verifier string            `json:"v"` // PKCE verifier for the leg to the IdP
	Payload  map[string]string `json:"p"` // the authorization request the player is answering
	Expires  int64             `json:"e"` // unix seconds
}

// sealer encrypts and authenticates sessions with AES-256-GCM. The key is derived from the
// configured secret, with a label so the same secret used elsewhere gives a different key.
type sealer struct{ aead cipher.AEAD }

func newSealer(secret []byte) (*sealer, error) {
	key := sha256.Sum256(append([]byte("agentgw oidc session v1\x00"), secret...))
	b, err := aes.NewCipher(key[:])
	if err != nil {
		return nil, err
	}
	a, err := cipher.NewGCM(b)
	if err != nil {
		return nil, err
	}
	return &sealer{aead: a}, nil
}

// maxCookie bounds what open will decode, so a huge cookie costs nothing.
const maxCookie = 8 << 10

func (s *sealer) seal(v session) (string, error) {
	plain, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, s.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	out := s.aead.Seal(nonce, nonce, plain, nil)
	return base64.RawURLEncoding.EncodeToString(out), nil
}

func (s *sealer) open(value string, now time.Time) (session, error) {
	var v session
	if value == "" || len(value) > maxCookie {
		return v, ErrSession
	}
	raw, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil || len(raw) < s.aead.NonceSize()+s.aead.Overhead() {
		return v, ErrSession
	}
	n := s.aead.NonceSize()
	plain, err := s.aead.Open(nil, raw[:n], raw[n:], nil)
	if err != nil {
		return v, ErrSession
	}
	if err := json.Unmarshal(plain, &v); err != nil {
		return session{}, ErrSession
	}
	if v.State == "" || v.Nonce == "" || v.Verifier == "" || !now.Before(time.Unix(v.Expires, 0)) {
		return session{}, ErrSession
	}
	return v, nil
}
