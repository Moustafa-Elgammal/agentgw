// Command gateway runs the development server: the dev HTTP API plus the MCP
// endpoint at /mcp. Both trust X-Dev-Player, so keep the listener on loopback.
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

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	"github.com/Moustafa-Elgammal/agentgw/internal/audit"
	"github.com/Moustafa-Elgammal/agentgw/internal/confirm"
	"github.com/Moustafa-Elgammal/agentgw/internal/confirm/pgstore"
	"github.com/Moustafa-Elgammal/agentgw/internal/connector"
	"github.com/Moustafa-Elgammal/agentgw/internal/gateway"
	"github.com/Moustafa-Elgammal/agentgw/internal/httpapi"
	"github.com/Moustafa-Elgammal/agentgw/internal/manifest"
	"github.com/Moustafa-Elgammal/agentgw/internal/mcpserver"
	"github.com/Moustafa-Elgammal/agentgw/internal/policy"
	"github.com/Moustafa-Elgammal/agentgw/internal/policy/redisstore"
)

func main() {
	var (
		manifestPath = flag.String("manifest", "", "path to the tool manifest (.json, or .yaml/.yml)")
		backendURL   = flag.String("backend-url", "", "base URL of the game backend")
		keyID        = flag.String("key-id", "dev-1", "signing key id sent to the backend")
		addr         = flag.String("addr", "127.0.0.1:8080", "listen address (keep it on loopback)")
		tenant       = flag.String("tenant", "dev", "tenant id for the dev server")
		postgresURL  = flag.String("postgres-url", os.Getenv("AGENTGW_POSTGRES_URL"), "Postgres URL for player confirmations (default: in-memory, single process)")
		redisAddr    = flag.String("redis-addr", os.Getenv("AGENTGW_REDIS_ADDR"), "Redis address for the shared policy store (default: in-memory, single process)")
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

	var pol policy.Policy = policy.NewMemory(policy.DefaultLimits, nil)
	if *redisAddr != "" {
		rdb := redis.NewClient(&redis.Options{Addr: *redisAddr})
		defer func() { _ = rdb.Close() }()
		rs := redisstore.New(rdb, "agentgw:", policy.DefaultLimits, nil)
		pingCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		err := rs.Ping(pingCtx)
		cancel()
		if err != nil {
			log.Error("redis", "addr", *redisAddr, "err", err)
			os.Exit(1)
		}
		pol = rs
		log.Info("policy store", "kind", "redis", "addr", *redisAddr)
	} else {
		log.Warn("policy store", "kind", "memory", "note", "limits are per process and reset on restart")
	}

	switches, _ := pol.(policy.Switchboard) // Memory and Redis both implement it
	var confirmations confirm.Store = confirm.NewMemory(nil)
	if *postgresURL != "" {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		pool, err := pgxpool.New(ctx, *postgresURL)
		if err == nil {
			ps := pgstore.New(pool, nil)
			if err = ps.Migrate(ctx); err == nil {
				confirmations = ps
				defer pool.Close()
				log.Info("confirmation store", "kind", "postgres")
			}
		}
		cancel()
		if err != nil {
			log.Error("postgres", "err", err)
			os.Exit(1)
		}
	} else {
		log.Warn("confirmation store", "kind", "memory", "note", "approvals are per process and lost on restart")
	}
	svc := gateway.New(m,
		pol,
		connector.NewHTTP(*backendURL, *keyID, []byte(secret)),
		audit.Slog{L: log},
		gateway.WithConfirmations(confirmations))

	mux := http.NewServeMux()
	mux.Handle("/mcp", mcpserver.Handler(svc, mcpserver.DevAuth(*tenant)))
	mux.Handle("/", httpapi.New(svc, httpapi.Config{Tenant: *tenant, Confirmations: confirmations, Switches: switches}))

	srv := &http.Server{
		Addr: *addr, Handler: mux,
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
