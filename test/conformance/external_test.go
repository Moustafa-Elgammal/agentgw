package conformance

// Black-box checks you can point at YOUR gateway and YOUR backend.
//
// The rest of this package tests the gateway code in process. These checks need nothing of
// the gateway's internals, so they also run against a deployed gateway and a studio's own
// backend. They are skipped unless the environment names a target.
//
// Gateway checks (docs/tutorials/07-test-your-integration.md):
//
//	AGENTGW_CONFORMANCE_URL        MCP endpoint, for example http://gateway:8080/mcp (required)
//	AGENTGW_CONFORMANCE_PLAYER     player id sent as X-Dev-Player (gateway started with --dev-auth)
//	AGENTGW_CONFORMANCE_TOKEN      bearer token instead (OAuth mode). One of the two is required
//	AGENTGW_CONFORMANCE_MANIFEST   manifest the gateway serves (default ../../examples/city/manifest.json)
//	AGENTGW_CONFORMANCE_SCOPE      coach to expect read tools only (default operator)
//
// Backend checks:
//
//	AGENTGW_CONFORMANCE_BACKEND_URL        base URL of the backend, for example http://city-backend:9090
//	AGENTGW_CONFORMANCE_BACKEND_KEY_ID     key id the gateway signs with (default dev-1)
//	AGENTGW_CONFORMANCE_BACKEND_SECRET     that key's secret
//	AGENTGW_CONFORMANCE_BACKEND_READ_PATH  a GET path the backend serves, for example /api/v1/city

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/Moustafa-Elgammal/agentgw/internal/core"
	"github.com/Moustafa-Elgammal/agentgw/internal/manifest"
	"github.com/Moustafa-Elgammal/agentgw/internal/policy"
	"github.com/Moustafa-Elgammal/agentgw/pkg/agentsig"
)

// check is one named, independent assertion. run returns nil when it holds.
type check struct {
	name string
	run  func(ctx context.Context) error
}

// ---------------------------------------------------------------------------------------
// Gateway checks

type gatewayTarget struct {
	url      string      // the MCP endpoint
	header   http.Header // identity: X-Dev-Player or Authorization
	manifest *manifest.Manifest
	coach    bool // the identity is a coach grant: only read tools are exposed
}

type headerRT struct{ h http.Header }

func (r headerRT) RoundTrip(req *http.Request) (*http.Response, error) {
	req = req.Clone(req.Context())
	for k, v := range r.h {
		req.Header[k] = v
	}
	return http.DefaultTransport.RoundTrip(req)
}

func (g gatewayTarget) session(ctx context.Context) (*mcp.ClientSession, error) {
	c := mcp.NewClient(&mcp.Implementation{Name: "conformance-external", Version: "0"}, nil)
	return c.Connect(ctx, &mcp.StreamableClientTransport{
		Endpoint: g.url, HTTPClient: &http.Client{Transport: headerRT{g.header}, Timeout: 30 * time.Second},
	}, nil)
}

// exposed is the set of tools this identity should see.
func (g gatewayTarget) exposed() map[string]core.Tool {
	out := map[string]core.Tool{}
	for _, t := range g.manifest.Tools {
		if t.Tier == core.TierSensitive || (g.coach && t.Tier != core.TierRead) {
			continue
		}
		out[t.Name] = t
	}
	return out
}

// callBody calls a tool and returns the JSON body the gateway put in the result.
func callBody(ctx context.Context, cs *mcp.ClientSession, name string, args map[string]any) (body map[string]any, isError bool, err error) {
	res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		return nil, true, err
	}
	if len(res.Content) == 0 {
		return nil, res.IsError, errors.New("the result has no content")
	}
	text, ok := res.Content[0].(*mcp.TextContent)
	if !ok {
		return nil, res.IsError, errors.New("the first content item is not text")
	}
	if err := json.Unmarshal([]byte(text.Text), &body); err != nil {
		return nil, res.IsError, fmt.Errorf("the result text is not a JSON object: %w", err)
	}
	return body, res.IsError, nil
}

func (g gatewayTarget) checks() []check {
	return []check{
		{"tools list matches the manifest", g.checkToolList},
		{"input schemas carry the manifest limits", g.checkSchemas},
		{"unknown tool is a tool error", g.checkUnknownTool},
		{"bad arguments are denied with invalid_arguments", g.checkBadArguments},
		{"results have the documented shape", g.checkResultShape},
		{"read tools reach the backend and succeed", g.checkReadToolsWork},
		{"a request with no credentials is refused", g.checkUnauthenticated},
	}
}

