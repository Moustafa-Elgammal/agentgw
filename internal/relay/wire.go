package relay

import (
	"encoding/json"
	"fmt"

	"github.com/Moustafa-Elgammal/agentgw/internal/core"
)

// Wire protocol v1.1 between the gateway and a game client. Every frame is one WebSocket
// text message holding one JSON object with a "type" field. The client connects with a
// ticket (see Tickets) minted by the studio's backend. The full description is in
// docs/PROTOCOL.md, "Relay". Version 1.1 only adds fields and frame types, so a v1 client
// (one that ignores unknown fields and never sends hello or bye) keeps working.
//
// Gateway to client:
//
//	{"type":"hello","protocol":1,"minor":1,"call_timeout_ms":10000,"ping_interval_ms":20000,
//	 "max_in_flight":8,"max_frame":1048576}              sent once after the upgrade
//	{"type":"call","id":"...","tool":"build","player":"alice","idem":"...","deadline_ms":9800,
//	 "args":{...}}
//
// Client to gateway:
//
//	{"type":"hello","protocol":1,"minor":1,"client":"unity-relay 0.1","capabilities":[]}   optional
//	{"type":"result","id":"...","ok":true,"data":{...}}
//	{"type":"result","id":"...","ok":false,"code":"not_enough_wood","message":"...","retry_after_ms":2000}
//	{"type":"bye","reason":"app quit"}                   optional graceful close
//
// The id of a result must be the id of a call the gateway sent on that same socket.
// Results for unknown or expired ids are dropped. Frames of an unknown type are ignored
// so the protocol can grow. A malformed frame, or a binary frame, closes the socket with
// code 1008. A newer connection of the same player replaces the older one, which is closed
// with code 4000 "replaced". Frames are limited to MaxFrame bytes. The client must answer
// WebSocket pings (any WebSocket library does so by default).
const (
	// ProtocolVersion is the major version announced in the hello frame. A client that
	// declares another major version in its own hello is closed with code 1002.
	ProtocolVersion = 1
	// ProtocolMinor is the minor version announced in the hello frame.
	ProtocolMinor = 1
	// MaxFrame is the largest frame either side may send.
	MaxFrame = 1 << 20

	// CloseReplaced is the close code sent to a socket replaced by a newer one.
	CloseReplaced = 4000

	// CodeClientBusy is the result code for a call refused because the socket already has
	// its limit of calls in flight, or because the client said it is busy.
	CodeClientBusy = "client_busy"

	// Limits on what a client may put in the optional frames, so they cannot grow logs or
	// messages without bound.
	maxClientName   = 64
	maxByeReason    = 128
	maxRetryAfterMS = 3600000

	typeHello  = "hello"
	typeCall   = "call"
	typeResult = "result"
	typeBye    = "bye"
)

// helloFrame is what the gateway sends once after the upgrade. A v1 client reads only
// Protocol and ignores the rest.
type helloFrame struct {
	Type          string `json:"type"`
	Protocol      int    `json:"protocol"`
	Minor         int    `json:"minor,omitempty"`
	CallTimeoutMS int64  `json:"call_timeout_ms,omitempty"`
	PingMS        int64  `json:"ping_interval_ms,omitempty"`
	MaxInFlight   int    `json:"max_in_flight,omitempty"`
	MaxFrame      int    `json:"max_frame,omitempty"`
}

// clientHello is the optional reply from the client.
type clientHello struct {
	Protocol     int      `json:"protocol"`
	Minor        int      `json:"minor"`
	Client       string   `json:"client"`
	Capabilities []string `json:"capabilities"`
}

// callFrame asks the client to run a tool.
type callFrame struct {
	Type   string `json:"type"`
	ID     string `json:"id"`
	Tool   string `json:"tool"`
	Player string `json:"player"`
	Idem   string `json:"idem,omitempty"`
	// DeadlineMS is how long the gateway will still wait for the result, in milliseconds
	// from the moment the frame was sent. A relative time, so client clocks do not matter.
	DeadlineMS int64          `json:"deadline_ms,omitempty"`
	Args       map[string]any `json:"args,omitempty"`
}

// inFrame is any frame the client sends; only result frames are acted on.
type inFrame struct {
	Type    string          `json:"type"`
	ID      string          `json:"id"`
	OK      bool            `json:"ok"`
	Code    string          `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data"`
	// RetryAfterMS lets a client that refuses or fails a call say when trying again makes
	// sense. It is added to the message the agent reads.
	RetryAfterMS int64  `json:"retry_after_ms"`
	Reason       string `json:"reason"` // bye frames
}

func (f inFrame) result() core.Result {
	r := core.Result{OK: f.OK, Code: f.Code, Message: f.Message, Data: f.Data}
	if !r.OK && r.Code == "" {
		r.Code = "client_error"
	}
	if !r.OK && f.RetryAfterMS > 0 {
		ms := f.RetryAfterMS
		if ms > maxRetryAfterMS {
			ms = maxRetryAfterMS
		}
		hint := fmt.Sprintf("retry after %d ms", ms)
		if r.Message == "" {
			r.Message = hint
		} else {
			r.Message += " (" + hint + ")"
		}
	}
	return r
}

// clean makes a client-supplied string safe to log: printable ASCII only, bounded length.
func clean(s string, max int) string {
	out := make([]rune, 0, len(s))
	for _, r := range s {
		if r < 0x20 || r > 0x7e {
			continue
		}
		out = append(out, r)
		if len(out) == max {
			break
		}
	}
	return string(out)
}

// Envelope kinds carried on the Bus between replicas.
const (
	kindCall   = "call"
	kindResult = "result"
)

// envelope is what replicas exchange. A call goes to the node that holds the socket;
// the result goes back to From.
type envelope struct {
	Kind   string         `json:"kind"`
	From   string         `json:"from"`
	ID     string         `json:"id"`
	Key    string         `json:"key,omitempty"`
	Tool   string         `json:"tool,omitempty"`
	Player string         `json:"player,omitempty"`
	Idem   string         `json:"idem,omitempty"`
	Args   map[string]any `json:"args,omitempty"`
	// DeadlineMS is the caller's remaining wait when the call was published.
	DeadlineMS int64        `json:"deadline_ms,omitempty"`
	Result     *core.Result `json:"result,omitempty"`
	Error      string       `json:"error,omitempty"` // outcome unknown: the caller fails closed
}
