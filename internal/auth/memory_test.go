package auth_test

import (
	"testing"
	"time"

	"github.com/Moustafa-Elgammal/agentgw/internal/auth"
	"github.com/Moustafa-Elgammal/agentgw/internal/auth/authtest"
)

func TestMemoryContract(t *testing.T) {
	authtest.Run(t, func(_ *testing.T, now func() time.Time) auth.Store { return auth.NewMemory(now) })
}

func TestTokenHelpers(t *testing.T) {
	a, ah := auth.NewToken()
	b, _ := auth.NewToken()
	if a == b || len(a) < 40 {
		t.Fatalf("tokens must be long and unique: %q %q", a, b)
	}
	if auth.HashToken(a) != ah || ah == a || len(ah) != 64 {
		t.Fatal("the stored form is a 64 char hash of the token")
	}
}
