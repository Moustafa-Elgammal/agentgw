package relay

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/Moustafa-Elgammal/agentgw/internal/core"
	"github.com/Moustafa-Elgammal/agentgw/internal/studiosig"
	"github.com/Moustafa-Elgammal/agentgw/pkg/agentsig"
)

var secret = []byte("relay-secret")

func quiet() Option { return WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))) }

type rig struct {
	t    *testing.T
	bus  *Memory
	tix  *Tickets
	hubs []*Hub
	srvs []*httptest.Server
}

func newRig(t *testing.T, n int, opts ...Option) *rig {
	t.Helper()
	r := &rig{t: t, bus: NewMemory(nil), tix: NewTickets(secret, 0, nil)}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	for i := 0; i < n; i++ {
		h := NewHub(r.bus, append([]Option{quiet()}, opts...)...)
		if err := h.Start(ctx); err != nil {
			t.Fatal(err)
		}
		srv := httptest.NewServer(Handler(h, r.tix))
		t.Cleanup(srv.Close)
		r.hubs = append(r.hubs, h)
		r.srvs = append(r.srvs, srv)
	}
	return r
}

// dial connects a fake game client for the player to hub i and reads the hello.
func (r *rig) dial(i int, player string) *websocket.Conn {
	r.t.Helper()
	tk, _ := r.tix.Mint("t1", player)
	url := "ws" + strings.TrimPrefix(r.srvs[i].URL, "http") + "/v1/relay/connect"
	ws, resp, err := websocket.Dial(context.Background(), url, &websocket.DialOptions{
		HTTPHeader: http.Header{"Authorization": {"Bearer " + tk}},
	})
	if err != nil {
		r.t.Fatal(err)
	}
	closeBody(resp)
	r.t.Cleanup(func() { _ = ws.CloseNow() })
	var h helloFrame
	readJSON(r.t, ws, &h)
	if h.Type != "hello" || h.Protocol != 1 {
		r.t.Fatalf("hello = %+v", h)
	}
	return ws
}

func readJSON(t *testing.T, ws *websocket.Conn, v any) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, b, err := ws.Read(ctx)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if err := json.Unmarshal(b, v); err != nil {
		t.Fatal(err)
	}
}

func writeJSON(t *testing.T, ws *websocket.Conn, v any) {
	t.Helper()
	b, _ := json.Marshal(v)
	if err := ws.Write(context.Background(), websocket.MessageText, b); err != nil {
		t.Fatal(err)
	}
}

// answer replies to one call with the given result.
func answer(t *testing.T, ws *websocket.Conn, res map[string]any) callFrame {
	t.Helper()
	var c callFrame
	readJSON(t, ws, &c)
	res["type"] = "result"
	res["id"] = c.ID
	writeJSON(t, ws, res)
	return c
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	for i := 0; i < 300; i++ {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("condition not met")
}

func TestCallLocalAndAcrossReplicas(t *testing.T) {
	tests := []struct {
		name         string
		client, call int // which hub the client is on, which hub issues the call
	}{
		{"same replica", 0, 0},
		{"other replica", 0, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newRig(t, 2)
			ws := r.dial(tt.client, "alice")
			type out struct {
				res core.Result
				err error
			}
			done := make(chan out, 1)
			go func() {
				res, err := r.hubs[tt.call].Call(context.Background(), "t1", "alice", "build", map[string]any{"n": 2.0}, "idem-1")
				done <- out{res, err}
			}()
			c := answer(t, ws, map[string]any{"ok": true, "data": map[string]any{"built": 1}})
			if c.Tool != "build" || c.Player != "alice" || c.Idem != "idem-1" || c.Args["n"] != 2.0 {
				t.Fatalf("call frame = %+v", c)
			}
			o := <-done
			if o.err != nil || !o.res.OK || string(o.res.Data) != `{"built":1}` {
				t.Fatalf("got %+v", o)
			}
			waitFor(t, func() bool { return r.hubs[0].pendingCount() == 0 && r.hubs[1].pendingCount() == 0 })
		})
	}
}

