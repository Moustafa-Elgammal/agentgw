package main

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/Moustafa-Elgammal/agentgw/pkg/agentsig"
)

// directRoute is how the gateway maps a tool of examples/city/manifest.yaml onto the fake city
// backend. Direct mode sends the same request itself, so the two runs differ only by the gateway.
type directRoute struct {
	method, path string
	hasBody      bool // the arguments are sent as the JSON body (POST); GET sends none
}

var directRoutes = map[string]directRoute{
	"get_city_state":  {http.MethodGet, "/api/v1/city", false},
	"collect_taxes":   {http.MethodPost, "/api/v1/taxes/collect", true},
	"sell_resource":   {http.MethodPost, "/api/v1/market/sell", true},
	"build_structure": {http.MethodPost, "/api/v1/build", true},
}

// directSigner signs requests the way the gateway's connector does.
type directSigner struct {
	keyID  string
	secret []byte
}

// request builds the signed backend request for one call. idem must be unique per call.
func (d directSigner) request(ctx context.Context, base, player, tool, args, idem string) (*http.Request, error) {
	r, ok := directRoutes[tool]
	if !ok {
		return nil, fmt.Errorf("direct mode does not know tool %q", tool)
	}
	var body []byte
	if r.hasBody {
		body = []byte(args)
	}
	req, err := http.NewRequestWithContext(ctx, r.method, strings.TrimRight(base, "/")+r.path, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	if r.hasBody {
		req.Header.Set("Content-Type", "application/json")
	}
	agentsig.Sign(req, d.keyID, d.secret, player, idem, body, time.Now())
	return req, nil
}
