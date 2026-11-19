#!/usr/bin/env bash
# Verifies tutorial 09 (docs/tutorials/09-run-it-on-kubernetes.md). Run from the repository root:
#   bash docs/tutorials/09-run-it-on-kubernetes.sh
# Needs Docker, kubectl, helm, curl and openssl, and a local Kubernetes cluster that kubectl already
# points at (Docker Desktop with Kubernetes enabled, kind, minikube). It refuses to run unless the
# current kubectl context is docker-desktop; set K8S_CONTEXT to use another local cluster:
#   K8S_CONTEXT=kind-agentgw bash docs/tutorials/09-run-it-on-kubernetes.sh
# Everything goes into one namespace (agentgw-tutorial), which the script deletes on exit. It
# refuses to start if that namespace already exists, so it never deletes something of yours.
set -euo pipefail

step() { printf '\n==> %s\n' "$*"; }
fail() { printf 'FAIL: %s\n' "$*" >&2; exit 1; }
expect() { # expect "text" "needle" : the text must contain the needle
  case "$1" in *"$2"*) printf 'ok: found %s\n' "$2" ;; *) fail "expected to find: $2"$'\n'"got: $1" ;; esac
}

BIN=.tutorial-tmp/bin
mkdir -p "$BIN"
export PATH="$PWD/$BIN:$PATH"

for tool in docker kubectl helm curl openssl; do
  command -v "$tool" >/dev/null || fail "$tool is not installed"
done
OS=$(uname -s | tr '[:upper:]' '[:lower:]')
case "$(uname -m)" in x86_64 | amd64) ARCH=amd64 ;; aarch64 | arm64) ARCH=arm64 ;; *) fail "unsupported CPU $(uname -m)" ;; esac

# --- the commands below are the ones the tutorial shows ---------------------------------

step "1. Check which cluster kubectl points at, and that the namespace is free"
WANT_CONTEXT="${K8S_CONTEXT:-docker-desktop}"
CONTEXT=$(kubectl config current-context 2>/dev/null || true)
echo "kubectl context: ${CONTEXT:-none}"
[ "$CONTEXT" = "$WANT_CONTEXT" ] || fail "kubectl points at '${CONTEXT:-none}', not '$WANT_CONTEXT'. Switch with: kubectl config use-context $WANT_CONTEXT (or set K8S_CONTEXT)"
expect "$(kubectl get nodes)" "Ready"
if kubectl get namespace agentgw-tutorial >/dev/null 2>&1; then
  fail "the namespace agentgw-tutorial already exists. Delete it yourself (kubectl delete namespace agentgw-tutorial) or pick another"
fi

GW_PORT=18209 OPS_PORT=19209   # keeps the script off ports you already use
GW=http://127.0.0.1:$GW_PORT   # must equal config.publicURL in examples/tutorials/k8s/values.yaml
OPS=http://127.0.0.1:$OPS_PORT
PIDS=()
CREATED_NS=""
stop_forwards() { for p in "${PIDS[@]:-}"; do [ -n "$p" ] && kill "$p" 2>/dev/null || true; done; PIDS=(); }
cleanup() {
  stop_forwards
  if [ -n "$CREATED_NS" ]; then
    helm uninstall agentgw --namespace agentgw-tutorial >/dev/null 2>&1 || true
    kubectl delete namespace agentgw-tutorial --wait=true >/dev/null 2>&1 || true
  fi
}
trap cleanup EXIT
forward() { # forward SERVICE LOCAL:REMOTE : port-forward in the background and wait until it answers
  kubectl -n agentgw-tutorial port-forward "svc/$1" "$2" >/dev/null 2>&1 &
  PIDS+=($!)
  for _ in $(seq 1 30); do curl -s -o /dev/null "http://127.0.0.1:${2%%:*}/" && return 0; sleep 1; done
  fail "port-forward to $1 did not come up"
}

