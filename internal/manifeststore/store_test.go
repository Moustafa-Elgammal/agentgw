package manifeststore_test

import (
	"testing"
	"time"

	"github.com/Moustafa-Elgammal/agentgw/internal/manifeststore"
	"github.com/Moustafa-Elgammal/agentgw/internal/manifeststore/storetest"
)

func TestMemoryContract(t *testing.T) {
	storetest.Run(t, func(_ *testing.T, now func() time.Time) manifeststore.Store {
		return manifeststore.NewMemory(now)
	})
}
