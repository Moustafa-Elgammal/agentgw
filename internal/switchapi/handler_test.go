package switchapi_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/Moustafa-Elgammal/agentgw/internal/audit"
	"github.com/Moustafa-Elgammal/agentgw/internal/connector"
	"github.com/Moustafa-Elgammal/agentgw/internal/core"
	"github.com/Moustafa-Elgammal/agentgw/internal/gateway"
	"github.com/Moustafa-Elgammal/agentgw/internal/manifest"
	"github.com/Moustafa-Elgammal/agentgw/internal/policy"
	"github.com/Moustafa-Elgammal/agentgw/internal/policy/redisstore"
	"github.com/Moustafa-Elgammal/agentgw/internal/studiosig"
	"github.com/Moustafa-Elgammal/agentgw/internal/switchapi"
	"github.com/Moustafa-Elgammal/agentgw/pkg/agentsig"
)

var adminSecret = []byte("admin-secret")

type rig struct {
	h   http.Handler
	n   int
	aud *audit.Memory
}

func newRig(sb policy.Switchboard) *rig {
	aud := &audit.Memory{}
	v := studiosig.New(studiosig.Config{KeyID: "admin-1", Secret: adminSecret, MaxBody: switchapi.MaxBody})
	return &rig{h: switchapi.Handler(sb, v, "t", aud), aud: aud}
}

func (r *rig) do(method, body, keyID, secret, idem string) *httptest.ResponseRecorder {
	r.n++
	req := httptest.NewRequest(method, switchapi.Path, strings.NewReader(body))
	if keyID != "" {
		if idem == "" {
			idem = "k" + strconv.Itoa(r.n)
		}
		agentsig.Sign(req, keyID, []byte(secret), "ops-alice", idem, []byte(body), time.Now())
	}
	w := httptest.NewRecorder()
	r.h.ServeHTTP(w, req)
	return w
}

func (r *rig) ok(method, body string) *httptest.ResponseRecorder {
	return r.do(method, body, "admin-1", string(adminSecret), "")
}

