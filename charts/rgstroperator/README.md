# rgstroperator

A Kubernetes operator for [rgstr](https://github.com/piqab/rgstr), a lightweight
OCI/Docker container registry with a web admin. Describe a registry with one
`Registry` resource; the operator creates and keeps up to date everything it
needs.

## Features

- **Registry with a web admin** — users, roles, projects, robot accounts,
  webhooks, retention policies, immutable tags, audit log.
- **Storage** — a PersistentVolumeClaim kept when the Registry is deleted, or an
  S3 bucket (AWS, MinIO, Ceph, Yandex Object Storage, R2).
- **High availability** — several replicas on PostgreSQL and S3; any of them
  serves push and pull, rolling updates without downtime, PodDisruptionBudget,
  spread over nodes and zones.
- **Sign-in** — generated admin password, LDAP / Active Directory, single
  sign-on with OpenID Connect (Keycloak, Authentik, Entra ID, Google), roles
  from groups.
- **Pull-through cache** — projects that cache Docker Hub, GHCR or any registry:
  no Docker Hub pull limits, faster CI, images available when the upstream is down.
- **Vulnerability scanning** — Trivy built in: findings in the web admin,
  pulls of vulnerable images refused per project.
- **Ingress with TLS** — cert-manager or your own certificate.
- **Metrics** — Prometheus, with a ServiceMonitor for the Prometheus Operator.
- **Backups** — to S3 by rgstr itself, CSI VolumeSnapshots on a schedule, or a
  Longhorn RecurringJob.

## Install

```bash
helm install rgstroperator oci://ghcr.io/piqab/charts/rgstroperator \
  -n rgstroperator-system --create-namespace
```

## Create a registry

```yaml
apiVersion: registry.rgstr.io/v1alpha1
kind: Registry
metadata:
  name: registry
spec:
  storage:
    size: 50Gi
  auth:
    enabled: true
  ingress:
    enabled: true
    host: registry.example.com
    tls: {clusterIssuer: letsencrypt}
  proxies:
    - {name: dockerhub, url: https://registry-1.docker.io, public: true}
  scan:
    enabled: true
```

```bash
kubectl get registry registry          # PHASE, READY, VERSION, URL
kubectl get secret registry-auth -o jsonpath='{.data.RGSTR_ADMIN_PASSWORD}' | base64 -d
```

Sign in at `https://registry.example.com/ui` as `admin` with this temporary
password and choose your own.

A highly available registry adds `replicas`, a PostgreSQL URL and a bucket:

```yaml
spec:
  replicas: 3
  database: {urlSecretRef: {name: rgstr-db, key: url}}
  storage:
    s3: {bucket: registry, endpoint: https://storage.example.com, secretRef: {name: rgstr-s3}}
```

## Chart values

| Value | Default | Description |
|---|---|---|
| `image.repository` | `ghcr.io/piqab/rgstroperator` | Operator image |
| `image.tag` | chart's `appVersion` | |
| `replicas` | `1` | Operator replicas (leader election) |
| `resources` | 10m / 64Mi requests, 500m / 256Mi limits | Operator resources |
| `nodeSelector`, `tolerations`, `affinity`, `priorityClassName` | — | Operator pod scheduling |
| `imagePullSecrets` | `[]` | |
| `serviceAccount.name` | release name | |

The full `Registry` reference, cluster mode, backups and restore are in the
[operator README](https://github.com/piqab/rgstroperator#readme); rgstr itself
is documented in the [rgstr README](https://github.com/piqab/rgstr#readme).
Both are also available in Russian.
