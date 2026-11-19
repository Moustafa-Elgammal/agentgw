// Command relayclient is a stand-in for a game client. It connects to the gateway's relay
// with a ticket and answers tool calls from a tiny in-memory game, so the relay can be tried
// without Unity. Get a ticket from POST /v1/studio/relay/tickets (signed with the studio key).
//
//	relayclient --url ws://127.0.0.1:8080/v1/relay/connect --ticket "$TICKET"
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"

	"github.com/coder/websocket"
)

type frame struct {
	Type     string         `json:"type"`
	ID       string         `json:"id,omitempty"`
	Tool     string         `json:"tool,omitempty"`
	Args     map[string]any `json:"args,omitempty"`
	Protocol int            `json:"protocol,omitempty"`
}

type result struct {
	Type    string `json:"type"`
	ID      string `json:"id"`
	OK      bool   `json:"ok"`
	Code    string `json:"code,omitempty"`
	Message string `json:"message,omitempty"`
	Data    any    `json:"data,omitempty"`
}

func main() {
	url := flag.String("url", "ws://127.0.0.1:8080/v1/relay/connect", "gateway relay URL")
	ticket := flag.String("ticket", os.Getenv("RELAY_TICKET"), "relay ticket (or RELAY_TICKET)")
	flag.Parse()
	if *ticket == "" {
		log.Fatal("a ticket is required")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	ws, resp, err := dial(ctx, *url, *ticket)
	if err != nil {
		log.Fatalf("connect: %v", err)
	}
	_ = resp.Body.Close()
	defer func() { _ = ws.CloseNow() }()

	farms := 0
	for {
		_, b, err := ws.Read(ctx)
		if err != nil {
			log.Printf("closed: %v", err)
			return
		}
		var f frame
		if json.Unmarshal(b, &f) != nil || f.Type != "call" {
			continue // hello and unknown frames
		}
		r := result{Type: "result", ID: f.ID, OK: true}
		switch f.Tool {
		case "build_farm":
			farms++
			r.Data = map[string]any{"farms": farms}
		case "get_state":
			r.Data = map[string]any{"farms": farms}
		default:
			r.OK, r.Code, r.Message = false, "unknown_tool", fmt.Sprintf("the game has no tool %q", f.Tool)
		}
		out, _ := json.Marshal(r)
		if err := write(ctx, ws, out); err != nil {
			log.Printf("write: %v", err)
			return
		}
		log.Printf("answered %s (%s)", f.Tool, f.ID)
	}
}

func dial(ctx context.Context, url, ticket string) (*websocket.Conn, *http.Response, error) {
	return websocket.Dial(ctx, url, &websocket.DialOptions{HTTPHeader: map[string][]string{"Authorization": {"Bearer " + ticket}}})
}

func write(ctx context.Context, ws *websocket.Conn, b []byte) error {
	return ws.Write(ctx, websocket.MessageText, b)
}
