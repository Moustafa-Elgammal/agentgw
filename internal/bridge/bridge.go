// Package bridge connects a local stdio MCP client to an upstream streamable HTTP
// MCP endpoint (the gateway's /mcp). Desktop clients that can only launch local
// stdio servers use it to reach a gateway on localhost.
//
// The bridge holds no policy. It lists the upstream tools on every tools/list and
// forwards every tools/call, so the gateway stays the only place where limits,
// scopes and audit are decided. Stdout is the protocol channel: the bridge
// only ever logs to the writer in Config.Log.
package bridge

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Config is everything Run needs.
type Config struct {
	// URL is the upstream streamable HTTP endpoint, for example http://127.0.0.1:8080/mcp.
	URL string
	// Headers are added to every upstream request (for example "Authorization": "Bearer ...").
	Headers http.Header
	// Transport is the local side. Use &mcp.StdioTransport{} in production.
	Transport mcp.Transport
	// Log receives diagnostics. It must never be stdout. Nil discards them.
	Log io.Writer
	// HTTPClient is used for upstream calls. Nil means a client with a 30s timeout.
	HTTPClient *http.Client
}

// ParseHeader parses a "Name: value" string.
func ParseHeader(s string) (name, value string, err error) {
	name, value, ok := strings.Cut(s, ":")
	name = strings.TrimSpace(name)
	if !ok || name == "" {
		return "", "", fmt.Errorf("invalid header %q, want \"Name: value\"", s)
	}
	return name, strings.TrimSpace(value), nil
}

// headerRT adds fixed headers and remembers whether the last answer was a 401.
type headerRT struct {
	next    http.RoundTripper
	headers http.Header
	denied  *atomic.Bool
}

func (h headerRT) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	for k, vs := range h.headers {
		r.Header.Del(k)
		for _, v := range vs {
			r.Header.Add(k, v)
		}
	}
	resp, err := h.next.RoundTrip(r)
	if err == nil {
		h.denied.Store(resp.StatusCode == http.StatusUnauthorized)
	}
	return resp, err
}

const unauthorizedHint = "upstream rejected the credentials (401): check the --header value, for example " +
	"\"Authorization: Bearer <token>\" for a gateway with OAuth, or \"X-Dev-Player: <name>\" for one started with --dev-auth"

type bridge struct {
	up     *mcp.ClientSession
	log    *slog.Logger
	denied *atomic.Bool
}

// Run connects to the upstream, then serves the local transport until the local
// client goes away or ctx ends. It fails if the upstream cannot be reached at startup.
func Run(ctx context.Context, cfg Config) error {
	if cfg.URL == "" {
		return errors.New("upstream URL is required")
	}
	if cfg.Transport == nil {
		return errors.New("local transport is required")
	}
	logw := cfg.Log
	if logw == nil {
		logw = io.Discard
	}
	b := &bridge{log: slog.New(slog.NewTextHandler(logw, nil)), denied: &atomic.Bool{}}

	hc := &http.Client{Timeout: 30 * time.Second}
	if cfg.HTTPClient != nil {
		c := *cfg.HTTPClient
		hc = &c
	}
	next := hc.Transport
	if next == nil {
		next = http.DefaultTransport
	}
	hc.Transport = headerRT{next: next, headers: cfg.Headers, denied: b.denied}

	client := mcp.NewClient(&mcp.Implementation{Name: "gwbridge", Version: "0.1.0"}, nil)
	up, err := client.Connect(ctx, &mcp.StreamableClientTransport{
		Endpoint: cfg.URL, HTTPClient: hc, DisableStandaloneSSE: true, MaxRetries: -1,
	}, nil)
	if err != nil {
		return b.upstreamError("connect to "+cfg.URL, err)
	}
	defer func() { _ = up.Close() }()
	b.up = up

	tools, err := b.listTools(ctx, nil)
	if err != nil {
		return b.upstreamError("list tools", err)
	}
	b.log.Info("connected", "upstream", cfg.URL, "tools", len(tools.Tools))

	srv := mcp.NewServer(&mcp.Implementation{Name: "gwbridge", Version: "0.1.0"},
		&mcp.ServerOptions{Capabilities: &mcp.ServerCapabilities{Tools: &mcp.ToolCapabilities{}}})
	srv.AddReceivingMiddleware(b.middleware)
	return srv.Run(ctx, cfg.Transport)
}

// upstreamError turns a failure into a readable error and logs the 401 hint.
func (b *bridge) upstreamError(what string, err error) error {
	if b.denied.Load() {
		b.log.Error(unauthorizedHint)
		return fmt.Errorf("%s: %s: %w", what, unauthorizedHint, err)
	}
	return fmt.Errorf("%s: %w", what, err)
}

// middleware answers tools/list and tools/call from the upstream. Everything else
// (initialize, ping) is handled by the local server itself.
func (b *bridge) middleware(next mcp.MethodHandler) mcp.MethodHandler {
	return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
		switch method {
		case "tools/list":
			p, _ := req.GetParams().(*mcp.ListToolsParams)
			res, err := b.listTools(ctx, p)
			if err != nil {
				return nil, b.upstreamError("list tools", err)
			}
			return res, nil
		case "tools/call":
			p, ok := req.GetParams().(*mcp.CallToolParamsRaw)
			if !ok {
				return next(ctx, method, req)
			}
			return b.callTool(ctx, p)
		}
		return next(ctx, method, req)
	}
}

func (b *bridge) listTools(ctx context.Context, p *mcp.ListToolsParams) (*mcp.ListToolsResult, error) {
	var params *mcp.ListToolsParams
	if p != nil {
		params = &mcp.ListToolsParams{Cursor: p.Cursor}
	}
	return b.up.ListTools(ctx, params)
}

// callTool forwards one call. Gateway denials come back as results with isError and
// are passed on unchanged. A transport failure becomes a tool error so the stdio
// session survives; a cancelled call is returned as an error instead.
func (b *bridge) callTool(ctx context.Context, p *mcp.CallToolParamsRaw) (mcp.Result, error) {
	var args any
	if len(p.Arguments) > 0 {
		args = p.Arguments
	}
	res, err := b.up.CallTool(ctx, &mcp.CallToolParams{Meta: p.Meta, Name: p.Name, Arguments: args})
	if err == nil {
		return res, nil
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	msg := fmt.Sprintf("the gateway could not be reached for tool %q: %v", p.Name, err)
	if b.denied.Load() {
		b.log.Error(unauthorizedHint)
		msg = fmt.Sprintf("tool %q was rejected by the gateway: %s", p.Name, unauthorizedHint)
	} else {
		b.log.Error("upstream call failed", "tool", p.Name, "err", err)
	}
	return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: msg}}}, nil
}
