package agentsig_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"time"

	"github.com/Moustafa-Elgammal/agentgw/pkg/agentsig"
)

// A backend verifies the signed request the gateway sent.
func ExampleVerify() {
	secret := []byte("shared-secret")
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

	// The gateway side signs.
	req := httptest.NewRequest(http.MethodPost, "/api/v1/market/sell", nil)
	agentsig.Sign(req, "k1", secret, "player-7", "idem-1", nil, now)

	// The backend side verifies. lookup returns the secret for a key id, or nil if unknown.
	lookup := func(id string) []byte {
		if id == "k1" {
			return secret
		}
		return nil
	}
	player, err := agentsig.Verify(req, nil, lookup, now, 0)
	fmt.Println(player, err)
	// Output: player-7 <nil>
}
