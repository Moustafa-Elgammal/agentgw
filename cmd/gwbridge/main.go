// Command gwbridge serves MCP over stdio and forwards to a streamable HTTP MCP
// endpoint, so stdio-only desktop clients can use the gateway on localhost.
// Stdout carries the protocol. Everything else goes to stderr.
package main

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/Moustafa-Elgammal/agentgw/internal/bridge"
)

type headerFlags []string

func (h *headerFlags) String() string     { return fmt.Sprint(*h) }
func (h *headerFlags) Set(s string) error { *h = append(*h, s); return nil }

func main() {
	url := flag.String("url", "", "upstream MCP endpoint, for example http://127.0.0.1:8080/mcp (required)")
	var hdrs headerFlags
	flag.Var(&hdrs, "header", "extra upstream request header \"Name: value\" (repeatable)")
	timeout := flag.Duration("timeout", 30*time.Second, "timeout for each upstream HTTP request")
	flag.Parse()
	if *url == "" {
		fmt.Fprintln(os.Stderr, "gwbridge: --url is required")
		flag.Usage()
		os.Exit(2)
	}
	h := http.Header{}
	for _, s := range hdrs {
		n, v, err := bridge.ParseHeader(s)
		if err != nil {
			fmt.Fprintln(os.Stderr, "gwbridge:", err)
			os.Exit(2)
		}
		h.Add(n, v)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	err := bridge.Run(ctx, bridge.Config{
		URL: *url, Headers: h, Transport: &mcp.StdioTransport{}, Log: os.Stderr,
		HTTPClient: &http.Client{Timeout: *timeout},
	})
	if err != nil && ctx.Err() == nil {
		fmt.Fprintln(os.Stderr, "gwbridge:", err)
		os.Exit(1)
	}
}
