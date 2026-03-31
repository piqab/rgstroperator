# rgstroperator

Kubernetes-оператор для [rgstr](https://github.com/piqab/rgstr) — минималистичного OCI/Docker container registry.

Оператор управляет жизненным циклом реестра через Custom Resource `Registry`:
создаёт `Deployment`, `Service` и `PersistentVolumeClaim`, читает секреты аутентификации из Kubernetes Secret.

---

## Быстрый деплой через kubectl (образ уже запушен)

Образ оператора: `ghcr.io/piqab/rgstroperator:latest`

### 1. Установить CRD, RBAC и Namespace

```bash
kubectl apply -f config/crd/registry.rgstr.io_registries.yaml
kubectl apply -f config/manager/serviceaccount.yaml
kubectl apply -f config/rbac/role.yaml
kubectl apply -f config/rbac/role_binding.yaml
```

### 2. Задеплоить оператор

```bash
kubectl apply -f config/manager/manager.yaml
```

Проверить, что под поднялся:

```bash
kubectl get pods -n rgstroperator-system
kubectl logs -n rgstroperator-system deploy/rgstroperator -f
```

### 3. Создать Registry CR

```bash
kubectl apply -f config/samples/registry_v1alpha1_registry.yaml
```

Проверить состояние:

```bash
kubectl get registries
kubectl get deploy,svc,pvc
```

### 4. Доступ к реестру

```bash
kubectl port-forward svc/my-registry 5000:5000
docker push localhost:5000/myrepo/image:tag
```

### Удалить всё

```bash
kubectl delete -f config/samples/registry_v1alpha1_registry.yaml
kubectl delete -f config/manager/manager.yaml
kubectl delete -f config/rbac/role_binding.yaml
kubectl delete -f config/rbac/role.yaml
kubectl delete -f config/manager/serviceaccount.yaml
kubectl delete -f config/crd/registry.rgstr.io_registries.yaml
```

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

Оператор собирается в Docker-образ, пушится в container registry и деплоится в кластер как `Deployment`.

**Требования:** Docker, kubectl.

Выберите реестр из вариантов ниже, затем переходите к [Шагу 2](#шаг-2-собрать-образ).

---

#### Реестры — авторизация

**ghcr.io (GitHub)**

Создайте PAT: **GitHub → Settings → Developer settings → Personal access tokens → Generate new token**  
Права: `write:packages`, `read:packages`.

```bash
export CR_PAT=ghp_YOUR_TOKEN
echo $CR_PAT | docker login ghcr.io -u YOUR_GITHUB_USERNAME --password-stdin
```

Имя образа: `ghcr.io/YOUR_GITHUB_USERNAME/rgstroperator:latest`

---

**Docker Hub**

```bash
docker login -u YOUR_DOCKERHUB_USERNAME
# введите пароль или Access Token (hub.docker.com → Account Settings → Security)
```

Имя образа: `YOUR_DOCKERHUB_USERNAME/rgstroperator:latest`

---

**GitLab Container Registry**

```bash
docker login registry.gitlab.com -u YOUR_GITLAB_USERNAME -p YOUR_ACCESS_TOKEN
```

Имя образа: `registry.gitlab.com/YOUR_NAMESPACE/YOUR_PROJECT/rgstroperator:latest`

---

**Yandex Container Registry**

```bash
# Авторизация через yc CLI
yc iam create-token | docker login --username iam --password-stdin cr.yandex
```

Имя образа: `cr.yandex/YOUR_REGISTRY_ID/rgstroperator:latest`

---

**Свой реестр (self-hosted)**

Если у вас уже развёрнут реестр (например, сам rgstr или distribution/distribution):

```bash
docker login YOUR_REGISTRY_HOST:PORT -u user -p password
```

Имя образа: `YOUR_REGISTRY_HOST:PORT/rgstroperator:latest`

Для HTTP-реестра без TLS добавьте в `/etc/docker/daemon.json`:

```json
{
  "insecure-registries": ["YOUR_REGISTRY_HOST:PORT"]
}
```

---

#### Шаг 2. Собрать образ

Подставьте имя образа из выбранного реестра:

```bash
make docker-build IMAGE=<имя_образа> TAG=latest
```

Пример для Docker Hub:

```bash
make docker-build IMAGE=myuser/rgstroperator TAG=latest
```

#### Шаг 3. Запушить образ

```bash
make docker-push IMAGE=<имя_образа> TAG=latest
```

#### Шаг 4. Указать образ в manager.yaml

Отредактируйте [config/manager/manager.yaml](config/manager/manager.yaml):

```yaml
containers:
  - name: manager
    image: <имя_образа>:latest  # ← сюда
```

> **Приватный реестр.** Если образ в приватном реестре, создайте `imagePullSecret` и добавьте его в `manager.yaml`:
>
> ```bash
> kubectl create secret docker-registry regcred \
>   --docker-server=<registry-host> \
>   --docker-username=<user> \
>   --docker-password=<password> \
>   -n rgstroperator-system
> ```
>
> ```yaml
> spec:
>   imagePullSecrets:
>     - name: regcred
> ```

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
