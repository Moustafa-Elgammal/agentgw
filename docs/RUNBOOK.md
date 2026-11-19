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
| `--public-url` | `http://127.0.0.1:8080` | OAuth issuer; the MCP resource is `<public-url>/mcp` |
| `--tenant` | `dev` | tenant id |
| `--dev-auth` | false | DEV ONLY. Trusts `X-Dev-Player`. Never on a reachable network |
| `--studio-key-id` | `dev-1` | studio key id (player calls, grants, confirmations, activity, relay tickets) |
| `--admin-key-id` | `admin-1` | admin key id (manifest endpoints, admin feed). Use a different secret than the studio key |
| `--studio-keys-file`, `--admin-keys-file` | none | extra keys for rotation, reloaded on SIGHUP |
| `--redis-addr` | `$AGENTGW_REDIS_ADDR` | Redis address |
| `--postgres-url` | `$AGENTGW_POSTGRES_URL` | Postgres URL |
| `--audit-retention` | `720h` | audit events older than this are deleted hourly. `0` keeps them forever |
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
  --set-file manifest=examples/city/manifest.json \
  --set config.publicURL=https://agentgw.example.com \
  --set config.backendURL=http://game-backend.games.svc:8080 \
  --set config.backendAllowPrivate=true \
  --set config.redisAddr=redis-master.cache.svc:6379
```

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
| `relay_calls_total` | `result` | `ok`, `denied`, `offline`, `error` |
| `http_requests_total` | `route`, `code` | public listener requests by route pattern |
| `agentgw_build_info` | `version` | always 1 |

"Failure" outcomes mean infrastructure trouble, not a refused player: `backend_unavailable`,
`outcome_unknown` (the call was cut off and may or may not have happened), `policy_unavailable`
and `confirmation_unavailable`. Everything else that is not `ok` is a normal denial (rate limit,
cap, cooldown, validation, scope, kill switch).

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

## Secrets and key rotation

Rotation has no downtime. Both a studio key set and an admin key set are reloaded on SIGHUP.

1. Put both the old and the new key in the key file, for example
   `{"studio-1":"<old>","studio-2":"<new>"}` (file mode 0600, JSON object of id to secret).
2. `kill -HUP <pid>` (Kubernetes: update the Secret, wait for the kubelet to refresh the mounted
   file, then signal the pods; or roll the Deployment). The log shows `studio keys loaded` with a count, never the secrets.
   A file that fails to parse is rejected and the current keys stay.
3. Switch the signing side to the new key id and secret.
4. Remove the old key from the file and send SIGHUP again.

The key from `--studio-key-id` plus `AGENTGW_SIGNING_SECRET` always stays valid, so a bad file
never locks the studio out. To rotate that one, move to the file and remove it only by
restarting with a new value. The same applies to the admin key and `AGENTGW_ADMIN_SECRET`.
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

- A kill switch blocks a tier, tool or player in the policy store. The only HTTP surface today is
  the dev admin API (`/v1/admin/switches`, needs `--dev-auth` and `X-Dev-Admin: true`), so it is
  for demos, not production.
- In production use the signed studio endpoints instead: upload a manifest without the tool (or
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
| Socket closes with code 1008 | the client sent a frame that is not valid JSON |

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

Flags: `--rate` (0 means as fast as the workers go), `--concurrency`, `--players` (each player has
its own limits), `--mix tool=weight,...` (default `get_city_state=8,sell_resource=2`),
`--args-file` (JSON `{"tool": {args}}` for tools not built in), `--min-rps`, `--timeout`.
Denials such as `rate_limited` are counted by code and are not failures; the demo limits are low
by design, so a high rate on one player shows them. Use many `--players` to measure throughput
rather than limits. A soak should show flat memory and a flat p99; watch both on the dashboard.
