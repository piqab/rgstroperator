#!/usr/bin/env bash
# End-to-end test of cluster mode in kind: PostgreSQL and SeaweedFS (S3) in
# the cluster, a Registry with three replicas on them. Pushes an image,
# then pulls it continuously through the Service while the Deployment is
# rolled and pods are deleted: no pull may fail.
#
#   RGSTR_SRC=../rgstr hack/e2e-ha.sh   # rgstr image built from source
#   KEEP=1 hack/e2e-ha.sh               # keep the cluster afterwards
set -euo pipefail
cd "$(dirname "$0")/.."

BIN=$PWD/bin
CLUSTER=${CLUSTER:-rgstr-e2e-ha}
VERSION=$(cat VERSION)
OPERATOR_IMAGE=ghcr.io/piqab/rgstroperator:$VERSION
RGSTR_IMAGE=$(sed -n 's/^const DefaultImage = "\(.*\)"/\1/p' internal/controller/defaults.go)
PG_IMAGE=postgres:16-alpine
S3_IMAGE=chrislusf/seaweedfs:latest
CURL_IMAGE=curlimages/curl:8.10.1
NS=e2e-ha
PORT=15098
k() { kubectl --context "kind-$CLUSTER" "$@"; }

step() { printf '\n\033[1m== %s\033[0m\n' "$*"; }
fail() {
  echo "FAIL: $*" >&2
  k -n $NS get registry,deploy,pod,pdb -o wide >&2 || true
  k -n $NS logs -l app.kubernetes.io/instance=e2e --tail=30 --prefix >&2 || true
  k -n rgstroperator-system logs deploy/rgstroperator --tail=30 >&2 || true
  exit 1
}
cleanup() {
  [ -n "${PF:-}" ] && kill "$PF" 2>/dev/null || true
  [ "${KEEP:-}" = 1 ] || "$BIN/kind" delete cluster --name "$CLUSTER" >/dev/null 2>&1 || true
}
trap cleanup EXIT

step "kind cluster $CLUSTER"
"$BIN/kind" get clusters 2>/dev/null | grep -qx "$CLUSTER" || "$BIN/kind" create cluster --name "$CLUSTER" --wait 120s

step "images"
docker build -q --build-arg VERSION="$VERSION" -t "$OPERATOR_IMAGE" . >/dev/null
if [ -n "${RGSTR_SRC:-}" ]; then
  docker build -q -t "$RGSTR_IMAGE" "$RGSTR_SRC" >/dev/null
else
  docker pull -q "$RGSTR_IMAGE" >/dev/null
fi
"$BIN/kind" load docker-image --name "$CLUSTER" "$OPERATOR_IMAGE" "$RGSTR_IMAGE"
# PostgreSQL, SeaweedFS and curl are pulled by the node itself.

step "operator"
k apply -f deploy/operator.yaml >/dev/null
k -n rgstroperator-system rollout status deploy/rgstroperator --timeout=120s

step "PostgreSQL and S3 in the cluster"
k create namespace $NS --dry-run=client -o yaml | k apply -f - >/dev/null
k apply -n $NS -f - >/dev/null <<EOF
apiVersion: apps/v1
kind: Deployment
metadata: {name: pg}
spec:
  selector: {matchLabels: {app: pg}}
  template:
    metadata: {labels: {app: pg}}
    spec:
      containers:
        - name: pg
          image: $PG_IMAGE
          env: [{name: POSTGRES_PASSWORD, value: pw}, {name: POSTGRES_DB, value: rgstr}]
          readinessProbe: {exec: {command: [pg_isready, -U, postgres]}, periodSeconds: 2}
---
apiVersion: v1
kind: Service
metadata: {name: pg}
spec: {selector: {app: pg}, ports: [{port: 5432}]}
---
apiVersion: v1
kind: Secret
metadata: {name: s3config}
stringData:
  s3.json: '{"identities":[{"name":"e2e","credentials":[{"accessKey":"e2ekey","secretKey":"e2esecret"}],"actions":["Admin","Read","Write","List","Tagging"]}]}'
---
apiVersion: apps/v1
kind: Deployment
metadata: {name: s3}
spec:
  selector: {matchLabels: {app: s3}}
  template:
    metadata: {labels: {app: s3}}
    spec:
      # Service links would set S3_PORT=tcp://…, which SeaweedFS reads as
      # its own setting.
      enableServiceLinks: false
      containers:
        - name: s3
          image: $S3_IMAGE
          args: [server, -s3, -s3.config=/etc/s3/s3.json]
          volumeMounts: [{name: config, mountPath: /etc/s3}]
          readinessProbe: {tcpSocket: {port: 8333}, periodSeconds: 2}
      volumes: [{name: config, secret: {secretName: s3config}}]
---
apiVersion: v1
kind: Service
metadata: {name: s3}
spec: {selector: {app: s3}, ports: [{port: 8333}]}
---
apiVersion: v1
kind: Secret
metadata: {name: rgstr-db}
stringData: {url: "postgres://postgres:pw@pg:5432/rgstr?sslmode=disable"}
---
apiVersion: v1
kind: Secret
metadata: {name: rgstr-s3}
stringData: {AWS_ACCESS_KEY_ID: e2ekey, AWS_SECRET_ACCESS_KEY: e2esecret}
EOF
k -n $NS rollout status deploy/pg --timeout=300s
k -n $NS rollout status deploy/s3 --timeout=300s
for _ in $(seq 30); do
  k -n $NS exec deploy/s3 -- sh -c 'echo "s3.bucket.create -name registry" | weed shell' 2>/dev/null | grep -q 'created bucket' && break
  sleep 2
