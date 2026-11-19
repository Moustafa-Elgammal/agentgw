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

// WithLogger sets the logger. Default slog.Default().
func WithLogger(l *slog.Logger) Option { return func(h *Hub) { h.log = l } }

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
	served map[string]string // call id -> caller node, for calls from other replicas
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

	mu      sync.Mutex
	conns   map[string]*conn
	pending map[string]*pendingCall
}

// NewHub builds a Hub on a bus.
func NewHub(bus Bus, opts ...Option) *Hub {
	h := &Hub{
		bus: bus, node: randID(), timeout: 10 * time.Second, ttl: 30 * time.Second, ping: 20 * time.Second,
		log: slog.Default(), conns: map[string]*conn{}, pending: map[string]*pendingCall{},
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

// deliver completes a local call. A non-empty key must match the call's player.
func (h *Hub) deliver(id, key string, o outcome) {
	h.mu.Lock()
	p, ok := h.pending[id]
	if ok && key != "" && p.key != key {
		ok = false
	}
	if ok {
		delete(h.pending, id)
	}
	h.mu.Unlock()
	if ok {
		p.ch <- o // buffered, one send per call
	}
}

func (h *Hub) serveRemote(ctx context.Context, e envelope) {
	h.mu.Lock()
	c := h.conns[e.Key]
	if c != nil {
		c.served[e.ID] = e.From
	}
	h.mu.Unlock()
	if c == nil {
		h.reply(ctx, e.From, envelope{Kind: kindResult, ID: e.ID, Result: offline()})
		return
	}
	if err := h.send(ctx, c, callFrame{Type: typeCall, ID: e.ID, Tool: e.Tool, Player: e.Player, Idem: e.Idem, Args: e.Args}); err != nil {
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
		p.conn = c
	}
	h.pending[id] = p
	h.mu.Unlock()
	defer func() {
		h.mu.Lock()
		delete(h.pending, id)
		h.mu.Unlock()
	}()

	if local {
		err = h.send(ctx, c, callFrame{Type: typeCall, ID: id, Tool: tool, Player: player, Idem: idem, Args: args})
	} else {
		var b []byte
		b, _ = json.Marshal(envelope{Kind: kindCall, From: h.node, ID: id, Key: k, Tool: tool, Player: player, Idem: idem, Args: args})
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
	c := &conn{key: k, ws: ws, cancel: cancel, served: map[string]string{}}
	defer cancel()

	if err := h.bus.Claim(ctx, k, h.node, h.ttl); err != nil {
		h.log.Warn("relay: claim failed", "err", err)
		_ = ws.Close(websocket.StatusInternalError, "unavailable")
		return
	}
	h.mu.Lock()
	old := h.conns[k]
	h.conns[k] = c
	h.mu.Unlock()
	if old != nil {
		go func() {
			_ = old.ws.Close(CloseReplaced, "replaced")
		}()
	}
	defer h.drop(c)

	hello, _ := json.Marshal(helloFrame{Type: typeHello, Protocol: ProtocolVersion})
	if err := ws.Write(ctx, websocket.MessageText, hello); err != nil {
		return
	}
	go h.keepAlive(ctx, c)

	for {
		typ, data, err := ws.Read(ctx)
		if err != nil {
			return
		}
		var f inFrame
		if typ != websocket.MessageText || json.Unmarshal(data, &f) != nil {
			_ = ws.Close(websocket.StatusPolicyViolation, "malformed frame")
			return
		}
		if f.Type != typeResult || f.ID == "" {
			continue
		}
		h.mu.Lock()
		from, served := c.served[f.ID]
		delete(c.served, f.ID)
		h.mu.Unlock()
		if served {
			r := f.result()
			h.reply(ctx, from, envelope{Kind: kindResult, ID: f.ID, Result: &r})
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
				_ = c.ws.Close(websocket.StatusGoingAway, "ping timeout")
				return
			}
		case <-ct.C:
			owner, ok, err := h.bus.Owner(ctx, c.key)
			switch {
			case err != nil:
				h.log.Warn("relay: claim check failed", "err", err)
			case ok && owner != h.node:
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
	served := c.served
	c.served = map[string]string{}
	h.mu.Unlock()

	for _, p := range failed {
		p.ch <- outcome{err: ErrDisconnected}
	}
	bg := context.Background()
	for id, from := range served {
		h.reply(bg, from, envelope{Kind: kindResult, ID: id, Error: "socket closed"})
	}
	if current {
		if err := h.bus.Release(bg, c.key, h.node); err != nil {
			h.log.Warn("relay: release failed", "err", err)
		}
	}
	_ = c.ws.CloseNow()
}

// pendingCount is for tests: calls still waiting.
func (h *Hub) pendingCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.pending)
}
