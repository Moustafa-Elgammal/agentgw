package confirm_test

import (
	"testing"
	"time"

	"github.com/Moustafa-Elgammal/agentgw/internal/confirm"
	"github.com/Moustafa-Elgammal/agentgw/internal/confirm/confirmtest"
)

func TestMemoryContract(t *testing.T) {
	confirmtest.Run(t, func(_ *testing.T, now func() time.Time) confirm.Store {
		return confirm.NewMemory(now)
	})
}
