package oidc

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	goidc "github.com/coreos/go-oidc/v3/oidc"
)

// DefaultAlgs are the ID token signature algorithms accepted when none are configured. They
// cover what identity providers sign with by default.
var DefaultAlgs = []string{"RS256", "ES256"}

// allowedAlgs is every asymmetric algorithm an ID token may use. "none" and the HMAC family are
// not in it, so they cannot be enabled by configuration: an HMAC key shared with the client would
// let the client forge tokens, and "none" is no signature at all.
var allowedAlgs = map[string]bool{
	"RS256": true, "RS384": true, "RS512": true,
	"PS256": true, "PS384": true, "PS512": true,
	"ES256": true, "ES384": true, "ES512": true,
	"EdDSA": true,
}

// ParseAlgs turns a comma separated list into an algorithm allow-list. An empty list gives
// DefaultAlgs. Anything outside the asymmetric set (none, HS256 and so on) is an error.
func ParseAlgs(list string) ([]string, error) {
	var out []string
	for _, a := range strings.Split(list, ",") {
		a = strings.TrimSpace(a)
		if a == "" {
			continue
		}
		if !allowedAlgs[a] {
			return nil, fmt.Errorf("oidc: signature algorithm %q is not allowed (use asymmetric ones such as RS256 or ES256)", a)
		}
		out = append(out, a)
	}
	if len(out) == 0 {
		return append([]string(nil), DefaultAlgs...), nil
	}
	return out, nil
}

// GoOIDCVerifier checks ID token signatures with github.com/coreos/go-oidc (which uses
// go-jose). go-oidc fetches the provider's keys from its jwks_uri, caches them, and fetches again
// when a token names a key id it does not know, so key rotation needs no restart. This adapter
// only narrows what it accepts and maps the result to Claims; the flow still runs CheckClaims.
type GoOIDCVerifier struct {
	v  *goidc.IDTokenVerifier
	hc *http.Client
}

// NewGoOIDCVerifier discovers the provider named by cfg.Issuer and builds a verifier that accepts
// only algs (nil means DefaultAlgs). It returns an error when discovery fails or the discovery
// document names a different issuer, so a gateway can refuse to start on a bad configuration.
// hc may be nil (10 second timeout, never follows redirects). now may be nil.
func NewGoOIDCVerifier(ctx context.Context, cfg Config, algs []string, hc *http.Client, now func() time.Time) (*GoOIDCVerifier, error) {
	if cfg.ClientID == "" {
		return nil, errors.New("oidc: client id is required")
	}
	if err := checkURL(cfg.Issuer, cfg.AllowInsecureHTTP); err != nil {
		return nil, fmt.Errorf("oidc: issuer: %w", err)
	}
	if len(algs) == 0 {
		algs = DefaultAlgs
	}
	for _, a := range algs {
		if !allowedAlgs[a] {
			return nil, fmt.Errorf("oidc: signature algorithm %q is not allowed", a)
		}
	}
	if hc == nil {
		hc = newHTTPClient()
	}
	if now == nil {
		now = time.Now
	}
	// The client travels in the context: go-oidc uses it for discovery and for every later key
	// fetch, so the timeout and the no-redirect rule apply to both.
	cctx := goidc.ClientContext(ctx, hc)
	p, err := goidc.NewProvider(cctx, cfg.Issuer)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrDiscovery, err)
	}
	v := p.VerifierContext(cctx, &goidc.Config{ClientID: cfg.ClientID, SupportedSigningAlgs: algs, Now: now})
	return &GoOIDCVerifier{v: v, hc: hc}, nil
}

// Verify implements Verifier. It never returns claims for a token whose signature, algorithm,
// issuer, audience, or expiry check failed.
func (g *GoOIDCVerifier) Verify(ctx context.Context, rawIDToken, nonce string) (Claims, error) {
	tok, err := g.v.Verify(goidc.ClientContext(ctx, g.hc), rawIDToken)
	if err != nil {
		return Claims{}, err
	}
	if nonce == "" || subtle.ConstantTimeCompare([]byte(nonce), []byte(tok.Nonce)) != 1 {
		return Claims{}, ErrNonce
	}
	var parts map[string]json.RawMessage
	if err := tok.Claims(&parts); err != nil {
		return Claims{}, fmt.Errorf("%w: %v", ErrToken, err)
	}
	raw := make(map[string]any, len(parts))
	for k, b := range parts {
		dec := json.NewDecoder(bytes.NewReader(b))
		dec.UseNumber() // a numeric player id must not lose digits to float64
		var v any
		if err := dec.Decode(&v); err != nil {
			return Claims{}, fmt.Errorf("%w: claim %q", ErrToken, k)
		}
		raw[k] = v
	}
	c := Claims{
		Issuer: tok.Issuer, Subject: tok.Subject, Audience: tok.Audience,
		Nonce: tok.Nonce, Expiry: tok.Expiry, IssuedAt: tok.IssuedAt, Raw: raw,
	}
	if s, ok := raw["azp"].(string); ok {
		c.AuthorizedParty = s
	}
	if n, ok := raw["nbf"].(json.Number); ok {
		if f, err := n.Float64(); err == nil {
			c.NotBefore = time.Unix(int64(f), 0)
		}
	}
	return c, nil
}
