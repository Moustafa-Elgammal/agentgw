// Package redisstore is the shared policy store: rate limits, daily caps,
// cooldowns and kill switches kept in Redis so several gateway replicas enforce
// one budget.
//
// Every decision runs as a single Lua script, so Admit, Reserve and Settle are
// atomic on the server and concurrent replicas cannot oversubscribe a cap. The
// clock is the caller's (not Redis TIME) so tests can control it. Replicas must
// therefore have reasonably synchronized clocks. Any Redis error is returned to
// the caller, which fails closed: the gateway refuses the call.
//
// Scripts touch keys in more than one hash slot (switches are global), so this
// store targets a single Redis node or a primary with replicas, not Redis Cluster.
package redisstore

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/Moustafa-Elgammal/agentgw/internal/core"
	"github.com/Moustafa-Elgammal/agentgw/internal/policy"
)

const (
	window    = time.Minute
	dailyTTL  = 48 * time.Hour
	sep       = "\x1f" // unit separator, joins the fields of a stored switch
	switchKey = "switches"
)

// Result codes returned by the scripts.
const (
	resOK        = 0
	resRate      = 1
	resSwitched  = 2
	resCooldown  = 3
	resCallCap   = 4
	resAmountCap = 5
)

// Both scripts check kill switches first. A call matches a switch when each
// field of the switch is either empty (wildcard) or equal, so the script tries
// the eight tenant/tool/player combinations.
const switchCheck = `
for i = 0, 7 do
  local a = (i % 2 == 1) and ARGV[@A@] or ""
  local b = (math.floor(i / 2) % 2 == 1) and ARGV[@B@] or ""
  local c = (math.floor(i / 4) % 2 == 1) and ARGV[@C@] or ""
  if redis.call('SISMEMBER', @K@, a .. '\31' .. b .. '\31' .. c) == 1 then return {2} end
end
`

// withSwitchCheck fills in where the tenant, tool and player sit in ARGV and
// which key holds the switch set.
func withSwitchCheck(tenantArg, toolArg, playerArg int, key string) string {
	return strings.NewReplacer(
		"@A@", fmt.Sprint(tenantArg), "@B@", fmt.Sprint(toolArg), "@C@", fmt.Sprint(playerArg), "@K@", key,
	).Replace(switchCheck)
}

var admitScript = redis.NewScript(withSwitchCheck(5, 6, 7, "KEYS[2]") + `
local now = tonumber(ARGV[1])
local win = tonumber(ARGV[2])
redis.call('ZREMRANGEBYSCORE', KEYS[1], '-inf', '(' .. (now - win))
if redis.call('ZCARD', KEYS[1]) >= tonumber(ARGV[3]) then return {1} end
redis.call('ZADD', KEYS[1], now, ARGV[4])
redis.call('PEXPIRE', KEYS[1], win + 1000)
return {0}
`)

// KEYS: 1 daily hash, 2 cooldown, 3 switches
// ARGV: 1 now_ms, 2 cooldown on (0/1), 3 per-day calls, 4 per-day amount, 5 amount,
//
//	6 tenant, 7 tool, 8 player, 9 daily ttl seconds
var reserveScript = redis.NewScript(withSwitchCheck(6, 7, 8, "KEYS[3]") + `
local now = tonumber(ARGV[1])
if ARGV[2] == '1' then
  local untilMs = tonumber(redis.call('GET', KEYS[2]) or '0')
  if now < untilMs then return {3, untilMs - now} end
end
local calls = tonumber(redis.call('HGET', KEYS[1], 'calls') or '0')
local amount = tonumber(redis.call('HGET', KEYS[1], 'amount') or '0')
local perCalls = tonumber(ARGV[3])
local perAmount = tonumber(ARGV[4])
local want = tonumber(ARGV[5])
if perCalls > 0 and calls + 1 > perCalls then return {4, perCalls} end
if perAmount > 0 and amount + want > perAmount then return {5, perAmount - amount} end
redis.call('HINCRBY', KEYS[1], 'calls', 1)
redis.call('HINCRBY', KEYS[1], 'amount', want)
redis.call('EXPIRE', KEYS[1], tonumber(ARGV[9]))
return {0}
`)

// Release only touches a record that still exists, like Memory does.
var releaseScript = redis.NewScript(`
if redis.call('EXISTS', KEYS[1]) == 1 then
  redis.call('HINCRBY', KEYS[1], 'calls', -tonumber(ARGV[1]))
  redis.call('HINCRBY', KEYS[1], 'amount', -tonumber(ARGV[2]))
end
return 1
`)

// Store implements policy.Policy and policy.Switchboard on Redis.
type Store struct {
	rdb    redis.Cmdable
	prefix string
	limits policy.Limits
	now    func() time.Time
}

// New builds a Store. prefix namespaces every key (use one per deployment). A nil clock uses time.Now.
func New(rdb redis.Cmdable, prefix string, l policy.Limits, now func() time.Time) *Store {
	if now == nil {
		now = time.Now
	}
	return &Store{rdb: rdb, prefix: prefix, limits: l, now: now}
}

// Ping checks the connection. Call it at startup so a bad address fails early.
func (s *Store) Ping(ctx context.Context) error { return s.rdb.Ping(ctx).Err() }

func principalKey(p core.Principal) string { return p.TenantID + "/" + p.PlayerID }

// k builds a key. The principal sits in a hash tag so its keys share a slot.
func (s *Store) k(parts ...string) string { return s.prefix + strings.Join(parts, ":") }

