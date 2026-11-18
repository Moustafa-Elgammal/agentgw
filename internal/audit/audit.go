// Package audit records every call an agent makes, including denied ones.
// Arguments are stored as a hash by default so the log does not hold game data.
package audit

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"log/slog"
	"sync"
	"time"
)

// Event is one audit record.
type Event struct {
	Time    time.Time `json:"time"`
	Tenant  string    `json:"tenant"`
	Player  string    `json:"player"`
	Tool    string    `json:"tool"`
	Tier    string    `json:"tier"`
	Outcome string    `json:"outcome"` // ok | denied | error
	Code    string    `json:"code,omitempty"`
	ArgsSHA string    `json:"args_sha256"`
}

// HashArgs returns the hex SHA-256 of the raw arguments.
func HashArgs(raw []byte) string {
	s := sha256.Sum256(raw)
	return hex.EncodeToString(s[:])
}

// Auditor receives events. Implementations must not block the call path for long.
type Auditor interface {
	Record(ctx context.Context, e Event)
}

// Memory keeps events in a slice. For tests and the dev server.
type Memory struct {
	mu     sync.Mutex
	events []Event
}

// Record implements Auditor.
func (m *Memory) Record(_ context.Context, e Event) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.events = append(m.events, e)
}

// Events returns a copy of everything recorded so far.
func (m *Memory) Events() []Event {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]Event(nil), m.events...)
}

// Slog writes events as structured log lines.
type Slog struct{ L *slog.Logger }

// Record implements Auditor.
func (s Slog) Record(ctx context.Context, e Event) {
	s.L.LogAttrs(ctx, slog.LevelInfo, "agent_call",
		slog.String("tenant", e.Tenant), slog.String("player", e.Player),
		slog.String("tool", e.Tool), slog.String("tier", e.Tier),
		slog.String("outcome", e.Outcome), slog.String("code", e.Code),
		slog.String("args_sha256", e.ArgsSHA))
}

// Multi fans out to several auditors.
type Multi []Auditor

// Record implements Auditor.
func (m Multi) Record(ctx context.Context, e Event) {
	for _, a := range m {
		a.Record(ctx, e)
	}
}