done

step "Registry with three replicas"
k apply -n $NS -f - >/dev/null <<EOF
apiVersion: registry.rgstr.io/v1alpha1
kind: Registry
metadata: {name: e2e}
spec:
  imagePullPolicy: Never
  replicas: 3
  auth: {enabled: true}
  database: {urlSecretRef: {name: rgstr-db, key: url}}
  storage:
    s3: {bucket: registry, endpoint: "http://s3:8333", secretRef: {name: rgstr-s3}}
EOF
for _ in $(seq 90); do
  [ "$(k -n $NS get registry e2e -o jsonpath='{.status.replicas}')" = 3/3 ] && break
  sleep 2
done
[ "$(k -n $NS get registry e2e -o jsonpath='{.status.replicas}')" = 3/3 ] || fail "replicas not 3/3"
k -n $NS get pdb e2e >/dev/null || fail "no PodDisruptionBudget"
k -n $NS get pvc 2>/dev/null | grep -q e2e && fail "a PVC was created for S3 storage"

step "push"
k -n $NS port-forward svc/e2e $PORT:5000 >/dev/null 2>&1 & PF=$!
for _ in $(seq 30); do curl -fs localhost:$PORT/healthz >/dev/null && break; sleep 1; done
R=localhost:$PORT
temp=$(k -n $NS get secret e2e-auth -o jsonpath='{.data.RGSTR_ADMIN_PASSWORD}' | base64 -d)
curl -fs -X POST -d "{\"username\":\"admin\",\"password\":\"$temp\",\"new_password\":\"e2e-Passw0rd\"}" $R/ui/api/password >/dev/null \
  || fail "changing the temporary admin password"
head -c 3000000 /dev/urandom > /tmp/rgstr-e2e-layer
digest=sha256:$(sha256sum /tmp/rgstr-e2e-layer | cut -d' ' -f1)
size=$(stat -c %s /tmp/rgstr-e2e-layer)
loc=$(curl -fs -u admin:e2e-Passw0rd -D - -o /dev/null -X POST $R/v2/e2e/app/blobs/uploads/ | tr -d '\r' | sed -n 's/^[Ll]ocation: //p')
curl -fs -u admin:e2e-Passw0rd -X PUT --data-binary @/tmp/rgstr-e2e-layer "$R$loc?digest=$digest" >/dev/null || fail "blob upload"
curl -fs -u admin:e2e-Passw0rd -X PUT -H 'Content-Type: application/vnd.oci.image.manifest.v1+json' \
  --data "{\"schemaVersion\":2,\"layers\":[{\"digest\":\"$digest\",\"size\":$size}]}" $R/v2/e2e/app/manifests/1.0 >/dev/null \
  || fail "manifest push"
kill $PF; PF=

step "pull through the Service while pods are replaced"
# Each loop: manifest and the 3 MB layer, checked against its digest.
k -n $NS run puller --image=$CURL_IMAGE --restart=Never --command -- sh -c "
  ok=0; bad=0; end=\$((\$(date +%s)+100))
  while [ \$(date +%s) -lt \$end ]; do
    if curl -fs -u admin:e2e-Passw0rd -H 'Accept: application/vnd.oci.image.manifest.v1+json' http://e2e:5000/v2/e2e/app/manifests/1.0 >/dev/null &&
       [ \"\$(curl -fsL -u admin:e2e-Passw0rd http://e2e:5000/v2/e2e/app/blobs/$digest | sha256sum | cut -d' ' -f1)\" = \"${digest#sha256:}\" ]; then
      ok=\$((ok+1))
    else
      bad=\$((bad+1)); echo \"failed at \$(date +%T)\"
    fi
  done
  echo \"pulls ok=\$ok failed=\$bad\"" >/dev/null
k -n $NS wait pod/puller --for=condition=Ready --timeout=180s >/dev/null
sleep 10
k -n $NS rollout restart deploy/e2e >/dev/null
k -n $NS rollout status deploy/e2e --timeout=180s >/dev/null
for _ in 1 2; do
  sleep 10
  victim=$(k -n $NS get pod -l app.kubernetes.io/instance=e2e -o jsonpath='{.items[0].metadata.name}')
  echo "deleting $victim"
  k -n $NS delete pod "$victim" --wait=false >/dev/null
done
k -n $NS wait pod/puller --for=jsonpath='{.status.phase}'=Succeeded --timeout=180s >/dev/null || fail "puller did not finish"
result=$(k -n $NS logs puller | tail -1)
echo "$result"
k -n $NS logs puller | grep -q '^failed at' && { k -n $NS logs puller | grep '^failed at' >&2; fail "pulls failed while pods were replaced"; }
echo "$result" | grep -q 'ok=[1-9]' || fail "no pull succeeded"

printf '\n\033[32mPASS\033[0m\n'