func TestClientDeniedResult(t *testing.T) {
	r := newRig(t, 2)
	ws := r.dial(0, "alice")
	done := make(chan core.Result, 1)
	go func() {
		res, _ := r.hubs[1].Call(context.Background(), "t1", "alice", "build", nil, "")
		done <- res
	}()
	answer(t, ws, map[string]any{"ok": false, "message": "no wood"})
	if res := <-done; res.OK || res.Code != "client_error" || res.Message != "no wood" {
		t.Fatalf("got %+v", res)
	}
}

func TestOffline(t *testing.T) {
	r := newRig(t, 1)
	res, err := r.hubs[0].Call(context.Background(), "t1", "nobody", "build", nil, "")
	if err != nil || res.OK || res.Code != "client_offline" {
		t.Fatalf("got %+v %v", res, err)
	}
	// Tenants are separate: alice of t1 is not alice of t2.
	r.dial(0, "alice")
	res, err = r.hubs[0].Call(context.Background(), "t2", "alice", "build", nil, "")
	if err != nil || res.Code != "client_offline" {
		t.Fatalf("other tenant: %+v %v", res, err)
	}
}

func TestReconnectReplacesOldSocket(t *testing.T) {
	tests := []struct {
		name     string
		oldHub   int
		freshHub int
	}{
		{"same replica", 0, 0},
		{"other replica", 0, 1}, // found at the old replica's next claim check
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newRig(t, 2, WithClaimTTL(150*time.Millisecond))
			old := r.dial(tt.oldHub, "alice")
			fresh := r.dial(tt.freshHub, "alice")
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			if _, _, err := old.Read(ctx); websocket.CloseStatus(err) != CloseReplaced {
				t.Fatalf("old socket close = %v", err)
			}
			done := make(chan core.Result, 1)
			go func() {
				res, _ := r.hubs[0].Call(context.Background(), "t1", "alice", "build", nil, "")
				done <- res
			}()
			answer(t, fresh, map[string]any{"ok": true})
			if !(<-done).OK {
				t.Fatal("call did not reach the newest socket")
			}
		})
	}
}

func TestReplacedSocketFailsPendingCalls(t *testing.T) {
	r := newRig(t, 1)
	ws := r.dial(0, "alice")
	errc := make(chan error, 1)
	go func() {
		_, err := r.hubs[0].Call(context.Background(), "t1", "alice", "build", nil, "")
		errc <- err
	}()
	var c callFrame
	readJSON(t, ws, &c) // call is in flight, never answered
	r.dial(0, "alice")
	if err := <-errc; !errors.Is(err, ErrDisconnected) {
		t.Fatalf("err = %v", err)
	}
	waitFor(t, func() bool { return r.hubs[0].pendingCount() == 0 })
}

func TestDisconnectFailsRemoteCall(t *testing.T) {
	r := newRig(t, 2)
	ws := r.dial(0, "alice")
	errc := make(chan error, 1)
	go func() {
		_, err := r.hubs[1].Call(context.Background(), "t1", "alice", "build", nil, "")
		errc <- err
	}()
	var c callFrame
	readJSON(t, ws, &c)
	_ = ws.Close(websocket.StatusNormalClosure, "bye")
	if err := <-errc; !errors.Is(err, ErrDisconnected) {
		t.Fatalf("err = %v", err)
	}
}

