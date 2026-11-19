// Package redisbus is the relay.Bus for several gateway replicas: presence is a Redis key
// with a TTL, and the replica inboxes are Redis pub/sub channels. Pub/sub is at most once,
// which matches the Bus contract: a call whose message is lost times out and fails closed.
package redisbus

import (
	"context"
	"time"

	"github.com/redis/go-redis/v9"
)

// Bus implements relay.Bus on Redis.
type Bus struct {
	c      redis.UniversalClient
	prefix string
}

// New builds a Bus. Every key and channel starts with prefix.
func New(c redis.UniversalClient, prefix string) *Bus { return &Bus{c: c, prefix: prefix} }

var release = redis.NewScript(`
if redis.call("GET", KEYS[1]) == ARGV[1] then return redis.call("DEL", KEYS[1]) end
return 0`)

func (b *Bus) key(k string) string   { return b.prefix + "presence:" + k }
func (b *Bus) inbox(n string) string { return b.prefix + "node:" + n }

// Claim implements relay.Bus.
func (b *Bus) Claim(ctx context.Context, key, node string, ttl time.Duration) error {
	return b.c.Set(ctx, b.key(key), node, ttl).Err()
}

// Release implements relay.Bus.
func (b *Bus) Release(ctx context.Context, key, node string) error {
	return release.Run(ctx, b.c, []string{b.key(key)}, node).Err()
}

// Owner implements relay.Bus.
func (b *Bus) Owner(ctx context.Context, key string) (string, bool, error) {
	v, err := b.c.Get(ctx, b.key(key)).Result()
	if err == redis.Nil {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return v, true, nil
}

// Publish implements relay.Bus.
func (b *Bus) Publish(ctx context.Context, node string, msg []byte) error {
	return b.c.Publish(ctx, b.inbox(node), msg).Err()
}

// Subscribe implements relay.Bus. It returns after Redis confirmed the subscription.
func (b *Bus) Subscribe(ctx context.Context, node string) (<-chan []byte, error) {
	ps := b.c.Subscribe(ctx, b.inbox(node))
	if _, err := ps.Receive(ctx); err != nil {
		_ = ps.Close()
		return nil, err
	}
	out := make(chan []byte, 256)
	in := ps.Channel()
	go func() {
		defer close(out)
		defer func() { _ = ps.Close() }()
		for {
			select {
			case <-ctx.Done():
				return
			case m, ok := <-in:
				if !ok {
					return
				}
				select {
				case out <- []byte(m.Payload):
				default: // slow consumer: drop
				}
			}
		}
	}()
	return out, nil
}
