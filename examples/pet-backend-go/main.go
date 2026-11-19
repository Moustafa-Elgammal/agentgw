// Command pet-backend-go is a toy pet shop REST API that accepts the gateway's signed calls.
// It is the "level 1" example: the backend verifies the signature (examples/signing/go) and
// honors the idempotency key, so a retried call never feeds a pet twice.
package main

import (
	"encoding/json"
	"log"
	"net/http"
	"os"
	"sync"
	"time"

	sigverify "github.com/Moustafa-Elgammal/agentgw/examples/signing/go"
)

type pet struct {
	Name    string `json:"name"`
	Species string `json:"species"`
	Hunger  int    `json:"hunger"` // 0 full, 100 starving
}

type shop struct {
	mu    sync.Mutex
	coins int
	pets  map[string]*pet
	order []string
}

func main() {
	secret := os.Getenv("AGENTGW_SIGNING_SECRET")
	if secret == "" {
		log.Fatal("set AGENTGW_SIGNING_SECRET")
	}
	keyID := os.Getenv("KEY_ID")
	if keyID == "" {
		keyID = "dev-1" // the gateway's --key-id default
	}
	s := &shop{coins: 100, pets: map[string]*pet{"rex": {Name: "rex", Species: "dog", Hunger: 80}}, order: []string{"rex"}}

	auth := &sigverify.Middleware{SecretFor: func(id string) []byte {
		if id == keyID {
			return []byte(secret)
		}
		return nil
	}}

	mux := http.NewServeMux()
	mux.Handle("GET /api/v1/shop", auth.Wrap(s.shop))
	mux.Handle("GET /api/v1/pets/{name}", auth.Wrap(s.pet))
	mux.Handle("POST /api/v1/pets/feed", auth.Wrap(s.feed))
	mux.Handle("POST /api/v1/pets/adopt", auth.Wrap(s.adopt))

	addr := os.Getenv("BACKEND_ADDR")
	if addr == "" {
		addr = "127.0.0.1:9090"
	}
	log.Print("pet shop listening")
	srv := &http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	log.Fatal(srv.ListenAndServe())
}

func reply(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// refuse answers a 4xx. The gateway shows the text to the agent, so keep it short: a code
// and a hint it can act on.
func refuse(w http.ResponseWriter, status int, text string) { http.Error(w, text, status) }

func (s *shop) shop(w http.ResponseWriter, _ *http.Request, _ string, _ []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	reply(w, 200, map[string]any{"coins": s.coins, "pets": s.order})
}

func (s *shop) pet(w http.ResponseWriter, r *http.Request, _ string, _ []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.pets[r.PathValue("name")]
	if !ok {
		refuse(w, 404, "no_such_pet: you do not own that pet")
		return
	}
	reply(w, 200, p)
}

func (s *shop) feed(w http.ResponseWriter, _ *http.Request, _ string, body []byte) {
	var in struct {
		Pet    string `json:"pet"`
		Food   string `json:"food"`
		Amount int    `json:"amount"`
	}
	if json.Unmarshal(body, &in) != nil || in.Amount < 1 {
		refuse(w, 400, "bad_request: send pet, food and amount")
		return
	}
	price := map[string]int{"kibble": 1, "fish": 2}[in.Food]
	if price == 0 {
		refuse(w, 400, "bad_request: food must be kibble or fish")
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.pets[in.Pet]
	if !ok {
		refuse(w, 404, "no_such_pet: you do not own that pet")
		return
	}
	cost := price * in.Amount
	if s.coins < cost {
		refuse(w, 422, "not_enough_coins: need "+itoa(cost)+", have "+itoa(s.coins))
		return
	}
	s.coins -= cost
	p.Hunger = max(0, p.Hunger-10*in.Amount)
	reply(w, 200, map[string]any{"pet": p.Name, "hunger": p.Hunger, "coins": s.coins})
}

func (s *shop) adopt(w http.ResponseWriter, _ *http.Request, _ string, body []byte) {
	var in struct {
		Name    string `json:"name"`
		Species string `json:"species"`
	}
	if json.Unmarshal(body, &in) != nil || in.Name == "" || in.Species == "" {
		refuse(w, 400, "bad_request: send name and species")
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, taken := s.pets[in.Name]; taken {
		refuse(w, 409, "name_taken: pick another name")
		return
	}
	if s.coins < 50 {
		refuse(w, 422, "not_enough_coins: need 50, have "+itoa(s.coins))
		return
	}
	s.coins -= 50
	s.pets[in.Name] = &pet{Name: in.Name, Species: in.Species, Hunger: 50}
	s.order = append(s.order, in.Name)
	reply(w, 201, map[string]any{"adopted": in.Name, "coins": s.coins})
}

func itoa(n int) string {
	b, _ := json.Marshal(n)
	return string(b)
}