func list(t *testing.T, r *rig) []policy.Switch {
	t.Helper()
	w := r.ok("GET", "")
	if w.Code != 200 {
		t.Fatalf("list = %d %s", w.Code, w.Body)
	}
	var out struct {
		Switches []policy.Switch `json:"switches"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	return out.Switches
}

func TestSwitchOnOffAndList(t *testing.T) {
	r := newRig(policy.NewMemory(policy.DefaultLimits, nil))
	if got := list(t, r); len(got) != 0 {
		t.Fatalf("fresh list = %v", got)
	}
	if w := r.ok("GET", ""); !strings.Contains(w.Body.String(), `"switches":[]`) {
		t.Fatalf("empty list must be [], got %s", w.Body)
	}
	steps := []struct {
		method, body string
		want         []policy.Switch
	}{
		{"PUT", `{"tool":"build_structure","reason":"exploit under investigation"}`, []policy.Switch{{Tenant: "t", Tool: "build_structure"}}},
		{"PUT", `{"tool":"build_structure"}`, []policy.Switch{{Tenant: "t", Tool: "build_structure"}}},                     // idempotent on
		{"PUT", `{"player":"p-9"}`, []policy.Switch{{Tenant: "t", Player: "p-9"}, {Tenant: "t", Tool: "build_structure"}}}, // listed by tenant, tool, player
		{"PUT", ``, []policy.Switch{{Tenant: "t"}, {Tenant: "t", Player: "p-9"}, {Tenant: "t", Tool: "build_structure"}}},  // whole game
		{"DELETE", ``, []policy.Switch{{Tenant: "t", Player: "p-9"}, {Tenant: "t", Tool: "build_structure"}}},
		{"DELETE", `{"tool":"build_structure"}`, []policy.Switch{{Tenant: "t", Player: "p-9"}}},
		{"DELETE", `{"tool":"build_structure"}`, []policy.Switch{{Tenant: "t", Player: "p-9"}}}, // idempotent off
		{"DELETE", `{"player":"p-9"}`, nil},
	}
	for i, s := range steps {
		if w := r.ok(s.method, s.body); w.Code != 200 {
			t.Fatalf("step %d %s %s = %d %s", i, s.method, s.body, w.Code, w.Body)
		}
		got := list(t, r)
		if len(got) != len(s.want) {
			t.Fatalf("step %d: list = %v, want %v", i, got, s.want)
		}
		for j := range got {
			if got[j] != s.want[j] {
				t.Fatalf("step %d: list = %v, want %v", i, got, s.want)
			}
		}
	}
}

func TestEveryChangeIsAudited(t *testing.T) {
	r := newRig(policy.NewMemory(policy.DefaultLimits, nil))
	r.ok("PUT", `{"tool":"build_structure","reason":"secret free text"}`)
	r.ok("DELETE", `{"tool":"build_structure"}`)
	r.ok("GET", "") // reads are not audited
	ev := r.aud.Events()
	if len(ev) != 2 {
		t.Fatalf("events = %+v", ev)
	}
	for i, want := range []string{switchapi.AuditToolOn, switchapi.AuditToolOff} {
		e := ev[i]
		if e.Tool != want || e.Tier != switchapi.AuditTierAdmin || e.Player != "ops-alice" || e.Tenant != "t" || e.Outcome != "ok" || len(e.ArgsSHA) != 64 {
			t.Errorf("event %d = %+v", i, e)
		}
		if b, _ := json.Marshal(e); strings.Contains(string(b), "secret free text") {
			t.Errorf("the reason must not reach the audit event: %s", b)
		}
	}
	if ev[0].ArgsSHA != ev[1].ArgsSHA {
		t.Error("on and off for the same target must hash the same target")
	}
}

func TestAuthValidationAndReplay(t *testing.T) {
	long := strings.Repeat("x", 201)
	tests := []struct {
		name   string
		method string
		body   string
		key    string
		secret string
		want   int
	}{
		{"unsigned", "PUT", `{}`, "", "", 401},
		{"unknown key", "PUT", `{}`, "other", string(adminSecret), 401},
		{"wrong secret", "PUT", `{}`, "admin-1", "nope", 401},
		{"bad json", "PUT", `{`, "admin-1", string(adminSecret), 400},
		{"unknown field", "PUT", `{"tenant":"other"}`, "admin-1", string(adminSecret), 400},
		{"trailing data", "PUT", `{} {}`, "admin-1", string(adminSecret), 400},
		{"tool with slash", "PUT", `{"tool":"a/b"}`, "admin-1", string(adminSecret), 400},
		{"tool uppercase", "PUT", `{"tool":"Build"}`, "admin-1", string(adminSecret), 400},
		{"player with crlf", "PUT", "{\"player\":\"p\\r\\nX: y\"}", "admin-1", string(adminSecret), 400},
		{"player with space", "PUT", `{"player":"a b"}`, "admin-1", string(adminSecret), 400},
		{"player too long", "PUT", `{"player":"` + strings.Repeat("p", 129) + `"}`, "admin-1", string(adminSecret), 400},
		{"reason too long", "PUT", `{"reason":"` + long + `"}`, "admin-1", string(adminSecret), 400},
		{"reason with newline", "PUT", `{"reason":"a\nb"}`, "admin-1", string(adminSecret), 400},
		{"body too large", "PUT", `{"reason":"` + strings.Repeat("a", switchapi.MaxBody) + `"}`, "admin-1", string(adminSecret), 413},
		{"ok", "PUT", `{"tool":"build_structure"}`, "admin-1", string(adminSecret), 200},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newRig(policy.NewMemory(policy.DefaultLimits, nil))
			w := r.do(tt.method, tt.body, tt.key, tt.secret, "")
			if w.Code != tt.want {
				t.Fatalf("status = %d, want %d: %s", w.Code, tt.want, w.Body)
			}
			if tt.want != 200 && len(r.aud.Events()) != 0 {
				t.Errorf("a refused request must leave no audit event")
			}
		})
	}

	t.Run("replay", func(t *testing.T) {
		r := newRig(policy.NewMemory(policy.DefaultLimits, nil))
		if w := r.do("PUT", `{"tool":"build_structure"}`, "admin-1", string(adminSecret), "same"); w.Code != 200 {
			t.Fatalf("first = %d", w.Code)
		}
		if w := r.do("PUT", `{"tool":"build_structure"}`, "admin-1", string(adminSecret), "same"); w.Code != 401 {
			t.Fatalf("replay = %d, want 401", w.Code)
		}
		if n := len(r.aud.Events()); n != 1 {
			t.Fatalf("events = %d, want 1", n)
		}
	})
}

// failing is a Switchboard whose store is down.
type failing struct{ policy.Switchboard }

var errDown = errors.New("store down")

func (failing) SetSwitch(context.Context, policy.Switch, bool) error { return errDown }
func (failing) Switches(context.Context) ([]policy.Switch, error)    { return nil, errDown }

func TestStoreFailureFailsClosed(t *testing.T) {
	r := newRig(failing{})
	for _, m := range []string{"GET", "PUT", "DELETE"} {
		w := r.ok(m, "")
		if w.Code != 503 || !strings.Contains(w.Body.String(), "temporarily_unavailable") {
			t.Errorf("%s = %d %s", m, w.Code, w.Body)
		}
	}
	if n := len(r.aud.Events()); n != 0 {
		t.Errorf("a failed change must not be audited as done, got %d events", n)
	}
}

const doc = `{"version":1,"tools":[
 {"name":"get_state","description":"State.","tier":"read","http":{"method":"GET","url":"/state"}},
 {"name":"build_structure","description":"Build.","tier":"medium","http":{"method":"POST","url":"/build"}}]}`

// A switch set through the API stops calls in the real pipeline, and clearing it restores them.
func TestSwitchBlocksThePipeline(t *testing.T) {
	m, err := manifest.Parse(strings.NewReader(doc))
	if err != nil {
		t.Fatal(err)
	}
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`{}`)) }))
	defer backend.Close()

	stores := map[string]func(t *testing.T) policy.Policy{
		"memory": func(*testing.T) policy.Policy { return policy.NewMemory(policy.DefaultLimits, nil) },
	}
	if addr := os.Getenv("AGENTGW_TEST_REDIS_ADDR"); addr != "" {
		stores["redis"] = func(t *testing.T) policy.Policy {
			c := redis.NewClient(&redis.Options{Addr: addr})
			t.Cleanup(func() { _ = c.Close() })
			return redisstore.New(c, "test:switchapi:"+strconv.FormatInt(time.Now().UnixNano(), 36)+":", policy.DefaultLimits, nil)
		}
	}
	for name, mk := range stores {
		t.Run(name, func(t *testing.T) {
			pol := mk(t)
			svc := gateway.New(m, pol, connector.NewHTTP(backend.URL, "k1", []byte("s"), connector.AllowPrivate()), &audit.Memory{})
			r := newRig(pol.(policy.Switchboard))
			call := func(tool, player string) core.Result {
				return svc.Call(context.Background(), core.Principal{TenantID: "t", PlayerID: player}, tool, json.RawMessage(`{}`))
			}
			if res := call("build_structure", "p1"); !res.OK {
				t.Fatalf("before the switch: %+v", res)
			}
			r.ok("PUT", `{"tool":"build_structure"}`)
			if res := call("build_structure", "p1"); res.OK || res.Code != "switched_off" {
				t.Fatalf("tool switch: %+v", res)
			}
			if res := call("get_state", "p1"); !res.OK {
				t.Fatalf("other tools must keep working: %+v", res)
			}
			r.ok("DELETE", `{"tool":"build_structure"}`)
			if res := call("build_structure", "p1"); !res.OK {
				t.Fatalf("after clearing: %+v", res)
			}
			r.ok("PUT", `{"player":"p1"}`)
			if res := call("get_state", "p1"); res.OK || res.Code != "switched_off" {
				t.Fatalf("player switch: %+v", res)
			}
			if res := call("get_state", "p2"); !res.OK {
				t.Fatalf("other players must keep working: %+v", res)
			}
			r.ok("DELETE", `{"player":"p1"}`)
			r.ok("PUT", ``)
			if res := call("get_state", "p2"); res.OK || res.Code != "switched_off" {
				t.Fatalf("whole game: %+v", res)
			}
			r.ok("DELETE", ``)
			if res := call("get_state", "p2"); !res.OK {
				t.Fatalf("after clearing the game switch: %+v", res)
			}
		})
	}
}
