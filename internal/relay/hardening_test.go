package relay

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/Moustafa-Elgammal/agentgw/internal/core"
)

// extRec records ExtObserver events.
type extRec struct {
	mu     sync.Mutex
	busy   int
	closes []string
}

func (e *extRec) Busy() { e.mu.Lock(); e.busy++; e.mu.Unlock() }
func (e *extRec) Closed(r string) {
	e.mu.Lock()
	e.closes = append(e.closes, r)
	e.mu.Unlock()
}
func (e *extRec) snapshot() (int, []string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.busy, append([]string(nil), e.closes...)
}
func (e *extRec) hasClose(reason string) bool {
	_, cs := e.snapshot()
	for _, c := range cs {
		if c == reason {
			return true
		}
	}
	return false
}

// dialRaw connects a fake game client and returns the socket and the raw hello frame.
func (r *rig) dialRaw(i int, player string) (*websocket.Conn, []byte) {
	r.t.Helper()
	tk, _ := r.tix.Mint("t1", player)
	url := "ws" + strings.TrimPrefix(r.srvs[i].URL, "http") + "/v1/relay/connect"
	ws, resp, err := websocket.Dial(context.Background(), url, &websocket.DialOptions{
		HTTPHeader: http.Header{"Authorization": {"Bearer " + tk}},
	})
	if err != nil {
		r.t.Fatal(err)
	}
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}
	r.t.Cleanup(func() { _ = ws.CloseNow() })
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, b, err := ws.Read(ctx)
	if err != nil {
		r.t.Fatal(err)
	}
	return ws, b
}

// closeCode reads until the socket ends and returns the close status.
func closeCode(t *testing.T, ws *websocket.Conn) websocket.StatusCode {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for {
		if _, _, err := ws.Read(ctx); err != nil {
			return websocket.CloseStatus(err)
		}
	}
}

type callOut struct {
	res core.Result
	err error
}

func callAsync(h *Hub, player string) chan callOut {
	ch := make(chan callOut, 1)
	go func() {
		res, err := h.Call(context.Background(), "t1", player, "build", nil, "")
		ch <- callOut{res, err}
	}()
	return ch
}

func TestHelloAnnouncesLimits(t *testing.T) {
	r := newRig(t, 1, WithCallTimeout(7*time.Second), WithPingInterval(13*time.Second), WithMaxInFlight(3))
	_, raw := r.dialRaw(0, "alice")
	var h map[string]any
	if err := json.Unmarshal(raw, &h); err != nil {
		t.Fatal(err)
	}
	want := map[string]float64{"protocol": 1, "minor": 1, "call_timeout_ms": 7000, "ping_interval_ms": 13000, "max_in_flight": 3, "max_frame": MaxFrame}
	if h["type"] != "hello" {
		t.Fatalf("type = %v", h["type"])
	}
	for k, v := range want {
		if h[k] != v {
			t.Errorf("hello[%s] = %v, want %v", k, h[k], v)
		}
	}
}

func TestClientHelloNegotiation(t *testing.T) {
	tests := []struct {
		name  string
		hello map[string]any // nil: a v1 client that sends none
		close websocket.StatusCode
		raw   string
	}{
		{name: "v1 client sends no hello"},
		{name: "v1.1 hello", hello: map[string]any{"type": "hello", "protocol": 1, "minor": 1, "client": "unity-relay 0.1", "capabilities": []string{"x"}}},
		{name: "hello without a version", hello: map[string]any{"type": "hello", "client": "tiny"}},
		{name: "control characters in the name are harmless", hello: map[string]any{"type": "hello", "protocol": 1, "client": "a\r\nb\x00"}},
		{name: "another major version", hello: map[string]any{"type": "hello", "protocol": 2}, close: websocket.StatusProtocolError},
		{name: "hello with a wrong type", raw: `{"type":"hello","protocol":"one"}`, close: websocket.StatusPolicyViolation},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ext := &extRec{}
			r := newRig(t, 1, WithExtObserver(ext))
			ws := r.dial(0, "alice")
			switch {
			case tt.raw != "":
				_ = ws.Write(context.Background(), websocket.MessageText, []byte(tt.raw))
			case tt.hello != nil:
				writeJSON(t, ws, tt.hello)
			}
			if tt.close != 0 {
				if got := closeCode(t, ws); got != tt.close {
					t.Fatalf("close = %d, want %d", got, tt.close)
				}
				return
			}
			done := callAsync(r.hubs[0], "alice")
			answer(t, ws, map[string]any{"ok": true})
			if o := <-done; o.err != nil || !o.res.OK {
				t.Fatalf("call after hello: %+v", o)
			}
			if tt.hello == nil {
				return
			}
			// A second hello, even a bad one, is ignored once the first was accepted.
			writeJSON(t, ws, map[string]any{"type": "hello", "protocol": 2})
			done = callAsync(r.hubs[0], "alice")
			answer(t, ws, map[string]any{"ok": true})
			if o := <-done; o.err != nil || !o.res.OK {
				t.Fatalf("call after second hello: %+v", o)
			}
		})
	}
}

