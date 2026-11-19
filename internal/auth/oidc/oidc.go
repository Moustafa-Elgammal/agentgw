// Package oidc lets a player prove who they are at the consent page by signing in with the
// studio's own identity provider (IdP), instead of typing a link code.
//
// The package is split on purpose. Everything that is plain HTTP and plain checks lives here and
// uses only the standard library: discovery, the authorization redirect with PKCE, state and nonce
// handling, the code exchange, the claim checks and the mapping from a claim to a player id. The
// one part that is easy to get wrong, verifying the ID token's signature against the IdP's keys,
// sits behind the Verifier interface, so the choice of library (or an in-house verifier) is a
// separate decision. See docs/design/oidc-consent.md.
//
// Even with a Verifier, Flow re-checks issuer, audience, authorized party, nonce, expiry and age
// itself (CheckClaims), so a Verifier that forgets one of them cannot weaken the flow.
package oidc

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"strconv"
	"time"
)

// Defaults.
const (
	DefaultSkew        = time.Minute
	DefaultMaxAge      = 10 * time.Minute
	DefaultSessionTTL  = 10 * time.Minute
	DefaultPlayerClaim = "sub"

	// MaxPlayerLen bounds a player id taken from a claim, like every other player id the
	// gateway signs into a request.
	MaxPlayerLen = 128
)

// Errors. Callers show the player one generic message for all of them except ErrDenied; the
// distinction is for logs and tests.
var (
	ErrSession     = errors.New("oidc: no valid sign-in session (missing, tampered or expired)")
	ErrState       = errors.New("oidc: state does not match the session")
	ErrReplay      = errors.New("oidc: this sign-in was already used")
	ErrDenied      = errors.New("oidc: the identity provider reported that sign-in was refused")
	ErrExchange    = errors.New("oidc: code exchange failed")
	ErrToken       = errors.New("oidc: ID token rejected")
	ErrIssuer      = errors.New("oidc: wrong issuer")
	ErrAudience    = errors.New("oidc: ID token is not for this client")
	ErrNonce       = errors.New("oidc: nonce does not match")
	ErrExpired     = errors.New("oidc: ID token expired, not yet valid, or too old")
	ErrPlayerClaim = errors.New("oidc: the player claim is missing or not a usable player id")
	ErrDiscovery   = errors.New("oidc: provider discovery failed")
)

// Claims is the verified content of an ID token.
type Claims struct {
	Issuer          string
	Subject         string
	Audience        []string
	AuthorizedParty string // azp
	Nonce           string
	Expiry          time.Time
	NotBefore       time.Time // zero when absent
	IssuedAt        time.Time // zero when absent
	// Raw is every claim of the token, decoded from JSON, so the player claim can be any of
	// them. Numbers may be float64 or json.Number.
	Raw map[string]any
}

// Verifier authenticates a raw ID token. Implementations MUST:
//
//   - verify the signature against the provider's keys (JWKS), with an algorithm allow-list
//     (RS256 and ES256 are enough) and never the algorithm the token itself asks for as "none"
//     or a symmetric one;
//   - check the issuer, the audience (it must contain the client id), exp and the nonce.
//
// Flow checks those claims again, but signature verification is only here. A Verifier returns
// an error for anything it cannot prove.
type Verifier interface {
	Verify(ctx context.Context, rawIDToken, nonce string) (Claims, error)
}

// Config configures one OIDC login.
type Config struct {
	// Issuer is the IdP's issuer URL, exactly as it appears in its ID tokens and discovery
	// document. It must be https (see AllowInsecureHTTP).
	Issuer       string
	ClientID     string
	ClientSecret string // empty means a public client (PKCE only)

	// RedirectURL is this gateway's callback, Issuer-of-the-gateway + /authorize/oidc/callback.
	// Register it at the IdP. It is fixed configuration, never taken from a request.
	RedirectURL string

	// AuthURL and TokenURL override discovery. Leave both empty to discover them from
	// Issuer/.well-known/openid-configuration.
	AuthURL  string
	TokenURL string

	// Scopes requested from the IdP. "openid" is always included.
	Scopes []string

	// PlayerClaim names the ID token claim that carries the studio's player id. It must be
	// stable for a person for ever and must equal the player id the studio's backend uses.
	// Default "sub". An email address or a display name is not stable.
	PlayerClaim string

	// SessionSecret seals the sign-in session cookie (AES-256-GCM). At least 32 bytes of
	// randomness. Replicas that may serve the two halves of one sign-in must share it.
	SessionSecret []byte

	Skew       time.Duration // clock skew tolerated on exp, nbf, iat. Default 1 minute.
	MaxAge     time.Duration // longest the token may be since iat. Default 10 minutes.
	SessionTTL time.Duration // time allowed between redirect and callback. Default 10 minutes.

	// AllowInsecureHTTP allows http for the IdP and the callback. For a local IdP in tests and
	// demos only.
	AllowInsecureHTTP bool
}

