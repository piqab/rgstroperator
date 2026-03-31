IMAGE ?= ghcr.io/piqab/rgstroperator
TAG   ?= latest

.PHONY: deps run install-cluster uninstall-cluster sample \
        build test docker-build docker-push deploy undeploy

# ── Local development (no compilation step) ────────────────────────────────

## Fetch Go dependencies
deps:
	go mod tidy

## Install CRD into the cluster, then run the operator locally via go run.
## Requires: kubectl configured + Go installed.
## Usage: make dev [KUBECONFIG=~/.kube/config]
dev: install-cluster
	go run ./cmd/main.go --leader-elect=false

## Just run the operator (CRD assumed already installed)
run:
	go run ./cmd/main.go --leader-elect=false

## Install only CRD + RBAC into the cluster (no operator Deployment)
install-cluster:
	kubectl apply -f config/crd/registry.rgstr.io_registries.yaml
	kubectl apply -f config/manager/serviceaccount.yaml
	kubectl apply -f config/rbac/role.yaml
	kubectl apply -f config/rbac/role_binding.yaml

## Remove CRD + RBAC from the cluster
uninstall-cluster:
	kubectl delete -f config/rbac/role_binding.yaml      --ignore-not-found
	kubectl delete -f config/rbac/role.yaml              --ignore-not-found
	kubectl delete -f config/manager/serviceaccount.yaml --ignore-not-found
	kubectl delete -f config/crd/registry.rgstr.io_registries.yaml --ignore-not-found

## Apply the sample Registry CR
sample:
	kubectl apply -f config/samples/registry_v1alpha1_registry.yaml

# ── In-cluster deployment (needs Docker image) ─────────────────────────────

## Build the operator binary
build:
	go build -o bin/manager ./cmd/main.go

## Run tests
test:
	go test ./... -v

## Build the Docker image
docker-build:
	docker build -t $(IMAGE):$(TAG) .

## Push the Docker image
docker-push:
	docker push $(IMAGE):$(TAG)

## Deploy the operator in-cluster (CRD + RBAC + manager Deployment)
deploy:
	kubectl apply -f config/crd/registry.rgstr.io_registries.yaml
	kubectl apply -f config/manager/serviceaccount.yaml
	kubectl apply -f config/rbac/role.yaml
	kubectl apply -f config/rbac/role_binding.yaml
	kubectl apply -f config/manager/manager.yaml

## Undeploy the operator from the cluster
undeploy:
	kubectl delete -f config/manager/manager.yaml        --ignore-not-found
	kubectl delete -f config/rbac/role_binding.yaml      --ignore-not-found
	kubectl delete -f config/rbac/role.yaml              --ignore-not-found
	kubectl delete -f config/manager/serviceaccount.yaml --ignore-not-found
	kubectl delete -f config/crd/registry.rgstr.io_registries.yaml --ignore-not-found
