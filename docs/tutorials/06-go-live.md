# Tutorial 6: Go live

Verified against agentgw v0.1.0 (see the [changelog](../../CHANGELOG.md)). About 20 minutes.
The script [`06-go-live.sh`](06-go-live.sh) runs every command below against a throwaway Docker
project, and CI runs that script.

This is the checklist between "it works on my laptop" and "players use it". You will run the
gateway with Postgres and Redis, upload and roll back a manifest with the admin key, rotate a
studio key without a restart, read the activity feed, put the gateway behind a proxy, watch it
fail closed when Redis goes away, and read the metrics. A Helm chart for Kubernetes is at the
end.

## Prerequisites

- Docker with Compose v2, and the repository checked out.
- Tutorials [4](04-link-a-player.md) and [5](05-ask-before-big-actions.md), or at least a feel for
  signed studio calls. The helpers are the same.
- Stop any other agentgw stack first (this one uses `127.0.0.1:8080` and `127.0.0.1:9091`).

## What "live" needs

| Piece | Why | What happens without it |
|---|---|---|
| Postgres (`--postgres-url`) | confirmations, OAuth clients and grants, manifest versions, the activity feed | the same stores live in memory and are lost on restart |
| Redis (`--redis-addr`) | rate limits, caps, cooldowns and switches shared by every replica | each replica counts on its own, so caps are per replica |
| Two keys | the **studio key** signs player calls (tickets, grants, confirmations). The **admin key** signs manifest and admin calls | one leaked key should not let anyone change your tools |
| A public HTTPS URL (`--public-url`) | it is the OAuth issuer and the MCP audience | tokens are bound to it, so it must be the address agents use |
| `--client-ip-header` | behind a proxy every request comes from the proxy | rate limits would treat all players as one address |

The compose file already starts Postgres and Redis next to the gateway. The override for this
tutorial, `examples/tutorials/compose.golive.yml`, adds the admin key, a key file and the trusted
header. Every secret in it is a placeholder.

## 1. Start with a key file

Keys can live in a JSON file of `{"key id": "secret"}`. The gateway reloads it when it receives
SIGHUP, so rotation needs no restart. Make a folder and a first key, `studio-a`:

```bash
mkdir -p .tutorial-keys
printf '{"studio-a":"golive-demo-studio-a-secret-0001"}\n' > .tutorial-keys/studio-keys.json
chmod 600 .tutorial-keys/studio-keys.json

dc() {
  docker compose -f docker-compose.yml -f examples/tutorials/compose.oauth.yml \
    -f examples/tutorials/compose.golive.yml "$@"
}
dc up -d --build gateway
```

In production the file comes from your secret manager. Never commit it.

Build the helpers and define the three signers the tutorial needs. The toolchain container plays
your backend, so it knows the secrets as environment variables:

```bash
dc build --quiet dev
dc run --rm -T dev go build -o bin/ ./examples/studiocall ./examples/oauthclient
studio_a() { dc run --rm -T dev bin/studiocall --key-id studio-a --secret-env STUDIO_A_SECRET "$@"; }
studio_b() { dc run --rm -T dev bin/studiocall --key-id studio-b --secret-env STUDIO_B_SECRET "$@"; }
admin()    { dc run --rm -T dev bin/studiocall --key-id admin-1 --secret-env AGENTGW_ADMIN_SECRET "$@"; }
oauth()    { dc run --rm -T dev bin/oauthclient "$@"; }
mcp() {
  curl -s -H "Authorization: Bearer $ACCESS" -H 'Content-Type: application/json' \
    -H 'Accept: application/json, text/event-stream' -H 'MCP-Protocol-Version: 2025-06-18' \
    -d "$1" http://127.0.0.1:8080/mcp
  echo
}
tool_names() { grep -o '"name":"[a-z_]*"' | tr '\n' ' '; echo; }
LIST='{"jsonrpc":"2.0","id":1,"method":"tools/list"}'
```

## 2. Health, readiness and metrics

The gateway serves these on a **separate ops port** (`--ops-addr`), so they are never on the
public listener. Keep that port on your private network.

```bash
curl -s http://127.0.0.1:9091/healthz
curl -s http://127.0.0.1:9091/readyz
curl -s http://127.0.0.1:9091/metrics | grep -E '^agentgw_build_info'
```

```text
{"status":"ok"}
{"status":"ready"}
agentgw_build_info{version="dev"} 1
```

`/healthz` says the process is up. `/readyz` says it can do its job: the manifest is loaded and
Redis and Postgres answer. Use the first for liveness and the second for readiness.

## 3. The studio signs with the key from the file