step "2. Build the gateway and the demo backend, and make sure the cluster can see them"
docker build -q --target gateway -t agentgw:tutorial .
docker build -q --target city-backend -t city-backend:tutorial .
# A cluster whose nodes are Docker containers (kind, and Docker Desktop's "kind" mode) has its own
# image store, so the images have to be loaded into it. A single node Docker Desktop cluster shares
# the Docker image store, and a cluster in the cloud would pull from a registry instead.
NODE=$(kubectl get nodes -o jsonpath='{.items[0].metadata.name}')
if docker inspect "$NODE" >/dev/null 2>&1; then
  KIND_CLUSTER=${NODE%-control-plane}
  command -v kind >/dev/null || {
    echo "kind is not installed: downloading it into $BIN"
    curl -sSfL -o "$BIN/kind" "https://kind.sigs.k8s.io/dl/v0.25.0/kind-$OS-$ARCH"
    chmod +x "$BIN/kind"
  }
  kind load docker-image agentgw:tutorial city-backend:tutorial --name "$KIND_CLUSTER"
else
  echo "the cluster shares the Docker image store: nothing to load"
fi

step "3. Make the namespace and the Secret (the chart never takes secrets inline)"
kubectl apply -f examples/tutorials/k8s/namespace.yaml
CREATED_NS=yes
SIGNING_SECRET=$(openssl rand -hex 24)
ADMIN_SECRET=$(openssl rand -hex 24)
kubectl -n agentgw-tutorial create secret generic agentgw-credentials \
  --from-literal=signing-secret="$SIGNING_SECRET" \
  --from-literal=admin-secret="$ADMIN_SECRET" \
  --from-literal=postgres-url='postgres://agentgw:agentgw-dev@postgres:5432/agentgw?sslmode=disable'
expect "$(kubectl -n agentgw-tutorial get secret agentgw-credentials -o jsonpath='{.data}')" "signing-secret"

step "4. Start Postgres, Redis and the demo backend"
kubectl apply -f examples/tutorials/k8s/data.yaml -f examples/tutorials/k8s/backend.yaml
kubectl -n agentgw-tutorial rollout status deploy/postgres deploy/redis deploy/city-backend --timeout=180s

step "5. Install the chart"
helm install agentgw deploy/helm/agentgw --namespace agentgw-tutorial \
  -f examples/tutorials/k8s/values.yaml \
  --set-file manifest=examples/city/manifest.yaml \
  --wait --timeout 180s
PODS=$(kubectl -n agentgw-tutorial get pods -l app.kubernetes.io/name=agentgw)
echo "$PODS"
expect "$PODS" "1/1"
READY=$(kubectl -n agentgw-tutorial get deploy agentgw -o jsonpath='{.status.readyReplicas}')
[ "$READY" = 2 ] || fail "want 2 ready replicas, got $READY"

step "6. The ops port answers: health, readiness, metrics"
forward agentgw-ops "$OPS_PORT:9090"
curl -s "$OPS/healthz"; echo
READYZ=$(curl -s "$OPS/readyz"); echo "$READYZ"
expect "$READYZ" "ready"
METRICS=$(curl -s "$OPS/metrics")
expect "$METRICS" "agentgw_build_info"
expect "$METRICS" "go_goroutines"

step "7. Build the two helper programs (for your own OS, inside Docker)"
docker compose --progress quiet -p agentgw-t09 build --quiet dev
docker compose --progress quiet -p agentgw-t09 run --rm -T --no-deps -e GOOS="$OS" -e GOARCH="$ARCH" -e CGO_ENABLED=0 \
  dev go build -o .tutorial-tmp/bin/ ./examples/studiocall ./examples/oauthclient
studio() { AGENTGW_SIGNING_SECRET=$SIGNING_SECRET studiocall --url "$GW" --key-id studio-1 "$@"; }
admin() { AGENTGW_ADMIN_SECRET=$ADMIN_SECRET studiocall --url "$GW" --key-id admin-1 --secret-env AGENTGW_ADMIN_SECRET "$@"; }
mcp() {
  curl -s -H "Authorization: Bearer $ACCESS" -H 'Content-Type: application/json' \
    -H 'Accept: application/json, text/event-stream' -H 'MCP-Protocol-Version: 2025-06-18' \
    -d "$1" "$GW/mcp"
  echo
}
tool_names() { grep -o '"name":"[a-z_]*"' | tr '\n' ' '; echo; }
LIST='{"jsonrpc":"2.0","id":1,"method":"tools/list"}'

