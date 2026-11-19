package redisbus

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"os"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/Moustafa-Elgammal/agentgw/internal/relay/bustest"
)

func TestRedisBus(t *testing.T) {
	addr := os.Getenv("AGENTGW_TEST_REDIS_ADDR")
	if addr == "" {
		t.Skip("AGENTGW_TEST_REDIS_ADDR not set; run through docker compose run --rm dev")
	}
	c := redis.NewClient(&redis.Options{Addr: addr})
	t.Cleanup(func() { _ = c.Close() })
	if err := c.Ping(context.Background()).Err(); err != nil {
		t.Fatalf("redis not reachable at %s: %v", addr, err)
	}
	bustest.Run(t, func(t *testing.T, _ func() time.Time) bustest.Bus {
		var n [6]byte
		_, _ = rand.Read(n[:])
		prefix := "test:" + hex.EncodeToString(n[:]) + ":"
		t.Cleanup(func() {
			ctx := context.Background()
			iter := c.Scan(ctx, 0, prefix+"*", 200).Iterator()
			for iter.Next(ctx) {
				_ = c.Del(ctx, iter.Val()).Err()
			}
		})
		return bustest.Bus{Bus: New(c, prefix), Advance: time.Sleep}
	})
}