func TestCallFrameCarriesDeadline(t *testing.T) {
	tests := []struct {
		name         string
		client, call int
	}{
		{"same replica", 0, 0},
		{"other replica", 0, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newRig(t, 2, WithCallTimeout(2*time.Second))
			ws := r.dial(tt.client, "alice")
			done := callAsync(r.hubs[tt.call], "alice")
			c := answer(t, ws, map[string]any{"ok": true})
			if c.DeadlineMS < 1000 || c.DeadlineMS > 2000 {
				t.Fatalf("deadline_ms = %d, want within 1000 to 2000", c.DeadlineMS)
			}
			<-done
		})
	}
}

func TestInFlightCap(t *testing.T) {
	tests := []struct {
		name         string
		client, call int
	}{
		{"same replica", 0, 0},
		{"other replica", 0, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ext := &extRec{}
			r := newRig(t, 2, WithMaxInFlight(2), WithExtObserver(ext))
			alice := r.dial(tt.client, "alice")
			bob := r.dial(tt.client, "bob")

			first, second := callAsync(r.hubs[tt.call], "alice"), callAsync(r.hubs[tt.call], "alice")
			var f1, f2 callFrame
			readJSON(t, alice, &f1)
			readJSON(t, alice, &f2)
			waitFor(t, func() bool { return r.hubs[tt.client].inFlightFor("t1", "alice") == 2 })

			// The third call is refused at once, as a denied result and not a Go error.
			res, err := r.hubs[tt.call].Call(context.Background(), "t1", "alice", "build", nil, "")
			if err != nil || res.OK || res.Code != CodeClientBusy {
				t.Fatalf("third call = %+v %v, want denied client_busy", res, err)
			}
			if n, _ := ext.snapshot(); n != 1 {
				t.Fatalf("busy events = %d, want 1", n)
			}
			// The cap is per socket: bob is unaffected.
			bobDone := callAsync(r.hubs[tt.call], "bob")
			answer(t, bob, map[string]any{"ok": true})
			if o := <-bobDone; o.err != nil || !o.res.OK {
				t.Fatalf("bob's call: %+v", o)
			}

			// Answering one frees a slot. Which call got which frame depends on scheduling,
			// so take whichever of the two finishes.
			writeJSON(t, alice, map[string]any{"type": "result", "id": f1.ID, "ok": true})
			var rest chan callOut
			select {
			case o := <-first:
				if o.err != nil || !o.res.OK {
					t.Fatalf("first: %+v", o)
				}
				rest = second
			case o := <-second:
				if o.err != nil || !o.res.OK {
					t.Fatalf("second: %+v", o)
				}
				rest = first
			}
			waitFor(t, func() bool { return r.hubs[tt.client].inFlightFor("t1", "alice") == 1 })
			fourth := callAsync(r.hubs[tt.call], "alice")
			var f4 callFrame
			readJSON(t, alice, &f4)
			writeJSON(t, alice, map[string]any{"type": "result", "id": f2.ID, "ok": true})
			writeJSON(t, alice, map[string]any{"type": "result", "id": f4.ID, "ok": true})
			if o := <-rest; !o.res.OK {
				t.Fatalf("remaining call: %+v", o)
			}
			if o := <-fourth; !o.res.OK {
				t.Fatalf("fourth: %+v", o)
			}
			waitFor(t, func() bool {
				return r.hubs[0].inFlightFor("t1", "alice") == 0 && r.hubs[1].inFlightFor("t1", "alice") == 0 &&
					r.hubs[0].pendingCount() == 0 && r.hubs[1].pendingCount() == 0
			})
		})
	}
}

