// Command backend is a tiny fake city REST API used to try the gateway locally.
// It verifies the gateway's signatures with pkg/agentsig, exactly as a real
// Go backend would.
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/Moustafa-Elgammal/agentgw/pkg/agentsig"
)

type city struct {
	mu    sync.Mutex
	Coins int            `json:"coins"`
	Res   map[string]int `json:"resources"`
	Built int            `json:"buildings"`
}

func main() {
	secret := os.Getenv("AGENTGW_SIGNING_SECRET")
	if secret == "" {
		log.Fatal("set AGENTGW_SIGNING_SECRET")
	}
	c := &city{Coins: 500, Res: map[string]int{"food": 60, "water": 60, "materials": 60}}
	prices := map[string]int{"food": 2, "water": 1, "materials": 4}

	verify := func(h func(w http.ResponseWriter, r *http.Request, body []byte)) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
			_, err := agentsig.Verify(r, body, func(id string) []byte {
				if id == "dev-1" {
					return []byte(secret)
				}
				return nil
			}, time.Now(), 0)
			if err != nil {
				http.Error(w, err.Error(), http.StatusUnauthorized)
				return
			}
			h(w, r, body)
		}
	}
	reply := func(w http.ResponseWriter, v any) { _ = json.NewEncoder(w).Encode(v) }

	http.HandleFunc("GET /api/v1/city", verify(func(w http.ResponseWriter, _ *http.Request, _ []byte) {
		c.mu.Lock()
		defer c.mu.Unlock()
		reply(w, c)
	}))
	http.HandleFunc("POST /api/v1/taxes/collect", verify(func(w http.ResponseWriter, _ *http.Request, _ []byte) {
		c.mu.Lock()
		defer c.mu.Unlock()
		c.Coins += 25
		reply(w, map[string]int{"collected": 25, "coins": c.Coins})
	}))
	http.HandleFunc("POST /api/v1/build", verify(func(w http.ResponseWriter, _ *http.Request, _ []byte) {
		c.mu.Lock()
		defer c.mu.Unlock()
		if c.Coins < 60 {
			http.Error(w, fmt.Sprintf("not_enough_coins: need 60, have %d", c.Coins), http.StatusUnprocessableEntity)
			return
		}
		c.Coins -= 60
		c.Built++
		reply(w, map[string]int{"coins": c.Coins, "buildings": c.Built})
	}))
	http.HandleFunc("POST /api/v1/market/sell", verify(func(w http.ResponseWriter, _ *http.Request, body []byte) {
		var in struct {
			Resource string `json:"resource"`
			Quantity int    `json:"quantity"`
		}
		if json.Unmarshal(body, &in) != nil {
			http.Error(w, "bad body", http.StatusBadRequest)
			return
		}
		c.mu.Lock()
		defer c.mu.Unlock()
		if c.Res[in.Resource] < in.Quantity {
			http.Error(w, "not_enough_"+in.Resource, http.StatusUnprocessableEntity)
			return
		}
		c.Res[in.Resource] -= in.Quantity
		earned := prices[in.Resource] * in.Quantity
		c.Coins += earned
		reply(w, map[string]int{"earned": earned, "coins": c.Coins})
	}))

	addr := os.Getenv("BACKEND_ADDR") // in a container: 0.0.0.0:9090, published on loopback only
	if addr == "" {
		addr = "127.0.0.1:9090"
	}
	log.Println("fake city backend listening")
	srv := &http.Server{Addr: addr, ReadHeaderTimeout: 5 * time.Second}
	log.Fatal(srv.ListenAndServe())
}
