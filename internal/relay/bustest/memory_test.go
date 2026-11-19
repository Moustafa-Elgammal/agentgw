package bustest_test

import (
	"testing"
	"time"

	"github.com/Moustafa-Elgammal/agentgw/internal/relay"
	"github.com/Moustafa-Elgammal/agentgw/internal/relay/bustest"
)

func TestMemoryBus(t *testing.T) {
	bustest.Run(t, func(t *testing.T, now func() time.Time) bustest.Bus {
		return bustest.Bus{Bus: relay.NewMemory(now)}
	})
}
