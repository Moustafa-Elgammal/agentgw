// Package bustest is the contract every relay.Bus must pass.
package bustest

import (
	"context"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/Moustafa-Elgammal/agentgw/internal/relay"
)

// Bus is a bus under test plus a way to move time forward. Memory advances a fake clock;
// Redis uses real TTLs, so its Advance sleeps.
type Bus struct {
	Bus     relay.Bus
	Advance func(time.Duration)
}

// Factory builds a fresh, isolated bus for one test. now is the clock a fake-clock
// implementation should use; real-time implementations ignore it.
type Factory func(t *testing.T, now func() time.Time) Bus

const ttl = 150 * time.Millisecond

func recv(t *testing.T, ch <-chan []byte) string {
	t.Helper()
	select {
	case m, ok := <-ch:
		if !ok {
			t.Fatal("channel closed")
		}
		return string(m)
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for a message")
	}
	return ""
}

func none(t *testing.T, ch <-chan []byte) {
	t.Helper()
	select {
	case m := <-ch:
		t.Fatalf("unexpected message %q", m)
	case <-time.After(100 * time.Millisecond):
	}
}

// Run executes the suite.
func Run(t *testing.T, newBus func(t *testing.T, now func() time.Time) Bus) {
	t.Helper()
	ctx := context.Background()
	mk := func(t *testing.T) Bus {
		var mu sync.Mutex
		cur := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
		b := newBus(t, func() time.Time { mu.Lock(); defer mu.Unlock(); return cur })
		if b.Advance == nil {
			b.Advance = func(d time.Duration) { mu.Lock(); cur = cur.Add(d); mu.Unlock() }
		}
		return b
	}
	owner := func(t *testing.T, b relay.Bus, key string) (string, bool) {
		t.Helper()
		n, ok, err := b.Owner(ctx, key)
		if err != nil {
			t.Fatal(err)
		}
		return n, ok
	}

	t.Run("presence", func(t *testing.T) {
		tests := []struct {
			name  string
			steps func(t *testing.T, b Bus)
			key   string
			node  string
			ok    bool
		}{
			{"claim then owner", func(t *testing.T, b Bus) {
				_ = b.Bus.Claim(ctx, "k", "n1", time.Minute)
			}, "k", "n1", true},
			{"unknown key", func(t *testing.T, b Bus) {}, "nope", "", false},
			{"different node replaces", func(t *testing.T, b Bus) {
				_ = b.Bus.Claim(ctx, "k", "n1", time.Minute)
				_ = b.Bus.Claim(ctx, "k", "n2", time.Minute)
			}, "k", "n2", true},
			{"release by owner", func(t *testing.T, b Bus) {
				_ = b.Bus.Claim(ctx, "k", "n1", time.Minute)
				_ = b.Bus.Release(ctx, "k", "n1")
			}, "k", "", false},
			{"release by non owner is a no-op", func(t *testing.T, b Bus) {
				_ = b.Bus.Claim(ctx, "k", "n1", time.Minute)
				_ = b.Bus.Release(ctx, "k", "n2")
			}, "k", "n1", true},
			{"release of unknown key", func(t *testing.T, b Bus) {
				_ = b.Bus.Release(ctx, "k", "n1")
			}, "k", "", false},
			{"keys are independent", func(t *testing.T, b Bus) {
				_ = b.Bus.Claim(ctx, "a", "n1", time.Minute)
				_ = b.Bus.Claim(ctx, "b", "n2", time.Minute)
			}, "a", "n1", true},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				b := mk(t)
				tt.steps(t, b)
				n, ok := owner(t, b.Bus, tt.key)
				if n != tt.node || ok != tt.ok {
					t.Fatalf("Owner = %q,%v want %q,%v", n, ok, tt.node, tt.ok)
				}
			})
		}
	})

	t.Run("claim expires", func(t *testing.T) {
		b := mk(t)
		if err := b.Bus.Claim(ctx, "k", "n1", ttl); err != nil {
			t.Fatal(err)
		}
		if _, ok := owner(t, b.Bus, "k"); !ok {
			t.Fatal("claim missing right after Claim")
		}
		b.Advance(ttl + 100*time.Millisecond)
		if n, ok := owner(t, b.Bus, "k"); ok {
			t.Fatalf("claim still held by %q after ttl", n)
		}
	})

	t.Run("refresh extends the claim", func(t *testing.T) {
		b := mk(t)
		_ = b.Bus.Claim(ctx, "k", "n1", 3*ttl)
		b.Advance(2 * ttl)
		_ = b.Bus.Claim(ctx, "k", "n1", 3*ttl)
		b.Advance(2 * ttl)
		if n, ok := owner(t, b.Bus, "k"); !ok || n != "n1" {
			t.Fatalf("refreshed claim lost: %q %v", n, ok)
		}
	})

	t.Run("publish reaches only that node", func(t *testing.T) {
		b := mk(t)
		cctx, cancel := context.WithCancel(ctx)
		defer cancel()
		c1, err := b.Bus.Subscribe(cctx, "n1")
		if err != nil {
			t.Fatal(err)
		}
		c2, err := b.Bus.Subscribe(cctx, "n2")
		if err != nil {
			t.Fatal(err)
		}
		if err := b.Bus.Publish(ctx, "n1", []byte("hello")); err != nil {
			t.Fatal(err)
		}
		if got := recv(t, c1); got != "hello" {
			t.Fatalf("n1 got %q", got)
		}
		none(t, c2)
	})

	t.Run("publish without subscriber is not an error", func(t *testing.T) {
		b := mk(t)
		if err := b.Bus.Publish(ctx, "ghost", []byte("x")); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("two subscribers both receive", func(t *testing.T) {
		b := mk(t)
		cctx, cancel := context.WithCancel(ctx)
		defer cancel()
		c1, _ := b.Bus.Subscribe(cctx, "n1")
		c2, _ := b.Bus.Subscribe(cctx, "n1")
		_ = b.Bus.Publish(ctx, "n1", []byte("m"))
		if recv(t, c1) != "m" || recv(t, c2) != "m" {
			t.Fatal("a subscriber missed the message")
		}
	})

	t.Run("messages keep order", func(t *testing.T) {
		b := mk(t)
		cctx, cancel := context.WithCancel(ctx)
		defer cancel()
		c, _ := b.Bus.Subscribe(cctx, "n1")
		for i := 0; i < 20; i++ {
			_ = b.Bus.Publish(ctx, "n1", []byte(strconv.Itoa(i)))
		}
		for i := 0; i < 20; i++ {
			if got := recv(t, c); got != strconv.Itoa(i) {
				t.Fatalf("message %d = %q", i, got)
			}
		}
	})

	t.Run("channel closes when ctx ends", func(t *testing.T) {
		b := mk(t)
		cctx, cancel := context.WithCancel(ctx)
		c, _ := b.Bus.Subscribe(cctx, "n1")
		cancel()
		deadline := time.After(2 * time.Second)
		for {
			select {
			case _, ok := <-c:
				if !ok {
					return
				}
			case <-deadline:
				t.Fatal("channel not closed after ctx cancel")
			}
		}
	})

	t.Run("messages are copied", func(t *testing.T) {
		b := mk(t)
		cctx, cancel := context.WithCancel(ctx)
		defer cancel()
		c, _ := b.Bus.Subscribe(cctx, "n1")
		msg := []byte("original")
		_ = b.Bus.Publish(ctx, "n1", msg)
		copy(msg, "XXXXXXXX")
		if got := recv(t, c); got != "original" {
			t.Fatalf("got %q", got)
		}
	})

	t.Run("concurrent use", func(t *testing.T) {
		b := mk(t)
		cctx, cancel := context.WithCancel(ctx)
		defer cancel()
		c, _ := b.Bus.Subscribe(cctx, "n1")
		go func() {
			for range c {
			}
		}()
		var wg sync.WaitGroup
		for i := 0; i < 16; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				node := "n" + strconv.Itoa(i%4)
				for j := 0; j < 20; j++ {
					_ = b.Bus.Claim(ctx, "k", node, time.Minute)
					_, _, _ = b.Bus.Owner(ctx, "k")
					_ = b.Bus.Publish(ctx, "n1", []byte("x"))
					_ = b.Bus.Release(ctx, "k", node)
				}
			}(i)
		}
		wg.Wait()
	})
}
