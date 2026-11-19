// Command studiocall plays the studio backend in the tutorials: it sends one request to a
// gateway studio endpoint, signed with agentsig, and prints the answer.
//
//	studiocall --player alice POST /v1/studio/link-tickets
//	studiocall --key-id admin-1 --secret-env AGENTGW_ADMIN_SECRET --player ci \
//	    --body-file manifest.yaml PUT /v1/studio/manifest
//
// The body goes to stdout and the status line (HTTP 201) to stderr. With --expect N the
// command exits 1 when the status is not N, so scripts can assert on it. Every call uses a
// fresh random idempotency key, as the replay rule in docs/PROTOCOL.md requires.
package main

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/Moustafa-Elgammal/agentgw/pkg/agentsig"
)

func main() {
	url := flag.String("url", "http://gateway:8080", "gateway base URL")
	keyID := flag.String("key-id", "dev-1", "signing key id")
	secretEnv := flag.String("secret-env", "AGENTGW_SIGNING_SECRET", "environment variable that holds the key's secret")
	player := flag.String("player", "", "player id (for the admin key: the acting admin)")
	body := flag.String("body", "", "request body")
	bodyFile := flag.String("body-file", "", "read the request body from this file")
	ctype := flag.String("content-type", "", "Content-Type of the body (default application/json when there is a body)")
	expect := flag.Int("expect", 0, "exit 1 unless the status is this")
	flag.Parse()
	if flag.NArg() != 2 || *player == "" {
		fmt.Fprintln(os.Stderr, "usage: studiocall [flags] --player ID METHOD PATH")
		os.Exit(2)
	}
	secret := os.Getenv(*secretEnv)
	if secret == "" {
		fmt.Fprintf(os.Stderr, "studiocall: %s is not set\n", *secretEnv)
		os.Exit(2)
	}
	payload := []byte(*body)
	if *bodyFile != "" {
		b, err := os.ReadFile(*bodyFile) //nolint:gosec // a path the tutorial's reader chose
		if err != nil {
			fmt.Fprintln(os.Stderr, "studiocall:", err)
			os.Exit(2)
		}
		payload = b
	}

	req, err := http.NewRequest(strings.ToUpper(flag.Arg(0)), strings.TrimRight(*url, "/")+flag.Arg(1), bytes.NewReader(payload))
	if err != nil {
		fmt.Fprintln(os.Stderr, "studiocall:", err)
		os.Exit(2)
	}
	if len(payload) > 0 {
		ct := *ctype
		if ct == "" {
			ct = "application/json"
		}
		req.Header.Set("Content-Type", ct)
	}
	idem := make([]byte, 16)
	_, _ = rand.Read(idem)
	agentsig.Sign(req, *keyID, []byte(secret), *player, hex.EncodeToString(idem), payload, time.Now())

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		fmt.Fprintln(os.Stderr, "studiocall:", err)
		os.Exit(1)
	}
	defer func() { _ = resp.Body.Close() }()
	out, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	fmt.Fprintf(os.Stderr, "HTTP %d\n", resp.StatusCode)
	_, _ = os.Stdout.Write(out)
	if len(out) > 0 && out[len(out)-1] != '\n' {
		fmt.Println()
	}
	if *expect != 0 && resp.StatusCode != *expect {
		os.Exit(1)
	}
}
