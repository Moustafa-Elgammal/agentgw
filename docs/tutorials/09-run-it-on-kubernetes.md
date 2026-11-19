# Tutorial 9: Run it on Kubernetes

Verified against agentgw v0.1.0 (see the [changelog](../../CHANGELOG.md)). About 25 minutes.
The script [`09-run-it-on-kubernetes.sh`](09-run-it-on-kubernetes.sh) runs every command below
against a local cluster. Unlike the other tutorials it needs a Kubernetes cluster, so it is not part
of `make guides-check`. It runs with `make k8s-tutorial-check`, and a weekly workflow runs it in a
kind cluster.

[Tutorial 6](06-go-live.md) ran the gateway with Postgres and Redis in Docker Compose. Here you
install the Helm chart in `deploy/helm/agentgw` on a local Kubernetes cluster, with two gateway
replicas, and play the city game through it. Then you upload a new manifest, delete a pod and
restart the deployment, and see that nothing was lost.

## Prerequisites

- Docker, the repository (`git clone https://github.com/Moustafa-Elgammal/agentgw && cd agentgw`),
  and `curl` and `openssl`.
- `kubectl` and `helm`. On macOS: `brew install kubectl helm`. Other systems: see
  [kubernetes.io](https://kubernetes.io/docs/tasks/tools/) and [helm.sh](https://helm.sh/docs/intro/install/).
- A **local** Kubernetes cluster that `kubectl` points at. Either:
  - Docker Desktop with Kubernetes turned on (Settings, Kubernetes, Enable). The context is
    `docker-desktop`.
  - Or [kind](https://kind.sigs.k8s.io): `brew install kind`, then `kind create cluster`. The
    context is `kind-kind`. Everything below works the same way.

Check which cluster you are about to use:

```bash
kubectl config current-context
kubectl get nodes
```

The script refuses to run unless the context is `docker-desktop`. To use another local cluster,
set `K8S_CONTEXT`, for example `K8S_CONTEXT=kind-kind make k8s-tutorial-check`. Do not point this at
a cluster you care about: the tutorial uses its own namespace, `agentgw-tutorial`, and deletes it at
the end, but it also loads two images into the cluster's nodes.

## What you are building

```mermaid
flowchart LR
  Agent["Your machine<br/>(curl, studiocall)"] -->|kubectl port-forward| Svc["Service agentgw"]
  Svc --> G1["gateway pod 1"]
  Svc --> G2["gateway pod 2"]
  G1 & G2 --> R[("Redis<br/>limits, caps")]
  G1 & G2 --> P[("Postgres<br/>grants, manifests, audit")]
  G1 & G2 -->|signed calls| B["city backend"]
```

Everything lives in one namespace. Postgres and Redis are small dev-only deployments
(`examples/tutorials/k8s/data.yaml`) with their data on an `emptyDir`: in production use managed
services. The demo backend is the same fake city as in the other tutorials.

## 1. Build the images and make the cluster see them

```bash
docker build --target gateway -t agentgw:tutorial .
docker build --target city-backend -t city-backend:tutorial .
```

Whether the cluster can see them depends on the cluster:

- A cluster whose nodes are Docker containers, which means kind and Docker Desktop in its "kind"
  mode (the nodes are called `desktop-control-plane` and so on), has its own image store. Load the
  images into it: `kind load docker-image agentgw:tutorial city-backend:tutorial --name <cluster>`.
  For kind the name is `kind`. For Docker Desktop it is `desktop`. The script works this out from
  the node name and downloads `kind` into `.tutorial-tmp/bin` if you do not have it.
- Docker Desktop in its single node mode shares the Docker image store, so nothing needs loading.
- A cloud cluster pulls from a registry. Push the images there and set `image.repository` and
  `image.tag` instead.

## 2. The namespace and the Secret

The chart never takes a secret inline. You create a Secret and name it with `existingSecret`. Keys:
`signing-secret` (required), `admin-secret` and `postgres-url`.

```bash
kubectl apply -f examples/tutorials/k8s/namespace.yaml

SIGNING_SECRET=$(openssl rand -hex 24)
ADMIN_SECRET=$(openssl rand -hex 24)
kubectl -n agentgw-tutorial create secret generic agentgw-credentials \
  --from-literal=signing-secret="$SIGNING_SECRET" \
  --from-literal=admin-secret="$ADMIN_SECRET" \
  --from-literal=postgres-url='postgres://agentgw:agentgw-dev@postgres:5432/agentgw?sslmode=disable'
```

The signing secret does two jobs. Your studio backend signs player calls with it (the studio key,
`studio-1`), and the gateway signs its calls to the game backend with it. The admin secret signs
manifest uploads and is a different key on purpose.

## 3. Postgres, Redis and the backend

```bash
kubectl apply -f examples/tutorials/k8s/data.yaml -f examples/tutorials/k8s/backend.yaml
kubectl -n agentgw-tutorial rollout status deploy/postgres deploy/redis deploy/city-backend --timeout=180s
```

The backend reads the same Secret, so it can verify the gateway's signatures.

## 4. Install the chart

The values file `examples/tutorials/k8s/values.yaml` holds the few settings that differ from the
defaults. Read it first. The important ones:

- `config.publicURL` is `http://127.0.0.1:18209`, the address you will use through `port-forward`.
  Tokens are bound to it. In production it is your public HTTPS URL.
- `config.backendAllowPrivate: true`, because the backend is a cluster address, which counts as
  private. The gateway refuses private addresses unless you say so.
- `config.redisAddr` turns on the shared policy store, so limits hold across both replicas.
- `config.manifestPoll: 2s` makes a manifest upload reach every replica within seconds.

The manifest itself comes from the repository's demo, as a file, not from the values:

```bash
helm install agentgw deploy/helm/agentgw --namespace agentgw-tutorial \
  -f examples/tutorials/k8s/values.yaml \
  --set-file manifest=examples/city/manifest.yaml \
  --wait --timeout 180s
kubectl -n agentgw-tutorial get pods -l app.kubernetes.io/name=agentgw
```

```text
NAME                      READY   STATUS    RESTARTS   AGE
agentgw-7c9f6d8b5-4xk2p   1/1     Running   0          20s
agentgw-7c9f6d8b5-m8wzt   1/1     Running   0          20s
```

`--wait` returned because both pods passed their readiness probe, which asks `/readyz` on the ops
port. The gateway is ready only when it can reach Redis and Postgres.

## 5. Health, readiness and metrics

The ops port (9090 in the chart) is not on the public Service. It has its own Service, for probes
and for Prometheus. Reach it from your machine:

```bash
kubectl -n agentgw-tutorial port-forward svc/agentgw-ops 19209:9090 &
curl -s http://127.0.0.1:19209/readyz
curl -s http://127.0.0.1:19209/metrics | grep -E '^agentgw_build_info|^go_goroutines'
```

For a real cluster, set `serviceMonitor.enabled` if you run the Prometheus Operator, and import
`deploy/grafana/` and `deploy/prometheus/alerts.yaml` (see the [runbook](../RUNBOOK.md)).

## 6. Play the game through the cluster

Build the two helper programs for your own system. They run in Docker, so you need no Go
installation:

```bash
docker compose build dev
docker compose run --rm --no-deps -e GOOS=$(uname -s | tr A-Z a-z) -e GOARCH=$(uname -m | sed 's/x86_64/amd64/; s/aarch64/arm64/') \
  -e CGO_ENABLED=0 dev go build -o .tutorial-tmp/bin/ ./examples/studiocall ./examples/oauthclient
export PATH="$PWD/.tutorial-tmp/bin:$PATH"
```

Reach the gateway itself and link a player, exactly as in [tutorial 4](04-link-a-player.md). The
studio backend (here, `studiocall`) signs a ticket request with the studio key:

```bash
kubectl -n agentgw-tutorial port-forward svc/agentgw 18209:80 &
GW=http://127.0.0.1:18209

TICKET=$(AGENTGW_SIGNING_SECRET=$SIGNING_SECRET studiocall --url $GW --key-id studio-1 \
  --player alice --expect 201 POST /v1/studio/link-tickets | sed 's/.*"ticket":"\([^"]*\)".*/\1/')
oauthclient --url $GW --ticket "$TICKET" --scope operator
```

The last command prints the tokens. Put the access token in `ACCESS` and call a tool:

```bash
ACCESS=<the access_token value>
curl -s -H "Authorization: Bearer $ACCESS" -H 'Content-Type: application/json' \
  -H 'Accept: application/json, text/event-stream' -H 'MCP-Protocol-Version: 2025-06-18' \
  -d '{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"get_city_state","arguments":{}}}' \
  $GW/mcp
```

The answer is the city's state. The call crossed the Service to one of the two gateway pods, passed
the policy checks with limits kept in Redis, was signed, and reached the backend pod. Selling
something works too, and shows up in the activity feed:

```bash
curl -s -H "Authorization: Bearer $ACCESS" -H 'Content-Type: application/json' \
  -H 'Accept: application/json, text/event-stream' -H 'MCP-Protocol-Version: 2025-06-18' \
  -d '{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"sell_resource","arguments":{"resource":"food","quantity":5}}}' \
  $GW/mcp
AGENTGW_SIGNING_SECRET=$SIGNING_SECRET studiocall --url $GW --key-id studio-1 \
  --player alice --expect 200 GET '/v1/studio/activity?limit=5'
```

## 7. Change the manifest while it runs

Upload the smaller manifest with the admin key. It is stored in Postgres, so both replicas serve it
within `manifestPoll`:

```bash
AGENTGW_ADMIN_SECRET=$ADMIN_SECRET studiocall --url $GW --key-id admin-1 \
  --secret-env AGENTGW_ADMIN_SECRET --player ci --expect 201 \
  --content-type application/yaml --body-file examples/tutorials/city-lite.yaml PUT /v1/studio/manifest
```

List the tools a few times. `sell_resource` is gone, whichever pod answers. The Helm value
`manifest` is only the seed for an empty store. After the first upload the store is the source of
truth, which is why a `helm upgrade` does not undo an upload.

## 8. Kill a pod

```bash
POD=$(kubectl -n agentgw-tutorial get pods -l app.kubernetes.io/name=agentgw -o jsonpath='{.items[0].metadata.name}')
kubectl -n agentgw-tutorial delete pod "$POD"
kubectl -n agentgw-tutorial rollout status deploy/agentgw
```

Kubernetes starts a new pod, and the Service sends traffic only to pods that are ready. Your token
still works and the uploaded manifest is still the active one, because both live in Postgres, not
in the pod. Restart your port forward first, since it was attached to the pod you deleted.

Then restart the whole deployment. The chart uses `maxUnavailable: 0` and a disruption budget of
one, so there is always a ready pod:

```bash
kubectl -n agentgw-tutorial rollout restart deploy/agentgw
kubectl -n agentgw-tutorial rollout status deploy/agentgw
kubectl -n agentgw-tutorial get pdb
```

## 9. Clean up

```bash
helm uninstall agentgw --namespace agentgw-tutorial
kubectl delete namespace agentgw-tutorial
```

Stop the port forwards (`kill %1 %2`, or close the terminal).

## In production

- Use `existingSecret` with sealed-secrets or external-secrets, never `--from-literal` on a shared
  machine. Add `keyFilesSecret` for key rotation without a restart ([tutorial 6](06-go-live.md)).
- Terminate TLS at an Ingress (`ingress.enabled`) and set `config.publicURL` to that HTTPS address.
- Set `config.clientIPHeader` only behind a proxy that overwrites it.
- Point `redisAddr` and `postgres-url` at managed services. Without Redis each replica counts limits
  on its own. Without Postgres, grants and manifests are lost when a pod goes.
- Start with `config.maxScope: coach`, then raise it to `operator`.
- Turn on `networkPolicy` and `serviceMonitor`, and import the dashboards and alerts.

## What you learned

- The chart takes everything secret from one Secret you create, and refuses to install without it.
- Two replicas share limits through Redis and state through Postgres, so a pod can die without
  losing grants, tokens or the active manifest.
- `/readyz` on the ops port decides whether a pod gets traffic.
- A manifest upload changes the running tools without a rollout.

## Next

[Tutorial 6: Go live](06-go-live.md) covers key rotation and the admin key in more depth. The
[runbook](../RUNBOOK.md#deploy-with-helm) has the production checklist, alerts and what to do when
Redis or Postgres is down.