func (g gatewayTarget) listed(ctx context.Context) (map[string]*mcp.Tool, error) {
	cs, err := g.session(ctx)
	if err != nil {
		return nil, fmt.Errorf("connect: %w", err)
	}
	defer func() { _ = cs.Close() }()
	res, err := cs.ListTools(ctx, nil)
	if err != nil {
		return nil, err
	}
	out := map[string]*mcp.Tool{}
	for _, tl := range res.Tools {
		out[tl.Name] = tl
	}
	return out, nil
}

func (g gatewayTarget) checkToolList(ctx context.Context) error {
	got, err := g.listed(ctx)
	if err != nil {
		return err
	}
	want := g.exposed()
	var problems []string
	for name, mt := range want {
		tl, ok := got[name]
		if !ok {
			problems = append(problems, "missing tool "+name)
			continue
		}
		if tl.Description != mt.Description {
			problems = append(problems, name+": description differs from the manifest")
		}
		if tl.Annotations == nil || tl.Annotations.ReadOnlyHint != (mt.Tier == core.TierRead) {
			problems = append(problems, name+": readOnlyHint must be true exactly for read tools")
		}
	}
	for name := range got {
		if _, ok := want[name]; !ok {
			problems = append(problems, "unexpected tool "+name+" (a sensitive tool, or one the manifest does not have)")
		}
	}
	return joinProblems(problems)
}

func (g gatewayTarget) checkSchemas(ctx context.Context) error {
	got, err := g.listed(ctx)
	if err != nil {
		return err
	}
	var problems []string
	for name, mt := range g.exposed() {
		tl, ok := got[name]
		if !ok {
			continue // reported by the list check
		}
		raw, _ := json.Marshal(tl.InputSchema)
		var sc struct {
			Type       string                    `json:"type"`
			Additional *bool                     `json:"additionalProperties"`
			Required   []string                  `json:"required"`
			Properties map[string]map[string]any `json:"properties"`
		}
		if err := json.Unmarshal(raw, &sc); err != nil || sc.Type != "object" || sc.Additional == nil || *sc.Additional {
			problems = append(problems, name+": the input schema must be a closed object, got "+string(raw))
			continue
		}
		required := map[string]bool{}
		for _, r := range sc.Required {
			required[r] = true
		}
		for pname, p := range mt.Params {
			prop, ok := sc.Properties[pname]
			if !ok {
				problems = append(problems, fmt.Sprintf("%s: parameter %s is missing from the schema", name, pname))
				continue
			}
			if required[pname] == p.Optional {
				problems = append(problems, fmt.Sprintf("%s: parameter %s required=%v but the manifest says optional=%v", name, pname, required[pname], p.Optional))
			}
			if len(p.Enum) > 0 && fmt.Sprint(prop["enum"]) != fmt.Sprint(toAny(p.Enum)) {
				problems = append(problems, fmt.Sprintf("%s: parameter %s enum %v differs from the manifest %v", name, pname, prop["enum"], p.Enum))
			}
			if p.Min != nil && fmt.Sprint(prop["minimum"]) != fmt.Sprint(*p.Min) {
				problems = append(problems, fmt.Sprintf("%s: parameter %s minimum %v differs from the manifest %d", name, pname, prop["minimum"], *p.Min))
			}
			if p.Max != nil && fmt.Sprint(prop["maximum"]) != fmt.Sprint(*p.Max) {
				problems = append(problems, fmt.Sprintf("%s: parameter %s maximum %v differs from the manifest %d", name, pname, prop["maximum"], *p.Max))
			}
		}
		if len(sc.Properties) != len(mt.Params) {
			problems = append(problems, fmt.Sprintf("%s: the schema has %d properties, the manifest %d params", name, len(sc.Properties), len(mt.Params)))
		}
	}
	return joinProblems(problems)
}

func toAny(in []string) []any {
	out := make([]any, len(in))
	for i, s := range in {
		out[i] = s
	}
	return out
}

