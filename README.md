# rgstroperator

Kubernetes-оператор для [rgstr](https://github.com/piqab/rgstr) — минималистичного OCI/Docker container registry.

Оператор управляет жизненным циклом реестра через Custom Resource `Registry`:
создаёт `Deployment`, `Service` и `PersistentVolumeClaim`, читает секреты аутентификации из Kubernetes Secret.

---

## Структура проекта

```
rgstroperator/
├── api/v1alpha1/
│   ├── groupversion_info.go          — group/version схема
│   ├── registry_types.go             — CRD типы (Registry, RegistrySpec, RegistryStatus)
│   └── zz_generated_deepcopy.go      — deepcopy
├── internal/controller/
│   └── registry_controller.go        — reconcile loop
├── cmd/main.go                        — entrypoint, manager setup
├── config/
│   ├── crd/registry.rgstr.io_registries.yaml
│   ├── rbac/role.yaml
│   ├── rbac/role_binding.yaml
│   ├── manager/serviceaccount.yaml
│   ├── manager/manager.yaml          — Deployment оператора (для in-cluster)
│   └── samples/
│       ├── registry_v1alpha1_registry.yaml
│       └── registry_auth_secret.yaml
├── Dockerfile
├── Makefile
└── go.mod
```

---

## Варианты запуска

### Вариант 1 — Локально (без сборки образа оператора)

Оператор работает на вашей машине и управляет кластером через `kubeconfig`.
Образ оператора **не нужен**.

**Требования:** Go 1.23+, kubectl, доступ к кластеру.

```bash
# 1. Зависимости
go mod tidy

# 2. Установить CRD и RBAC в кластер
make install-cluster

# 3. Запустить оператор локально
make run
```

В другом терминале:

```bash
# Создать реестр
make sample
# или
kubectl apply -f config/samples/registry_v1alpha1_registry.yaml

# Проверить состояние
kubectl get registries
kubectl get deploy,svc,pvc
```

Для запуска CRD + оператора одной командой:

```bash
make dev
```

---

### Вариант 2 — In-cluster (оператор внутри Kubernetes)

Оператор собирается в Docker-образ, пушится в ghcr.io и деплоится в кластер как `Deployment`.

**Требования:** Docker, kubectl, доступ к ghcr.io (GitHub аккаунт).

#### Шаг 1. Авторизоваться в ghcr.io

Создайте Personal Access Token (PAT) на GitHub:  
**Settings → Developer settings → Personal access tokens → Generate new token**  
Необходимые права: `write:packages`, `read:packages`, `delete:packages`.

```bash
# Сохранить токен в переменную (или вставить вручную при запросе пароля)
export CR_PAT=ghp_YOUR_TOKEN_HERE

# Войти в ghcr.io
echo $CR_PAT | docker login ghcr.io -u YOUR_GITHUB_USERNAME --password-stdin
```

При успехе: `Login Succeeded`.

#### Шаг 2. Собрать образ оператора

```bash
make docker-build IMAGE=ghcr.io/YOUR_GITHUB_USERNAME/rgstroperator TAG=latest
```

Или вручную:

```bash
docker build -t ghcr.io/YOUR_GITHUB_USERNAME/rgstroperator:latest .
```

#### Шаг 3. Запушить образ в ghcr.io

```bash
make docker-push IMAGE=ghcr.io/YOUR_GITHUB_USERNAME/rgstroperator TAG=latest
```

Или вручную:

```bash
docker push ghcr.io/YOUR_GITHUB_USERNAME/rgstroperator:latest
```

После пуша образ появится на `https://github.com/YOUR_GITHUB_USERNAME?tab=packages`.

> **Видимость пакета.** По умолчанию образ **приватный**. Чтобы сделать его публичным:  
> GitHub → ваш пакет → Package settings → Change visibility → Public.  
> Для приватного образа добавьте `imagePullSecret` в [config/manager/manager.yaml](config/manager/manager.yaml).

#### Шаг 4. Указать свой образ в manager.yaml

Отредактируйте [config/manager/manager.yaml](config/manager/manager.yaml):

```yaml
containers:
  - name: manager
    image: ghcr.io/YOUR_USER/rgstroperator:latest  # ← сюда
```

#### Шаг 3. Задеплоить всё в кластер

```bash
make deploy
```

Это применит:
- CRD `registries.registry.rgstr.io`
- `Namespace` + `ServiceAccount` `rgstroperator` в `rgstroperator-system`
- `ClusterRole` + `ClusterRoleBinding`
- `Deployment` оператора

#### Шаг 4. Создать Registry CR

```bash
make sample
# или
kubectl apply -f config/samples/registry_v1alpha1_registry.yaml
```

#### Проверка

```bash
kubectl get registries
kubectl get pods -n rgstroperator-system
kubectl logs -n rgstroperator-system deploy/rgstroperator -f
```

---

### Вариант 3 — In-cluster с образом из локального кластера (kind/minikube)

Для локальных кластеров можно не пушить образ в внешний реестр.

**kind:**

```bash
make docker-build IMAGE=rgstroperator TAG=latest
kind load docker-image rgstroperator:latest
# В manager.yaml: image: rgstroperator:latest, imagePullPolicy: Never
make deploy
```

**minikube:**

```bash
eval $(minikube docker-env)
make docker-build IMAGE=rgstroperator TAG=latest
# В manager.yaml: image: rgstroperator:latest, imagePullPolicy: Never
make deploy
```

---

## Custom Resource: Registry

```yaml
apiVersion: registry.rgstr.io/v1alpha1
kind: Registry
metadata:
  name: my-registry
  namespace: default
spec:
  image: ghcr.io/piqab/rgstr:latest   # образ реестра
  replicas: 1
  port: 5000
  serviceType: ClusterIP               # ClusterIP | NodePort | LoadBalancer

  storage:
    size: 20Gi
    # storageClassName: standard       # если не указан — используется default class

  gcInterval: "1h"
  uploadTTL: "24h"

  # Публичные репозитории (анонимный pull без авторизации)
  # publicRepos: "public/**"

  # Аутентификация
  # auth:
  #   enabled: true
  #   secretRef:
  #     name: my-registry-auth         # Secret с RGSTR_AUTH_SECRET и RGSTR_USERS
  #   tokenTTL: "1h"

  resources:
    requests:
      cpu: "100m"
      memory: "128Mi"
    limits:
      cpu: "500m"
      memory: "512Mi"
```

### Поля spec

| Поле | По умолчанию | Описание |
|---|---|---|
| `image` | `ghcr.io/piqab/rgstr:latest` | Образ реестра |
| `replicas` | `1` | Количество подов |
| `port` | `5000` | Порт реестра |
| `serviceType` | `ClusterIP` | Тип Kubernetes Service |
| `storage.size` | `10Gi` | Размер PVC |
| `storage.storageClassName` | default class | StorageClass |
| `auth.enabled` | `false` | Включить аутентификацию |
| `auth.secretRef.name` | — | Имя Secret с данными auth |
| `auth.tokenTTL` | `1h` | Время жизни JWT-токена |
| `auth.realm` | авто | URL токен-эндпоинта |
| `gcInterval` | `1h` | Интервал сборки мусора |
| `uploadTTL` | `24h` | TTL незавершённых загрузок |
| `publicRepos` | — | Glob-паттерны публичных репо |
| `resources` | — | CPU/memory requests и limits |

---

## Аутентификация

Создайте Secret с bcrypt-хешами паролей (генерация в репозитории [rgstr](https://github.com/piqab/rgstr)):

```bash
# Сгенерировать хеш пароля (в директории rgstr)
go run ./cmd/mkpasswd alice mysecret
# → RGSTR_USERS=alice:$2a$10$...
```

```yaml
apiVersion: v1
kind: Secret
metadata:
  name: my-registry-auth
  namespace: default
type: Opaque
stringData:
  RGSTR_AUTH_SECRET: "your-jwt-signing-secret"
  RGSTR_USERS: "alice:$2a$10$HASH1,bob:$2a$10$HASH2"
```

```yaml
# В Registry CR:
auth:
  enabled: true
  secretRef:
    name: my-registry-auth
```

---

## Доступ к реестру

```bash
# Получить ClusterIP
kubectl get registry my-registry -o jsonpath='{.status.clusterIP}'

# Через port-forward
kubectl port-forward svc/my-registry 5000:5000

docker tag alpine localhost:5000/myrepo/alpine:latest
docker push localhost:5000/myrepo/alpine:latest
docker pull localhost:5000/myrepo/alpine:latest
```

Для HTTP (без TLS) добавьте в `/etc/docker/daemon.json`:

```json
{
  "insecure-registries": ["<clusterIP>:5000"]
}
```

---

## Makefile — команды

| Команда | Описание |
|---|---|
| `make deps` | `go mod tidy` |
| `make run` | Запустить оператор локально (`go run`) |
| `make dev` | `install-cluster` + `run` |
| `make install-cluster` | Применить CRD + RBAC в кластер |
| `make uninstall-cluster` | Удалить CRD + RBAC из кластера |
| `make sample` | Применить пример Registry CR |
| `make build` | Собрать бинарь в `bin/manager` |
| `make test` | Запустить тесты |
| `make docker-build` | Собрать Docker-образ оператора |
| `make docker-push` | Запушить образ |
| `make deploy` | Полный деплой в кластер (CRD + RBAC + Deployment) |
| `make undeploy` | Удалить всё из кластера |

---

## Ресурсы, которыми управляет оператор

Для каждого объекта `Registry` оператор создаёт:

| Ресурс | Имя | Namespace |
|---|---|---|
| `PersistentVolumeClaim` | `<name>-data` | тот же, что CR |
| `Deployment` | `<name>` | тот же, что CR |
| `Service` | `<name>` | тот же, что CR |

Все ресурсы имеют `ownerReference` на `Registry` — при удалении CR все ресурсы удаляются автоматически.
