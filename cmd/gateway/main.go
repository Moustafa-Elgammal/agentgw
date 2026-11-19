// Command gateway runs the gateway: the MCP endpoint at /mcp, protected by OAuth
// bearer tokens from the embedded authorization server. With --dev-auth it instead
// trusts the X-Dev-Player header and also serves the dev HTTP API, so keep that
// listener on loopback.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	"github.com/Moustafa-Elgammal/agentgw/internal/activity"
	"github.com/Moustafa-Elgammal/agentgw/internal/audit"
	auditpg "github.com/Moustafa-Elgammal/agentgw/internal/audit/pgstore"
	"github.com/Moustafa-Elgammal/agentgw/internal/auth"
	authpg "github.com/Moustafa-Elgammal/agentgw/internal/auth/pgstore"
	"github.com/Moustafa-Elgammal/agentgw/internal/bodylimit"
	"github.com/Moustafa-Elgammal/agentgw/internal/confirm"
	"github.com/Moustafa-Elgammal/agentgw/internal/confirm/pgstore"
	"github.com/Moustafa-Elgammal/agentgw/internal/connector"
	"github.com/Moustafa-Elgammal/agentgw/internal/core"
	"github.com/Moustafa-Elgammal/agentgw/internal/gateway"
	"github.com/Moustafa-Elgammal/agentgw/internal/httpapi"
	"github.com/Moustafa-Elgammal/agentgw/internal/manifest"
	"github.com/Moustafa-Elgammal/agentgw/internal/manifeststore"
	mspg "github.com/Moustafa-Elgammal/agentgw/internal/manifeststore/pgstore"
	"github.com/Moustafa-Elgammal/agentgw/internal/mcpserver"
	"github.com/Moustafa-Elgammal/agentgw/internal/obs"
	"github.com/Moustafa-Elgammal/agentgw/internal/policy"
	"github.com/Moustafa-Elgammal/agentgw/internal/policy/redisstore"
	"github.com/Moustafa-Elgammal/agentgw/internal/relay"
	"github.com/Moustafa-Elgammal/agentgw/internal/relay/redisbus"
	"github.com/Moustafa-Elgammal/agentgw/internal/studiosig"
	"github.com/Moustafa-Elgammal/agentgw/internal/switchapi"
)

// auditPruneLoop deletes audit events older than the retention once an hour until ctx ends.
func auditPruneLoop(ctx context.Context, log *slog.Logger, s audit.Store, retention time.Duration) {
	t := time.NewTicker(time.Hour)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			n, err := s.Prune(ctx, time.Now().Add(-retention))
			if err != nil {
				log.Error("prune audit store", "err", err)
				continue
			}
			log.Info("pruned audit store", "rows", n)
		}
	}
}

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

// syncLoop applies the store's active manifest every interval until ctx ends, so a rollback
// on one replica reaches the others.
func syncLoop(ctx context.Context, log *slog.Logger, m *manifeststore.Manager, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			sctx, cancel := context.WithTimeout(ctx, 5*time.Second)
			if err := m.Sync(sctx); err != nil {
				log.Error("manifest sync", "err", err)
			}
			cancel()
		}
	}
}

// newPGPool opens the shared Postgres pool. maxConns and minConns override the URL's
// pool_max_conns and pool_min_conns when above 0; with neither set pgx picks its defaults
// (at most the larger of 4 and the CPU count, none kept idle). The effective settings are
// logged once, because a pool that fills to its cap under load looks like a connection leak
// from the outside.
func newPGPool(ctx context.Context, log *slog.Logger, url string, maxConns, minConns int) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		return nil, err
	}
	source := "driver default"
	if maxConns > 0 {
		cfg.MaxConns, source = int32(min(maxConns, math.MaxInt32)), "flag"
	} else if strings.Contains(url, "pool_max_conns") {
		source = "url"
	}
	if minConns > 0 {
		cfg.MinConns = int32(min(minConns, math.MaxInt32))
	}
	if cfg.MinConns > cfg.MaxConns {
		return nil, fmt.Errorf("--postgres-min-conns (%d) is above the pool maximum (%d)", cfg.MinConns, cfg.MaxConns)
	}
	log.Info("postgres pool", "max_conns", cfg.MaxConns, "min_conns", cfg.MinConns, "max_from", source, "cpus", runtime.NumCPU())
	return pgxpool.NewWithConfig(ctx, cfg)
}

