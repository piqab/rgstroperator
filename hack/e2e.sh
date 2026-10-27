#!/usr/bin/env bash
# End-to-end test in a kind cluster: installs the operator from
# deploy/operator.yaml, creates a Registry with authentication and checks
# that rgstr becomes ready, signs in with the generated admin password,
# accepts a push, reports its version, and keeps its data volume when the
# Registry is deleted.
#
#   hack/e2e.sh                       # rgstr image pulled from GHCR
#   RGSTR_SRC=../rgstr hack/e2e.sh    # rgstr image built from source
#   KEEP=1 hack/e2e.sh                # keep the cluster afterwards
set -euo pipefail
cd "$(dirname "$0")/.."

BIN=$PWD/bin
CLUSTER=${CLUSTER:-rgstr-e2e}
VERSION=$(cat VERSION)
OPERATOR_IMAGE=ghcr.io/piqab/rgstroperator:$VERSION
RGSTR_IMAGE=$(sed -n 's/^const DefaultImage = "\(.*\)"/\1/p' internal/controller/defaults.go)
NS=e2e
PORT=15099
KCTX="--context kind-$CLUSTER"
k() { kubectl $KCTX "$@"; }

step() { printf '\n\033[1m== %s\033[0m\n' "$*"; }
fail() { echo "FAIL: $*" >&2; k -n $NS get registry,deploy,pod,pvc,secret -o wide >&2 || true; k -n $NS logs deploy/e2e --tail=50 >&2 || true; k -n rgstroperator-system logs deploy/rgstroperator --tail=50 >&2 || true; exit 1; }
cleanup() {
  [ -n "${PF:-}" ] && kill "$PF" 2>/dev/null || true
  [ "${KEEP:-}" = 1 ] || "$BIN/kind" delete cluster --name "$CLUSTER" >/dev/null 2>&1 || true
}
trap cleanup EXIT

step "kind cluster $CLUSTER"
"$BIN/kind" get clusters 2>/dev/null | grep -qx "$CLUSTER" || "$BIN/kind" create cluster --name "$CLUSTER" --wait 120s

step "images: $OPERATOR_IMAGE, $RGSTR_IMAGE"
docker build -q --build-arg VERSION="$VERSION" -t "$OPERATOR_IMAGE" . >/dev/null
if [ -n "${RGSTR_SRC:-}" ]; then
  docker build -q -t "$RGSTR_IMAGE" "$RGSTR_SRC" >/dev/null
else
  docker pull -q "$RGSTR_IMAGE" >/dev/null
fi
"$BIN/kind" load docker-image --name "$CLUSTER" "$OPERATOR_IMAGE" "$RGSTR_IMAGE"

step "install the operator"
k apply -f deploy/operator.yaml >/dev/null
k -n rgstroperator-system rollout status deploy/rgstroperator --timeout=120s

step "create a Registry with auth and metrics"
k create namespace $NS --dry-run=client -o yaml | k apply -f - >/dev/null
k apply -f - >/dev/null <<EOF
apiVersion: registry.rgstr.io/v1alpha1
kind: Registry
metadata: {name: e2e, namespace: $NS}
spec:
  imagePullPolicy: Never
  storage: {size: 1Gi}
  auth: {enabled: true}
  metrics: {enabled: true}
EOF
k -n $NS wait registry/e2e --for=jsonpath='{.status.phase}'=Running --timeout=180s || fail "registry not Running"

step "talk to rgstr"
k -n $NS port-forward svc/e2e $PORT:5000 >/dev/null 2>&1 & PF=$!
for _ in $(seq 30); do curl -fs localhost:$PORT/healthz >/dev/null && break; sleep 1; done
R=localhost:$PORT
version=$(curl -fs $R/healthz | sed 's/.*"version":"\([^"]*\)".*/\1/')
echo "rgstr $version"
[ "$(curl -s -o /dev/null -w '%{http_code}' $R/v2/)" = 401 ] || fail "/v2/ should require auth"

temp=$(k -n $NS get secret e2e-auth -o jsonpath='{.data.RGSTR_ADMIN_PASSWORD}' | base64 -d)
[ -n "$temp" ] || fail "no generated admin password"
curl -fs -X POST -d "{\"username\":\"admin\",\"password\":\"$temp\",\"new_password\":\"e2e-Passw0rd\"}" $R/ui/api/password >/dev/null \
  || fail "changing the temporary admin password"

data='hello from e2e'
digest=sha256:$(printf %s "$data" | sha256sum | cut -d' ' -f1)
loc=$(curl -fs -u admin:e2e-Passw0rd -D - -o /dev/null -X POST $R/v2/e2e/app/blobs/uploads/ | tr -d '\r' | sed -n 's/^[Ll]ocation: //p')
curl -fs -u admin:e2e-Passw0rd -X PUT --data-binary "$data" "$R$loc?digest=$digest" >/dev/null || fail "blob upload"
curl -fs -u admin:e2e-Passw0rd -X PUT -H 'Content-Type: application/vnd.oci.image.manifest.v1+json' \
  --data "{\"schemaVersion\":2,\"layers\":[{\"digest\":\"$digest\",\"size\":${#data}}]}" $R/v2/e2e/app/manifests/1.0 >/dev/null \
  || fail "manifest push"
curl -fs -u admin:e2e-Passw0rd $R/v2/_catalog | grep -q '"e2e/app"' || fail "pushed repository not in the catalog"
curl -fs $R/metrics | grep -q 'rgstr_events_total{action="push"} 1' || fail "push not in the metrics"

step "status reports the version"
for _ in $(seq 60); do
  got=$(k -n $NS get registry e2e -o jsonpath='{.status.version}')
  [ "$got" = "$version" ] && break
  k -n $NS annotate registry e2e e2e/poke="$(date +%s%N)" --overwrite >/dev/null
  sleep 2
done
[ "$got" = "$version" ] || fail "status.version is '$got', want '$version'"
kill $PF; PF=

step "deleting the Registry keeps the data"
k -n $NS delete registry e2e --wait=true --timeout=60s >/dev/null
k -n $NS get pvc e2e-data >/dev/null || fail "PVC deleted with the Registry"
k -n $NS get secret e2e-auth >/dev/null || fail "auth Secret deleted with the Registry"

printf '\n\033[32mPASS\033[0m\n'