func TestRetryAfterHint(t *testing.T) {
	tests := []struct {
		name string
		in   inFrame
		want core.Result
	}{
		{"hint added to a message", inFrame{OK: false, Code: "client_busy", Message: "loading", RetryAfterMS: 2000}, core.Result{Code: "client_busy", Message: "loading (retry after 2000 ms)"}},
		{"hint alone becomes the message", inFrame{OK: false, Code: "client_busy", RetryAfterMS: 500}, core.Result{Code: "client_busy", Message: "retry after 500 ms"}},
		{"huge hint is clamped", inFrame{OK: false, Code: "x", RetryAfterMS: 1 << 40}, core.Result{Code: "x", Message: "retry after 3600000 ms"}},
		{"negative hint is ignored", inFrame{OK: false, Code: "x", Message: "m", RetryAfterMS: -5}, core.Result{Code: "x", Message: "m"}},
		{"no code defaults to client_error", inFrame{OK: false, RetryAfterMS: 10}, core.Result{Code: "client_error", Message: "retry after 10 ms"}},
		{"success ignores the hint", inFrame{OK: true, RetryAfterMS: 10}, core.Result{OK: true}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.in.result()
			if got.OK != tt.want.OK || got.Code != tt.want.Code || got.Message != tt.want.Message {
				t.Fatalf("got %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestByeFailsPendingCalls(t *testing.T) {
	ext := &extRec{}
	r := newRig(t, 2, WithExtObserver(ext))
	ws := r.dial(0, "alice")
	done := callAsync(r.hubs[1], "alice")
	var c callFrame
	readJSON(t, ws, &c)
	writeJSON(t, ws, map[string]any{"type": "bye", "reason": "app quit\r\n"})
	if got := closeCode(t, ws); got != websocket.StatusNormalClosure {
		t.Fatalf("close = %d, want 1000", got)
	}
	o := <-done
	if !errors.Is(o.err, ErrDisconnected) {
		t.Fatalf("pending call after bye: %+v, want ErrDisconnected", o)
	}
	waitFor(t, func() bool { return ext.hasClose(CloseBye) })
	waitFor(t, func() bool {
		_, ok, _ := r.bus.Owner(context.Background(), "t1/alice")
		return !ok
	})
	// After a bye the player is offline until the client reconnects.
	res, err := r.hubs[1].Call(context.Background(), "t1", "alice", "build", nil, "")
	if err != nil || res.Code != "client_offline" {
		t.Fatalf("after bye: %+v %v", res, err)
	}
}

// A client that never answers: every call times out, and nothing stays behind.
func TestSlowClientLeavesNothingPending(t *testing.T) {
	r := newRig(t, 2, WithCallTimeout(60*time.Millisecond), WithMaxInFlight(4))
	ws := r.dial(0, "alice")
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := r.hubs[i%2].Call(context.Background(), "t1", "alice", "build", nil, "")
			errs <- err
		}(i)
	}
	wg.Wait()
	close(errs)
	timeouts, busy := 0, 0
	for err := range errs {
		switch {
		case errors.Is(err, ErrTimeout):
			timeouts++
		case err == nil:
			busy++ // refused by the cap, which is an answer and not an error
		default:
			t.Fatalf("unexpected error: %v", err)
		}
	}
	if timeouts < 4 {
		t.Fatalf("timeouts = %d, busy = %d, want at least 4 timeouts", timeouts, busy)
	}
	waitFor(t, func() bool {
		return r.hubs[0].pendingCount() == 0 && r.hubs[1].pendingCount() == 0 && r.hubs[0].inFlightFor("t1", "alice") == 0
	})
	_ = ws
}

func TestGarbageFramesCloseTheSocket(t *testing.T) {
	tests := []struct {
		name   string
		typ    websocket.MessageType
		data   []byte
		close  websocket.StatusCode
		reason string
	}{
		{"not json", websocket.MessageText, []byte("{not"), websocket.StatusPolicyViolation, CloseMalformed},
		{"json array", websocket.MessageText, []byte("[1,2]"), websocket.StatusPolicyViolation, CloseMalformed},
		{"binary frame", websocket.MessageBinary, []byte{0, 1, 2}, websocket.StatusPolicyViolation, CloseMalformed},
		{"frame over the limit", websocket.MessageText, []byte(`{"type":"x","pad":"` + strings.Repeat("a", MaxFrame) + `"}`), websocket.StatusMessageTooBig, CloseGone},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ext := &extRec{}
			r := newRig(t, 1, WithExtObserver(ext))
			ws := r.dial(0, "alice")
			ws.SetReadLimit(2 * MaxFrame)
			_ = ws.Write(context.Background(), tt.typ, tt.data)
			if got := closeCode(t, ws); got != tt.close {
				t.Fatalf("close = %d, want %d", got, tt.close)
			}
			waitFor(t, func() bool { return ext.hasClose(tt.reason) })
		})
	}
}

