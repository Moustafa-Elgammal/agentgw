# Runbook

How to run, watch and fix agentgw. Everything here matches the code in `cmd/gateway` and the
design docs. For what the endpoints accept, see [PROTOCOL.md](PROTOCOL.md); for why, see
[SECURITY-MODEL.md](SECURITY-MODEL.md).

## Moving parts

| Part | Needed for | If it is missing |
|---|---|---|
| Gateway (this binary) | everything | no tools for any agent |
| Redis (`--redis-addr`) | shared rate limits, caps, cooldowns, kill switches, relay routing across replicas | each replica keeps its own limits in memory; the relay only works within one replica |
| Postgres (`--postgres-url`) | player confirmations, OAuth clients and grants, manifest versions, audit feed | the same stores live in memory, per process, lost on restart |
| Game backend (`--backend-url`) | REST tools | those calls fail with `backend_unavailable` |
| Game clients (relay) | tools with `backend: relay` | calls answer `client_offline` |

Run more than one replica only with both Redis and Postgres configured.

## Start and stop

Docker Compose (demo, dev auth, loopback only): `docker compose up --build`, stop with
`docker compose down`.

Kubernetes: `deploy/helm/agentgw` (see [Deploy with Helm](#deploy-with-helm)).

The gateway exits on SIGINT or SIGTERM after giving in-flight HTTP requests 10 seconds. Open
relay sockets are closed with the process; game clients must reconnect with a fresh ticket.
On start it creates its Postgres tables if they are missing (idempotent), seeds the manifest
store from `--manifest` only if no version is active, and logs `manifest` with the active
version.

## Configuration

Flags (all in `cmd/gateway/main.go`; `gateway --help` prints them):

| Flag | Default | Meaning |
|---|---|---|
| `--manifest` | required | seed manifest file (`.json`, `.yaml`, `.yml`). Used only while the store has no active version |
| `--backend-url` | required | game backend base URL |
| `--backend-allow-private` | false | let the connector call loopback and private addresses. The default refuses them, so an in-cluster backend needs this on |
| `--key-id` | `dev-1` | key id the gateway sends when it signs backend calls |
| `--addr` | `127.0.0.1:8080` | public listener. Use `0.0.0.0:8080` in a container |
| `--ops-addr` | `127.0.0.1:9090` | `/healthz`, `/readyz`, `/metrics`. Never expose it publicly. Empty disables it |
| `--postgres-max-conns` | driver default | Most Postgres connections the shared pool opens. 0 keeps the pgx default, the larger of 4 and the CPU count, or the URL's `pool_max_conns`. The effective value is logged at start (`postgres pool`) and exposed as `agentgw_pgx_pool_max_conns` |
| `--postgres-min-conns` | 0 | Connections the pool keeps open when idle (0 keeps none) |
| `--public-url` | `http://127.0.0.1:8080` | OAuth issuer; the MCP resource is `<public-url>/mcp` |
| `--tenant` | `dev` | tenant id |
| `--dev-auth` | false | DEV ONLY. Trusts `X-Dev-Player`. Never on a reachable network |
| `--studio-key-id` | `dev-1` | studio key id (player calls, grants, confirmations, activity, relay tickets) |
| `--admin-key-id` | `admin-1` | admin key id (manifest endpoints, admin feed). Use a different secret than the studio key |
| `--studio-keys-file`, `--admin-keys-file` | none | extra keys for rotation, reloaded on SIGHUP. With `--studio-keys-only` or `--admin-keys-only` they are the only keys, and the key from the flag and environment is retired |
| `--backend-key-file` | none | `{"id","secret"}` of the key that signs calls to the backend, reloaded on SIGHUP |
| `--redis-addr` | `$AGENTGW_REDIS_ADDR` | Redis address |
| `--postgres-url` | `$AGENTGW_POSTGRES_URL` | Postgres URL |
| `--audit-retention` | `720h` | audit events older than this are deleted hourly. `0` keeps them forever |
| `--audit-queue`, `--audit-batch-size`, `--audit-flush-interval`, `--audit-overflow` | `0`, `500`, `200ms`, `drop` | batch audit writes (Postgres only). `--audit-queue` is the queue size in events, 0 writes each event before the call returns. See "Audit write path" below |
| `--manifest-poll` | `15s` | how often a replica picks up a manifest activated elsewhere. `0` disables |
| `--relay-ticket-ttl` | `60s` | relay ticket lifetime |
| `--register-per-hour`, `--max-clients` | `10`, `10000` | OAuth client registration limits (per process) |
| `--client-ip-header` | none | trusted proxy header with the client address. Set it only behind a proxy that overwrites it |

Secrets are environment variables, never flags:

| Variable | Required | Effect |
|---|---|---|
| `AGENTGW_SIGNING_SECRET` | yes | secret for the studio key and for backend signing |
| `AGENTGW_ADMIN_SECRET` | no | turns on the manifest endpoints and the admin feed (with `--admin-keys-file`) |
| `AGENTGW_RELAY_SECRET` | no | turns on the relay (ticket minting and `/v1/relay/connect`) |
| `AGENTGW_POSTGRES_URL`, `AGENTGW_REDIS_ADDR` | no | same as the flags |
| `AGENTGW_OIDC_CLIENT_SECRET`, `AGENTGW_OIDC_SESSION_SECRET` | with `--oidc-issuer` | the client secret registered at the identity provider, and a key of at least 32 bytes that seals the sign-in cookie. See [Sign in with your identity provider](#sign-in-with-your-identity-provider-oidc) |

Logs are JSON on stderr. Secrets, key ids with secrets, tokens and tool arguments are never
logged; audit stores a hash of arguments.

## Deploy with Helm

```bash
kubectl create secret generic agentgw-credentials \
  --from-literal=signing-secret="$(openssl rand -hex 32)" \
  --from-literal=admin-secret="$(openssl rand -hex 32)" \
  --from-literal=relay-secret="$(openssl rand -hex 32)" \
  --from-literal=postgres-url='postgres://user:pass@host:5432/agentgw?sslmode=require'
helm install agentgw deploy/helm/agentgw \
  --set existingSecret=agentgw-credentials \
  --set-file manifest=examples/city/manifest.yaml \
  --set config.publicURL=https://agentgw.example.com \
  --set config.backendURL=http://game-backend.games.svc:8080 \
  --set config.backendAllowPrivate=true \
  --set config.redisAddr=redis-master.cache.svc:6379
```

The seed manifest is written to a ConfigMap as `manifest.yaml`. The gateway reads the format from
the file name, so for a JSON manifest also pass `--set manifestFile=manifest.json`.

The chart never takes secrets inline. Probes use the ops port (`/healthz` for liveness and
startup, `/readyz` for readiness). The ops port is on a separate `-ops` Service for scraping and
is not on the public Service or the Ingress. Turn on `serviceMonitor.enabled`,
`networkPolicy.enabled` and `autoscaling.enabled` as needed; see `values.yaml`.

Check the chart without installing it, no host tools needed:

```bash
make helm-check      # helm lint and helm template for the default and the all-options values
make deploy-check    # helm-check plus promtool on the alert rules and a parse of the dashboard
```

The dashboard is `deploy/grafana/agentgw.json` (import it in Grafana) and the alert rules are
`deploy/prometheus/alerts.yaml`.

## Health and readiness

On the ops port:

- `GET /healthz` is 200 while the process serves.
- `GET /readyz` is 200 only when every configured dependency answers within 2 seconds, else 503
  with `{"status":"unready","failing":[{"check":"redis","error":"..."}]}`. Checks: `redis` and
  `postgres` when configured, and `manifest` (an active manifest is loaded).
- `GET /metrics` is Prometheus text.

| Metric | Labels | Meaning |
|---|---|---|
| `gateway_calls_total` | `tool`, `outcome` | calls; `outcome` is `ok` or the denial code |
| `gateway_call_duration_seconds` | `tool` | histogram of whole-call latency |
| `relay_connections` | none | sockets held by this replica |
| `relay_calls_total` | `result` | `ok`, `denied`, `offline`, `busy`, `error` |
| `relay_busy_total` | none | calls refused because a player's socket was at its in-flight limit |
| `relay_socket_closes_total` | `reason` | `bye`, `flood`, `malformed`, `unsupported`, `ping`, `replaced`, `disconnected` |
| `http_requests_total` | `route`, `code` | public listener requests by route pattern |
| `agentgw_build_info` | `version` | always 1 |

"Failure" outcomes mean infrastructure trouble, not a refused player: `backend_unavailable`,
`outcome_unknown` (the call was cut off and may or may not have happened), `policy_unavailable`
and `confirmation_unavailable`. Everything else that is not `ok` is a normal denial (rate limit,
cap, cooldown, validation, scope, kill switch).

## Connection pools and runtime metrics

`/metrics` also carries the numbers that explain slow or leaking behavior from the inside. They are read at scrape time, so a value is never older than the scrape.

| Metric | Meaning |
|---|---|
| `agentgw_pgx_pool_acquired_conns`, `_idle_conns`, `_total_conns`, `_max_conns` | Postgres pool: in use, idle, open, and the cap |
| `agentgw_pgx_pool_acquire_total`, `_empty_acquire_total`, `_canceled_acquire_total` | acquires, those that had to wait, those abandoned |
| `agentgw_pgx_pool_acquire_duration_seconds_total` | time spent acquiring; its rate is seconds of waiting per second |
| `agentgw_redis_pool_total_conns`, `_idle_conns`, `_hits_total`, `_misses_total`, `_timeouts_total` | Redis pool; rising timeouts mean it is too small |
| `go_goroutines`, `go_memstats_heap_alloc_bytes`, `go_memstats_heap_inuse_bytes`, `go_memstats_sys_bytes` | Go runtime memory and goroutines |
| `go_gc_cycles_total`, `go_gc_pause_seconds_total` | garbage collection |
| `process_open_fds` | open file descriptors (Linux) |

The Postgres pool opens connections as concurrency needs them, up to its cap, and keeps them. So
the number of Postgres connections climbs under the first minutes of load and then stays level.
That is a pool filling up, not a leak. A leak looks like `agentgw_pgx_pool_total_conns` above
`agentgw_pgx_pool_max_conns`, which cannot happen, or connections from the database's side
(`pg_stat_activity`) that keep rising past the sum of all replicas' caps.

## Pilot rollout: coach first

Start a pilot read only, then raise the cap when it has been quiet.

1. Run with `--max-scope=coach` (or `AGENTGW_MAX_SCOPE=coach`; Helm: `config.maxScope: coach`).
   Every agent is read only, whatever the player chose or an older grant says. The consent page
   offers only Coach, a client that asks for `scope=operator` is refused with `invalid_scope`,
   tokens and refreshes report `coach`, and action tools are not listed. A call that names one
   anyway is `scope_insufficient`.
2. Watch the activity feed and `gateway_calls_total` for a few days. Read tools should show up
   as `ok`, and there should be no `scope_insufficient` you did not expect.
3. Move to operator by restarting with `--max-scope=operator` (the default). Stored operator
   grants are back in force as they were: the cap never rewrites a grant a player made earlier.
   Players who linked while the cap was coach hold coach grants and need to link again to give
   operator.

The cap applies to every front door, including `--dev-auth`.

## Request limits

Request bodies are capped at the front door: 1 MiB by default (`--max-body`), 64 KiB for the OAuth
endpoints and the studio API, and the manifest upload keeps its own 1 MiB. An oversized body gets
`413 {"error":"too_large"}` before any handler reads it. The server also closes keep-alive
connections after 120 seconds idle.

## Symptoms and actions

| Symptom or alert | Likely cause | Do this |
|---|---|---|
| `AgentgwPolicyStoreDown`, many `policy_unavailable` | Redis down or unreachable | Restore Redis. Calls fail closed meanwhile and nothing is lost. Limits and cooldowns live in Redis, so a Redis that lost its data starts everyone with fresh budgets |
| `confirmation_unavailable` | Postgres down, for tools with `confirm: true` | Restore Postgres. Those calls are refused with "nothing was changed"; other tools still work |
| `AgentgwBackendUnavailable`, `backend_unavailable` | backend down, refusing signatures, or the URL is private and `--backend-allow-private` is off | Check the backend and its clock (signatures allow a 5 minute skew). Reservations are released and no cooldown starts, so players can retry |
| `outcome_unknown` | the backend was slow (10 s connector timeout) or the caller cancelled mid-call | Check the backend for whether the action happened before telling the player to retry. Audit shows the event |
| `AgentgwHighLatencyP99` | slow backend or saturated replica | Compare `gateway_call_duration_seconds` by tool; scale replicas if CPU is high |
| `AgentgwReplicasNotReady` | `/readyz` failing | `curl :9090/readyz` on the pod: the `failing` list names the check |
| `AgentgwRelayOfflineSpike`, `client_offline` | clients not connected, tickets not minted, wrong relay secret | See [Relay troubleshooting](#relay-troubleshooting) |
| 401 `invalid_client` on studio calls | wrong key or secret, clock skew over 5 minutes, replayed idempotency key | Fix the signing side; each request needs a fresh `Idempotency-Key` |
| 413 `too_large` on a manifest upload | body over 1 MiB | Trim the manifest |
| `rate_limited` rising | an agent is looping, or limits are too tight | Look at the activity feed for the player; consider a kill switch for the tool |
| `AgentgwPostgresPoolSaturated`, slow audit or confirmation writes | every pooled Postgres connection is in use and callers wait | Look at the "Postgres pool" panels: `acquired` at the cap and a rising wait rate. Raise `--postgres-max-conns` (stay under the database's `max_connections`, counting every replica), look for slow queries in Postgres, or add a replica. Connections that level off at the cap are normal, not a leak |
| `AgentgwGoroutinesGrowing` | a leak, or a dependency that stopped answering so work piles up | Check `relay_connections` and backend latency first. A steady climb with flat traffic is a leak: restart the replica and keep the heap and goroutine panels for the bug report |

## Sign in with your identity provider (OIDC)

By default a player proves who they are at the consent page with a link code from the studio's
backend. If the studio already runs an OIDC identity provider (IdP), the consent page can offer a
second way: **Sign in with your game account**. It is off until `--oidc-issuer` is set. The design
and its checks are in [design/oidc-consent.md](design/oidc-consent.md).

1. At the IdP, register a confidential client. The redirect URI is the gateway's public URL plus
   `/authorize/oidc/callback`, for example `https://gw.example.com/authorize/oidc/callback`, and it
   must match `--public-url` exactly.
2. Pick the claim that carries the player id your game uses. It must never change for a person
   and must match the id the game backend knows: use `sub` or a custom claim, not an email address
   or a display name. Set it with `--oidc-player-claim` (default `sub`).
3. Start the gateway with the issuer and client id, and the secrets in the environment:

   ```bash
   export AGENTGW_OIDC_CLIENT_SECRET='<client secret from the IdP>'
   export AGENTGW_OIDC_SESSION_SECRET="$(openssl rand -base64 48)"
   gateway ... --public-url=https://gw.example.com \
     --oidc-issuer=https://login.example.com --oidc-client-id=agentgw
   ```

   Helm: set `oidc.issuer`, `oidc.clientId`, `oidc.playerClaim`, and put `oidc-client-secret` and
   `oidc-session-secret` in the `existingSecret`.
4. Check the log for `oidc sign-in enabled` (it names the issuer, claim and callback, never a
   secret), then open the consent page. The sign-in button is there.

What to know:

- **The gateway checks at start.** It refuses to run if a secret is missing or the session secret
  is under 32 bytes, if the algorithm list is unsafe (`none` and the HS family are refused), if the
  IdP cannot be reached, or if its discovery document names a different issuer. A wrong issuer shows
  up at deploy time, not when a player clicks. OIDC flags without `--oidc-issuer`, and
  `--oidc-issuer` with `--dev-auth`, are also start errors.
- **Algorithms.** `--oidc-algs` defaults to `RS256,ES256`. Keys rotate at the IdP without a
  restart.
- **Every replica uses the same `AGENTGW_OIDC_SESSION_SECRET`.** The sign-in state lives in a
  sealed cookie, so the callback may land on any replica. Changing the secret only fails sign-ins
  that are in progress; those players start again. Nobody is signed out.
- **A failed sign-in never grants anything.** The agent sees `access_denied` and nothing more.
  The reason (bad signature, wrong audience, missing player claim) is in the gateway log.
- **`--max-scope coach` still applies.** Players who sign in through the IdP can grant coach only.
- **Local testing** with an IdP on `http://127.0.0.1`: add `--oidc-allow-insecure-http`. Never in
  production.

## Secrets and key rotation

Rotation has no downtime. Both a studio key set and an admin key set are reloaded on SIGHUP.

1. Put both the old and the new key in the key file, for example
   `{"studio-1":"<old>","studio-2":"<new>"}` (file mode 0600, JSON object of id to secret).
2. `kill -HUP <pid>` (Kubernetes: update the Secret, wait for the kubelet to refresh the mounted
   file, then signal the pods; or roll the Deployment). The log shows `studio keys loaded` with the key ids
   added and removed, never the secrets. A file that fails to parse is rejected and the current keys stay.
3. Switch the signing side to the new key id and secret.
4. Remove the old key from the file and send SIGHUP again.

### Retiring the key from the environment

By default the key from `--studio-key-id` plus `AGENTGW_SIGNING_SECRET` stays valid for the life of
the process, so a bad file never locks the studio out. Start with `--studio-keys-only` (and
`--admin-keys-only` for the admin key and `AGENTGW_ADMIN_SECRET`) and the file becomes the whole
set: the environment key is not accepted, and removing a key from the file plus SIGHUP retires it
with no restart. Both flags need their key file, and startup fails without it. A reload that would
leave no keys is refused and the current keys stay.

### The key that signs calls to your backend

The gateway signs every call to your backend with `--key-id` and `AGENTGW_SIGNING_SECRET`. To rotate
that key without a restart, point `--backend-key-file` at a JSON file `{"id":"...","secret":"..."}`.
It is read at start and on SIGHUP, and a call already in flight finishes with the key it started
with. Rotate in this order, because the backend has to learn the new key first:

1. Make the backend accept both the old and the new key.
2. Write the new key into the file, send SIGHUP, and look for `backend signing key loaded` with
   `key_id` and `previous_key_id`.
3. Remove the old key from the backend.

A file with an empty id or secret, or one that does not parse, keeps the current key and logs
`backend key reload failed`. With `--studio-keys-only` and `--backend-key-file` together,
`AGENTGW_SIGNING_SECRET` is no longer needed.

Every reload counts in `agentgw_key_reloads_total{kind,result}` (`kind` is `studio`, `admin` or
`backend`; `result` is `ok` or `error`). Alert on a rising `error` count after a rotation.
`AGENTGW_RELAY_SECRET` is read at start, so rotating it needs a restart of every replica (all replicas must share one value). Tickets minted before the restart stop working; open sockets drop with the restart and clients reconnect with a new ticket.

## Manifest upload and rollback

Signed with the admin key (see [PROTOCOL.md](PROTOCOL.md), "Studio manifest endpoints").

- Upload: `PUT /v1/studio/manifest`. Invalid manifests return 422 and change nothing.
- List versions: `GET /v1/studio/manifest/versions`.
- Roll back: `POST /v1/studio/manifest/versions/{id}/activate`. Other replicas follow within
  `--manifest-poll`. A stored version that no longer validates under newer rules is refused.
- A call already running finishes with the tools it started with.

A rollback is also the quickest way to take a tool away from agents without a restart.

## Kill switches and revoking access

- A kill switch blocks a tool, a player or the whole game on the next call, on every replica.
  Set it with the admin key (the same one that uploads manifests), using the `studiocall` helper
  or any client that signs like [PROTOCOL.md](PROTOCOL.md):

  ```bash
  # turn a tool off, with a reason for the log
  studiocall --key-id admin-1 --secret-env AGENTGW_ADMIN_SECRET --player ops-alice \
    --body '{"tool":"build_structure","reason":"duplicate items"}' PUT /v1/studio/switches
  studiocall --key-id admin-1 --secret-env AGENTGW_ADMIN_SECRET --player ops-alice GET /v1/studio/switches
  # turn it back on
  studiocall --key-id admin-1 --secret-env AGENTGW_ADMIN_SECRET --player ops-alice \
    --body '{"tool":"build_structure"}' DELETE /v1/studio/switches
  ```

  Body fields: `tool`, `player`, `reason`. Leave `tool` and `player` out to switch the whole game
  off. Only one switch per target exists, and on or off twice is harmless. A switch blocks every
  tier it matches, reads included. The agent gets `switched_off`. Each change is an audit event
  (`admin.switch.on` or `admin.switch.off`, with the admin in `player`) in the admin activity feed,
  and a `kill switch changed` warning in the log carries the reason. Switches live in Redis, so
  without `--redis-addr` they are per process and lost on restart. The endpoint exists only when an
  admin key is set. The dev admin API (`/v1/admin/switches`) is for demos only.
- Other ways to cut access: upload a manifest without the tool (or
  roll back), or revoke a player's agents with `DELETE /v1/studio/grants/{id}` or
  `DELETE /v1/studio/grants` (effective immediately; tokens are looked up, not self-contained).

## Relay troubleshooting

| Check | How |
|---|---|
| Is the relay on? | Startup log `relay` with `enabled=true`. Off without `AGENTGW_RELAY_SECRET`, and never mounted under `--dev-auth` |
| Ticket 401 `invalid_ticket` | expired (60 s), signed with a different relay secret (replicas must share it), or the client waited too long to connect |
| Mint call 401 | studio signature wrong: it uses the studio key, not the admin key |
| `client_offline` | no socket for that player on any replica. Check `relay_connections` and that all replicas share one Redis |
| Calls time out (10 s) | the client connected but is not answering `call` frames with matching `id`; or a replica lost Redis pub/sub |
| Socket closes with code 4000 | the same player connected again; the older one is replaced |
| Socket closes with code 1008 | the client sent a frame that is not valid JSON, a binary frame, or more than `--relay-max-frames-per-sec` frames a second (`relay_socket_closes_total{reason="malformed"}` or `"flood"`) |
| Socket closes with code 1002 | the client's `hello` declared a major protocol version other than 1 (`reason="unsupported"`) |
| Socket closes with code 1001 | the client stopped answering pings (`reason="ping"`); usually a game paused in the background or a dead network |
| Many `client_busy` results | the game client answers slowly, so `--relay-max-in-flight` calls pile up. Check `relay_busy_total`; fix the client, or raise the flag |
| A player reconnects in a loop | two clients for the same player keep replacing each other (code 4000). `relay_socket_closes_total{reason="replaced"}` climbs. Find the second client |
| Flags | `--relay-max-in-flight` (default 8), `--relay-max-frames-per-sec` (default 100) |

## Redis and Postgres outages

- **Redis down:** policy checks fail closed (`policy_unavailable`); `/readyz` fails the `redis`
  check; relay calls across replicas fail or time out. Nothing is allowed through to the
  backend while limits cannot be checked.
- **Postgres down:** calls to `confirm: true` tools fail closed (`confirmation_unavailable`),
  OAuth token checks fail so MCP calls are refused, studio endpoints answer 503
  `temporarily_unavailable`, and audit writes are logged as errors (`audit store write failed`)
  while the stdout audit line still records the call. `/readyz` fails the `postgres` check.
- **Backend down:** see the symptom table. Failed backend calls release their reservation and
  start no cooldown.

## Backup and retention

- Postgres holds everything durable: manifest versions, OAuth clients and grants, confirmations
  and the audit feed. Back it up with your usual `pg_dump` or snapshots. The tables are
  `manifest_versions`, `manifest_active`, `audit_events` and the confirmation and auth tables.
- Redis holds limits, cooldowns and switches only. It is safe to lose; counters start fresh.
- `--audit-retention` (default 30 days) prunes `audit_events` hourly; the log line
  `pruned audit store` shows the row count. Keep a longer copy in your log pipeline if you need
  it, since the gateway also writes every audit event to stdout as JSON.

## Audit write path

By default each call writes its audit event to Postgres before the result is returned. That is the
safest setting and the right one for a pilot under about 20 calls per second.

For more volume, set `--audit-queue` (for example `--audit-queue=10000`). Events go on an in-process
queue and one writer sends them to Postgres in batches (up to `--audit-batch-size` events, or after
`--audit-flush-interval`, whichever comes first) with one `COPY`. The call never waits for the
database. In a test, Postgres commits fell by a factor of about 400, so use it when Postgres is the
thing you are protecting. See [design/audit-write-path.md](design/audit-write-path.md) for the numbers
and the other options.

What you give up, and how to see it:

- **A crash or kill can lose queued events**, at most the queue size and normally no more than one
  flush interval. A graceful stop (`SIGTERM`) writes the queue first, within 10 seconds.
- **A full queue drops the event** (`--audit-overflow=drop`, the default) and counts it. With
  `--audit-overflow=block` a call waits for room until its own 5 second deadline, then drops.
- **A failed batch is lost**, not retried, and logged as `audit batch write failed`. Today's single
  insert behaves the same way: audit never turns a good call into an error.
- **The feed lags other replicas by up to one flush interval.** On the replica that took the call, the
  feed flushes first, so it is current.
- The stdout `agent_call` log line is unchanged and still written for every call. It is your second
  copy of each event.

Alert on any rate of `audit_dropped_total{reason}` and on `audit_flush_errors_total`. Watch
`audit_queue_depth` (it should sit near zero) and `audit_flush_duration_seconds`.

## Upgrade

1. Read the release notes and [DECISIONS.md](DECISIONS.md) for compatibility notes.
2. Run `docker compose run --rm dev go run ./cmd/gwctl validate <manifest>` against the new
   build's manifest rules.
3. Roll out one replica at a time (`helm upgrade` does a rolling update with no unavailable
   pods). New versions create missing tables on start; there is no destructive migration.
4. Watch `/readyz`, the failure share and p99 for 10 minutes. To go back, `helm rollback`; the
   manifest version in the store is not touched by a binary rollback.

## Load and soak

`test/load` drives a running gateway through the dev HTTP API, so start the gateway with
`--dev-auth` (the compose `gateway` service does). It reports throughput, errors, denials by
code and p50, p95 and p99, and exits 1 when a threshold is breached.

```bash
docker compose up -d --build gateway

# Smoke, 30 seconds
docker compose run --rm load --url http://gateway:8080 --duration 30s --rate 200 \
  --max-error-rate 0.01 --max-p99 300ms

# Soak, one hour at a steady rate; watch the dashboard while it runs
docker compose run --rm load --url http://gateway:8080 --duration 1h --rate 100 \
  --concurrency 32 --players 500 --max-error-rate 0.001 --max-p99 500ms
```

The soak script does the whole thing, including a clean stack and resource sampling:

```bash
DURATION=1h RATE=100 PLAYERS=2000 scripts/soak.sh
```

Reference result, one hour at 100 calls per second, 32 workers and 2000 players, on a laptop with
Docker Compose, one gateway, Redis, Postgres and the fake backend on the same machine:

| Measure | Result |
|---|---|
| Calls | 359,763 (287,870 succeeded, 71,892 denied by the fake backend, 1 failed) |
| Latency p50, p95, p99 | 1.9 ms, 4.4 ms, 6.5 ms (slowest call 202 ms) |
| Gateway memory | 15 to 19 MiB after warm-up, no sustained growth |
| Gateway CPU | 7 to 14 percent of one core |
| Redis keys | 4,000, which is two per simulated player |
| Postgres sessions | 14, flat for the second half: the gateway's pool at its cap of 8 plus Postgres's own 6 |
| `/readyz` | 200 in all 97 samples |

### What the gateway adds per call

`scripts/bench-overhead.sh` sends the same signed request straight to the fake city backend
(`test/load --direct-url`) and through the gateway, with the same workers and rate, so the
difference is the gateway's own work: policy in Redis, validation, signing and the audit write.

```bash
scripts/bench-overhead.sh                                            # about 20 minutes, 3 repetitions
DURATION=10 MAX_DURATION=5 WARMUP=2 REPS=1 scripts/bench-overhead.sh # quick check of the script
python3 scripts/bench-report.py overhead bench-results/<run>         # write site/src/data/overhead.json
```

Each repetition runs two tools, a read tool (`get_city_state`) and a capped action tool
(`sell_resource`), each direct and then through the gateway, in two phases: paced at `RATE` calls
per second for `DURATION` seconds, and flat out (rate 0) for `MAX_DURATION` seconds. The first
`WARMUP` seconds of every run are dropped, and the reported value of each measure is the median over
the repetitions. The stack uses its own compose project (`agentgw-bench`) and a fake backend with a
huge stock (`scripts/bench.compose.yml`), so every action call succeeds and takes the full path:
reserve, signed call, settle.

Read it with care. The fake backend answers in microseconds, so the gateway looks large in relative
terms. A real backend that takes tens of milliseconds makes the gateway's share small. "Flat out" is
bounded by the machine and by the load generator, so treat it as a floor for one gateway process.
`test/load --direct-url` and `--warmup` also work on their own.

Read your own run against these. The numbers show what the gateway adds, not what a real game
backend costs. The Postgres container's memory kept rising during the run (27 to 154 MiB), which
is what the audit write path note (`docs/design/audit-write-path.md`) is about.

It starts the compose stack under its own project name, runs the load tool, and samples the
gateway, Redis and Postgres every 30 seconds (memory, Redis keys, Postgres connections, CPU,
`/readyz`). It writes `soak-results/<time>/` with `load.txt`, `samples.csv` and `summary.txt`, and
exits nonzero if the load tool breaches a threshold, gateway memory or Postgres connections grow
by more than `MAX_GROWTH_PCT` (default 30) between the first and last quarter, or `/readyz` ever
fails. Use `DURATION=2m` to check the script itself.

Flags: `--rate` (0 means as fast as the workers go), `--concurrency`, `--players` (each player has
its own limits), `--mix tool=weight,...` (default `get_city_state=8,sell_resource=2`),
`--args-file` (JSON `{"tool": {args}}` for tools not built in), `--min-rps`, `--timeout`.
Denials such as `rate_limited` are counted by code and are not failures; the demo limits are low
by design, so a high rate on one player shows them. Use many `--players` to measure throughput
rather than limits. A soak should show flat memory and a flat p99; watch both on the dashboard.