step "8. Reach the gateway from your machine and link a player"
forward agentgw "$GW_PORT:80"
OUT=$(curl -s -i -X POST "$GW/mcp" -d '{}')
expect "$OUT" "401"
expect "$OUT" "resource_metadata="
TICKET=$(studio --player alice --expect 201 POST /v1/studio/link-tickets | sed 's/.*"ticket":"\([^"]*\)".*/\1/')
[ -n "$TICKET" ] || fail "no ticket"
TOKENS=$(oauthclient --url "$GW" --ticket "$TICKET" --scope operator)
ACCESS=$(echo "$TOKENS" | sed 's/.*"access_token":"\([^"]*\)".*/\1/')
expect "$TOKENS" '"scope":"operator"'

step "9. A real call goes through the gateway to the backend in the cluster"
TOOLS=$(mcp "$LIST" | tool_names); echo "$TOOLS"
expect "$TOOLS" '"name":"get_city_state"'
expect "$TOOLS" '"name":"sell_resource"'
STATE=$(mcp '{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"get_city_state","arguments":{}}}')
echo "${STATE:0:200}..."
expect "$STATE" 'coins'
SOLD=$(mcp '{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"sell_resource","arguments":{"resource":"food","quantity":5}}}')
echo "$SOLD"
expect "$SOLD" '"ok\":true'
AUDIT=$(studio --player alice --expect 200 GET '/v1/studio/activity?limit=5'); echo "${AUDIT:0:240}..."
expect "$AUDIT" 'sell_resource'

step "10. Upload a smaller manifest with the admin key. Every replica serves it within seconds"
admin --player ci --expect 201 --content-type application/yaml --body-file examples/tutorials/city-lite.yaml PUT /v1/studio/manifest
sleep 5
for _ in 1 2 3 4 5 6; do
  TOOLS=$(mcp "$LIST" | tool_names)
  case "$TOOLS" in *sell_resource*) fail "an old tool list is still served by a replica: $TOOLS" ;; esac
done
echo "$TOOLS"
expect "$TOOLS" '"name":"get_city_state"'

step "11. Delete a gateway pod. Kubernetes starts another and nothing is lost"
POD=$(kubectl -n agentgw-tutorial get pods -l app.kubernetes.io/name=agentgw -o jsonpath='{.items[0].metadata.name}')
echo "deleting $POD"
stop_forwards
kubectl -n agentgw-tutorial delete pod "$POD" --wait=true
kubectl -n agentgw-tutorial rollout status deploy/agentgw --timeout=180s
READY=$(kubectl -n agentgw-tutorial get deploy agentgw -o jsonpath='{.status.readyReplicas}')
[ "$READY" = 2 ] || fail "want 2 ready replicas after the restart, got $READY"
forward agentgw "$GW_PORT:80"
forward agentgw-ops "$OPS_PORT:9090"
expect "$(curl -s "$OPS/readyz")" "ready"
TOOLS=$(mcp "$LIST" | tool_names); echo "$TOOLS"
expect "$TOOLS" '"name":"get_city_state"'
case "$TOOLS" in *sell_resource*) fail "the uploaded manifest was lost in the restart" ;; esac
echo "ok: the token and the uploaded manifest survived (they live in Postgres, not in the pod)"

step "12. Restart every pod with a rolling update. The service stays up"
kubectl -n agentgw-tutorial rollout restart deploy/agentgw
kubectl -n agentgw-tutorial rollout status deploy/agentgw --timeout=180s
stop_forwards
forward agentgw "$GW_PORT:80"
expect "$(mcp "$LIST" | tool_names)" '"name":"get_city_state"'

step "13. The disruption budget keeps one pod up during maintenance"
PDB=$(kubectl -n agentgw-tutorial get pdb); echo "$PDB"
expect "$PDB" "agentgw"

step "14. Remove everything the tutorial made"
stop_forwards
helm uninstall agentgw --namespace agentgw-tutorial
kubectl delete namespace agentgw-tutorial --wait=true
CREATED_NS=""
if kubectl get namespace agentgw-tutorial >/dev/null 2>&1; then fail "the namespace is still there"; fi
echo "ok: namespace deleted"

printf '\nPASS: tutorial 09\n'
