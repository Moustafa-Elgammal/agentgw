package relay

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/coder/websocket"

	"github.com/Moustafa-Elgammal/agentgw/internal/core"
)

// Errors a Call can return. Both mean the outcome on the client is unknown.
var (
	ErrDisconnected = errors.New("relay: client disconnected before it answered")
	ErrTimeout      = errors.New("relay: client did not answer in time")
)

// Option configures a Hub.
type Option func(*Hub)

// WithCallTimeout bounds how long a call waits for the client. Default 10s.
func WithCallTimeout(d time.Duration) Option { return func(h *Hub) { h.timeout = d } }

// WithClaimTTL sets how long a socket claim lives on the bus; it is refreshed every
// third of that. Default 30s.
func WithClaimTTL(d time.Duration) Option { return func(h *Hub) { h.ttl = d } }

// WithPingInterval sets how often sockets are pinged. Default 20s.
func WithPingInterval(d time.Duration) Option { return func(h *Hub) { h.ping = d } }

// WithMaxInFlight caps how many calls may wait for one player's socket at the same time.
// A call over the cap is refused at once with the denied result client_busy, so a slow
// game client cannot pile up work. Default 8.
func WithMaxInFlight(n int) Option { return func(h *Hub) { h.maxInFlight = n } }

// WithMaxFramesPerSecond limits how fast one client may send frames (a burst of twice the
// rate is allowed). A client over the limit is closed with code 1008. Default 100.
func WithMaxFramesPerSecond(n int) Option { return func(h *Hub) { h.maxFrames = n } }

// WithLogger sets the logger. Default slog.Default().
func WithLogger(l *slog.Logger) Option { return func(h *Hub) { h.log = l } }

// Observer is told about sockets and call results, for metrics. It must not block.
type Observer interface {
	// Connections is called with +1 when a socket is accepted and -1 when it ends.
	Connections(delta int)
	// Call is called once per Hub.Call with ok, denied, offline or error.
	Call(result string)
}

// WithObserver reports socket and call counts to o.
func WithObserver(o Observer) Option { return func(h *Hub) { h.obs = o } }

// Why a socket ended, as reported to an ExtObserver.
const (
	CloseBye         = "bye"          // the client said goodbye
	CloseFlood       = "flood"        // too many frames per second
	CloseMalformed   = "malformed"    // not JSON, or a binary frame
	CloseUnsupported = "unsupported"  // the client declared another major protocol version
	ClosePing        = "ping"         // the client stopped answering pings
	CloseReplacedBy  = "replaced"     // a newer connection of the same player took over
	CloseGone        = "disconnected" // the connection dropped or the server stopped
)

// ExtObserver is an optional second observer for the v1.1 events. Pass it with
// WithExtObserver. It must not block.
type ExtObserver interface {
	// Busy is called when a call is refused because the socket is at its in-flight limit.
	Busy()
	// Closed is called once per socket with one of the Close* reasons.
	Closed(reason string)
}

// WithExtObserver reports busy refusals and the reasons sockets end to o.
func WithExtObserver(o ExtObserver) Option { return func(h *Hub) { h.ext = o } }

type outcome struct {
	res core.Result
	err error
}

type pendingCall struct {
	ch   chan outcome
	conn *conn // the local socket a local call was sent to; nil for remote calls
	key  string
}

type conn struct {
	key    string
	ws     *websocket.Conn
	cancel context.CancelFunc
	served map[string]servedCall // call id -> caller, for calls from other replicas
	local  int                   // calls from this replica waiting on this socket
	client string                // what the client called itself in its hello, for logs
	why    string                // why the socket ended, set before it closes (Close* reason)
}

// servedCall is a call that another replica sent down this replica's socket. The caller
// stops waiting after its deadline, so the entry expires then too: otherwise a client that
// never answers would fill the socket's in-flight slots for good.
type servedCall struct {
	from    string
	expires time.Time
}

