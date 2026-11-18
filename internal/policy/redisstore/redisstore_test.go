package redisstore

import (
	"context"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/Moustafa-Elgammal/agentgw/internal/audit"
	"github.com/Moustafa-Elgammal/agentgw/internal/core"
	"github.com/Moustafa-Elgammal/agentgw/internal/gateway"
	"github.com/Moustafa-Elgammal/agentgw/internal/manifest"
	"github.com/Moustafa-Elgammal/agentgw/internal/policy"
	"github.com/Moustafa-Elgammal/agentgw/internal/policy/policytest"
)

// These tests need a real Redis: docker compose run --rm dev make test starts one
// and sets AGENTGW_TEST_REDIS_ADDR. Without it they skip.
func testClient(t *testing.T) *redis.Client {
	t.Helper()
	addr := os.Getenv("AGENTGW_TEST_REDIS_ADDR")
	if addr == "" {
		t.Skip("AGENTGW_TEST_REDIS_ADDR not set; run through docker compose run --rm dev")
	}
	c := redis.NewClient(&redis.Options{Addr: addr})
	t.Cleanup(func() { _ = c.Close() })
	if err := c.Ping(context.Background()).Err(); err != nil {
		t.Fatalf("redis not reachable at %s: %v", addr, err)
	}
	return c
}

// uniquePrefix isolates one test's keys and removes them afterwards.
func uniquePrefix(t *testing.T, c *redis.Client) string {
	t.Helper()
	prefix := "test:" + nonce() + ":"
	t.Cleanup(func() {
		ctx := context.Background()
		iter := c.Scan(ctx, 0, prefix+"*", 200).Iterator()
		for iter.Next(ctx) {
			_ = c.Del(ctx, iter.Val()).Err()
		}
	})
	return prefix
}

func TestRedisContract(t *testing.T) {
	c := testClient(t)
	policytest.Run(t, func(t *testing.T, l policy.Limits, now func() time.Time) policytest.Store {
		return New(c, uniquePrefix(t, c), l, now)
	})
}

// Two stores on one Redis stand in for two gateway replicas.
func TestReplicasShareOneBudget(t *testing.T) {
	c := testClient(t)
	prefix := uniquePrefix(t, c)
	now := func() time.Time { return time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC) }
	stores := []*Store{New(c, prefix, policy.DefaultLimits, now), New(c, prefix, policy.DefaultLimits, now)}
	tool := core.Tool{Name: "sell", Tier: core.TierMedium, Caps: core.Caps{PerDayAmount: 100, AmountParam: "q"}}
	p := core.Principal{TenantID: "t", PlayerID: "p1"}

	var wg sync.WaitGroup
	var granted atomic.Int64
	for i := 0; i < 200; i++ {
		wg.Add(1)
		go func(s *Store) {
			defer wg.Done()
			r, d, err := s.Reserve(context.Background(), p, tool, 10)
			if err == nil && d.Allowed {
				granted.Add(10)
				_ = s.Settle(context.Background(), r, true)
			}
		}(stores[i%2])
	}
	wg.Wait()
	if granted.Load() != 100 {
		t.Fatalf("two replicas granted %d, want exactly the cap of 100", granted.Load())
	}

	// A switch set through one replica is seen by the other.
	if err := stores[0].SetSwitch(context.Background(), policy.Switch{Tenant: "t"}, true); err != nil {
		t.Fatal(err)
	}
	if d, _ := stores[1].Admit(context.Background(), p, tool); d.Allowed || d.Code != "switched_off" {
		t.Fatalf("replica 2 must honor the switch, got %+v", d)
	}
}

// When Redis is unreachable every operation returns an error and never an allow.
func TestFailsClosedWhenRedisIsDown(t *testing.T) {
	c := redis.NewClient(&redis.Options{
		Addr: "127.0.0.1:1", DialTimeout: 200 * time.Millisecond, MaxRetries: -1,
	})
	defer func() { _ = c.Close() }()
	s := New(c, "down:", policy.DefaultLimits, nil)
	ctx := context.Background()
	p := core.Principal{TenantID: "t", PlayerID: "p1"}
	tool := core.Tool{Name: "sell", Tier: core.TierMedium, Caps: core.Caps{PerDayCalls: 1}}

	if d, err := s.Admit(ctx, p, tool); err == nil || d.Allowed {
		t.Errorf("Admit must error and not allow: %+v %v", d, err)
	}
	if r, d, err := s.Reserve(ctx, p, tool, 1); err == nil || d.Allowed || r.Key != "" {
		t.Errorf("Reserve must error and not allow: %+v %+v %v", r, d, err)
	}
	if err := s.SetSwitch(ctx, policy.Switch{}, true); err == nil {
		t.Error("SetSwitch must report the failure")
	}
	if _, err := s.Switches(ctx); err == nil {
		t.Error("Switches must report the failure")
	}
	if err := s.Ping(ctx); err == nil {
		t.Error("Ping must fail")
	}
}

// Sensitive tools are refused without touching Redis, so they stay refused during an outage.
func TestSensitiveRefusedEvenWhenRedisIsDown(t *testing.T) {
	c := redis.NewClient(&redis.Options{Addr: "127.0.0.1:1", DialTimeout: 100 * time.Millisecond, MaxRetries: -1})
	defer func() { _ = c.Close() }()
	s := New(c, "down:", policy.DefaultLimits, nil)
	d, err := s.Admit(context.Background(), core.Principal{}, core.Tool{Name: "gift", Tier: core.TierSensitive})
	if err != nil || d.Allowed || d.Code != "tier_disabled" {
		t.Fatalf("got %+v %v", d, err)
	}
}

type countingConn struct{ calls atomic.Int64 }

func (c *countingConn) Call(context.Context, core.Principal, core.Tool, map[string]any, string) (core.Result, error) {
	c.calls.Add(1)
	return core.Result{OK: true}, nil
}

// The spec says: policy store down means calls are refused. Check it through the gateway.
func TestGatewayRefusesEverythingWhenRedisIsDown(t *testing.T) {
	m, err := manifest.Parse(strings.NewReader(`{"version":1,"tools":[
	 {"name":"get_state","description":"State.","tier":"read","http":{"method":"GET","url":"/s"}},
	 {"name":"collect","description":"Collect.","tier":"safe","http":{"method":"POST","url":"/c"}}]}`))
	if err != nil {
		t.Fatal(err)
	}
	c := redis.NewClient(&redis.Options{Addr: "127.0.0.1:1", DialTimeout: 100 * time.Millisecond, MaxRetries: -1})
	defer func() { _ = c.Close() }()
	conn, aud := &countingConn{}, &audit.Memory{}
	svc := gateway.New(m, New(c, "down:", policy.DefaultLimits, nil), conn, aud)

	for _, tool := range []string{"get_state", "collect"} {
		res := svc.Call(context.Background(), core.Principal{TenantID: "t", PlayerID: "p"}, tool, nil)
		if res.OK || res.Code != "policy_unavailable" {
			t.Errorf("%s: want policy_unavailable, got %+v", tool, res)
		}
	}
	if conn.calls.Load() != 0 {
		t.Error("the backend must not be reached during an outage")
	}
	for _, e := range aud.Events() {
		if e.Outcome != "error" || e.Code != "policy_unavailable" {
			t.Errorf("outage must be audited as an error: %+v", e)
		}
	}
	if len(aud.Events()) != 2 {
		t.Errorf("want 2 audit events, got %d", len(aud.Events()))
	}
}
