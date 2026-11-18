package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Moustafa-Elgammal/agentgw/internal/audit"
	"github.com/Moustafa-Elgammal/agentgw/internal/connector"
	"github.com/Moustafa-Elgammal/agentgw/internal/core"
	"github.com/Moustafa-Elgammal/agentgw/internal/manifest"
	"github.com/Moustafa-Elgammal/agentgw/internal/policy"
)

var (
	secret = []byte("s")
	player = core.Principal{TenantID: "t", PlayerID: "p1"}
)

const doc = `{"version":1,"tools":[
 {"name":"get_state","description":"State.","tier":"read","http":{"method":"GET","url":"/state"}},
 {"name":"sell_resource","description":"Sell.","tier":"medium",
  "caps":{"per_day_amount":100,"amount_param":"quantity"},
  "http":{"method":"POST","url":"/sell"},
  "params":{"resource":{"type":"string","enum":["food","water"]},
            "quantity":{"type":"integer","min":1,"max":1000}}},
 {"name":"collect","description":"Collect.","tier":"safe","caps":{"cooldown_seconds":60},
  "http":{"method":"POST","url":"/collect"}},
 {"name":"gift","description":"Gift.","tier":"sensitive","http":{"method":"POST","url":"/gift"}}
]}`

type fixture struct {
	svc     *Service
	audit   *audit.Memory
	backend *httptest.Server
	hits    *int
	fail    *bool
}

func setup(t *testing.T, limits policy.Limits) fixture {
	t.Helper()
	m, err := manifest.Parse(strings.NewReader(doc))
	if err != nil {
		t.Fatal(err)
	}
	hits, fail := new(int), new(bool)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		*hits++
		if *fail {
			w.WriteHeader(500)
			return
		}
		_, _ = w.Write([]byte(`{"done":true}`))
	}))
	t.Cleanup(srv.Close)
	a := &audit.Memory{}
	svc := New(m, policy.NewMemory(limits, nil), connector.NewHTTP(srv.URL, "k1", secret), a)
	return fixture{svc, a, srv, hits, fail}
}

func call(f fixture, name, args string) core.Result {
	return f.svc.Call(context.Background(), player, name, json.RawMessage(args))
}

func TestHappyPathAndAudit(t *testing.T) {
	f := setup(t, policy.DefaultLimits)
	res := call(f, "sell_resource", `{"resource":"food","quantity":10}`)
	if !res.OK || *f.hits != 1 {
		t.Fatalf("res=%+v hits=%d", res, *f.hits)
	}
	ev := f.audit.Events()
	if len(ev) != 1 || ev[0].Outcome != "ok" || ev[0].Tool != "sell_resource" || len(ev[0].ArgsSHA) != 64 {
		t.Fatalf("audit = %+v", ev)
	}
}

func TestValidationErrorsAreAgentReadable(t *testing.T) {
	f := setup(t, policy.DefaultLimits)
	cases := map[string]string{
		`{"resource":"gold","quantity":1}`:       "choose one of",
		`{"resource":"food","quantity":0}`:       "at least 1",
		`{"resource":"food","quantity":1.5}`:     "must be an integer",
		`{"resource":"food"}`:                    `missing argument "quantity"`,
		`{"resource":"food","quantity":1,"x":2}`: `unknown argument "x"`,
		`[1,2]`:                                  "JSON object",
	}
	for args, want := range cases {
		res := call(f, "sell_resource", args)
		if res.OK || res.Code != "invalid_arguments" || !strings.Contains(res.Message, want) {
			t.Errorf("%s -> %+v (want %q)", args, res, want)
		}
	}
	if *f.hits != 0 {
		t.Fatal("invalid calls must never reach the backend")
	}
}

func TestInvalidAttemptsStillCountForRateLimit(t *testing.T) {
	f := setup(t, policy.Limits{ActionsPerMinute: 3, ReadsPerMinute: 10})
	for i := 0; i < 3; i++ {
		if res := call(f, "sell_resource", `{"resource":"gold","quantity":1}`); res.Code != "invalid_arguments" {
			t.Fatalf("attempt %d: %+v", i, res)
		}
	}
	if res := call(f, "sell_resource", `{"resource":"food","quantity":1}`); res.Code != "rate_limited" {
		t.Fatalf("a valid call after 3 invalid ones must be rate limited, got %+v", res)
	}
}

func TestDailyCapUsesAmountParam(t *testing.T) {
	f := setup(t, policy.DefaultLimits)
	if res := call(f, "sell_resource", `{"resource":"food","quantity":70}`); !res.OK {
		t.Fatal(res)
	}
	res := call(f, "sell_resource", `{"resource":"food","quantity":40}`)
	if res.Code != "daily_amount_cap" || !strings.Contains(res.Message, "30 left") {
		t.Fatalf("got %+v", res)
	}
}

func TestBackendFailureReleasesReservation(t *testing.T) {
	f := setup(t, policy.DefaultLimits)
	*f.fail = true
	res := call(f, "sell_resource", `{"resource":"food","quantity":90}`)
	if res.Code != "backend_unavailable" {
		t.Fatalf("got %+v", res)
	}
	*f.fail = false
	if res := call(f, "sell_resource", `{"resource":"food","quantity":90}`); !res.OK {
		t.Fatalf("the failed call must not consume the daily cap: %+v", res)
	}
}

func TestCooldownAfterSuccessOnly(t *testing.T) {
	f := setup(t, policy.DefaultLimits)
	if res := call(f, "collect", `{}`); !res.OK {
		t.Fatal(res)
	}
	if res := call(f, "collect", `{}`); res.Code != "cooldown" {
		t.Fatalf("got %+v", res)
	}
}

func TestSensitiveAndUnknownToolsAreDenied(t *testing.T) {
	f := setup(t, policy.DefaultLimits)
	if res := call(f, "gift", `{}`); res.Code != "tier_disabled" {
		t.Fatalf("got %+v", res)
	}
	if res := call(f, "nope", `{}`); res.Code != "unknown_tool" {
		t.Fatalf("got %+v", res)
	}
	if *f.hits != 0 {
		t.Fatal("denied calls must not reach the backend")
	}
	last := f.audit.Events()
	if len(last) != 2 || last[0].Outcome != "denied" {
		t.Fatalf("denials must be audited: %+v", last)
	}
}

type brokenPolicy struct{ policy.Policy }

func (brokenPolicy) Admit(context.Context, core.Principal, core.Tool) (policy.Decision, error) {
	return policy.Decision{}, errors.New("redis down")
}

func TestPolicyOutageFailsClosed(t *testing.T) {
	f := setup(t, policy.DefaultLimits)
	f.svc.policy = brokenPolicy{}
	res := call(f, "sell_resource", `{"resource":"food","quantity":1}`)
	if res.Code != "policy_unavailable" || *f.hits != 0 {
		t.Fatalf("got %+v hits=%d", res, *f.hits)
	}
}