// slack is how long past its deadline a served call is remembered, so a result that
// arrives just late is still forwarded (the caller drops it if it has already given up).
var servedSlack = time.Second

// inFlight is how many calls wait on the socket, after forgetting served calls whose
// caller has given up. h.mu must be held.
func (c *conn) inFlight(now time.Time) int {
	for id, sc := range c.served {
		if now.After(sc.expires) {
			delete(c.served, id)
		}
	}
	return c.local + len(c.served)
}

// bucket is a token bucket that limits how fast a client may send frames.
type bucket struct {
	rate, burst float64
	tokens      float64
	last        time.Time
}

func (b *bucket) take(now time.Time) bool {
	if b.last.IsZero() {
		b.tokens = b.burst
	} else {
		b.tokens += now.Sub(b.last).Seconds() * b.rate
		if b.tokens > b.burst {
			b.tokens = b.burst
		}
	}
	b.last = now
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

// Hub holds this replica's game sockets and routes calls to the replica that has the
// right one. Start it before issuing calls.
type Hub struct {
	bus     Bus
	node    string
	timeout time.Duration
	ttl     time.Duration
	ping    time.Duration
	log     *slog.Logger
	obs     Observer    // nil: no metrics
	ext     ExtObserver // nil: no v1.1 metrics

	maxInFlight int
	maxFrames   int

	mu      sync.Mutex
	conns   map[string]*conn
	pending map[string]*pendingCall
}

// NewHub builds a Hub on a bus.
func NewHub(bus Bus, opts ...Option) *Hub {
	h := &Hub{
		bus: bus, node: randID(), timeout: 10 * time.Second, ttl: 30 * time.Second, ping: 20 * time.Second,
		maxInFlight: 8, maxFrames: 100, log: slog.Default(), conns: map[string]*conn{}, pending: map[string]*pendingCall{},
	}
	for _, o := range opts {
		o(h)
	}
	return h
}

func randID() string {
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func key(tenant, player string) string { return tenant + "/" + player }

// Node is this replica's id on the bus.
func (h *Hub) Node() string { return h.node }

// Start subscribes to this replica's inbox and serves it until ctx ends.
func (h *Hub) Start(ctx context.Context) error {
	in, err := h.bus.Subscribe(ctx, h.node)
	if err != nil {
		return fmt.Errorf("relay: subscribe: %w", err)
	}
	go func() {
		for msg := range in {
			h.dispatch(ctx, msg)
		}
	}()
	return nil
}

// Run is Start that blocks until ctx ends.
func (h *Hub) Run(ctx context.Context) error {
	if err := h.Start(ctx); err != nil {
		return err
	}
	<-ctx.Done()
	return nil
}

func (h *Hub) dispatch(ctx context.Context, msg []byte) {
	var e envelope
	if json.Unmarshal(msg, &e) != nil {
		return
	}
	switch e.Kind {
	case kindCall:
		h.serveRemote(ctx, e)
	case kindResult:
		o := outcome{}
		switch {
		case e.Error != "":
			o.err = fmt.Errorf("%w: %s", ErrDisconnected, e.Error)
		case e.Result != nil:
			o.res = *e.Result
		default:
			return
		}
		h.deliver(e.ID, "", o)
	}
}

// removePending forgets a pending call and gives its socket slot back. It reports whether
// the call was still pending. h.mu must be held.
func (h *Hub) removePending(id string) (*pendingCall, bool) {
	p, ok := h.pending[id]
	if !ok {
		return nil, false
	}
	delete(h.pending, id)
	if p.conn != nil && p.conn.local > 0 {
		p.conn.local--
	}
	return p, true
}

// deliver completes a local call. A non-empty key must match the call's player.
func (h *Hub) deliver(id, key string, o outcome) {
	h.mu.Lock()
	p, ok := h.pending[id]
	if ok && key != "" && p.key != key {
		ok = false
	}
	if ok {
		h.removePending(id)
	}
	h.mu.Unlock()
	if ok {
		p.ch <- o // buffered, one send per call
	}
}

func busy() *core.Result {
	r := core.Denied("client_busy", "the player's game client has too many calls in flight, try again shortly")
	return &r
}

func (h *Hub) noteBusy() {
	if h.ext != nil {
		h.ext.Busy()
	}
}

// deadlineMS is how many milliseconds of waiting the context has left, at least 1, or 0
// when it has no deadline.
func deadlineMS(ctx context.Context) int64 {
	d, ok := ctx.Deadline()
	if !ok {
		return 0
	}
	ms := time.Until(d).Milliseconds()
	if ms < 1 {
		ms = 1
	}
	return ms
}

func (h *Hub) serveRemote(ctx context.Context, e envelope) {
	h.mu.Lock()
	c := h.conns[e.Key]
	full := c != nil && c.inFlight(time.Now()) >= h.maxInFlight
	if c != nil && !full {
		wait := h.timeout
		if e.DeadlineMS > 0 {
			wait = time.Duration(e.DeadlineMS) * time.Millisecond
		}
		c.served[e.ID] = servedCall{from: e.From, expires: time.Now().Add(wait + servedSlack)}
	}
	h.mu.Unlock()
	if c == nil {
		h.reply(ctx, e.From, envelope{Kind: kindResult, ID: e.ID, Result: offline()})
		return
	}
	if full {
		h.noteBusy()
		h.reply(ctx, e.From, envelope{Kind: kindResult, ID: e.ID, Result: busy()})
		return
	}
	if err := h.send(ctx, c, callFrame{Type: typeCall, ID: e.ID, Tool: e.Tool, Player: e.Player, Idem: e.Idem, DeadlineMS: e.DeadlineMS, Args: e.Args}); err != nil {
		h.mu.Lock()
		delete(c.served, e.ID)
		h.mu.Unlock()
		h.reply(ctx, e.From, envelope{Kind: kindResult, ID: e.ID, Error: "send failed"})
	}
}

func offline() *core.Result {
	r := core.Denied("client_offline", "the player's game client is not connected")
	return &r
}

func (h *Hub) reply(ctx context.Context, node string, e envelope) {
	e.From = h.node
	b, _ := json.Marshal(e)
	if err := h.bus.Publish(ctx, node, b); err != nil {
		h.log.Warn("relay: reply failed", "err", err)
	}
}

func (h *Hub) send(ctx context.Context, c *conn, f callFrame) error {
	b, err := json.Marshal(f)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	return c.ws.Write(ctx, websocket.MessageText, b)
}

// Call runs a tool on the player's game client, wherever in the fleet the socket is.
// No socket is a denied client_offline result. A bus failure, a lost socket or a timeout
// is a Go error, because the client may or may not have acted.
func (h *Hub) Call(ctx context.Context, tenant, player, tool string, args map[string]any, idem string) (core.Result, error) {
	res, err := h.call(ctx, tenant, player, tool, args, idem)
	if h.obs != nil {
		switch {
		case err != nil:
			h.obs.Call("error")
		case res.OK:
			h.obs.Call("ok")
		case res.Code == "client_offline":
			h.obs.Call("offline")
		case res.Code == CodeClientBusy:
			h.obs.Call("busy")
		default:
			h.obs.Call("denied")
		}
	}
	return res, err
}

func (h *Hub) call(ctx context.Context, tenant, player, tool string, args map[string]any, idem string) (core.Result, error) {
	k := key(tenant, player)
	owner, ok, err := h.bus.Owner(ctx, k)
	if err != nil {
		return core.Result{}, fmt.Errorf("relay: owner lookup: %w", err)
	}
	if !ok {
		return *offline(), nil
	}
	ctx, cancel := context.WithTimeout(ctx, h.timeout)
	defer cancel()

	id := randID()
	p := &pendingCall{ch: make(chan outcome, 1), key: k}
	h.mu.Lock()
	local := owner == h.node
	var c *conn
	if local {
		c = h.conns[k]
		if c == nil {
			h.mu.Unlock()
			return *offline(), nil
		}
		if c.inFlight(time.Now()) >= h.maxInFlight {
			h.mu.Unlock()
			h.noteBusy()
			return *busy(), nil
		}
		p.conn = c
		c.local++
	}
	h.pending[id] = p
	h.mu.Unlock()
	defer func() {
		h.mu.Lock()
		h.removePending(id)
		h.mu.Unlock()
	}()

	if local {
		err = h.send(ctx, c, callFrame{Type: typeCall, ID: id, Tool: tool, Player: player, Idem: idem, DeadlineMS: deadlineMS(ctx), Args: args})
	} else {
		var b []byte
		b, _ = json.Marshal(envelope{Kind: kindCall, From: h.node, ID: id, Key: k, Tool: tool, Player: player, Idem: idem, DeadlineMS: deadlineMS(ctx), Args: args})
		err = h.bus.Publish(ctx, owner, b)
	}
	if err != nil {
		return core.Result{}, fmt.Errorf("relay: send: %w", err)
	}
	select {
	case o := <-p.ch:
		return o.res, o.err
	case <-ctx.Done():
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return core.Result{}, ErrTimeout
		}
		return core.Result{}, ctx.Err()
	}
}

// Serve owns one accepted socket until it closes. The caller authenticated the player.
func (h *Hub) Serve(ctx context.Context, tenant, player string, ws *websocket.Conn) {
	ws.SetReadLimit(MaxFrame)
	k := key(tenant, player)
	ctx, cancel := context.WithCancel(ctx)
	c := &conn{key: k, ws: ws, cancel: cancel, served: map[string]servedCall{}}
	defer cancel()

	if err := h.bus.Claim(ctx, k, h.node, h.ttl); err != nil {
		h.log.Warn("relay: claim failed", "err", err)
		_ = ws.Close(websocket.StatusInternalError, "unavailable")
		return
	}
	h.mu.Lock()
	old := h.conns[k]
	h.conns[k] = c
	if old != nil && old.why == "" {
		old.why = CloseReplacedBy
	}
	h.mu.Unlock()
	if old != nil {
		go func() {
			_ = old.ws.Close(CloseReplaced, "replaced")
		}()
	}
	defer h.drop(c)
	if h.obs != nil {
		h.obs.Connections(1)
		defer h.obs.Connections(-1)
	}

	hello, _ := json.Marshal(helloFrame{
		Type: typeHello, Protocol: ProtocolVersion, Minor: ProtocolMinor,
		CallTimeoutMS: h.timeout.Milliseconds(), PingMS: h.ping.Milliseconds(),
		MaxInFlight: h.maxInFlight, MaxFrame: MaxFrame,
	})
	if err := ws.Write(ctx, websocket.MessageText, hello); err != nil {
		return
	}
	go h.keepAlive(ctx, c)

	limit := &bucket{rate: float64(h.maxFrames), burst: float64(2 * h.maxFrames)}
	gotHello := false
	for {
		typ, data, err := ws.Read(ctx)
		if err != nil {
			return
		}
		if h.maxFrames > 0 && !limit.take(time.Now()) {
			h.setWhy(c, CloseFlood)
			_ = ws.Close(websocket.StatusPolicyViolation, "too many frames")
			return
		}
		var f inFrame
		if typ != websocket.MessageText || json.Unmarshal(data, &f) != nil {
			h.setWhy(c, CloseMalformed)
			_ = ws.Close(websocket.StatusPolicyViolation, "malformed frame")
			return
		}
		switch f.Type {
		case typeHello:
			if gotHello {
				continue
			}
			gotHello = true
			var ch clientHello
			if json.Unmarshal(data, &ch) != nil {
				h.setWhy(c, CloseMalformed)
				_ = ws.Close(websocket.StatusPolicyViolation, "malformed hello")
				return
			}
			if ch.Protocol != 0 && ch.Protocol != ProtocolVersion {
				h.setWhy(c, CloseUnsupported)
				_ = ws.Close(websocket.StatusProtocolError, "unsupported protocol")
				return
			}
			h.mu.Lock()
			c.client = clean(ch.Client, maxClientName)
			h.mu.Unlock()
			h.log.Debug("relay: client hello", "client", clean(ch.Client, maxClientName), "minor", ch.Minor)
			continue
		case typeBye:
			// A graceful close. Calls still waiting on this socket fail with ErrDisconnected,
			// because the client left before answering them.
			h.log.Debug("relay: client bye", "reason", clean(f.Reason, maxByeReason))
			h.setWhy(c, CloseBye)
			_ = ws.Close(websocket.StatusNormalClosure, "bye")
			return
		}
		if f.Type != typeResult || f.ID == "" {
			continue
		}
		h.mu.Lock()
		sc, served := c.served[f.ID]
		delete(c.served, f.ID)
		h.mu.Unlock()
		if served {
			r := f.result()
			h.reply(ctx, sc.from, envelope{Kind: kindResult, ID: f.ID, Result: &r})
			continue
		}
		h.deliver(f.ID, k, outcome{res: f.result()})
	}
}

// keepAlive pings the socket and refreshes its claim. It also notices another replica
// taking the player over and closes this socket.
func (h *Hub) keepAlive(ctx context.Context, c *conn) {
	pt := time.NewTicker(h.ping)
	ct := time.NewTicker(h.ttl / 3)
	defer pt.Stop()
	defer ct.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-pt.C:
			pctx, cancel := context.WithTimeout(ctx, h.ping/2+time.Second)
			err := c.ws.Ping(pctx)
			cancel()
			if err != nil {
				h.setWhy(c, ClosePing)
				_ = c.ws.Close(websocket.StatusGoingAway, "ping timeout")
				return
			}
		case <-ct.C:
			owner, ok, err := h.bus.Owner(ctx, c.key)
			switch {
			case err != nil:
				h.log.Warn("relay: claim check failed", "err", err)
			case ok && owner != h.node:
				h.setWhy(c, CloseReplacedBy)
				_ = c.ws.Close(CloseReplaced, "replaced")
				return
			default:
				if err := h.bus.Claim(ctx, c.key, h.node, h.ttl); err != nil {
					h.log.Warn("relay: claim refresh failed", "err", err)
				}
			}
		}
	}
}