// mountRelay serves the game client's WebSocket and the studio's ticket endpoint when
// AGENTGW_RELAY_SECRET is set. Without it relay tools answer client_offline.
// version labels agentgw_build_info. Release builds set it with -ldflags "-X main.version=...".
var version = "dev"

func mountRelay(log *slog.Logger, mux *http.ServeMux, hub *relay.Hub, tenant string, sv *studiosig.Verifier, ttl time.Duration) {
	rs := os.Getenv("AGENTGW_RELAY_SECRET")
	if rs == "" {
		log.Info("relay", "enabled", false, "note", "set AGENTGW_RELAY_SECRET to serve game clients")
		return
	}
	tickets := relay.NewTickets([]byte(rs), ttl, nil)
	mux.Handle("/v1/relay/connect", relay.Handler(hub, tickets))
	mux.Handle("/v1/studio/relay/tickets", relay.MintHandler(tickets, sv, tenant))
	log.Info("relay", "enabled", true, "ticket_ttl", ttl.String())
}

// reloadKeys loads the key set into v and registers a reload for SIGHUP. The set is the flag key
// plus the file's keys, or only the file's keys when only is set (the flag key is then retired, so
// rotating it needs no restart). A failed reload keeps the keys in use and logs the error, so a
// typo in the file never locks the studio out. Each load logs the key ids added and removed, never
// secrets, and counts in agentgw_key_reloads_total. It reports whether the first load worked.
func reloadKeys(log *slog.Logger, v *studiosig.Verifier, kind, id string, secret []byte, file string, only bool, reloads *obs.KeyReloads, reloaders *[]func()) bool {
	prev := map[string]struct{}{}
	load := func() error {
		keys := map[string][]byte{}
		if !only && id != "" && len(secret) > 0 {
			keys[id] = secret
		}
		if file != "" {
			fk, err := studiosig.LoadKeys(file)
			if err != nil {
				return err
			}
			for k, sec := range fk {
				keys[k] = sec
			}
		}
		if len(keys) == 0 {
			return errors.New("no keys to accept")
		}
		v.SetKeys(keys)
		next := make(map[string]struct{}, len(keys))
		for k := range keys {
			next[k] = struct{}{}
		}
		added, removed := studiosig.DiffIDs(prev, next)
		prev = next
		log.Info(kind+" keys loaded", "file", file, "count", len(keys), "added", added, "removed", removed, "primary_retired", only) // ids only, never secrets
		return nil
	}
	if err := load(); err != nil {
		log.Error("keys", "kind", kind, "file", file, "err", err)
		reloads.Inc(kind, "error")
		return false
	}
	reloads.Inc(kind, "ok")
	*reloaders = append(*reloaders, func() {
		if err := load(); err != nil {
			log.Error("key reload failed, keeping current keys", "kind", kind, "file", file, "err", err)
			reloads.Inc(kind, "error")
			return
		}
		reloads.Inc(kind, "ok")
	})
	return true
}

// reloadBackendKey loads the key that signs calls to the backend from file, now and on SIGHUP.
// A bad file keeps the key in use. It reports whether the first load worked.
func reloadBackendKey(log *slog.Logger, c *connector.HTTP, file string, reloads *obs.KeyReloads, reloaders *[]func()) bool {
	load := func() error {
		kp, err := studiosig.LoadKeyPair(file)
		if err != nil {
			return err
		}
		prev := c.CurrentKeyID()
		if err := c.SetKey(kp.ID, kp.Secret); err != nil {
			return err
		}
		log.Info("backend signing key loaded", "file", file, "key_id", kp.ID, "previous_key_id", prev) // ids only
		return nil
	}
	if err := load(); err != nil {
		log.Error("backend key", "file", file, "err", err)
		reloads.Inc("backend", "error")
		return false
	}
	reloads.Inc("backend", "ok")
	*reloaders = append(*reloaders, func() {
		if err := load(); err != nil {
			log.Error("backend key reload failed, keeping current key", "file", file, "err", err)
			reloads.Inc("backend", "error")
			return
		}
		reloads.Inc("backend", "ok")
	})
	return true
}