```bash
OUT=$(studio_a --player alice --expect 201 POST /v1/studio/link-tickets); echo "$OUT"
TICKET=$(echo "$OUT" | sed 's/.*"ticket":"\([^"]*\)".*/\1/')
ACCESS=$(oauth --ticket "$TICKET" --scope operator | sed 's/.*"access_token":"\([^"]*\)".*/\1/')
mcp "$LIST" | tool_names
```

```text
HTTP 201
{"expires_at":"2026-11-19T12:12:00.998442046Z","ticket":"8VZJ-QCJD"}
"name":"build_structure" "name":"collect_taxes" "name":"get_city_state" "name":"sell_resource"
```

The agent is linked and sees all four tools.

## 4. Upload a smaller manifest with the admin key

The admin key is different from the studio key. Whoever holds it decides which tools agents
may call. `X-AG-Player` names who is acting. Here it is `ci`, as if a deploy pipeline did it, and
it is stored on the version.

`examples/tutorials/city-lite.yaml` has only two tools. Upload it as YAML, the same format you
write your manifests in:

```bash
admin --player ci --expect 201 --content-type application/yaml \
  --body-file examples/tutorials/city-lite.yaml PUT /v1/studio/manifest
mcp "$LIST" | tool_names
```

```text
HTTP 201
{"id":2,"sha256":"f92e83f3...","actor":"ci","created_at":"2026-11-19T12:07:45.754277Z","tools":2,"active":true}
"name":"collect_taxes" "name":"get_city_state"
```

No restart, and the same agent token. The next `tools/list` already shows two tools.

JSON works as well. Send it with `Content-Type: application/json`, which is what `studiocall`
uses when you give it a body and no `--content-type`. The gateway keeps every version as JSON
whichever format you upload, so a YAML and a JSON upload of the same manifest are the same
version content. Here is a one-tool JSON upload:

```bash
printf '{"version":1,"tools":[{"name":"get_city_state","description":"Overview of the city.","tier":"read","http":{"method":"GET","url":"/api/v1/city"}}]}' > .tutorial-keys/one.json
admin --player ci --expect 201 --body-file .tutorial-keys/one.json PUT /v1/studio/manifest
mcp "$LIST" | tool_names
```

```text
HTTP 201
{"id":3,"sha256":"307e152c...","actor":"ci","created_at":"2026-11-19T12:46:22.145399041Z","tools":1,"active":true}
"name":"get_city_state"
```

(`studiocall` takes its flags before the method and path.)

## 5. List the versions and roll back

```bash
admin --player ci --expect 200 GET /v1/studio/manifest/versions
```

```text
HTTP 200
{"versions":[{"id":3,"actor":"ci","tools":1,"active":true,...},{"id":2,"actor":"ci","tools":2,"active":false,...},{"id":1,"actor":"bootstrap","tools":4,"active":false,...}]}
```

(Shortened.) Version 1 is the file the gateway started from (`actor: bootstrap`). Roll back to it:

```bash
admin --player ci --expect 200 POST /v1/studio/manifest/versions/1/activate
mcp "$LIST" | tool_names
```

```text
HTTP 200
{"id":1,"sha256":"e630c073...","actor":"bootstrap","tools":4,"active":true}
"name":"build_structure" "name":"collect_taxes" "name":"get_city_state" "name":"sell_resource"
```

A rollback is the fastest way to take a tool away from agents. Other replicas follow within
`--manifest-poll` (15 seconds by default, 2 seconds in this tutorial). A call that is already
running finishes with the tools it started with.

## 6. A bad manifest changes nothing

```bash
printf '{"version":1,"tools":[]}' > .tutorial-keys/bad.json
admin --player ci --expect 422 --body-file .tutorial-keys/bad.json PUT /v1/studio/manifest
```

```text
HTTP 422
{"detail":"manifeststore: invalid manifest: manifest invalid:\n  - at least one tool is required","error":"invalid_manifest"}
```

The same validator as `gwctl validate` runs on every upload, and it lists every problem at once.
The active version stays as it was. A YAML upload with a typo says where:

```bash
printf 'version: 1\ntools:\n  - name: get_city_state\n    description: x\n    tir: read\n' > .tutorial-keys/typo.yaml
admin --player ci --expect 422 --content-type application/yaml --body-file .tutorial-keys/typo.yaml PUT /v1/studio/manifest
```

```text
HTTP 422
{"detail":"manifeststore: invalid manifest: manifest: decode yaml: [5:5] unknown field \"tir\"\n   2 | tools:\n   3 |   - name: get_city_state\n   4 |     description: x\n\u003e  5 |     tir: read\n           ^\n","error":"invalid_manifest"}
```

## 7. Rotate the studio key without a restart