// drop forgets a closed socket and fails the calls that were waiting on it.
func (h *Hub) drop(c *conn) {
	h.mu.Lock()
	current := h.conns[c.key] == c
	if current {
		delete(h.conns, c.key)
	}
	var failed []*pendingCall
	for id, p := range h.pending {
		if p.conn == c {
			failed = append(failed, p)
			delete(h.pending, id)
		}
	}
	c.local = 0
	served := c.served
	c.served = map[string]servedCall{}
	why := c.why
	h.mu.Unlock()
	if why == "" {
		why = CloseGone
	}
	if h.ext != nil {
		h.ext.Closed(why)
	}

	for _, p := range failed {
		p.ch <- outcome{err: ErrDisconnected}
	}
	bg := context.Background()
	for id, sc := range served {
		h.reply(bg, sc.from, envelope{Kind: kindResult, ID: id, Error: "socket closed"})
	}
	if current {
		if err := h.bus.Release(bg, c.key, h.node); err != nil {
			h.log.Warn("relay: release failed", "err", err)
		}
	}
	_ = c.ws.CloseNow()
}

// setWhy records why a socket is ending, unless a reason is already set.
func (h *Hub) setWhy(c *conn, why string) {
	h.mu.Lock()
	if c.why == "" {
		c.why = why
	}
	h.mu.Unlock()
}

// inFlightFor is for tests: calls waiting on a player's socket on this replica.
func (h *Hub) inFlightFor(tenant, player string) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	if c := h.conns[key(tenant, player)]; c != nil {
		return c.inFlight(time.Now())
	}
	return 0
}

// pendingCount is for tests: calls still waiting.
func (h *Hub) pendingCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.pending)
}
