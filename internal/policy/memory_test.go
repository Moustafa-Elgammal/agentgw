package policy_test

import (
	"testing"
	"time"

	"github.com/Moustafa-Elgammal/agentgw/internal/policy"
	"github.com/Moustafa-Elgammal/agentgw/internal/policy/policytest"
)

func TestMemoryContract(t *testing.T) {
	policytest.Run(t, func(_ *testing.T, l policy.Limits, now func() time.Time) policytest.Store {
		return policy.NewMemory(l, now)
	})
}
