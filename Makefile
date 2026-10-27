IMAGE   ?= ghcr.io/piqab/rgstroperator
VERSION ?= $(shell cat VERSION)
TAG     ?= $(VERSION)

BIN := $(CURDIR)/bin
CONTROLLER_GEN := $(BIN)/controller-gen
SETUP_ENVTEST  := $(BIN)/setup-envtest
ENVTEST_K8S    ?= 1.31.x

.PHONY: all generate manifests bundle build test e2e run install uninstall \
        sample deploy undeploy docker-build docker-push tools

all: generate manifests bundle build

## Regenerate deepcopy code from the API types
generate: $(CONTROLLER_GEN)
	$(CONTROLLER_GEN) object paths="./..."

## Regenerate the CRD and RBAC from kubebuilder markers
manifests: $(CONTROLLER_GEN)
	$(CONTROLLER_GEN) rbac:roleName=rgstroperator-manager-role crd paths="./..." \
		output:crd:artifacts:config=config/crd/bases output:rbac:artifacts:config=config/rbac

## Assemble deploy/operator.yaml (CRD + RBAC + manager) for VERSION
bundle: manifests
	./hack/bundle.sh

## Build the operator binary
build:
	go build -o bin/manager ./cmd/main.go

## Run unit and envtest tests (downloads a kube-apiserver and etcd once)
test: $(SETUP_ENVTEST)
	KUBEBUILDER_ASSETS="$$($(SETUP_ENVTEST) use $(ENVTEST_K8S) --bin-dir $(CURDIR)/testbin -p path)" \
		go test ./... -coverprofile cover.out

## End-to-end test in a kind cluster (RGSTR_SRC=../rgstr builds rgstr from source)
e2e: bundle $(BIN)/kind
	./hack/e2e.sh

## Run the operator locally against the current kubeconfig
run: install
	go run ./cmd/main.go --leader-elect=false

## Install the CRD (and the operator's namespace) into the cluster
install: manifests
	kubectl apply -f config/crd/bases/
	kubectl apply -f config/manager/namespace.yaml

## Remove the CRD from the cluster (deletes every Registry resource!)
uninstall:
	kubectl delete -f config/crd/bases/ --ignore-not-found

## Apply the sample Registry
sample:
	kubectl apply -f config/samples/registry_v1alpha1_registry.yaml

## Deploy the operator in-cluster from deploy/operator.yaml
deploy: bundle
	kubectl apply -f deploy/operator.yaml

## Remove the operator (Registry resources and their PVCs are kept)
undeploy:
	kubectl delete -f config/manager/manager.yaml --ignore-not-found

## Build the operator image
docker-build:
	docker build --build-arg VERSION=$(VERSION) -t $(IMAGE):$(TAG) .

## Push the operator image
docker-push:
	docker push $(IMAGE):$(TAG)

## Install the development tools into ./bin
tools: $(CONTROLLER_GEN) $(SETUP_ENVTEST)

$(CONTROLLER_GEN):
	GOBIN=$(BIN) go install sigs.k8s.io/controller-tools/cmd/controller-gen@v0.16.5

$(BIN)/kind:
	GOBIN=$(BIN) go install sigs.k8s.io/kind@v0.24.0

$(SETUP_ENVTEST):
	GOBIN=$(BIN) go install sigs.k8s.io/controller-runtime/tools/setup-envtest@release-0.19