func (g gatewayTarget) checkUnknownTool(ctx context.Context) error {
	cs, err := g.session(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = cs.Close() }()
	res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "launch_missiles"})
	if err == nil && !res.IsError {
		return errors.New("calling an unknown tool must not succeed")
	}
	return nil
}

// badArgs returns calls that must be refused for a tool: an argument it does not have and,
// when it has parameters, the first parameter with the wrong type.
func badArgs(t core.Tool) []map[string]any {
	out := []map[string]any{{"zz_not_a_param": 1}}
	names := make([]string, 0, len(t.Params))
	for n := range t.Params {
		names = append(names, n)
	}
	sort.Strings(names)
	if len(names) > 0 {
		wrong := any(true) // a boolean is the wrong type for every string, integer and number
		if t.Params[names[0]].Type == "boolean" {
			wrong = "not-a-boolean"
		}
		out = append(out, map[string]any{names[0]: wrong})
	}
	return out
}

func (g gatewayTarget) checkBadArguments(ctx context.Context) error {
	cs, err := g.session(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = cs.Close() }()
	var problems []string
	for name, t := range g.exposed() {
		for _, args := range badArgs(t) {
			body, isErr, err := callBody(ctx, cs, name, args)
			switch {
			case err != nil:
				problems = append(problems, fmt.Sprintf("%s %v: %v", name, args, err))
			case !isErr || body["ok"] != false || body["code"] != "invalid_arguments":
				problems = append(problems, fmt.Sprintf("%s %v: want isError with code invalid_arguments, got %v", name, args, body))
			}
		}
	}
	return joinProblems(problems)
}

func (g gatewayTarget) checkResultShape(ctx context.Context) error {
	cs, err := g.session(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = cs.Close() }()
	for name, t := range g.exposed() {
		args := badArgs(t)[0] // a denial is the one result every gateway can produce on demand
		res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
		if err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		sc, ok := res.StructuredContent.(map[string]any)
		if !ok {
			return fmt.Errorf("%s: structuredContent must be an object, got %T", name, res.StructuredContent)
		}
		if _, ok := sc["ok"].(bool); !ok {
			return fmt.Errorf("%s: structuredContent needs a boolean ok, got %v", name, sc)
		}
		if sc["ok"] == false {
			if c, _ := sc["code"].(string); c == "" {
				return fmt.Errorf("%s: a denial needs a code the agent can read, got %v", name, sc)
			}
			if m, _ := sc["message"].(string); m == "" {
				return fmt.Errorf("%s: a denial needs a message the agent can read, got %v", name, sc)
			}
		}
		return nil // one tool is enough to prove the shape
	}
	return errors.New("the identity sees no tools")
}

