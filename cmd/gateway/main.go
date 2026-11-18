// Command gateway runs the M0 development server.
package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/Moustafa-Elgammal/agentgw/internal/audit"
	"github.com/Moustafa-Elgammal/agentgw/internal/connector"
	"github.com/Moustafa-Elgammal/agentgw/internal/gateway"
	"github.com/Moustafa-Elgammal/agentgw/internal/httpapi"
	"github.com/Moustafa-Elgammal/agentgw/internal/manifest"
	"github.com/Moustafa-Elgammal/agentgw/internal/policy"
)

func main() {
	var (
		manifestPath = flag.String("manifest", "", "path to the tool manifest (JSON)")
		backendURL   = flag.String("backend-url", "", "base URL of the game backend")
		keyID        = flag.String("key-id", "dev-1", "signing key id sent to the backend")
		addr         = flag.String("addr", "127.0.0.1:8080", "listen address (keep it on loopback)")
		tenant       = flag.String("tenant", "dev", "tenant id for the dev server")
	)
	flag.Parse()
	log := slog.New(slog.NewJSONHandler(os.Stderr, nil))

	secret := os.Getenv("AGENTGW_SIGNING_SECRET")
	if *manifestPath == "" || *backendURL == "" || secret == "" {
		log.Error("usage: AGENTGW_SIGNING_SECRET=... gateway --manifest m.json --backend-url http://host:port")
		os.Exit(2)
	}
	m, err := manifest.LoadFile(*manifestPath)
	if err != nil {
		log.Error("manifest", "err", err)
		os.Exit(1)
	}

	svc := gateway.New(m,
		policy.NewMemory(policy.DefaultLimits, nil),
		connector.NewHTTP(*backendURL, *keyID, []byte(secret)),
		audit.Slog{L: log})

	srv := &http.Server{
		Addr: *addr, Handler: httpapi.New(svc, *tenant),
		ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 30 * time.Second,
	}
	go func() {
		log.Info("listening", "addr", *addr, "tools", len(m.Tools))
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("server", "err", err)
			os.Exit(1)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = srv.Shutdown(ctx)
}