The rule: **add the new key, switch your backend to it, then remove the old one.** Both keys work
during the overlap, so nothing breaks. Add `studio-b`, then tell the gateway to reload:

```bash
printf '{"studio-a":"golive-demo-studio-a-secret-0001","studio-b":"golive-demo-studio-b-secret-0002"}\n' \
  > .tutorial-keys/studio-keys.json
dc kill -s HUP gateway
sleep 1
dc logs gateway 2>&1 | grep 'studio keys loaded' | grep studio-keys.json | tail -n 1
```

```text
{"time":"...","level":"INFO","msg":"studio keys loaded","file":"/etc/agentgw-keys/studio-keys.json","count":3}
```

The log shows how many keys are loaded and never their ids or secrets. The count is 3 because the
key from `--studio-key-id` and `AGENTGW_SIGNING_SECRET` is always valid as well (more on that
below). Both new and old keys work now:

```bash
studio_b --player alice --expect 200 GET /v1/studio/grants
studio_a --player alice --expect 200 GET /v1/studio/grants
```

Your backend is on `studio-b` now. Retire `studio-a`:

```bash
printf '{"studio-b":"golive-demo-studio-b-secret-0002"}\n' > .tutorial-keys/studio-keys.json
dc kill -s HUP gateway
sleep 1
studio_a --player alice --expect 401 GET /v1/studio/grants
studio_b --player alice --expect 200 GET /v1/studio/grants
```

```text
{"error":"invalid_client"}
HTTP 401
HTTP 200
```

A file that does not parse never locks you out. The gateway logs the error and keeps the current
keys:

```bash
printf 'not json' > .tutorial-keys/studio-keys.json
dc kill -s HUP gateway
sleep 1
dc logs gateway 2>&1 | grep 'key reload failed' | tail -n 1
```

```text
{"time":"...","level":"ERROR","msg":"key reload failed, keeping current keys","file":"/etc/agentgw-keys/studio-keys.json","err":"studiosig: key file ..."}
```

Two things to know:

- The key named by `--studio-key-id` with `AGENTGW_SIGNING_SECRET` is read at start and always
  stays valid. That secret also signs the gateway's calls to your backend. To rotate **it**, move
  your studio to a file key as above and change the environment value on the next restart.
- The admin key works the same way with `--admin-keys-file` and `AGENTGW_ADMIN_SECRET`. The
  relay secret (`AGENTGW_RELAY_SECRET`) is read at start only, so rotating it needs a restart.

Put the file back so the rest of the tutorial keeps working:

```bash
printf '{"studio-b":"golive-demo-studio-b-secret-0002"}\n' > .tutorial-keys/studio-keys.json
dc kill -s HUP gateway
```

## 8. The activity feed

Make the agent do two things, one that works and one that is denied:

```bash
mcp '{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"get_city_state","arguments":{}}}' >/dev/null
mcp '{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"sell_resource","arguments":{"resource":"food","quantity":5000}}}' >/dev/null
```

Your game can show the player what their agents did. The player feed is signed with the studio
key and always scoped to the player in `X-AG-Player`:

```bash
studio_b --player alice --expect 200 GET '/v1/studio/activity?limit=5'
```

```text
HTTP 200
{"events":[{"id":2,"time":"2026-11-19T12:08:39.224257Z","tool":"sell_resource","tier":"medium","outcome":"denied","code":"invalid_arguments","args_sha256":"f82ba1c7..."},{"id":1,"time":"...","tool":"get_city_state","tier":"read","outcome":"ok","args_sha256":"..."}],"next_before":...}
```

You see tool, tier, outcome and code. There are no arguments, only a hash. Your support team
gets the admin view, signed with the admin key. It can see every player:

```bash
admin --player ci --expect 200 GET '/v1/studio/admin/activity?limit=3'
```

Events are kept for `--audit-retention` (720 hours by default; `0` keeps them forever) and pruned
hourly.

## 9. Behind a proxy, the limiter must see the real client

Registration of OAuth clients is limited per client address (10 an hour). Behind a load balancer
every request would come from the balancer, so everyone would share one limit. The gateway can
read the address from a header you trust, `--client-ip-header`. The override sets
`X-Forwarded-For`. Try it. Ten registrations from one address pass, the eleventh is refused, and
another address is not affected:

```bash
for i in $(seq 1 10); do
  curl -s -o /dev/null -w '%{http_code} ' -H 'X-Forwarded-For: 203.0.113.7' -H 'Content-Type: application/json' \
    -d '{"client_name":"x","redirect_uris":["http://127.0.0.1/cb"]}' http://127.0.0.1:8080/register
done; echo
curl -s -o /dev/null -w '11th from .7: %{http_code}\n' -H 'X-Forwarded-For: 203.0.113.7' -H 'Content-Type: application/json' \
  -d '{"client_name":"x","redirect_uris":["http://127.0.0.1/cb"]}' http://127.0.0.1:8080/register
curl -s -o /dev/null -w '1st from .8: %{http_code}\n' -H 'X-Forwarded-For: 203.0.113.8' -H 'Content-Type: application/json' \
  -d '{"client_name":"x","redirect_uris":["http://127.0.0.1/cb"]}' http://127.0.0.1:8080/register
```

```text
201 201 201 201 201 201 201 201 201 201
11th from .7: 429
1st from .8: 201
```

**Set this flag only when a proxy you control overwrites the header.** If clients can send their
own `X-Forwarded-For`, they can pick a fresh address for every request and walk around the limit.
Terminate TLS at the proxy too, and set `--public-url` to its HTTPS address.

## 10. Redis goes away: the gateway fails closed

Safety limits live in Redis. If the gateway cannot check them it does not guess. Stop Redis:

```bash
dc stop redis
mcp '{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"get_city_state","arguments":{}}}'
curl -s -w '\nHTTP %{http_code}\n' http://127.0.0.1:9091/readyz
```

```text
... "code":"policy_unavailable","message":"safety checks are unavailable, try again later" ...
{"failing":[{"check":"redis","error":"dial tcp: lookup redis on 127.0.0.11:53: no such host"}],"status":"unready"}
HTTP 503
```

Calls are refused with `policy_unavailable`, a code the agent can read, and `/readyz` says which
check fails, so your orchestrator pulls the replica from rotation. Start Redis and it recovers on
its own:

```bash
dc start redis
mcp '{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"get_city_state","arguments":{}}}'
```

If Postgres goes away instead, calls to `confirm` tools and OAuth token checks fail closed, and
studio endpoints answer `503 temporarily_unavailable`. The outage table is in the
[runbook](../RUNBOOK.md#redis-and-postgres-outages).

## 11. Metrics

```bash
curl -s http://127.0.0.1:9091/metrics | grep -E '^gateway_calls_total'
```

```text
gateway_calls_total{tool="get_city_state",outcome="ok"} 2
gateway_calls_total{tool="get_city_state",outcome="policy_unavailable"} 1
gateway_calls_total{tool="sell_resource",outcome="invalid_arguments"} 1
```

`outcome` is `ok` or the denial code. A rise in `rate_limited`, `backend_unavailable` or
`policy_unavailable` is your early warning. The repository ships a Grafana dashboard
(`deploy/grafana/agentgw.json`) and Prometheus alert rules (`deploy/prometheus/alerts.yaml`).

## Kill switches: what exists today

Be careful here. The gateway has kill switches (block a tier, a tool or a player in the policy
store), but the **only HTTP surface for them today is the dev admin API**, which needs
`--dev-auth` and must never face a network. There is no studio-signed endpoint for switches yet.

In an incident, use what is signed and works in production:

- **Take a tool away:** upload a manifest without it, or roll back (step 4 and 5). It takes effect
  on the next call.
- **Cut off a player's agents:** `DELETE /v1/studio/grants/{id}` or `DELETE /v1/studio/grants`
  (tutorial 4). It takes effect on the next call.
- **Stop everything:** scale the gateway to zero, or upload a manifest that holds only read tools.

The [runbook](../RUNBOOK.md#kill-switches-and-revoking-access) says the same.

## Clean up

```bash
dc down -v
rm -rf .tutorial-keys
```

## Kubernetes

The repository has a Helm chart in `deploy/helm/agentgw`. It never takes secrets inline: you
create a Secret and name it with `existingSecret`. It sets non-root, a read-only filesystem,
probes on the ops port, a disruption budget, and optional ingress, autoscaling, `ServiceMonitor`
and `NetworkPolicy`. The install command is in the [runbook](../RUNBOOK.md#deploy-with-helm). Check
the chart without installing anything:

```bash
make helm-check
```

## What you learned

- Postgres and Redis make state survive restarts and be shared, and the gateway fails closed when
  Redis is gone.
- The admin key manages the manifest: upload, list, roll back. The studio key serves players. Keep
  them different.
- Keys rotate by adding the new one, switching, and removing the old one, with SIGHUP and no
  restart.
- Run behind a proxy with `--client-ip-header` only when the proxy overwrites that header.
- Health, readiness and metrics are on the ops port. Kill switches have no production HTTP
  endpoint yet. Use manifest rollback and grant revocation.

## Next

[Tutorial 7: Test your integration](07-test-your-integration.md).

Reference: [runbook](../RUNBOOK.md), [flags](../reference/flags.md) and the
[studio API](../reference/studio-api.md).
