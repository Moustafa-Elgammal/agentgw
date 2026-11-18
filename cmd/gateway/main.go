// Command gateway runs the gateway: the MCP endpoint at /mcp, protected by OAuth
// bearer tokens from the embedded authorization server. With --dev-auth it instead
// trusts the X-Dev-Player header and also serves the dev HTTP API, so keep that
// listener on loopback.
package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	"github.com/Moustafa-Elgammal/agentgw/internal/audit"
	"github.com/Moustafa-Elgammal/agentgw/internal/auth"
	authpg "github.com/Moustafa-Elgammal/agentgw/internal/auth/pgstore"
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

// pruneLoop removes expired OAuth rows once an hour until ctx ends.
func pruneLoop(ctx context.Context, log *slog.Logger, s interface {
	Prune(context.Context) (int64, error)
}) {
	t := time.NewTicker(time.Hour)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			n, err := s.Prune(ctx)
			if err != nil {
				log.Error("prune auth store", "err", err)
				continue
			}
			log.Info("pruned auth store", "rows", n)
		}
	}
}

func main() {
	var (
		manifestPath = flag.String("manifest", "", "path to the tool manifest (.json, or .yaml/.yml)")
		backendURL   = flag.String("backend-url", "", "base URL of the game backend")
		keyID        = flag.String("key-id", "dev-1", "signing key id sent to the backend")
		addr         = flag.String("addr", "127.0.0.1:8080", "listen address (keep it on loopback)")
		tenant       = flag.String("tenant", "dev", "tenant id for the dev server")
		postgresURL  = flag.String("postgres-url", os.Getenv("AGENTGW_POSTGRES_URL"), "Postgres URL for player confirmations (default: in-memory, single process)")
		publicURL    = flag.String("public-url", "http://127.0.0.1:8080", "public base URL of this gateway: the OAuth issuer, and the MCP resource is <public-url>/mcp")
		devAuth      = flag.Bool("dev-auth", false, "DEV ONLY: trust the X-Dev-Player header and serve the dev HTTP API instead of requiring OAuth tokens")
		studioKeyID  = flag.String("studio-key-id", "dev-1", "key id the studio backend uses to sign link ticket requests (secret: AGENTGW_SIGNING_SECRET)")
		redisAddr    = flag.String("redis-addr", os.Getenv("AGENTGW_REDIS_ADDR"), "Redis address for the shared policy store (default: in-memory, single process)")
	)
	flag.Parse()
	log := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	ctx0, stopAll := context.WithCancel(context.Background()) // ends background loops on shutdown
	defer stopAll()

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
	var authStore auth.Store = auth.NewMemory(nil)
	if *postgresURL != "" {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		pool, err := pgxpool.New(ctx, *postgresURL)
		if err == nil {
			cs, as := pgstore.New(pool, nil), authpg.New(pool, nil)
			if err = cs.Migrate(ctx); err == nil {
				err = as.Migrate(ctx)
			}
			if err == nil {
				confirmations, authStore = cs, as
				defer pool.Close()
				go pruneLoop(ctx0, log, as)
				log.Info("stores", "confirmations", "postgres", "auth", "postgres")
			}
		}
		cancel()
		if err != nil {
			log.Error("postgres", "err", err)
			os.Exit(1)
		}
	} else {
		log.Warn("confirmation and auth stores", "kind", "memory", "note", "approvals, clients and grants are per process and lost on restart")
	}
	svc := gateway.New(m,
		pol,
		connector.NewHTTP(*backendURL, *keyID, []byte(secret)),
		audit.Slog{L: log},
		gateway.WithConfirmations(confirmations))

	mux := http.NewServeMux()
	if *devAuth {
		log.Warn("DEV AUTH ENABLED: identity comes from the X-Dev-Player header and the dev API is mounted. Never expose this listener beyond loopback")
		mux.Handle("/mcp", mcpserver.Handler(svc, mcpserver.DevAuth(*tenant)))
		mux.Handle("/", httpapi.New(svc, httpapi.Config{Tenant: *tenant, Confirmations: confirmations, Switches: switches}))
	} else {
		issuer := strings.TrimRight(*publicURL, "/")
		rs := &auth.ResourceServer{
			Store: authStore, Resource: issuer + "/mcp", Tenant: *tenant,
			MetadataURL: issuer + "/.well-known/oauth-protected-resource/mcp",
		}
		as := auth.NewServer(auth.ServerConfig{
			Store: authStore, Issuer: issuer, Resource: rs.Resource, Tenant: *tenant,
			StudioKeyID: *studioKeyID, StudioSecret: []byte(secret),
		})
		mux.Handle("/mcp", mcpserver.Handler(svc, rs.Authenticate))
		mux.Handle("GET /.well-known/oauth-protected-resource", rs.MetadataHandler(issuer))
		mux.Handle("GET /.well-known/oauth-protected-resource/mcp", rs.MetadataHandler(issuer))
		mux.Handle("/", as.Handler())
		// The game's own UI lists and revokes a player's linked agents through these
		// studio-signed endpoints.
		mux.Handle("/v1/studio/grants", as.StudioHandler())
		mux.Handle("/v1/studio/grants/", as.StudioHandler())
		log.Info("oauth", "issuer", issuer, "resource", rs.Resource)
	}

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
