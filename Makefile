.PHONY: all build test lint fmt vet generate manifests docker-build docker-push install uninstall run

IMG ?= ghcr.io/prismer-ai/k8s4claw:latest

all: fmt vet build

##@ Development

build: ## Build operator binary.
	go build -o bin/operator ./cmd/operator/

build-ipcbus: ## Build IPC Bus binary.
	go build -o bin/ipcbus ./cmd/ipcbus/

run: ## Run operator locally against the configured cluster.
	go run ./cmd/operator/

ENVTEST_ASSETS ?= $(shell setup-envtest use -p path 2>/dev/null)

test: ## Run tests (requires setup-envtest for controller tests).
	KUBEBUILDER_ASSETS="$(ENVTEST_ASSETS)" go test -race ./internal/...

lint: ## Run linter.
	golangci-lint run ./...

fmt: ## Run gofmt.
	gofmt -w .
	goimports -w .

vet: ## Run go vet.
	go vet ./...

generate: ## Generate deepcopy methods.
	controller-gen object paths="./api/..."

manifests: ## Generate CRD manifests.
	controller-gen crd paths="./api/..." output:crd:artifacts:config=config/crd/bases

##@ Deployment

install: manifests ## Install CRDs into the cluster.
	kubectl apply -f config/crd/bases/

uninstall: ## Uninstall CRDs from the cluster.
	kubectl delete -f config/crd/bases/

docker-build: ## Build docker image.
	docker build -t $(IMG) .

docker-push: ## Push docker image.
	docker push $(IMG)

deploy: manifests ## Deploy operator to the cluster.
	kubectl apply -f config/rbac/
	kubectl apply -f config/manager/

##@ Help

help: ## Display this help.
	@awk 'BEGIN {FS = ":.*##"; printf "\nUsage:\n  make \033[36m<target>\033[0m\n"} /^[a-zA-Z_0-9-]+:.*?##/ { printf "  \033[36m%-15s\033[0m %s\n", $$1, $$2 } /^##@/ { printf "\n\033[1m%s\033[0m\n", substr($$0, 5) } ' $(MAKEFILE_LIST)