// A client that floods valid but useless frames is closed, and its goroutines end.
func TestFloodClosesSocketWithoutLeaks(t *testing.T) {
	ext := &extRec{}
	r := newRig(t, 1, WithMaxFramesPerSecond(20), WithExtObserver(ext))
	base := runtime.NumGoroutine()
	ws := r.dial(0, "alice")
	closed := make(chan websocket.StatusCode, 1)
	go func() { closed <- closeCode(t, ws) }()
	frame := []byte(`{"type":"noise"}`)
	for i := 0; i < 400; i++ {
		if err := ws.Write(context.Background(), websocket.MessageText, frame); err != nil {
			break
		}
	}
	if got := <-closed; got != websocket.StatusPolicyViolation {
		t.Fatalf("close = %d, want 1008", got)
	}
	waitFor(t, func() bool { return ext.hasClose(CloseFlood) })
	_ = ws.CloseNow()
	waitFor(t, func() bool { return runtime.NumGoroutine() <= base+2 })
	res, err := r.hubs[0].Call(context.Background(), "t1", "alice", "build", nil, "")
	if err != nil || res.Code != "client_offline" {
		t.Fatalf("after flood: %+v %v", res, err)
	}
}

func TestBucket(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	b := &bucket{rate: 10, burst: 20}
	taken := 0
	for b.take(now) {
		taken++
	}
	if taken != 20 {
		t.Fatalf("burst = %d, want 20", taken)
	}
	if !b.take(now.Add(time.Second)) {
		t.Fatal("no refill after a second")
	}
	// Refill is capped at the burst.
	b = &bucket{rate: 10, burst: 20}
	b.take(now)
	taken = 0
	for b.take(now.Add(time.Hour)) {
		taken++
	}
	if taken != 20 {
		t.Fatalf("capped refill = %d, want 20", taken)
	}
}

// A client that never answers must not fill its socket's slots for good when the callers
// are on another replica: the owning replica forgets a call once its caller gave up.
func TestSilentClientDoesNotWedgeRemoteCallers(t *testing.T) {
	old := servedSlack
	servedSlack = 20 * time.Millisecond
	t.Cleanup(func() { servedSlack = old })

	r := newRig(t, 2, WithMaxInFlight(2), WithCallTimeout(60*time.Millisecond))
	ws := r.dial(0, "alice")
	for i := 0; i < 2; i++ {
		if _, err := r.hubs[1].Call(context.Background(), "t1", "alice", "build", nil, ""); !errors.Is(err, ErrTimeout) {
			t.Fatalf("call %d: %v", i, err)
		}
	}
	// Both slots are still held on the owner right now; they expire with the callers' deadline.
	waitFor(t, func() bool { return r.hubs[0].inFlightFor("t1", "alice") == 0 })
	done := callAsync(r.hubs[1], "alice")
	var late callFrame
	for i := 0; i < 3; i++ { // the two timed out frames, then the new one
		readJSON(t, ws, &late)
	}
	writeJSON(t, ws, map[string]any{"type": "result", "id": late.ID, "ok": true})
	if o := <-done; o.err != nil || !o.res.OK {
		t.Fatalf("a new call after the timeouts: %+v", o)
	}
}
