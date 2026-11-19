package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/Moustafa-Elgammal/agentgw/internal/auth"
	"github.com/Moustafa-Elgammal/agentgw/internal/auth/oidc"
)

// oidcOptions are the --oidc-* flags. OIDC is on only when Issuer is set.
type oidcOptions struct {
	Issuer      string
	ClientID    string
	PlayerClaim string
	Scopes      string
	Algs        string
	// AllowHTTP lets the issuer be plain http on a loopback address (for a local demo IdP).
	AllowHTTP bool
}

// Secrets come from the environment, never from flags, so they stay out of process listings.
const (
	envOIDCClientSecret  = "AGENTGW_OIDC_CLIENT_SECRET"  //nolint:gosec // an environment variable name, not a credential
	envOIDCSessionSecret = "AGENTGW_OIDC_SESSION_SECRET" //nolint:gosec // an environment variable name, not a credential
)

// minSessionSecret is the shortest session secret accepted: it keys AES-256-GCM.
const minSessionSecret = 32

// buildOIDC turns the flags into a sign-in flow, or returns nil when OIDC is not configured. It
// checks everything at start: a missing secret, a bad algorithm list, an unreachable provider or
// an issuer that does not match its own discovery document stop the gateway with a clear error,
// instead of leaving a sign-in button that fails for players later. publicURL is the gateway's own
// public base URL; the callback registered at the identity provider is publicURL plus
// auth.OIDCCallbackPath.
func buildOIDC(ctx context.Context, log *slog.Logger, o oidcOptions, publicURL string) (auth.OIDCLogin, error) {
	if o.Issuer == "" {
		for name, set := range map[string]bool{
			"--oidc-client-id": o.ClientID != "", envOIDCClientSecret: os.Getenv(envOIDCClientSecret) != "",
			envOIDCSessionSecret: os.Getenv(envOIDCSessionSecret) != "",
		} {
			if set {
				return nil, fmt.Errorf("%s is set but --oidc-issuer is not: set the issuer to turn OIDC sign-in on, or remove the OIDC settings", name)
			}
		}
		return nil, nil
	}
	var problems []string
	if o.ClientID == "" {
		problems = append(problems, "--oidc-client-id is required")
	}
	clientSecret := os.Getenv(envOIDCClientSecret)
	if clientSecret == "" {
		problems = append(problems, envOIDCClientSecret+" is required (the client secret registered at the identity provider)")
	}
	sessionSecret := os.Getenv(envOIDCSessionSecret)
	if len(sessionSecret) < minSessionSecret {
		problems = append(problems, fmt.Sprintf("%s is required and must be at least %d bytes of randomness (for example: openssl rand -base64 48)", envOIDCSessionSecret, minSessionSecret))
	}
	algs, err := oidc.ParseAlgs(o.Algs)
	if err != nil {
		problems = append(problems, err.Error())
	}
	if len(problems) > 0 {
		return nil, errors.New("OIDC sign-in is enabled (--oidc-issuer) but not configured: " + strings.Join(problems, "; "))
	}

	cfg := oidc.Config{
		Issuer: o.Issuer, ClientID: o.ClientID, ClientSecret: clientSecret,
		RedirectURL:       strings.TrimRight(publicURL, "/") + auth.OIDCCallbackPath,
		Scopes:            strings.Fields(strings.ReplaceAll(o.Scopes, ",", " ")),
		PlayerClaim:       o.PlayerClaim,
		SessionSecret:     []byte(sessionSecret),
		AllowInsecureHTTP: o.AllowHTTP,
	}
	dctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	v, err := oidc.NewGoOIDCVerifier(dctx, cfg, algs, nil, nil)
	if err != nil {
		return nil, fmt.Errorf("OIDC discovery at %s failed (the gateway will not start with a sign-in it cannot offer): %w", o.Issuer, err)
	}
	flow, err := oidc.NewFlow(cfg, v, nil, nil)
	if err != nil {
		return nil, fmt.Errorf("OIDC configuration: %w", err)
	}
	origin, ok := flow.AuthOrigin(dctx)
	if !ok {
		return nil, fmt.Errorf("OIDC discovery at %s did not give a usable authorization endpoint", o.Issuer)
	}
	claim := o.PlayerClaim
	if claim == "" {
		claim = oidc.DefaultPlayerClaim
	}
	log.Info("oidc sign-in enabled", "issuer", o.Issuer, "client_id", o.ClientID, "player_claim", claim,
		"algs", strings.Join(algs, ","), "idp_origin", origin, "callback", cfg.RedirectURL) // secrets are never logged
	return flow, nil
}