func nonce() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func (s *Store) switchesKey() string { return s.k(switchKey) }

// Admit enforces the tier switch, kill switches and the per-minute rate limit.
func (s *Store) Admit(ctx context.Context, p core.Principal, t core.Tool) (policy.Decision, error) {
	if t.Tier == core.TierSensitive {
		return policy.TierDisabled(t.Name), nil
	}
	kind, max := "read", s.limits.ReadsPerMinute
	if t.Tier.IsAction() {
		kind, max = "action", s.limits.ActionsPerMinute
	}
	res, err := admitScript.Run(ctx, s.rdb,
		[]string{s.k("rl", "{"+principalKey(p)+"}", kind), s.switchesKey()},
		s.now().UnixMilli(), window.Milliseconds(), max, nonce(), p.TenantID, t.Name, p.PlayerID,
	).Int64Slice()
	if err != nil {
		return policy.Decision{}, fmt.Errorf("redisstore: admit: %w", err)
	}
	switch res[0] {
	case resOK:
		return policy.Allow(), nil
	case resSwitched:
		return policy.SwitchedOff(t.Name), nil
	case resRate:
		return policy.RateLimited(kind, max), nil
	}
	return policy.Decision{}, fmt.Errorf("redisstore: admit: unexpected script result %v", res)
}

// Reserve claims the cooldown and daily caps for a call whose arguments are valid.
func (s *Store) Reserve(ctx context.Context, p core.Principal, t core.Tool, amount int64) (policy.Reservation, policy.Decision, error) {
	now := s.now()
	day := now.UTC().Format("2006-01-02")
	pk := "{" + principalKey(p) + "}"
	dailyKey := s.k("day", pk, t.Name, day)
	cdKey := s.k("cd", pk, t.Name)
	cdOn := 0
	if t.Caps.CooldownSeconds > 0 {
		cdOn = 1
	}
	res, err := reserveScript.Run(ctx, s.rdb,
		[]string{dailyKey, cdKey, s.switchesKey()},
		now.UnixMilli(), cdOn, t.Caps.PerDayCalls, t.Caps.PerDayAmount, amount,
		p.TenantID, t.Name, p.PlayerID, int64(dailyTTL.Seconds()),
	).Int64Slice()
	if err != nil {
		return policy.Reservation{}, policy.Decision{}, fmt.Errorf("redisstore: reserve: %w", err)
	}
	switch res[0] {
	case resOK:
		return policy.Reservation{Key: dailyKey, Day: day, Amount: amount, Calls: 1, CDKey: cdKey,
			Cooldown: time.Duration(t.Caps.CooldownSeconds) * time.Second}, policy.Allow(), nil
	case resSwitched:
		return policy.Reservation{}, policy.SwitchedOff(t.Name), nil
	case resCooldown:
		return policy.Reservation{}, policy.Cooldown(t.Name, int((time.Duration(res[1])*time.Millisecond).Seconds())+1), nil
	case resCallCap:
		return policy.Reservation{}, policy.DailyCallCap(t.Name, t.Caps.PerDayCalls), nil
	case resAmountCap:
		return policy.Reservation{}, policy.DailyAmountCap(t.Name, amount, res[1]), nil
	}
	return policy.Reservation{}, policy.Decision{}, fmt.Errorf("redisstore: reserve: unexpected script result %v", res)
}

// Settle keeps the claim when ok is true and releases it when the call failed.
func (s *Store) Settle(ctx context.Context, r policy.Reservation, ok bool) error {
	if r.Key == "" {
		return nil
	}
	if !ok {
		if err := releaseScript.Run(ctx, s.rdb, []string{r.Key}, r.Calls, r.Amount).Err(); err != nil {
			return fmt.Errorf("redisstore: release: %w", err)
		}
		return nil
	}
	if r.Cooldown > 0 { // only a successful call starts the cooldown
		until := s.now().Add(r.Cooldown).UnixMilli()
		if err := s.rdb.Set(ctx, r.CDKey, until, r.Cooldown+time.Second).Err(); err != nil {
			return fmt.Errorf("redisstore: cooldown: %w", err)
		}
	}
	return nil
}

func encode(sw policy.Switch) string { return sw.Tenant + sep + sw.Tool + sep + sw.Player }

func decode(v string) (policy.Switch, bool) {
	f := strings.Split(v, sep)
	if len(f) != 3 {
		return policy.Switch{}, false
	}
	return policy.Switch{Tenant: f[0], Tool: f[1], Player: f[2]}, true
}

// SetSwitch turns a kill switch on or off. It is idempotent.
func (s *Store) SetSwitch(ctx context.Context, sw policy.Switch, on bool) error {
	var err error
	if on {
		err = s.rdb.SAdd(ctx, s.switchesKey(), encode(sw)).Err()
	} else {
		err = s.rdb.SRem(ctx, s.switchesKey(), encode(sw)).Err()
	}
	if err != nil {
		return fmt.Errorf("redisstore: set switch: %w", err)
	}
	return nil
}

// Switches lists the switches that are on, in the order policy.SortSwitches defines.
func (s *Store) Switches(ctx context.Context) ([]policy.Switch, error) {
	members, err := s.rdb.SMembers(ctx, s.switchesKey()).Result()
	if err != nil {
		return nil, fmt.Errorf("redisstore: list switches: %w", err)
	}
	out := make([]policy.Switch, 0, len(members))
	for _, m := range members {
		if sw, ok := decode(m); ok {
			out = append(out, sw)
		}
	}
	policy.SortSwitches(out)
	return out, nil
}
