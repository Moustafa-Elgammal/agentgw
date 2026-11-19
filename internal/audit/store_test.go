package audit_test

import (
	"context"
	"testing"
	"time"

	"github.com/Moustafa-Elgammal/agentgw/internal/audit"
	"github.com/Moustafa-Elgammal/agentgw/internal/audit/auditest"
)

func TestRingContract(t *testing.T) {
	auditest.Run(t, func(t *testing.T) audit.Store { return &audit.Ring{Cap: 1000} })
}

func TestRingEvictsOldest(t *testing.T) {
	r := &audit.Ring{Cap: 3}
	ctx := context.Background()
	for i := 0; i < 5; i++ {
		r.Record(ctx, audit.Event{Tenant: "t", Tool: "x", Time: time.Unix(int64(i), 0)})
	}
	got, err := r.Query(ctx, audit.Query{Tenant: "t"})
	if err != nil || len(got) != 3 {
		t.Fatalf("got %d (%v), want 3", len(got), err)
	}
	if got[0].ID != 5 || got[2].ID != 3 {
		t.Errorf("kept ids %d..%d, want 5..3", got[0].ID, got[2].ID)
	}
}