func TestTimeoutAndCancel(t *testing.T) {
	r := newRig(t, 1, WithCallTimeout(80*time.Millisecond))
	ws := r.dial(0, "alice")
	if _, err := r.hubs[0].Call(context.Background(), "t1", "alice", "build", nil, ""); !errors.Is(err, ErrTimeout) {
		t.Fatalf("timeout: %v", err)
	}
	var c callFrame
	readJSON(t, ws, &c)
	// A late answer is dropped without harm.
	writeJSON(t, ws, map[string]any{"type": "result", "id": c.ID, "ok": true})

	ctx, cancel := context.WithCancel(context.Background())
	errc := make(chan error, 1)
	go func() {
		_, err := r.hubs[0].Call(ctx, "t1", "alice", "build", nil, "")
		errc <- err
	}()
	readJSON(t, ws, &c)
	cancel()
	if err := <-errc; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel: %v", err)
	}
	waitFor(t, func() bool { return r.hubs[0].pendingCount() == 0 })
}

func TestUnknownAndForeignResultsAreDropped(t *testing.T) {
	r := newRig(t, 1)
	alice := r.dial(0, "alice")
	bob := r.dial(0, "bob")
	done := make(chan core.Result, 1)
	go func() {
		res, _ := r.hubs[0].Call(context.Background(), "t1", "alice", "build", nil, "")
		done <- res
	}()
	var c callFrame
	readJSON(t, alice, &c)
	writeJSON(t, bob, map[string]any{"type": "result", "id": c.ID, "ok": true}) // bob cannot answer for alice
	writeJSON(t, alice, map[string]any{"type": "result", "id": "nope", "ok": true})
	writeJSON(t, alice, map[string]any{"type": "future-thing"})
	select {
	case res := <-done:
		t.Fatalf("call completed by a foreign or unknown result: %+v", res)
	case <-time.After(150 * time.Millisecond):
	}
	writeJSON(t, alice, map[string]any{"type": "result", "id": c.ID, "ok": true})
	if !(<-done).OK {
		t.Fatal("real answer refused")
	}
}

func TestMalformedFrameClosesSocket(t *testing.T) {
	r := newRig(t, 1)
	ws := r.dial(0, "alice")
	_ = ws.Write(context.Background(), websocket.MessageText, []byte("{not json"))
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, _, err := ws.Read(ctx)
	if websocket.CloseStatus(err) != websocket.StatusPolicyViolation {
		t.Fatalf("close = %v", err)
	}
	waitFor(t, func() bool {
		o, ok, _ := r.bus.Owner(context.Background(), "t1/alice")
		return !ok && o == ""
	})
}

func TestTickets(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	tk := NewTickets(secret, time.Minute, clock)
	good, exp := tk.Mint("t1", "alice")
	if !exp.Equal(now.Add(time.Minute)) {
		t.Fatalf("exp = %v", exp)
	}
	other := NewTickets([]byte("different"), time.Minute, clock)
	otherTk, _ := other.Mint("t1", "alice")
	emptyP, _ := tk.Mint("t1", "")
	emptyT, _ := tk.Mint("", "alice")
	payload, sig, _ := strings.Cut(good, ".")
	tests := []struct {
		name   string
		ticket string
		at     time.Time
		ok     bool
	}{
		{"valid", good, now, true},
		{"valid until expiry", good, now.Add(59 * time.Second), true},
		{"expired", good, now.Add(time.Minute), false},
		{"empty", "", now, false},
		{"no dot", payload, now, false},
		{"tampered payload", payload[:len(payload)-2] + "AA." + sig, now, false},
		{"tampered sig", payload + "." + sig[:len(sig)-2] + "AA", now, false},
		{"wrong secret", otherTk, now, false},
		{"empty player", emptyP, now, false},
		{"empty tenant", emptyT, now, false},
		{"oversize", strings.Repeat("a", maxTicket+1), now, false},
		{"garbage sig", payload + ".!!!", now, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			now = tt.at
			tenant, player, err := tk.Verify(tt.ticket)
			if (err == nil) != tt.ok {
				t.Fatalf("err = %v", err)
			}
			if tt.ok && (tenant != "t1" || player != "alice") {
				t.Fatalf("got %q %q", tenant, player)
			}
			if !tt.ok && !errors.Is(err, ErrInvalidTicket) {
				t.Fatalf("err = %v", err)
			}
		})
	}
}

