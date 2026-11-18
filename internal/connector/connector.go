// Package connector calls a studio's backend for a tool. Level 0 maps a tool onto
// an existing REST endpoint; every request is signed with pkg/agentsig.
package connector

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
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
}

const maxBody = 1 << 20 // 1 MiB

var placeholder = regexp.MustCompile(`\{([a-z][a-z0-9_]*)\}`)

// NewHTTP builds a connector with a 10 second timeout.
func NewHTTP(baseURL, keyID string, secret []byte) *HTTP {
	return &HTTP{
		BaseURL: strings.TrimRight(baseURL, "/"), KeyID: keyID, Secret: secret,
		Client: &http.Client{Timeout: 10 * time.Second}, Now: time.Now,
	}
}

// Call sends the request. A transport failure is a Go error (the caller fails
// closed and releases its reservation). A backend 4xx is a normal denied Result.
func (c *HTTP) Call(ctx context.Context, p core.Principal, t core.Tool, args map[string]any, idem string) (core.Result, error) {
	rest := map[string]any{}
	for k, v := range args {
		rest[k] = v
	}
	path := placeholder.ReplaceAllStringFunc(t.HTTP.URL, func(m string) string {
		name := m[1 : len(m)-1]
		v := fmt.Sprint(rest[name])
		delete(rest, name)
		return url.PathEscape(v)
	})

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
	agentsig.Sign(req, c.KeyID, c.Secret, p.PlayerID, idem, body, c.Now())

	resp, err := c.Client.Do(req)
	if err != nil {
		return core.Result{}, fmt.Errorf("backend unreachable: %w", err)
	}
	defer resp.Body.Close()
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
