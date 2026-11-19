package relay

import (
	"encoding/json"

	"github.com/Moustafa-Elgammal/agentgw/internal/core"
)

// Wire protocol v1 between the gateway and a game client. Every frame is one WebSocket
// text message holding one JSON object with a "type" field. The client connects with a
// ticket (see Tickets) minted by the studio's backend.
//
// Gateway to client:
//
//	{"type":"hello","protocol":1}                        sent once after the upgrade
//	{"type":"call","id":"...","tool":"build","player":"alice","idem":"...","args":{...}}
//
// Client to gateway:
//
//	{"type":"result","id":"...","ok":true,"data":{...}}
//	{"type":"result","id":"...","ok":false,"code":"not_enough_wood","message":"..."}
//
// The id of a result must be the id of a call the gateway sent on that same socket.
// Results for unknown or expired ids are dropped. Frames of an unknown type are ignored
// so the protocol can grow. A malformed frame, or a binary frame, closes the socket with
// code 1008. A newer connection of the same player replaces the older one, which is closed
// with code 4000 "replaced". Frames are limited to MaxFrame bytes. The client must answer
// WebSocket pings (any WebSocket library does so by default).
const (
	// ProtocolVersion is announced in the hello frame.
	ProtocolVersion = 1
	// MaxFrame is the largest frame either side may send.
	MaxFrame = 1 << 20

	// CloseReplaced is the close code sent to a socket replaced by a newer one.
	CloseReplaced = 4000

	typeHello  = "hello"
	typeCall   = "call"
	typeResult = "result"
)

// helloFrame announces the protocol version.
type helloFrame struct {
	Type     string `json:"type"`
	Protocol int    `json:"protocol"`
}

// callFrame asks the client to run a tool.
type callFrame struct {
	Type   string         `json:"type"`
	ID     string         `json:"id"`
	Tool   string         `json:"tool"`
	Player string         `json:"player"`
	Idem   string         `json:"idem,omitempty"`
	Args   map[string]any `json:"args,omitempty"`
}

// inFrame is any frame the client sends; only result frames are acted on.
type inFrame struct {
	Type    string          `json:"type"`
	ID      string          `json:"id"`
	OK      bool            `json:"ok"`
	Code    string          `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data"`
}

func (f inFrame) result() core.Result {
	r := core.Result{OK: f.OK, Code: f.Code, Message: f.Message, Data: f.Data}
	if !r.OK && r.Code == "" {
		r.Code = "client_error"
	}
	return r
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
	Result *core.Result   `json:"result,omitempty"`
	Error  string         `json:"error,omitempty"` // outcome unknown: the caller fails closed
}
