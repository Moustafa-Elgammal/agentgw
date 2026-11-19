// Package relay lets a game whose logic runs in the client (for example a Unity game)
// take tool calls from the gateway. The game opens an outbound WebSocket to the gateway;
// the gateway sends a call down that socket and waits for the result. With several gateway
// replicas, a Bus finds the replica that holds the socket and carries the call and the
// result between replicas.
package relay

import (
	"context"
	"errors"
	"sync"
	"time"
)

// Bus connects gateway replicas. Implementations: Memory (tests, one process) and
// redisbus (Redis keys and pub/sub). Every implementation must pass bustest.Run.
type Bus interface {
	// Claim records that node holds the socket named key, for ttl. The same node may call it
	// again to refresh. A different node's claim replaces it (a player who reconnects through
	// another replica moves the socket).
	Claim(ctx context.Context, key, node string, ttl time.Duration) error
	// Release removes the claim only if node still owns it.
	Release(ctx context.Context, key, node string) error
	// Owner returns the node that holds key. ok is false when nobody does or the claim expired.
	Owner(ctx context.Context, key string) (node string, ok bool, err error)
	// Publish delivers msg to the inbox of node. Delivery is at most once: a node that is not
	// subscribed drops it.
	Publish(ctx context.Context, node string, msg []byte) error
	// Subscribe returns node's inbox. The channel closes when ctx ends.
	Subscribe(ctx context.Context, node string) (<-chan []byte, error)
}

// ErrNoBus is returned when a bus operation is attempted after the bus was closed.
var ErrNoBus = errors.New("relay: bus closed")

// Memory is an in-process Bus. Several Hubs sharing one Memory behave like several
// replicas sharing Redis.
type Memory struct {
	now func() time.Time

	mu     sync.Mutex
	claims map[string]memClaim
	inbox  map[string][]chan []byte
}

type memClaim struct {
	node    string
	expires time.Time
}

// NewMemory builds a Memory bus. A nil clock uses time.Now.
func NewMemory(now func() time.Time) *Memory {
	if now == nil {
		now = time.Now
	}
	return &Memory{now: now, claims: map[string]memClaim{}, inbox: map[string][]chan []byte{}}
}

// Claim implements Bus.
func (m *Memory) Claim(_ context.Context, key, node string, ttl time.Duration) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.claims[key] = memClaim{node: node, expires: m.now().Add(ttl)}
	return nil
}

// Release implements Bus.
func (m *Memory) Release(_ context.Context, key, node string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if c, ok := m.claims[key]; ok && c.node == node {
		delete(m.claims, key)
	}
	return nil
}

// Owner implements Bus.
func (m *Memory) Owner(_ context.Context, key string) (string, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	c, ok := m.claims[key]
	if !ok || !m.now().Before(c.expires) {
		delete(m.claims, key)
		return "", false, nil
	}
	return c.node, true, nil
}

// Publish implements Bus.
func (m *Memory) Publish(_ context.Context, node string, msg []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock() // sends never block, and the lock keeps them from racing a close
	for _, ch := range m.inbox[node] {
		select {
		case ch <- append([]byte(nil), msg...):
		default: // slow subscriber: drop, like an overloaded broker
		}
	}
	return nil
}

// Subscribe implements Bus.
func (m *Memory) Subscribe(ctx context.Context, node string) (<-chan []byte, error) {
	ch := make(chan []byte, 256)
	m.mu.Lock()
	m.inbox[node] = append(m.inbox[node], ch)
	m.mu.Unlock()
	go func() {
		<-ctx.Done()
		m.mu.Lock()
		subs := m.inbox[node]
		for i, c := range subs {
			if c == ch {
				m.inbox[node] = append(subs[:i:i], subs[i+1:]...)
				break
			}
		}
		m.mu.Unlock()
		close(ch)
	}()
	return ch, nil
}
