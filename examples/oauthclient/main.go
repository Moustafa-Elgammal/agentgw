// Command oauthclient is a scripted MCP client for the tutorials. It does what an agent's
// MCP client does to link with a player: discover the gateway, register, open the consent
// page, let the player enter the link ticket and pick a scope, and exchange the code for
// tokens (PKCE S256). It prints the tokens as JSON.
//
//	oauthclient --url http://gateway:8080 --ticket "$TICKET" --scope operator
//
// The ticket comes from the studio backend (POST /v1/studio/link-tickets, see
// examples/studiocall). Refresh with --refresh-token instead of --ticket.
package main

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

const redirect = "http://127.0.0.1/callback" // never opened: the client reads the redirect

func main() {
	base := flag.String("url", "http://gateway:8080", "gateway base URL")
	ticket := flag.String("ticket", "", "link ticket the player got from the game")
	scope := flag.String("scope", "coach", "scope the player chooses on the consent page: coach or operator")
	refresh := flag.String("refresh-token", "", "trade this refresh token for new tokens instead of linking")
	clientID := flag.String("client-id", "", "client id from an earlier run (required with --refresh-token)")
	name := flag.String("name", "tutorial agent", "client name shown on the consent page")
	flag.Parse()

	c := &client{http: &http.Client{
		Timeout:       15 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}
	if *refresh != "" {
		if *clientID == "" {
			fatal("--client-id is required with --refresh-token")
		}
		c.printTokens(*clientID, c.token(*base, url.Values{
			"grant_type": {"refresh_token"}, "client_id": {*clientID}, "refresh_token": {*refresh}}))
		return
	}
	if *ticket == "" {
		fatal("--ticket is required")
	}

	// 1. Discovery: the protected resource names its authorization server.
	prm := c.getJSON(*base + "/.well-known/oauth-protected-resource/mcp")
	resource, _ := prm["resource"].(string)
	issuers, _ := prm["authorization_servers"].([]any)
	if resource == "" || len(issuers) != 1 {
		fatal("unexpected protected resource metadata")
	}
	issuer, _ := issuers[0].(string)

	// 2. Dynamic client registration.
	reg, _ := json.Marshal(map[string]any{"client_name": *name, "redirect_uris": []string{redirect}})
	resp := c.do("POST", *base+"/register", "application/json", string(reg))
	var registered struct {
		ClientID string `json:"client_id"`
	}
	mustJSON(resp, 201, &registered)

	// 3. Authorization request with PKCE.
	raw := make([]byte, 32)
	_, _ = rand.Read(raw)
	verifier := base64.RawURLEncoding.EncodeToString(raw)
	sum := sha256.Sum256([]byte(verifier))
	q := url.Values{
		"client_id": {registered.ClientID}, "redirect_uri": {redirect}, "response_type": {"code"},
		"code_challenge": {base64.RawURLEncoding.EncodeToString(sum[:])}, "code_challenge_method": {"S256"},
		"state": {"tutorial"}, "resource": {resource},
	}
	page := c.do("GET", *base+"/authorize?"+q.Encode(), "", "")
	if page.status != 200 {
		fatal(fmt.Sprintf("consent page: HTTP %d", page.status))
	}

	// 4. The player types the ticket and picks a scope on the consent page.
	form := url.Values{}
	for k, v := range q {
		form[k] = v
	}
	form.Set("ticket", *ticket)
	form.Set("scope_choice", *scope)
	form.Set("decision", "allow")
	resp = c.do("POST", *base+"/authorize", "application/x-www-form-urlencoded", form.Encode())
	loc, err := url.Parse(resp.header.Get("Location"))
	if resp.status != http.StatusSeeOther || err != nil || loc.Query().Get("code") == "" {
		fatal(fmt.Sprintf("authorize: HTTP %d: %s", resp.status, strings.TrimSpace(string(resp.body))))
	}
	if loc.Query().Get("iss") != issuer {
		fatal("the redirect did not carry the expected issuer")
	}

	// 5. Trade the code for tokens.
	c.printTokens(registered.ClientID, c.token(*base, url.Values{
		"grant_type": {"authorization_code"}, "code": {loc.Query().Get("code")}, "client_id": {registered.ClientID},
		"redirect_uri": {redirect}, "code_verifier": {verifier}, "resource": {resource},
	}))
}

type client struct{ http *http.Client }

type reply struct {
	status int
	header http.Header
	body   []byte
}

func (c *client) do(method, u, ctype, body string) reply {
	req, err := http.NewRequest(method, u, strings.NewReader(body))
	if err != nil {
		fatal(err.Error())
	}
	if ctype != "" {
		req.Header.Set("Content-Type", ctype)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		fatal(err.Error())
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return reply{resp.StatusCode, resp.Header, b}
}

func (c *client) getJSON(u string) map[string]any {
	r := c.do("GET", u, "", "")
	var out map[string]any
	mustJSON(r, 200, &out)
	return out
}

func mustJSON(r reply, want int, v any) {
	if r.status != want || json.Unmarshal(r.body, v) != nil {
		fatal(fmt.Sprintf("HTTP %d (wanted %d): %s", r.status, want, strings.TrimSpace(string(r.body))))
	}
}

type tokens struct {
	ClientID string `json:"client_id"`
	Access   string `json:"access_token"`
	Refresh  string `json:"refresh_token"`
	Scope    string `json:"scope"`
	Type     string `json:"token_type"`
}

func (c *client) token(base string, form url.Values) tokens {
	r := c.do("POST", base+"/token", "application/x-www-form-urlencoded", form.Encode())
	var tk tokens
	mustJSON(r, 200, &tk)
	return tk
}

func (c *client) printTokens(clientID string, tk tokens) {
	tk.ClientID = clientID
	out, _ := json.Marshal(tk) //nolint:gosec // printing the tokens is the point of this tool
	fmt.Println(string(out))
}

func fatal(msg string) {
	fmt.Fprintln(os.Stderr, "oauthclient:", msg)
	os.Exit(1)
}