// readable returns the read tools that can be called with no arguments.
func (g gatewayTarget) readable() []string {
	var out []string
	for name, t := range g.exposed() {
		if t.Tier != core.TierRead {
			continue
		}
		needs := false
		for _, p := range t.Params {
			needs = needs || !p.Optional
		}
		if !needs {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

func (g gatewayTarget) checkReadToolsWork(ctx context.Context) error {
	names := g.readable()
	if len(names) == 0 {
		return errors.New("the manifest has no read tool that needs no arguments, so there is nothing safe to call")
	}
	cs, err := g.session(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = cs.Close() }()
	var problems []string
	for _, name := range names {
		body, isErr, err := callBody(ctx, cs, name, map[string]any{})
		switch {
		case err != nil:
			problems = append(problems, fmt.Sprintf("%s: %v", name, err))
		case isErr || body["ok"] != true:
			problems = append(problems, fmt.Sprintf("%s: want ok, got %v (is the backend up, and does it accept the gateway's signature?)", name, body))
		}
	}
	return joinProblems(problems)
}

func (g gatewayTarget) checkUnauthenticated(ctx context.Context) error {
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, g.url, strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusUnauthorized {
		return fmt.Errorf("status %d, want 401", resp.StatusCode)
	}
	return nil
}

// ---------------------------------------------------------------------------------------
// Backend checks

type backendTarget struct {
	url      string
	keyID    string
	secret   []byte
	readPath string // a GET path that exists and needs no arguments
}

// send signs a GET with the given parameters and returns the status. tweak may change the
// request after signing, to model tampering.
func (b backendTarget) send(ctx context.Context, path string, at time.Time, secret []byte, player string, tweak func(*http.Request)) (int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(b.url, "/")+path, nil)
	if err != nil {
		return 0, err
	}
	agentsig.Sign(req, b.keyID, secret, "player-1", "conf-"+fmt.Sprint(time.Now().UnixNano()), nil, at)
	if player == "" {
		req.Header.Del(agentsig.HeaderPlayer)
	}
	if tweak != nil {
		tweak(req)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
	return resp.StatusCode, nil
}

func refused(code int) bool { return code == 400 || code == 401 || code == 403 }

func (b backendTarget) checks() []check {
	refusal := func(name string, f func(ctx context.Context) (int, error)) check {
		return check{name, func(ctx context.Context) error {
			code, err := f(ctx)
			if err != nil {
				return err
			}
			if !refused(code) {
				return fmt.Errorf("the backend answered %d, want 400, 401 or 403", code)
			}
			return nil
		}}
	}
	return []check{
		{"a correctly signed request is accepted", func(ctx context.Context) error {
			code, err := b.send(ctx, b.readPath, time.Now(), b.secret, "player-1", nil)
			if err != nil {
				return err
			}
			if code < 200 || code > 299 {
				return fmt.Errorf("the backend answered %d to a valid request for %s, want 2xx (wrong key, secret or path?)", code, b.readPath)
			}
			return nil
		}},
		refusal("a request with no signature is refused", func(ctx context.Context) (int, error) {
			req, _ := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(b.url, "/")+b.readPath, nil)
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				return 0, err
			}
			defer func() { _ = resp.Body.Close() }()
			return resp.StatusCode, nil
		}),
		refusal("a request signed with the wrong secret is refused", func(ctx context.Context) (int, error) {
			return b.send(ctx, b.readPath, time.Now(), []byte("not-the-secret"), "player-1", nil)
		}),
		refusal("a request with an old timestamp is refused", func(ctx context.Context) (int, error) {
			return b.send(ctx, b.readPath, time.Now().Add(-time.Hour), b.secret, "player-1", nil)
		}),
		refusal("a request with a timestamp far in the future is refused", func(ctx context.Context) (int, error) {
			return b.send(ctx, b.readPath, time.Now().Add(time.Hour), b.secret, "player-1", nil)
		}),
		refusal("a request whose signed query was changed in transit is refused", func(ctx context.Context) (int, error) {
			// The path stays a real route, so a 404 cannot hide a backend that skips the check.
			return b.send(ctx, b.readPath, time.Now(), b.secret, "player-1", func(r *http.Request) {
				r.URL.RawQuery = "tampered=1"
			})
		}),
		refusal("a request for a different player than the one signed is refused", func(ctx context.Context) (int, error) {
			return b.send(ctx, b.readPath, time.Now(), b.secret, "player-1", func(r *http.Request) {
				r.Header.Set(agentsig.HeaderPlayer, "player-2")
			})
		}),
		refusal("a request with no player header is refused", func(ctx context.Context) (int, error) {
			return b.send(ctx, b.readPath, time.Now(), b.secret, "", nil)
		}),
	}
}

func joinProblems(p []string) error {
	if len(p) == 0 {
		return nil
	}
	sort.Strings(p)
	return errors.New(strings.Join(p, "\n"))
}

// ---------------------------------------------------------------------------------------
// Tests

// TestExternalGateway runs the gateway checks against the deployment named in the environment.
func TestExternalGateway(t *testing.T) {
	url := os.Getenv("AGENTGW_CONFORMANCE_URL")
	if url == "" {
		t.Skip("AGENTGW_CONFORMANCE_URL is not set")
	}
	header := http.Header{}
	switch {
	case os.Getenv("AGENTGW_CONFORMANCE_TOKEN") != "":
		header.Set("Authorization", "Bearer "+os.Getenv("AGENTGW_CONFORMANCE_TOKEN"))
	case os.Getenv("AGENTGW_CONFORMANCE_PLAYER") != "":
		header.Set("X-Dev-Player", os.Getenv("AGENTGW_CONFORMANCE_PLAYER"))
	default:
		t.Fatal("set AGENTGW_CONFORMANCE_PLAYER (gateway with --dev-auth) or AGENTGW_CONFORMANCE_TOKEN")
	}
	path := os.Getenv("AGENTGW_CONFORMANCE_MANIFEST")
	if path == "" {
		path = manifestPath
	}
	m, err := manifest.LoadFile(path)
	if err != nil {
		t.Fatalf("load the manifest the gateway serves: %v", err)
	}
	g := gatewayTarget{url: url, header: header, manifest: m, coach: os.Getenv("AGENTGW_CONFORMANCE_SCOPE") == "coach"}
	runChecks(t, g.checks())
}

// TestExternalBackend runs the backend checks against the backend named in the environment.
func TestExternalBackend(t *testing.T) {
	url := os.Getenv("AGENTGW_CONFORMANCE_BACKEND_URL")
	if url == "" {
		t.Skip("AGENTGW_CONFORMANCE_BACKEND_URL is not set")
	}
	b := backendTarget{
		url: url, keyID: os.Getenv("AGENTGW_CONFORMANCE_BACKEND_KEY_ID"),
		secret:   []byte(os.Getenv("AGENTGW_CONFORMANCE_BACKEND_SECRET")),
		readPath: os.Getenv("AGENTGW_CONFORMANCE_BACKEND_READ_PATH"),
	}
	if b.keyID == "" {
		b.keyID = "dev-1"
	}
	if len(b.secret) == 0 || b.readPath == "" {
		t.Fatal("set AGENTGW_CONFORMANCE_BACKEND_SECRET and AGENTGW_CONFORMANCE_BACKEND_READ_PATH")
	}
	runChecks(t, b.checks())
}

func runChecks(t *testing.T, cs []check) {
	t.Helper()
	for _, c := range cs {
		t.Run(c.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			defer cancel()
			if err := c.run(ctx); err != nil {
				t.Error(err)
			}
		})
	}
}

