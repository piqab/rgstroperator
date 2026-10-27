# rgstroperator

[![Artifact Hub](https://img.shields.io/endpoint?url=https://artifacthub.io/badge/repository/rgstroperator)](https://artifacthub.io/packages/search?repo=rgstroperator)

Kubernetes-оператор для [rgstr](https://github.com/piqab/rgstr), минималистичного OCI/Docker container registry.

Вы описываете реестр одним ресурсом `Registry`. Оператор создаёт и поддерживает:

- **хранилище:** `PersistentVolumeClaim` с данными, который по умолчанию переживает удаление ресурса;
- **секрет аутентификации:** ключ подписи токенов и временный пароль первого администратора генерируются сами;
- **Deployment и Service** rgstr;
- **Ingress** с TLS, в том числе через cert-manager;
- **метрики** Prometheus и `ServiceMonitor`;
- **резервные копии** тома: CSI `VolumeSnapshot` по расписанию или `RecurringJob` в Longhorn.

Состояние реестра (фаза, URL, версия rgstr, последняя копия) видно в `kubectl get registry`.

```
$ kubectl get rgstr
NAME          PHASE     READY   VERSION   URL                            IMAGE                       AGE
my-registry   Running   true    v1.7.0    https://registry.example.com   ghcr.io/piqab/rgstr:1.7.0   5m
```

## Установка

**Требования:** Kubernetes 1.27+.

Вариант 1. Через kubectl, одним манифестом (CRD, RBAC и Deployment оператора):

```bash
kubectl apply -f https://github.com/piqab/rgstroperator/releases/latest/download/operator.yaml
```

Вариант 2. Через Helm:

```bash
helm install rgstroperator oci://ghcr.io/piqab/charts/rgstroperator \
  -n rgstroperator-system --create-namespace
```

Настройки чарта (ресурсы, nodeSelector, tolerations, imagePullSecrets) описаны в [values.yaml](charts/rgstroperator/values.yaml). Чарт также есть на [Artifact Hub](https://artifacthub.io/packages/search?repo=rgstroperator).

Проверка:

```bash
kubectl -n rgstroperator-system get pods
kubectl -n rgstroperator-system logs deploy/rgstroperator
```

## Быстрый старт

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
kubectl get registry my-registry -w        # ждём PHASE=Running

# временный пароль администратора "admin":
kubectl get secret my-registry-auth -o jsonpath='{.data.RGSTR_ADMIN_PASSWORD}' | base64 -d

kubectl port-forward svc/my-registry 5000:5000
# http://localhost:5000/ui: войти как admin и задать постоянный пароль
```

Полный пример со всеми опциями лежит в [config/samples/registry_v1alpha1_registry.yaml](config/samples/registry_v1alpha1_registry.yaml).

## Спецификация

| Поле | По умолчанию | Описание |
|---|---|---|
| `image` | `ghcr.io/piqab/rgstr:1.7.0` | Образ rgstr. По умолчанию это релиз, с которым протестирована эта версия оператора |
| `imagePullPolicy`, `imagePullSecrets` | `IfNotPresent` | Скачивание образа rgstr |
| `replicas` | `1` | `1` запускает реестр, `0` останавливает. rgstr работает одним процессом на том, поэтому больше одной реплики не бывает |
| `port` | `5000` | Порт rgstr и Service |
| `serviceType` | `ClusterIP` | `ClusterIP`, `NodePort` или `LoadBalancer` |
| `storage.size` | `10Gi` | Размер тома. Увеличение расширяет PVC, если StorageClass это позволяет |
| `storage.storageClassName` | класс по умолчанию | Например, `longhorn` |
| `storage.accessModes` | `[ReadWriteOnce]` | |
| `storage.existingClaim` | — | Использовать существующий PVC, например восстановленный из копии |
| `storage.retainOnDelete` | `true` | Сохранять PVC и сгенерированный секрет при удалении `Registry` |
| `auth.enabled` | `false` | Включить аутентификацию |
| `auth.secretRef.name` | создаётся `<name>-auth` | Свой Secret. Все его ключи становятся переменными окружения rgstr |
| `auth.adminUser` | `admin` | Имя первого администратора |
| `auth.admins`, `auth.readOnlyUsers` | — | Списки администраторов и пользователей только на чтение |
| `auth.tokenTTL` | как в rgstr | Время жизни токенов `docker login` |
| `auth.sessionTTL` | как в rgstr (12h) | Время жизни сессий веб-админки |
| `auth.realm` | `<URL>/v2/auth` | URL токен-эндпоинта |
| `gcInterval` | `1h` | Интервал сборки мусора, `0` выключает её |
| `uploadTTL` | `24h` | Время жизни незавершённых загрузок |
| `publicRepos` | — | Glob-паттерны репозиториев с анонимным pull, например `public/**` |
| `trustedProxies` | — | Сети, которым доверяется `X-Forwarded-For`, например CIDR ingress-контроллера |
| `env` | — | Дополнительные переменные rgstr. Они добавляются последними и переопределяют остальные |
| `resources` | — | Запросы и лимиты CPU/памяти |
| `pod.*` | — | `nodeSelector`, `tolerations`, `affinity`, `securityContext`, `containerSecurityContext`, `annotations`, `labels`, `priorityClassName`, `serviceAccountName` |
| `ingress.*` | выключен | См. [Ingress и TLS](#ingress-и-tls) |
| `metrics.*` | выключены | См. [Метрики](#метрики) |
| `backup.*` | выключены | См. [Резервные копии](#резервные-копии) |

Справка по любому полю прямо из кластера: `kubectl explain registry.spec.backup`.

### Что создаётся

| Ресурс | Имя | Удаляется вместе с `Registry` |
|---|---|---|
| `PersistentVolumeClaim` | `<name>-data` или `storage.existingClaim` | нет, если не задано `retainOnDelete: false` |
| `Secret` | `<name>-auth` (если нет `auth.secretRef`) | нет, если не задано `retainOnDelete: false` |
| `Deployment`, `Service` | `<name>` | да |
| `Ingress` | `<name>` | да |
| `ServiceMonitor` | `<name>` | да |
| `VolumeSnapshot` | `<name>-<дата>-<время>` | нет |
| Longhorn `RecurringJob` | в `longhorn-system` | да (через finalizer) |

Оператор поддерживает созданные объекты в нужном состоянии: ручные изменения откатываются при следующей сверке, не реже раза в 5 минут.

## Аутентификация

С `auth.enabled: true` и без `secretRef` оператор создаёт Secret `<name>-auth`:

- `RGSTR_AUTH_SECRET`: случайный ключ подписи токенов;
- `RGSTR_ADMIN_PASSWORD`: временный пароль первого администратора. При первом входе rgstr требует его сменить.

Оператор никогда не перезаписывает этот Secret. Пользователи, роли и пароли хранятся в томе rgstr, а управляются через веб-админку (`/ui`).

Свой Secret ([пример](config/samples/registry_auth_secret.yaml)) подключается так:

```yaml
auth:
  enabled: true
  secretRef: {name: my-registry-auth}
```

При изменении Secret оператор перезапускает rgstr.

## Ingress и TLS

```yaml
ingress:
  enabled: true
  host: registry.example.com
  className: nginx
  tls:
    clusterIssuer: letsencrypt     # cert-manager; или secretName: my-tls
```

- **Зачем TLS:** kubelet и Docker без настройки `insecure-registries` скачивают образы только по HTTPS, поэтому TLS включён по умолчанию.
- **Сертификат:** с `clusterIssuer` или `issuer` его выпускает cert-manager в Secret `<name>-tls`.
- **Класс `nginx`:** оператор снимает лимит на размер запроса и увеличивает таймауты, чтобы проходили большие слои. Свои аннотации задаются в `ingress.annotations` и имеют приоритет.
- **Что публикуется:** только `/v2`, `/ui`, `/stats`, `/healthz` и `/favicon.ico`. `/metrics` наружу не публикуется.
- **Адрес:** `status.url` становится `https://<host>`, и на него же указывает realm токенов.

Если перед ingress-контроллером есть ещё прокси, укажите его сети в `trustedProxies`. Тогда в журнале и для fail2ban в rgstr будут реальные IP клиентов.

## Метрики

```yaml
metrics:
  enabled: true                    # /metrics на Service (порт registry)
  serviceMonitor:
    enabled: true                  # нужен Prometheus Operator
    labels: {release: prometheus}  # по каким меткам Prometheus выбирает ServiceMonitor
```

Список метрик приведён в [README rgstr](https://github.com/piqab/rgstr#метрики). Если CRD `ServiceMonitor` в кластере нет, условие `MetricsReady` об этом сообщит, а сам реестр продолжит работать.

## Резервные копии

Копируется весь том: образы, пользователи, метаданные и журнал. Копии не принадлежат `Registry` и не удаляются вместе с ним.

### VolumeSnapshot (любой CSI-драйвер со snapshot-ами)

```yaml
backup:
  enabled: true
  schedule: "0 3 * * *"            # cron, UTC
  keep: 7
  method: VolumeSnapshot
  volumeSnapshotClassName: csi-snapclass   # пусто: класс по умолчанию
```

Оператор сам создаёт `VolumeSnapshot` по расписанию и удаляет старые сверх `keep`. Если оператор был остановлен в момент запуска, пропущенная копия делается сразу после старта. Время последней и следующей копии видно в `status.lastBackupTime` и `status.nextBackupTime`.

Нужны CRD и контроллер [external-snapshotter](https://github.com/kubernetes-csi/external-snapshotter).

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
    task: backup          # в backup target Longhorn (S3/NFS, вне узла); snapshot: локальный снимок
    # namespace: longhorn-system
```

Оператор создаёт в Longhorn `RecurringJob` и подписывает на него PVC реестра. Расписание, создание копий и их ротацию берёт на себя Longhorn. Для `task: backup` в Longhorn должен быть настроен backup target.

### Восстановление

Создайте PVC из копии и укажите его в `storage.existingClaim`.

Из VolumeSnapshot:

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

Из Longhorn: в UI Longhorn выберите **Backup → Restore** и создайте PV/PVC для восстановленного тома.

Затем:

```yaml
spec:
  storage:
    existingClaim: my-registry-restored
```

## Статус и диагностика

```bash
kubectl get rgstr
kubectl describe registry my-registry     # условия и события
```

| Условие | Смысл |
|---|---|
| `Ready` | Под rgstr готов |
| `StorageReady` | Том создан |
| `AuthReady` | Секрет аутентификации на месте |
| `IngressReady` | Ingress получил адрес |
| `MetricsReady` | ServiceMonitor создан |
| `BackupReady` | Расписание копий работает |

`PHASE` принимает значения `Pending`, `Running`, `Stopped` (`replicas: 0`) или `Degraded` (одна из частей не удалась, причина есть в условиях и событиях).

## Удаление

```bash
kubectl delete registry my-registry   # PVC, auth-секрет и копии остаются
kubectl delete -f https://github.com/piqab/rgstroperator/releases/latest/download/operator.yaml
# или: helm uninstall rgstroperator -n rgstroperator-system
```

Удаление CRD (`make uninstall`) удаляет все ресурсы `Registry` в кластере.

## Разработка

**Требования:** Go 1.23+, make, Docker. Для e2e нужны также kubectl и curl.

Инструменты (`controller-gen`, `setup-envtest`, `kind`, `helm`) ставятся в `./bin` автоматически.

| Команда | Что делает |
|---|---|
| `make generate manifests` | Перегенерировать deepcopy, CRD и RBAC из маркеров kubebuilder |
| `make bundle` | Собрать `deploy/operator.yaml` и обновить CRD, RBAC и версию в чарте |
| `make test` | Unit- и envtest-тесты (настоящий kube-apiserver, без кластера) |
| `make e2e` | Полный тест в kind: установка, вход, push, метрики, сохранность данных |
| `make chart` | `helm lint` и пакет чарта в `dist/` |
| `make run` | Запустить оператор локально против текущего kubeconfig |
| `make install`, `make sample` | Установить CRD и применить пример |
| `make docker-build` | Собрать образ оператора |

Варианты запуска e2e:

```bash
RGSTR_SRC=../rgstr make e2e     # rgstr собирается из исходников
INSTALL=helm make e2e           # оператор ставится чартом
KEEP=1 make e2e                 # оставить кластер kind после теста
```

CRD, RBAC, `deploy/operator.yaml` и чарт генерируются из кода, CI проверяет, что они закоммичены актуальными.

### Релиз

Версия хранится в файле `VERSION`. После `make bundle` и коммита:

```bash
git tag v$(cat VERSION) && git push origin v$(cat VERSION)
```

GitHub Actions после этого:

- собирает образ `ghcr.io/piqab/rgstroperator:<версия>` для amd64 и arm64;
- создаёт Release с `operator.yaml` и пакетом чарта;
- публикует чарт в `oci://ghcr.io/piqab/charts`.

## Лицензия

[MIT](LICENSE)