// CheckClaims is the part of ID token validation that needs no cryptography. now is the
// current time and nonce the value this sign-in sent to the IdP.
func CheckClaims(c Claims, cfg Config, nonce string, now time.Time) error {
	skew := cfg.Skew
	if skew <= 0 {
		skew = DefaultSkew
	}
	maxAge := cfg.MaxAge
	if maxAge <= 0 {
		maxAge = DefaultMaxAge
	}
	if c.Issuer == "" || c.Issuer != cfg.Issuer {
		return ErrIssuer
	}
	found := false
	for _, a := range c.Audience {
		if a == cfg.ClientID {
			found = true
		}
	}
	if !found {
		return ErrAudience
	}
	// OIDC Core 3.1.3.7: with several audiences, azp must be present and be this client.
	if len(c.Audience) > 1 && c.AuthorizedParty != cfg.ClientID {
		return ErrAudience
	}
	if c.AuthorizedParty != "" && c.AuthorizedParty != cfg.ClientID {
		return ErrAudience
	}
	if nonce == "" || subtle.ConstantTimeCompare([]byte(nonce), []byte(c.Nonce)) != 1 {
		return ErrNonce
	}
	if c.Expiry.IsZero() || !now.Before(c.Expiry.Add(skew)) {
		return ErrExpired
	}
	if !c.NotBefore.IsZero() && now.Add(skew).Before(c.NotBefore) {
		return ErrExpired
	}
	if !c.IssuedAt.IsZero() {
		if c.IssuedAt.After(now.Add(skew)) || now.Sub(c.IssuedAt) > maxAge+skew {
			return ErrExpired
		}
	}
	return nil
}

// PlayerID reads the player claim from c. It must be a string or an integral number, 1 to
// MaxPlayerLen printable ASCII characters with no spaces, so it is safe in a signed header.
func PlayerID(c Claims, claim string) (string, error) {
	if claim == "" {
		claim = DefaultPlayerClaim
	}
	v := c.Raw[claim]
	if claim == "sub" && v == nil {
		v = c.Subject
	}
	var s string
	switch t := v.(type) {
	case string:
		s = t
	case float64:
		if t != float64(int64(t)) {
			return "", ErrPlayerClaim
		}
		s = strconv.FormatInt(int64(t), 10)
	case interface{ String() string }: // json.Number
		s = t.String()
	default:
		return "", ErrPlayerClaim
	}
	if s == "" || len(s) > MaxPlayerLen {
		return "", ErrPlayerClaim
	}
	for i := 0; i < len(s); i++ {
		if s[i] <= 0x20 || s[i] >= 0x7f {
			return "", ErrPlayerClaim
		}
	}
	return s, nil
}

// validate reports a configuration problem, so a bad setup fails at start and not at the first
// player.
func (c Config) validate() error {
	if c.ClientID == "" {
		return errors.New("oidc: client id is required")
	}
	if len(c.SessionSecret) < 32 {
		return errors.New("oidc: session secret must be at least 32 bytes")
	}
	for name, u := range map[string]string{"issuer": c.Issuer, "redirect url": c.RedirectURL} {
		if err := checkURL(u, c.AllowInsecureHTTP); err != nil {
			return fmt.Errorf("oidc: %s: %w", name, err)
		}
	}
	for name, u := range map[string]string{"auth url": c.AuthURL, "token url": c.TokenURL} {
		if u == "" {
			continue
		}
		if err := checkURL(u, c.AllowInsecureHTTP); err != nil {
			return fmt.Errorf("oidc: %s: %w", name, err)
		}
	}
	if (c.AuthURL == "") != (c.TokenURL == "") {
		return errors.New("oidc: set both auth url and token url, or neither (discovery)")
	}
	return nil
}
