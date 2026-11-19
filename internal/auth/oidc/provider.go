package oidc

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// maxResponse bounds every body read from the IdP.
const maxResponse = 1 << 20

// checkURL accepts an https URL with a host and no credentials or fragment. Plain http is
// accepted only when allowInsecure is set and the host is loopback, so a typo cannot send a
// client secret over the network in the clear.
func checkURL(raw string, allowInsecure bool) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.User != nil || u.Fragment != "" {
		return errors.New("not a usable absolute URL")
	}
	switch u.Scheme {
	case "https":
		return nil
	case "http":
		if allowInsecure && isLoopback(u.Hostname()) {
			return nil
		}
		return errors.New("must be https (http is allowed only for a loopback IdP with AllowInsecureHTTP)")
	}
	return errors.New("must be https")
}

func isLoopback(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// endpoints are the two IdP URLs the flow needs.
type endpoints struct {
	auth, token string
	fetched     time.Time
}

// discoveryTTL is how long a discovered document is trusted before it is fetched again.
const discoveryTTL = time.Hour

type provider struct {
	cfg Config
	hc  *http.Client
	now func() time.Time

	mu sync.Mutex
	ep *endpoints
}

// newHTTPClient never follows redirects: an IdP endpoint that redirects is misconfigured, and
// following it would send the client secret to a place nobody configured.
func newHTTPClient() *http.Client {
	return &http.Client{
		Timeout:       10 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

// endpoints returns the IdP's authorization and token endpoints, from configuration or from the
// discovery document (cached; a failed refresh keeps the last good copy).
func (p *provider) endpoints(ctx context.Context) (endpoints, error) {
	if p.cfg.AuthURL != "" {
		return endpoints{auth: p.cfg.AuthURL, token: p.cfg.TokenURL}, nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.ep != nil && p.now().Sub(p.ep.fetched) < discoveryTTL {
		return *p.ep, nil
	}
	ep, err := p.discover(ctx)
	if err != nil {
		if p.ep != nil {
			return *p.ep, nil
		}
		return endpoints{}, err
	}
	p.ep = &ep
	return ep, nil
}

func (p *provider) discover(ctx context.Context) (endpoints, error) {
	u := strings.TrimRight(p.cfg.Issuer, "/") + "/.well-known/openid-configuration"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return endpoints{}, fmt.Errorf("%w: %v", ErrDiscovery, err)
	}
	req.Header.Set("Accept", "application/json")
	resp, err := p.hc.Do(req)
	if err != nil {
		return endpoints{}, fmt.Errorf("%w: %v", ErrDiscovery, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return endpoints{}, fmt.Errorf("%w: status %d", ErrDiscovery, resp.StatusCode)
	}
	var doc struct {
		Issuer                string   `json:"issuer"`
		AuthorizationEndpoint string   `json:"authorization_endpoint"`
		TokenEndpoint         string   `json:"token_endpoint"`
		CodeChallengeMethods  []string `json:"code_challenge_methods_supported"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxResponse)).Decode(&doc); err != nil {
		return endpoints{}, fmt.Errorf("%w: bad document", ErrDiscovery)
	}
	// The document must name the issuer we configured. A mismatch is the mix-up attack's
	// signature (OIDC Discovery 4.3), so it is refused.
	if doc.Issuer != p.cfg.Issuer {
		return endpoints{}, fmt.Errorf("%w: issuer in the document does not match the configured issuer", ErrDiscovery)
	}
	if len(doc.CodeChallengeMethods) > 0 {
		ok := false
		for _, m := range doc.CodeChallengeMethods {
			ok = ok || m == "S256"
		}
		if !ok {
			return endpoints{}, fmt.Errorf("%w: the provider does not support PKCE S256", ErrDiscovery)
		}
	}
	for _, e := range []string{doc.AuthorizationEndpoint, doc.TokenEndpoint} {
		if err := checkURL(e, p.cfg.AllowInsecureHTTP); err != nil {
			return endpoints{}, fmt.Errorf("%w: endpoint %q: %v", ErrDiscovery, e, err)
		}
	}
	return endpoints{auth: doc.AuthorizationEndpoint, token: doc.TokenEndpoint, fetched: p.now()}, nil
}

// exchange trades the authorization code for an ID token at the token endpoint.
func (p *provider) exchange(ctx context.Context, tokenURL, code, verifier string) (string, error) {
	form := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {p.cfg.RedirectURL},
		"client_id":     {p.cfg.ClientID},
		"code_verifier": {verifier},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return "", ErrExchange
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	if p.cfg.ClientSecret != "" {
		// RFC 6749 2.3.1: the id and the secret are form-encoded, then used as Basic credentials.
		cred := url.QueryEscape(p.cfg.ClientID) + ":" + url.QueryEscape(p.cfg.ClientSecret)
		req.Header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(cred)))
	}
	resp, err := p.hc.Do(req)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrExchange, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("%w: status %d", ErrExchange, resp.StatusCode)
	}
	var tok struct {
		IDToken string `json:"id_token"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxResponse)).Decode(&tok); err != nil || tok.IDToken == "" {
		return "", fmt.Errorf("%w: no id_token in the response", ErrExchange)
	}
	return tok.IDToken, nil
}
