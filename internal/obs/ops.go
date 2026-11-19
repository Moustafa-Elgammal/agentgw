package obs

import (
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"sync"
	"time"
)

// Check reports whether one dependency is ready. A nil error means ready.
type Check struct {
	Name string
	Fn   func(ctx context.Context) error
}

// CheckTimeout bounds each readiness check.
const CheckTimeout = 2 * time.Second

// Ops builds the handler for the operations listener: GET /healthz, GET /readyz and GET
// /metrics. Serve it on its own address (loopback by default), never on the public one.
// /healthz is 200 whenever the process is serving. /readyz is 200 only when every check
// passes, otherwise 503 with {"status":"unready","failing":[{"check":...,"error":...}]}.
// reg may be nil to leave /metrics off.
func Ops(reg *Registry, checks ...Check) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		type failure struct {
			Check string `json:"check"`
			Error string `json:"error"`
		}
		var (
			mu      sync.Mutex
			failing = []failure{}
			wg      sync.WaitGroup
		)
		for _, c := range checks {
			wg.Add(1)
			go func() {
				defer wg.Done()
				ctx, cancel := context.WithTimeout(r.Context(), CheckTimeout)
				defer cancel()
				done := make(chan error, 1) // buffered: a check that ignores ctx must not leak on send
				go func() { done <- c.Fn(ctx) }()
				var err error
				select {
				case err = <-done:
				case <-ctx.Done():
					err = ctx.Err()
				}
				if err != nil {
					mu.Lock()
					failing = append(failing, failure{c.Name, err.Error()})
					mu.Unlock()
				}
			}()
		}
		wg.Wait()
		if len(failing) > 0 {
			sort.Slice(failing, func(i, j int) bool { return failing[i].Check < failing[j].Check })
			writeJSON(w, http.StatusServiceUnavailable, map[string]any{"status": "unready", "failing": failing})
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
	})
	if reg != nil {
		mux.Handle("GET /metrics", reg.Handler())
	}
	return mux
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}
