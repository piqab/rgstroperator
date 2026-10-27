# rgstroperator

**English** | [Русский](README.ru.md)

[![Artifact Hub](https://img.shields.io/endpoint?url=https://artifacthub.io/badge/repository/rgstroperator)](https://artifacthub.io/packages/search?repo=rgstroperator)

A Kubernetes operator for [rgstr](https://github.com/piqab/rgstr), a minimalist OCI/Docker container registry.

You describe a registry with a single `Registry` resource. The operator creates and maintains:

- **storage:** a `PersistentVolumeClaim` with the data, which by default survives deletion of the resource;
- **auth secret:** the token signing key and the temporary password of the first administrator are generated automatically;
- **LDAP / Active Directory sign-in and single sign-on (OIDC)** with group-based roles;
- **Deployment and Service** for rgstr, including a [multi-replica cluster](#cluster-ha) on S3 and PostgreSQL;
- **Ingress** with TLS, including via cert-manager;
- Prometheus **metrics** and a `ServiceMonitor`;
- **backups:** to S3 by rgstr itself (no CSI driver needed), scheduled CSI `VolumeSnapshot`s, or a Longhorn `RecurringJob`.

The registry state (phase, URL, rgstr version, last backup) is shown by `kubectl get registry`.

```
$ kubectl get rgstr
NAME          PHASE     READY   VERSION   URL                            IMAGE                       AGE
my-registry   Running   true    v1.10.1   https://registry.example.com   ghcr.io/piqab/rgstr:1.10.1   5m
```

## Installation

**Requirements:** Kubernetes 1.27+.

Option 1. With kubectl, as a single manifest (CRD, RBAC and the operator Deployment):

```bash
kubectl apply -f https://github.com/piqab/rgstroperator/releases/latest/download/operator.yaml
```

Option 2. With Helm:

```bash
helm install rgstroperator oci://ghcr.io/piqab/charts/rgstroperator \
  -n rgstroperator-system --create-namespace
```

Chart settings (resources, nodeSelector, tolerations, imagePullSecrets) are described in [values.yaml](charts/rgstroperator/values.yaml). The chart is also available on [Artifact Hub](https://artifacthub.io/packages/search?repo=rgstroperator).

Verify:

```bash
kubectl -n rgstroperator-system get pods
kubectl -n rgstroperator-system logs deploy/rgstroperator
```

## Quick start

```yaml
apiVersion: registry.rgstr.io/v1alpha1
kind: Registry
metadata:
  name: my-registry
  namespace: default
spec:
  storage:
    size: 20Gi
  auth:
    enabled: true
```

```bash
kubectl apply -f my-registry.yaml
kubectl get registry my-registry -w        # wait for PHASE=Running

# temporary password of the "admin" administrator:
kubectl get secret my-registry-auth -o jsonpath='{.data.RGSTR_ADMIN_PASSWORD}' | base64 -d

kubectl port-forward svc/my-registry 5000:5000
# http://localhost:5000/ui: sign in as admin and set a permanent password
```

A full example with all options is in [config/samples/registry_v1alpha1_registry.yaml](config/samples/registry_v1alpha1_registry.yaml).

## Specification

| Field | Default | Description |
|---|---|---|
| `image` | `ghcr.io/piqab/rgstr:1.10.1` | rgstr image. Defaults to the release this operator version was tested with |
| `imagePullPolicy`, `imagePullSecrets` | `IfNotPresent` | Pulling the rgstr image |
| `replicas` | `1` | `0` stops the registry. More than one only in [cluster mode](#cluster-ha) |
| `proxies[]`, `proxyTTL` | — | Pull-through cache projects, see [Pull-through cache](#pull-through-cache) |
| `scan.*` | off | Vulnerability scanning with Trivy, see [Vulnerability scanning](#vulnerability-scanning) |
| `database.urlSecretRef` | — | PostgreSQL: enables [cluster mode](#cluster-ha) |
| `storage.s3.*` | — | Data in a bucket instead of a volume (cluster) |
| `port` | `5000` | rgstr and Service port |
| `serviceType` | `ClusterIP` | `ClusterIP`, `NodePort` or `LoadBalancer` |
| `storage.size` | `10Gi` | Volume size. Increasing it expands the PVC if the StorageClass allows it |
| `storage.storageClassName` | default class | For example, `longhorn` |
| `storage.accessModes` | `[ReadWriteOnce]` | |
| `storage.existingClaim` | — | Use an existing PVC, for example one restored from a backup |
| `storage.retainOnDelete` | `true` | Keep the PVC and the generated secret when the `Registry` is deleted |
| `auth.enabled` | `false` | Enable authentication |
| `auth.secretRef.name` | `<name>-auth` is created | Your own Secret. All its keys become rgstr environment variables |
| `auth.adminUser` | `admin` | Name of the first administrator |
| `auth.admins`, `auth.readOnlyUsers` | — | Lists of administrators and read-only users |
| `auth.tokenTTL` | as in rgstr | Lifetime of `docker login` tokens |
| `auth.sessionTTL` | as in rgstr (12h) | Lifetime of web admin sessions |
| `auth.realm` | `<URL>/v2/auth` | Token endpoint URL |
| `auth.ldap.*` | — | LDAP / AD password sign-in, see [LDAP](#ldap-and-active-directory) |
| `auth.oidc.*` | — | Web admin sign-in via OpenID Connect, see [Single sign-on](#single-sign-on-oidc) |
| `gcInterval` | `1h` | Garbage collection interval, `0` disables it |
| `uploadTTL` | `24h` | Lifetime of incomplete uploads |
| `publicRepos` | — | Glob patterns of repositories with anonymous pull, for example `public/**` |
| `trustedProxies` | — | Networks trusted for `X-Forwarded-For`, for example the ingress controller CIDR |
| `env` | — | Extra rgstr variables. They are added last and override the others |
| `resources` | — | CPU/memory requests and limits |
| `pod.*` | — | `nodeSelector`, `tolerations`, `affinity`, `securityContext`, `containerSecurityContext`, `annotations`, `labels`, `priorityClassName`, `serviceAccountName` |
| `ingress.*` | disabled | See [Ingress and TLS](#ingress-and-tls) |
| `metrics.*` | disabled | See [Metrics](#metrics) |
| `backup.*` | disabled | See [Backups](#backups) |

Reference for any field, straight from the cluster: `kubectl explain registry.spec.backup`.

### What gets created

| Resource | Name | Deleted with the `Registry` |
|---|---|---|
| `PersistentVolumeClaim` | `<name>-data` or `storage.existingClaim` (not created with `storage.s3`) | no, unless `retainOnDelete: false` is set |
| `Secret` | `<name>-auth` (if there is no `auth.secretRef`) | no, unless `retainOnDelete: false` is set |
| `Deployment`, `Service` | `<name>` | yes |
| `Ingress` | `<name>` | yes |
| `PodDisruptionBudget` | `<name>` (when `replicas` is greater than 1) | yes |
| `ServiceMonitor` | `<name>` | yes |
| `VolumeSnapshot` | `<name>-<date>-<time>` | no |
| Longhorn `RecurringJob` | in `longhorn-system` | yes (via a finalizer) |

The operator keeps the objects it creates in the desired state: manual changes are reverted on the next reconcile, at least every 5 minutes.

## Authentication

With `auth.enabled: true` and no `secretRef`, the operator creates the Secret `<name>-auth`:

- `RGSTR_AUTH_SECRET`: a random token signing key;
- `RGSTR_ADMIN_PASSWORD`: the temporary password of the first administrator. rgstr requires changing it on first sign-in.

The operator never overwrites this Secret. Users, roles and passwords are stored on the rgstr volume and managed through the web admin (`/ui`).

To use your own Secret ([example](config/samples/registry_auth_secret.yaml)):

```yaml
auth:
  enabled: true
  secretRef: {name: my-registry-auth}
```

When the Secret changes, the operator restarts rgstr. The same applies to the Secrets referenced by `auth.ldap`, `auth.oidc` and `backup.s3`.

### LDAP and Active Directory

docker login and the web admin accept the directory password. Users appear in rgstr on first sign-in; local users (the first administrator, `RGSTR_USERS`, users created in the web admin) always take precedence over directory users with the same name.

```yaml
auth:
  enabled: true
  ldap:
    url: ldaps://dc1.corp.example:636
    bindDN: CN=svc-rgstr,OU=Service,DC=corp,DC=example
    bindPasswordSecretRef: {name: rgstr-ldap, key: password}
    baseDN: DC=corp,DC=example
    userFilter: (&(objectClass=user)(sAMAccountName=%s))
    adminGroups: [Registry-Admins]
    allowedGroups: [Developers]      # who is allowed in at all
    # readOnlyGroups: [...]
    # defaultRole: user
```

For OpenLDAP without `memberOf`, set `groupFilter: (&(objectClass=groupOfNames)(member=%s))` and `groupBaseDN`. All fields and examples are in the [rgstr README](https://github.com/piqab/rgstr#ldap-and-active-directory).

### Single sign-on (OIDC)

A "Sign in with …" button appears on the sign-in page. The callback URL defaults to `<status.url>/ui/oidc/callback` and must be registered with the provider.

```yaml
auth:
  enabled: true
  oidc:
    issuer: https://sso.example.com/realms/main
    clientID: rgstr
    clientSecretRef: {name: rgstr-oidc, key: client-secret}
    displayName: Keycloak
    adminGroups: [registry-admins]
```

Docker cannot sign in through a browser: an SSO user creates a **CLI secret** in the web admin (button in the header) and uses it as the password for `docker login`.

## Ingress and TLS

```yaml
ingress:
  enabled: true
  host: registry.example.com
  className: nginx
  tls:
    clusterIssuer: letsencrypt     # cert-manager; or secretName: my-tls
```

- **Why TLS:** without an `insecure-registries` setting, kubelet and Docker pull images only over HTTPS, so TLS is enabled by default.
- **Certificate:** with `clusterIssuer` or `issuer`, cert-manager issues it into the Secret `<name>-tls`.
- **`nginx` class:** the operator removes the request size limit and raises timeouts so that large layers get through. Your own annotations go in `ingress.annotations` and take precedence.
- **What is exposed:** only `/v2`, `/ui`, `/stats`, `/healthz` and `/favicon.ico`. `/metrics` is not exposed.
- **Address:** `status.url` becomes `https://<host>`, and the token realm points to it as well.

If there is another proxy in front of the ingress controller, list its networks in `trustedProxies`. Then the rgstr log and fail2ban see the real client IPs.

## Cluster (HA)

With `spec.database` (PostgreSQL) and `spec.storage.s3`, the registry runs as several pods: any of them accepts push and pull, and a pod failure, node failure or upgrade interrupts neither push nor pull.

```yaml
apiVersion: registry.rgstr.io/v1alpha1
kind: Registry
metadata: {name: registry}
spec:
  replicas: 3
  auth: {enabled: true}
  database:
    urlSecretRef: {name: rgstr-db, key: url}   # postgres://user:pass@host:5432/rgstr?sslmode=require
  storage:
    s3:
      bucket: registry                          # create it beforehand
      endpoint: https://storage.yandexcloud.net # empty: AWS S3
      region: ru-central1
      secretRef: {name: rgstr-s3}               # AWS_ACCESS_KEY_ID, AWS_SECRET_ACCESS_KEY
      # redirect: true   # clients download layers directly from S3, bypassing rgstr
  ingress:
    enabled: true
    host: registry.example.com
    tls: {clusterIssuer: letsencrypt}
```

What the operator does in this mode:

- No PVC is created: data lives in the bucket, state (users, projects, settings, sessions, log) in PostgreSQL. rgstr creates its tables itself.
- The Deployment is updated with `RollingUpdate` and `maxUnavailable: 0`: new pods come up before old ones go away.
- Readiness is `/readyz`: a pod that loses the database or S3 drops out of the Service instead of answering with errors.
- A `PodDisruptionBudget` (`maxUnavailable: 1`) keeps a node drain from evicting all pods at once; pods are spread across nodes and zones (`topologySpreadConstraints`) unless you set your own `pod.affinity`.
- `status.replicas` shows ready pods, for example `3/3`.

High availability of PostgreSQL and S3 themselves is up to them: for example, CloudNativePG or managed PostgreSQL, and S3 with replication. Backups in this mode are database backups plus bucket versioning or replication; `spec.backup` does not apply.

Instead of S3, a `ReadWriteMany` volume (CephFS, NFS) works too: remove `storage.s3` and set `storage.accessModes: [ReadWriteMany]`.

**Migrating an existing registry.** Stop it (`replicas: 0`) and move the data with a Job running `rgstr migrate -from /data`, mounting the old PVC at `/data` and passing the same `RGSTR_DATABASE_URL` and `RGSTR_STORAGE_S3_*`; then set `database` and `storage.s3` in the Registry. Details are in the [rgstr README](https://github.com/piqab/rgstr#cluster-ha).

## Pull-through cache

Projects that cache other registries: `<registry>/<name>/<image>` is fetched from the upstream on the first pull and served by rgstr afterwards. This avoids Docker Hub pull limits and keeps images available when the upstream is down.

```yaml
spec:
  proxies:
    - name: dockerhub
      url: https://registry-1.docker.io
      public: true                              # anonymous pulls through the cache
      credentialsSecretRef: {name: dockerhub}   # optional: keys username, password (token)
    - name: ghcr
      url: https://ghcr.io
  proxyTTL: 5m                                  # how often tags are re-checked upstream
```

```bash
docker pull registry.example.com/dockerhub/library/nginx:1.27
```

More caches can be added in the web admin. Details are in the [rgstr README](https://github.com/piqab/rgstr#pull-through-cache).

## Vulnerability scanning

```yaml
spec:
  scan:
    enabled: true
    interval: 24h      # rescans; new images are scanned on push and when cached
  resources:
    limits: {memory: 1Gi}   # Trivy needs a few hundred MB while scanning
```

The image page of the web admin shows the findings; a project can refuse pulls of vulnerable images in its settings. Trivy's database (about 70 MB) is kept on the data volume, or in an `emptyDir` when the data is in S3. Details are in the [rgstr README](https://github.com/piqab/rgstr#vulnerability-scanning).

## Metrics

```yaml
metrics:
  enabled: true                    # /metrics on the Service (registry port)
  serviceMonitor:
    enabled: true                  # requires Prometheus Operator
    labels: {release: prometheus}  # labels Prometheus uses to select ServiceMonitors
```

The list of metrics is in the [rgstr README](https://github.com/piqab/rgstr#metrics). If the `ServiceMonitor` CRD is missing from the cluster, the `MetricsReady` condition reports it, and the registry itself keeps working.

## Backups

The whole volume is backed up: images, users, metadata and the log. Backups are not owned by the `Registry` and are not deleted with it.

| Method | Required in the cluster | Where backups go |
|---|---|---|
| `S3` | nothing, just a bucket | to S3-compatible storage, outside the cluster |
| `VolumeSnapshot` | a CSI driver with snapshot support and external-snapshotter | usually the same storage system |
| `Longhorn` | Longhorn | the Longhorn backup target (`task: backup`) or locally (`task: snapshot`) |

### S3 (done by rgstr itself)

```yaml
backup:
  enabled: true
  method: S3
  schedule: "0 3 * * *"            # cron, UTC
  keep: 7
  s3:
    bucket: backups                # create it beforehand
    prefix: rgstr/prod
    endpoint: https://storage.yandexcloud.net   # empty: AWS S3
    region: ru-central1
    secretRef: {name: rgstr-s3}    # AWS_ACCESS_KEY_ID, AWS_SECRET_ACCESS_KEY
```

```bash
kubectl create secret generic rgstr-s3 \
  --from-literal=AWS_ACCESS_KEY_ID=... --from-literal=AWS_SECRET_ACCESS_KEY=...
```

rgstr archives the data on schedule while the registry is running and deletes backups beyond `keep`. Backups are consistent: garbage collection waits while a backup is in progress. Any S3-compatible storage works: AWS, MinIO, Ceph, SeaweedFS, Yandex Object Storage, R2. The operator takes the time of the last backup from rgstr's `/healthz` into `status.lastBackupTime`. If the next backup is more than an hour late, the `BackupReady` condition becomes `False` with reason `Overdue`. The list of backups and a "Back up now" button are on the "Backups" page of the web admin.

### VolumeSnapshot (any CSI driver with snapshot support)

```yaml
backup:
  enabled: true
  schedule: "0 3 * * *"            # cron, UTC
  keep: 7
  method: VolumeSnapshot
  volumeSnapshotClassName: csi-snapclass   # empty: default class
```

The operator creates a `VolumeSnapshot` on schedule and deletes old ones beyond `keep`. If the operator was stopped at the scheduled time, the missed backup is taken right after it starts. The times of the last and next backups are shown in `status.lastBackupTime` and `status.nextBackupTime`.

Requires the [external-snapshotter](https://github.com/kubernetes-csi/external-snapshotter) CRDs and controller.

### Longhorn

```yaml
storage:
  storageClassName: longhorn
backup:
  enabled: true
  schedule: "0 3 * * *"
  keep: 7
  method: Longhorn
  longhorn:
    task: backup          # to the Longhorn backup target (S3/NFS, off the node); snapshot: local snapshot
    # namespace: longhorn-system
```

The operator creates a Longhorn `RecurringJob` and attaches the registry PVC to it. Longhorn handles the schedule, creating backups and rotating them. For `task: backup`, a backup target must be configured in Longhorn.

### Restore

From S3: stop the registry and run a Job with `rgstr restore` on the same volume, then start the registry again.

```bash
kubectl patch registry my-registry --type merge -p '{"spec":{"replicas":0}}'
kubectl apply -f restore-job.yaml
kubectl wait --for=condition=complete job/my-registry-restore --timeout=1h
kubectl patch registry my-registry --type merge -p '{"spec":{"replicas":1}}'
```

`restore-job.yaml`:

```yaml
apiVersion: batch/v1
kind: Job
metadata: {name: my-registry-restore}
spec:
  backoffLimit: 0
  template:
    spec:
      restartPolicy: Never
      containers:
        - name: restore
          image: ghcr.io/piqab/rgstr:1.10.1
          args: [restore, -force, latest]   # or a name: rgstr-backup-20261027-030000.tar
          env:
            - {name: RGSTR_BACKUP_TARGET, value: s3://backups/rgstr/prod}
            - {name: RGSTR_BACKUP_S3_ENDPOINT, value: https://storage.yandexcloud.net}
            - {name: RGSTR_BACKUP_S3_REGION, value: ru-central1}
          envFrom: [{secretRef: {name: rgstr-s3}}]
          volumeMounts: [{name: data, mountPath: /data}]
      volumes:
        - {name: data, persistentVolumeClaim: {claimName: my-registry-data}}
```

With `-force`, the previous data is moved to `.before-restore-<time>/` on the volume. Once the restore is verified, it can be deleted. You can also restore into a new empty PVC without `-force` and then set it in `storage.existingClaim`.

From VolumeSnapshot or Longhorn: create a PVC from the backup and set it in `storage.existingClaim`.

From VolumeSnapshot:

```yaml
apiVersion: v1
kind: PersistentVolumeClaim
metadata: {name: my-registry-restored}
spec:
  accessModes: [ReadWriteOnce]
  resources: {requests: {storage: 20Gi}}
  dataSource:
    apiGroup: snapshot.storage.k8s.io
    kind: VolumeSnapshot
    name: my-registry-20261027-030000
```

From Longhorn: in the Longhorn UI, choose **Backup → Restore** and create a PV/PVC for the restored volume.

Then:

```yaml
spec:
  storage:
    existingClaim: my-registry-restored
```

## Status and troubleshooting

```bash
kubectl get rgstr
kubectl describe registry my-registry     # conditions and events
```

| Condition | Meaning |
|---|---|
| `Ready` | The rgstr pod is ready |
| `StorageReady` | The volume is created |
| `AuthReady` | The auth secret is in place |
| `IngressReady` | The Ingress has an address |
| `MetricsReady` | The ServiceMonitor is created |
| `BackupReady` | The backup schedule is working; `False` with reason `Overdue` if an S3 backup is late |

`PHASE` is one of `Pending`, `Running`, `Stopped` (`replicas: 0`) or `Degraded` (one of the parts failed; the reason is in the conditions and events).

## Uninstall

```bash
kubectl delete registry my-registry   # the PVC, auth secret and backups remain
kubectl delete -f https://github.com/piqab/rgstroperator/releases/latest/download/operator.yaml
# or: helm uninstall rgstroperator -n rgstroperator-system
```

Deleting the CRD (`make uninstall`) deletes all `Registry` resources in the cluster.

## Development

**Requirements:** Go 1.26+, make, Docker. e2e also needs kubectl and curl.

Tools (`controller-gen`, `setup-envtest`, `kind`, `helm`) are installed into `./bin` automatically.

| Command | What it does |
|---|---|
| `make generate manifests` | Regenerate deepcopy, CRD and RBAC from kubebuilder markers |
| `make bundle` | Build `deploy/operator.yaml` and update the CRD, RBAC and version in the chart |
| `make test` | Unit and envtest tests (a real kube-apiserver, no cluster) |
| `make e2e` | Full test in kind: install, sign-in, push, metrics, data persistence |
| `make e2e-ha` | Cluster in kind: 3 replicas on PostgreSQL and S3, uninterrupted pull during upgrades and pod deletion |
| `make chart` | `helm lint` and chart package into `dist/` |
| `make run` | Run the operator locally against the current kubeconfig |
| `make install`, `make sample` | Install the CRD and apply the sample |
| `make docker-build` | Build the operator image |

e2e run options:

```bash
RGSTR_SRC=../rgstr make e2e     # rgstr is built from source
INSTALL=helm make e2e           # the operator is installed with the chart
KEEP=1 make e2e                 # keep the kind cluster after the test
```

The CRD, RBAC, `deploy/operator.yaml` and the chart are generated from code; CI checks that they are committed up to date.

### Release

The version is stored in the `VERSION` file. After `make bundle` and a commit:

```bash
git tag v$(cat VERSION) && git push origin v$(cat VERSION)
```

GitHub Actions then:

- builds the image `ghcr.io/piqab/rgstroperator:<version>` for amd64 and arm64;
- creates a Release with `operator.yaml` and the chart package;
- publishes the chart to `oci://ghcr.io/piqab/charts`.

## License

[MIT](LICENSE)