func TestHandlerRefusesBadTickets(t *testing.T) {
	r := newRig(t, 1)
	now := time.Now()
	expired := NewTickets(secret, time.Second, func() time.Time { return now.Add(-time.Hour) })
	old, _ := expired.Mint("t1", "alice")
	good, _ := r.tix.Mint("t1", "alice")
	tests := []struct{ name, query, auth string }{
		{"none", "", ""},
		{"tampered", "?ticket=" + good + "x", ""},
		{"expired", "?ticket=" + old, ""},
		{"bad header wins over good query", "?ticket=" + good, "Bearer nope"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req, _ := http.NewRequest("GET", r.srvs[0].URL+"/v1/relay/connect"+tt.query, nil)
			if tt.auth != "" {
				req.Header.Set("Authorization", tt.auth)
			}
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = resp.Body.Close() }()
			var body map[string]string
			_ = json.NewDecoder(resp.Body).Decode(&body)
			if resp.StatusCode != 401 || body["error"] != "invalid_ticket" {
				t.Fatalf("got %d %v", resp.StatusCode, body)
			}
		})
	}
	// A ticket in the query works for clients that cannot set headers.
	url := "ws" + strings.TrimPrefix(r.srvs[0].URL, "http") + "/v1/relay/connect?ticket=" + good
	ws, resp, err := websocket.Dial(context.Background(), url, nil)
	if err != nil {
		t.Fatal(err)
	}
	closeBody(resp)
	_ = ws.CloseNow()
}

func TestMintHandler(t *testing.T) {
	tix := NewTickets(secret, 0, nil)
	v := studiosig.New(studiosig.Config{KeyID: "studio-1", Secret: []byte("studio-secret")})
	srv := httptest.NewServer(MintHandler(tix, v, "t1"))
	defer srv.Close()
	do := func(sec []byte, idem string) *http.Response {
		req, _ := http.NewRequest("POST", srv.URL+"/v1/studio/relay/tickets", nil)
		agentsig.Sign(req, "studio-1", sec, "alice", idem, nil, time.Now())
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return resp
	}
	resp := do([]byte("studio-secret"), "m1")
	defer func() { _ = resp.Body.Close() }()
	var body struct {
		Ticket    string    `json:"ticket"`
		ExpiresAt time.Time `json:"expires_at"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&body)
	if resp.StatusCode != 201 || resp.Header.Get("Cache-Control") != "no-store" || body.ExpiresAt.IsZero() {
		t.Fatalf("got %d %+v", resp.StatusCode, body)
	}
	if tenant, player, err := tix.Verify(body.Ticket); err != nil || tenant != "t1" || player != "alice" {
		t.Fatalf("ticket = %q %q %v", tenant, player, err)
	}
	bad := do([]byte("wrong"), "m2")
	defer func() { _ = bad.Body.Close() }()
	if bad.StatusCode != 401 {
		t.Fatalf("bad signature: %d", bad.StatusCode)
	}
	replay := do([]byte("studio-secret"), "m1")
	defer func() { _ = replay.Body.Close() }()
	if replay.StatusCode != 401 {
		t.Fatalf("replay: %d", replay.StatusCode)
	}
}

func TestConnector(t *testing.T) {
	r := newRig(t, 1)
	ws := r.dial(0, "alice")
	c := NewConnector(r.hubs[0])
	done := make(chan core.Result, 1)
	go func() {
		res, _ := c.Call(context.Background(), core.Principal{TenantID: "t1", PlayerID: "alice"}, core.Tool{Name: "build"}, map[string]any{"a": "b"}, "i")
		done <- res
	}()
	got := answer(t, ws, map[string]any{"ok": true})
	if got.Tool != "build" || !(<-done).OK {
		t.Fatalf("got %+v", got)
	}
}

// closeBody closes the handshake response body, which the library may leave nil.
func closeBody(resp *http.Response) {
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}
}