// The checks are code too. These tests keep them honest: they must pass on a gateway and a
// backend that are known to be right, and fail on a backend that is known to be wrong.

func TestExternalChecksPassOnTheInProcessGateway(t *testing.T) {
	g := newRig(t, policy.Limits{ActionsPerMinute: 1000, ReadsPerMinute: 1000})
	m := g.m
	runChecks(t, gatewayTarget{url: g.url, header: http.Header{"X-Dev-Player": {"ext-1"}}, manifest: m}.checks())
}

func TestExternalChecksPassOnAReferenceBackend(t *testing.T) {
	b := newBackend(t)
	runChecks(t, backendTarget{url: b.srv.URL, keyID: keyID, secret: secret, readPath: "/api/v1/city"}.checks())
}

func TestExternalBackendChecksCatchALaxBackend(t *testing.T) {
	lax := newLaxBackend(t)
	for _, c := range (backendTarget{url: lax, keyID: keyID, secret: secret, readPath: "/api/v1/city"}).checks() {
		err := c.run(context.Background())
		accepts := strings.Contains(c.name, "is accepted")
		if accepts && err != nil {
			t.Errorf("%s: %v", c.name, err)
		}
		if !accepts && err == nil {
			t.Errorf("%q passed on a backend that accepts everything", c.name)
		}
	}
}

func TestExternalGatewayChecksCatchAWrongManifest(t *testing.T) {
	g := newRig(t, policy.Limits{ActionsPerMinute: 1000, ReadsPerMinute: 1000})
	// The checker believes the gateway serves a tool it does not: the list check must fail.
	wrong := *g.m
	wrong.Tools = append(append([]core.Tool(nil), g.m.Tools...), core.Tool{
		Name: "extra_tool", Description: "not served", Tier: core.TierRead,
		HTTP: core.HTTPBinding{Method: "GET", URL: "/x"},
	})
	target := gatewayTarget{url: g.url, header: http.Header{"X-Dev-Player": {"ext-2"}}, manifest: &wrong}
	if err := target.checkToolList(context.Background()); err == nil || !strings.Contains(err.Error(), "extra_tool") {
		t.Fatalf("the list check must name the missing tool, got %v", err)
	}
	// A wrong authority is refused too: with no identity the gateway answers 401, so the
	// session cannot even start.
	anon := gatewayTarget{url: g.url, header: http.Header{}, manifest: g.m}
	if err := anon.checkToolList(context.Background()); err == nil {
		t.Fatal("an identity the gateway refuses must fail the list check")
	}
}

// newLaxBackend is a backend that does no signature verification at all.
func newLaxBackend(t *testing.T) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`{}`)) }))
	t.Cleanup(srv.Close)
	return srv.URL
}