func main() {
	var (
		manifestPath  = flag.String("manifest", "", "path to the tool manifest (YAML for .yaml or .yml, JSON for anything else)")
		backendURL    = flag.String("backend-url", "", "base URL of the game backend")
		allowPrivate  = flag.Bool("backend-allow-private", false, "let the gateway call a backend on a loopback or private address (compose and cluster networks). Off by default: the connector refuses those addresses")
		keyID         = flag.String("key-id", "dev-1", "signing key id sent to the backend")
		addr          = flag.String("addr", "127.0.0.1:8080", "listen address (keep it on loopback)")
		tenant        = flag.String("tenant", "dev", "tenant id for the dev server")
		postgresURL   = flag.String("postgres-url", os.Getenv("AGENTGW_POSTGRES_URL"), "Postgres URL for player confirmations (default: in-memory, single process)")
		pgMaxConns    = flag.Int("postgres-max-conns", 0, "most Postgres connections the pool opens (0 keeps the driver default, the larger of 4 and the CPU count; a pool_max_conns in the URL is used when this is 0)")
		pgMinConns    = flag.Int("postgres-min-conns", 0, "Postgres connections kept open when idle (0 keeps the driver default of none; a pool_min_conns in the URL is used when this is 0)")
		publicURL     = flag.String("public-url", "http://127.0.0.1:8080", "public base URL of this gateway: the OAuth issuer, and the MCP resource is <public-url>/mcp")
		devAuth       = flag.Bool("dev-auth", false, "DEV ONLY: trust the X-Dev-Player header and serve the dev HTTP API instead of requiring OAuth tokens")
		studioKeyID   = flag.String("studio-key-id", "dev-1", "key id the studio backend uses to sign link ticket requests (secret: AGENTGW_SIGNING_SECRET)")
		registerHour  = flag.Int("register-per-hour", 10, "client registrations allowed per hour per client address (negative disables)")
		maxClients    = flag.Int("max-clients", 10000, "most OAuth clients this process will register (negative disables)")
		clientIPHdr   = flag.String("client-ip-header", "", "trusted header carrying the client address, for example X-Forwarded-For. Set it ONLY behind a proxy that overwrites it, or clients can evade rate limits")
		studioKeys    = flag.String("studio-keys-file", "", "JSON file {\"key-id\":\"secret\"} of extra studio keys, for rotation. Reloaded on SIGHUP. Added to --studio-key-id")
		auditKeep     = flag.Duration("audit-retention", 720*time.Hour, "how long audit events are kept for the activity feed (0 keeps them forever)")
		auditQueue    = flag.Int("audit-queue", 0, "queue audit events and write them in batches (needs --postgres-url). The value is the queue size in events; 0 writes each event before the call returns. A crash can lose queued events")
		auditBatch    = flag.Int("audit-batch-size", 500, "most audit events in one batch write (with --audit-queue)")
		auditFlush    = flag.Duration("audit-flush-interval", 200*time.Millisecond, "longest an audit event waits for its batch (with --audit-queue)")
		auditOverflow = flag.String("audit-overflow", "drop", "when the audit queue is full: drop the event and count it, or block until the call's deadline (with --audit-queue)")
		studioOnly    = flag.Bool("studio-keys-only", false, "accept only the keys in --studio-keys-file; the key from --studio-key-id and AGENTGW_SIGNING_SECRET is retired, so it can be rotated with SIGHUP and no restart")
		adminOnly     = flag.Bool("admin-keys-only", false, "accept only the keys in --admin-keys-file; the key from --admin-key-id and AGENTGW_ADMIN_SECRET is retired")
		backendKey    = flag.String("backend-key-file", "", "JSON file {\"id\":\"...\",\"secret\":\"...\"} with the key that signs calls to the backend. Reloaded on SIGHUP. Replaces --key-id and AGENTGW_SIGNING_SECRET for backend calls")
		adminKeys     = flag.String("admin-keys-file", "", "JSON file {\"key-id\":\"secret\"} of admin keys, for rotation. Reloaded on SIGHUP. Added to --admin-key-id")
		adminKeyID    = flag.String("admin-key-id", "admin-1", "key id the studio uses to sign manifest calls (secret: AGENTGW_ADMIN_SECRET, must differ from the player key)")
		manifestPoll  = flag.Duration("manifest-poll", 15*time.Second, "how often to pick up a manifest version activated elsewhere (0 disables)")
		relayTTL      = flag.Duration("relay-ticket-ttl", 60*time.Second, "how long a relay ticket stays valid (secret: AGENTGW_RELAY_SECRET; unset disables the relay)")
		relayInFlight = flag.Int("relay-max-in-flight", 8, "calls that may wait on one player's game client at once; more get the denied result client_busy")
		relayFrames   = flag.Int("relay-max-frames-per-sec", 100, "frames per second one game client may send before its socket is closed (burst is twice this)")
		opsAddr       = flag.String("ops-addr", "127.0.0.1:9090", "listen address for /healthz, /readyz and /metrics. Keep it off public networks (empty disables)")
		maxScope      = flag.String("max-scope", os.Getenv("AGENTGW_MAX_SCOPE"), "the most any player may grant: coach (read only, recommended for a first pilot) or operator. Empty means operator")
		oidcIssuer    = flag.String("oidc-issuer", "", "turn on sign in with your identity provider at the consent page: its OIDC issuer URL. Needs --oidc-client-id and the secrets AGENTGW_OIDC_CLIENT_SECRET and AGENTGW_OIDC_SESSION_SECRET (not with --dev-auth)")
		oidcClientID  = flag.String("oidc-client-id", "", "OIDC client id registered at the identity provider, with the redirect URI <public-url>/authorize/oidc/callback")
		oidcClaim     = flag.String("oidc-player-claim", "sub", "ID token claim that carries the player id your game uses. It must be stable for a person for ever: not an email address or a display name")
		oidcScopes    = flag.String("oidc-scopes", "openid", "scopes requested from the identity provider, space or comma separated (openid is always included)")
		oidcAlgs      = flag.String("oidc-algs", "RS256,ES256", "ID token signature algorithms accepted. Asymmetric only: none and the HS family are refused")
		oidcHTTP      = flag.Bool("oidc-allow-insecure-http", false, "DEV ONLY: allow an http issuer on a loopback address, for a local identity provider")
		maxBody       = flag.Int64("max-body", 1048576, "largest request body in bytes. OAuth and studio endpoints keep their lower caps, manifest upload its own")
		redisAddr     = flag.String("redis-addr", os.Getenv("AGENTGW_REDIS_ADDR"), "Redis address for the shared policy store (default: in-memory, single process)")
	)
	showVersion := flag.Bool("version", false, "print the version and exit")
	flag.Parse()
	if *showVersion {
		fmt.Println(version)
		return
	}
	var reloaders []func()
	log := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	ctx0, stopAll := context.WithCancel(context.Background()) // ends background loops on shutdown
	defer stopAll()

	secret := os.Getenv("AGENTGW_SIGNING_SECRET")
	needSecret := !*studioOnly || *backendKey == "" // file keys can replace it entirely
	if *manifestPath == "" || *backendURL == "" || (needSecret && secret == "") {
		log.Error("usage: AGENTGW_SIGNING_SECRET=... gateway --manifest m.json --backend-url http://host:port")
		os.Exit(2)
	}
	if *studioOnly && *studioKeys == "" {
		log.Error("--studio-keys-only needs --studio-keys-file")
		os.Exit(2)
	}
	if *adminOnly && *adminKeys == "" {
		log.Error("--admin-keys-only needs --admin-keys-file")
		os.Exit(2)
	}
	scopeCap := core.Scope(*maxScope)
	if scopeCap == "" {
		scopeCap = core.ScopeOperator
	}
	if !scopeCap.Valid() {
		log.Error("usage: --max-scope must be coach or operator", "got", *maxScope)
		os.Exit(2)
	}
	if scopeCap == core.ScopeCoach {
		log.Info("max-scope is coach", "note", "every grant is read only, whatever the player chose")
	}
	m, err := manifest.LoadFile(*manifestPath)
	if err != nil {
		log.Error("manifest", "err", err)
		os.Exit(1)
	}

	var pol policy.Policy = policy.NewMemory(policy.DefaultLimits, nil)
	var relayBus relay.Bus = relay.NewMemory(nil)
	metrics := obs.NewMetrics(version)
	obs.RegisterRuntime(metrics.Reg)
	var ready []obs.Check
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
		relayBus = redisbus.New(rdb, "agentgw:relay:")
		obs.RegisterRedisPool(metrics.Reg, func() obs.RedisPoolStats {
			s := rdb.PoolStats()
			return obs.RedisPoolStats{Total: s.TotalConns, Idle: s.IdleConns, Hits: s.Hits, Misses: s.Misses, Timeouts: s.Timeouts}
		})
		ready = append(ready, obs.Check{Name: "redis", Fn: func(ctx context.Context) error { return rdb.Ping(ctx).Err() }})
		log.Info("policy store", "kind", "redis", "addr", *redisAddr)
	} else {
		log.Warn("policy store", "kind", "memory", "note", "limits are per process and reset on restart")
	}

	switches, _ := pol.(policy.Switchboard) // Memory and Redis both implement it
	if *auditOverflow != "drop" && *auditOverflow != "block" {
		log.Error("--audit-overflow must be drop or block", "got", *auditOverflow)
		os.Exit(2)
	}
	if *auditQueue > 0 && *postgresURL == "" {
		log.Warn("--audit-queue is ignored without --postgres-url", "note", "the in-memory audit store has nothing to batch")
	}
	var confirmations confirm.Store = confirm.NewMemory(nil)
	var authStore auth.Store = auth.NewMemory(nil)
	var manifests manifeststore.Store = manifeststore.NewMemory(nil)
	var auditStore audit.Store = &audit.Ring{}
	if *postgresURL != "" {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		pool, err := newPGPool(ctx, log, *postgresURL, *pgMaxConns, *pgMinConns)
		if err == nil {
			obs.RegisterPgxPool(metrics.Reg, func() obs.PoolStats {
				s := pool.Stat()
				return obs.PoolStats{
					Acquired: s.AcquiredConns(), Idle: s.IdleConns(), Total: s.TotalConns(), Max: s.MaxConns(),
					AcquireCount: s.AcquireCount(), EmptyAcquireCount: s.EmptyAcquireCount(),
					CanceledAcquireCount: s.CanceledAcquireCount(), AcquireDuration: s.AcquireDuration(),
				}
			})
			cs, as := pgstore.New(pool, nil), authpg.New(pool, nil)
			if err = cs.Migrate(ctx); err == nil {
				err = as.Migrate(ctx)
			}
			ms := mspg.New(pool, nil)
			if err == nil {
				err = ms.Migrate(ctx)
			}
			aus := auditpg.New(pool)
			aus.OnError = func(err error) { log.Error("audit store write failed", "err", err) }
			if err == nil {
				err = aus.Migrate(ctx)
			}
			if err == nil {
				confirmations, authStore, manifests, auditStore = cs, as, ms, aus
				defer pool.Close()
				if *auditQueue > 0 {
					var ab *audit.Batcher
					overflow := audit.Drop
					if *auditOverflow == "block" {
						overflow = audit.Block
					}
					ab, err = audit.NewBatcher(aus, audit.BatcherConfig{
						Queue: *auditQueue, BatchSize: *auditBatch, FlushInterval: *auditFlush, Overflow: overflow,
						OnError: func(err error) { log.Error("audit batch write failed", "err", err) },
						Observer: obs.NewAuditMetrics(metrics.Reg, func() int {
							if ab == nil {
								return 0
							}
							return ab.Depth()
						}),
					})
					if err == nil {
						auditStore = ab
						// Deferred after pool.Close, so it runs before it: queued events are written first.
						defer func() {
							dctx, dcancel := context.WithTimeout(context.Background(), 10*time.Second)
							defer dcancel()
							if cerr := ab.Close(dctx); cerr != nil {
								log.Error("audit queue did not drain", "err", cerr, "queued", ab.Depth())
							}
						}()
						log.Info("audit batching", "queue", *auditQueue, "batch", *auditBatch, "flush", auditFlush.String(), "overflow", *auditOverflow)
					}
				}
				ready = append(ready, obs.Check{Name: "postgres", Fn: pool.Ping})
				go pruneLoop(ctx0, log, as)
				log.Info("stores", "confirmations", "postgres", "auth", "postgres", "manifests", "postgres", "audit", "postgres")
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
	relayHub := relay.NewHub(relayBus, relay.WithObserver(metrics), relay.WithExtObserver(obs.NewRelayExt(metrics.Reg)),
		relay.WithMaxInFlight(*relayInFlight), relay.WithMaxFramesPerSecond(*relayFrames))
	if err := relayHub.Start(ctx0); err != nil {
		log.Error("relay", "err", err)
		os.Exit(1)
	}
	var connOpts []connector.Option
	if *allowPrivate {
		connOpts = append(connOpts, connector.AllowPrivate())
		log.Warn("backend-allow-private is on", "note", "the gateway may call loopback and private addresses; use it only for a trusted backend host")
	}
	keyReloads := obs.NewKeyReloads(metrics.Reg)
	backendConn := connector.NewHTTP(*backendURL, *keyID, []byte(secret), connOpts...)
	if *backendKey != "" && !reloadBackendKey(log, backendConn, *backendKey, keyReloads, &reloaders) {
		os.Exit(1)
	}
	svc := gateway.New(m,
		pol,
		connector.Mux{HTTP: backendConn, Relay: relay.NewConnector(relayHub)},
		audit.Multi{audit.Slog{L: log}, auditStore},
		gateway.WithConfirmations(confirmations),
		gateway.WithObserver(metrics),
		gateway.WithMaxScope(scopeCap))
	ready = append(ready, obs.Check{Name: "manifest", Fn: func(context.Context) error {
		if len(svc.Tools()) == 0 {
			return errors.New("no tools loaded")
		}
		return nil
	}})

	if *auditKeep > 0 {
		go auditPruneLoop(ctx0, log, auditStore, *auditKeep)
	}

	mgr := manifeststore.NewManager(manifests, *tenant, svc)
	seedFmt := manifeststore.JSON
	if ext := strings.ToLower(filepath.Ext(*manifestPath)); ext == ".yaml" || ext == ".yml" {
		seedFmt = manifeststore.YAML
	}
	seed, err := os.ReadFile(*manifestPath)
	if err != nil {
		log.Error("manifest", "err", err)
		os.Exit(1)
	}
	bctx, bcancel := context.WithTimeout(context.Background(), 10*time.Second)
	av, seeded, err := mgr.Bootstrap(bctx, "bootstrap", seed, seedFmt)
	bcancel()
	if err != nil {
		log.Error("manifest bootstrap", "err", err)
		os.Exit(1)
	}
	log.Info("manifest", "version", av.ID, "sha256", av.SHA256, "seeded_from_file", seeded)
	if *manifestPoll > 0 {
		go syncLoop(ctx0, log, mgr, *manifestPoll)
	}

	mux := http.NewServeMux()
	if *devAuth {
		if *oidcIssuer != "" {
			log.Error("--oidc-issuer needs OAuth mode: remove --dev-auth, which has no consent page")
			os.Exit(1)
		}
		log.Warn("DEV AUTH ENABLED: identity comes from the X-Dev-Player header and the dev API is mounted. Never expose this listener beyond loopback")
		mux.Handle("/mcp", mcpserver.Handler(svc, mcpserver.DevAuth(*tenant)))
		mux.Handle("/", httpapi.New(svc, httpapi.Config{Tenant: *tenant, Confirmations: confirmations, Switches: switches}))
	} else {
		issuer := strings.TrimRight(*publicURL, "/")
		rs := &auth.ResourceServer{
			Store: authStore, Resource: issuer + "/mcp", Tenant: *tenant,
			MetadataURL: issuer + "/.well-known/oauth-protected-resource/mcp",
			MaxScope:    scopeCap,
		}
		// One replay cache for every studio-signed endpoint, so a captured request cannot be
		// replayed against a different endpoint either.
		sv := studiosig.New(studiosig.Config{KeyID: *studioKeyID, Secret: []byte(secret)})
		if !reloadKeys(log, sv, "studio", *studioKeyID, []byte(secret), *studioKeys, *studioOnly, keyReloads, &reloaders) {
			os.Exit(1)
		}
		oidcLogin, err := buildOIDC(ctx0, log, oidcOptions{Issuer: *oidcIssuer, ClientID: *oidcClientID, PlayerClaim: *oidcClaim, Scopes: *oidcScopes, Algs: *oidcAlgs, AllowHTTP: *oidcHTTP}, *publicURL)
		if err != nil {
			log.Error("oidc", "err", err)
			os.Exit(1)
		}
		as := auth.NewServer(auth.ServerConfig{
			OIDC:  oidcLogin,
			Log:   log,
			Store: authStore, Issuer: issuer, Resource: rs.Resource, Tenant: *tenant, MaxScope: scopeCap,
			StudioKeyID: *studioKeyID, StudioSecret: []byte(secret), Verifier: sv,
			RegisterPerHour: *registerHour, MaxClients: *maxClients, ClientIPHeader: *clientIPHdr,
		})
		mux.Handle("/mcp", mcpserver.Handler(svc, rs.Authenticate))
		mux.Handle("GET /.well-known/oauth-protected-resource", rs.MetadataHandler(issuer))
		mux.Handle("GET /.well-known/oauth-protected-resource/mcp", rs.MetadataHandler(issuer))
		mux.Handle("/", as.Handler())
		// The game's own UI lists and revokes a player's linked agents through these
		// studio-signed endpoints.
		mux.Handle("/v1/studio/grants", as.StudioHandler())
		mux.Handle("/v1/studio/grants/", as.StudioHandler())
		// Same for approving or denying a confirmation: without --dev-auth this is the only
		// player path, and it is signed by the studio, never reachable with an agent's token.
		studio := confirm.StudioHandler(confirm.StudioConfig{Store: confirmations, Tenant: *tenant, KeyID: *studioKeyID, Secret: []byte(secret), Verifier: sv})
		mux.Handle("/v1/studio/confirmations", studio)
		mux.Handle("/v1/studio/confirmations/", studio)
		// The player's own activity feed: what their agents did, signed by the studio.
		mux.Handle(activity.PlayerPath, activity.PlayerHandler(auditStore, *tenant, sv))
		mountRelay(log, mux, relayHub, *tenant, sv, *relayTTL)
		if adminSecret := os.Getenv("AGENTGW_ADMIN_SECRET"); adminSecret != "" || *adminKeys != "" {
			av := studiosig.New(studiosig.Config{MaxBody: manifeststore.MaxUpload})
			if switchapi.MaxBody > manifeststore.MaxUpload {
				panic("switchapi.MaxBody must not exceed the admin verifier body limit")
			}
			if !reloadKeys(log, av, "admin", *adminKeyID, []byte(adminSecret), *adminKeys, *adminOnly, keyReloads, &reloaders) {
				os.Exit(1)
			}
			mh := manifeststore.Handler(mgr, av)
			mux.Handle("/v1/studio/manifest", mh)
			mux.Handle("/v1/studio/manifest/", mh)
			mux.Handle(activity.AdminPath, activity.AdminHandler(auditStore, *tenant, av))
			mux.Handle(switchapi.Path, switchapi.Handler(switches, av, *tenant, audit.Multi{audit.Slog{L: log}, auditStore}))
		} else {
			log.Info("manifest endpoints not mounted", "reason", "neither AGENTGW_ADMIN_SECRET nor --admin-keys-file is set")
		}
		log.Info("oauth", "issuer", issuer, "resource", rs.Resource)
	}

	srv := &http.Server{
		Addr: *addr, Handler: metrics.Middleware(bodylimit.FrontDoor(mux, *maxBody)),
		// WriteTimeout is safe here: /mcp answers with plain JSON (no streaming) and the relay
		// WebSocket is hijacked, which clears the connection deadlines. IdleTimeout frees
		// keep-alive connections that clients forgot.
		ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 30 * time.Second,
		IdleTimeout: 120 * time.Second,
	}
	if *opsAddr != "" {
		ops := &http.Server{
			Addr: *opsAddr, Handler: obs.Ops(metrics.Reg, ready...),
			ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 5 * time.Second, WriteTimeout: 10 * time.Second,
		}
		defer func() { _ = ops.Close() }()
		go func() {
			log.Info("ops listening", "addr", *opsAddr)
			if err := ops.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				log.Error("ops server", "err", err)
				os.Exit(1)
			}
		}()
	}
	go func() {
		log.Info("listening", "addr", *addr, "tools", len(m.Tools))
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("server", "err", err)
			os.Exit(1)
		}
	}()

	stop := make(chan os.Signal, 1)
	hup := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	signal.Notify(hup, syscall.SIGHUP)
	go func() {
		for range hup {
			for _, r := range reloaders {
				r()
			}
		}
	}()
	<-stop
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = srv.Shutdown(ctx)
}
