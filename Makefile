.PHONY: all build test lint fmt vet generate manifests docker-build docker-push install uninstall run

IMG ?= ghcr.io/prismer-ai/k8s4claw:latest
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
INIT_IMG ?= ghcr.io/prismer-ai/claw-init:$(VERSION)
IPCBUS_IMG ?= ghcr.io/prismer-ai/claw-ipcbus:$(VERSION)
SLACK_IMG ?= ghcr.io/prismer-ai/claw-channel-slack:$(VERSION)
DISCORD_IMG ?= ghcr.io/prismer-ai/claw-channel-discord:$(VERSION)
WEBHOOK_IMG ?= ghcr.io/prismer-ai/claw-channel-webhook:$(VERSION)
OPENCLAW_IMG ?= ghcr.io/prismer-ai/k8s4claw-openclaw:$(VERSION)
LDFLAGS := -X github.com/Prismer-AI/k8s4claw/internal/runtime.InitContainerImage=$(INIT_IMG)

all: fmt vet build

##@ Development

build: ## Build operator binary.
	go build -ldflags "$(LDFLAGS)" -o bin/operator ./cmd/operator/

build-ipcbus: ## Build IPC Bus binary.
	go build -o bin/ipcbus ./cmd/ipcbus/

build-claw4k8s: ## Build claw4k8s companion binary.
	go build -o bin/claw4k8s ./cmd/claw4k8s/

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

docker-build: ## Build operator docker image.
	docker build -t $(IMG) .

docker-build-init: ## Build claw-init docker image.
	docker build -t $(INIT_IMG) -f Dockerfile.init .

docker-build-ipcbus: ## Build IPC bus docker image.
	docker build -t $(IPCBUS_IMG) -f Dockerfile.ipcbus .

docker-build-slack: ## Build Slack channel sidecar docker image.
	docker build -t $(SLACK_IMG) -f Dockerfile.channel-slack .

docker-build-discord: ## Build Discord channel sidecar docker image.
	docker build -t $(DISCORD_IMG) -f Dockerfile.channel-discord .

docker-build-webhook: ## Build webhook channel sidecar docker image.
	docker build -t $(WEBHOOK_IMG) -f Dockerfile.channel-webhook .

docker-build-openclaw: ## Build OpenClaw runtime docker image.
	docker build -t $(OPENCLAW_IMG) -f runtimes/openclaw/Dockerfile runtimes/openclaw/

docker-build-all: docker-build docker-build-init docker-build-ipcbus docker-build-slack docker-build-discord docker-build-webhook docker-build-openclaw ## Build all docker images.

docker-push: ## Push operator docker image.
	docker push $(IMG)

docker-push-all: docker-push ## Push all docker images.
	docker push $(INIT_IMG)
	docker push $(IPCBUS_IMG)
	docker push $(SLACK_IMG)
	docker push $(DISCORD_IMG)
	docker push $(WEBHOOK_IMG)
	docker push $(OPENCLAW_IMG)

deploy: manifests ## Deploy operator to the cluster.
	kubectl apply -f config/rbac/
	kubectl apply -f config/manager/

##@ Help

help: ## Display this help.
	@awk 'BEGIN {FS = ":.*##"; printf "\nUsage:\n  make \033[36m<target>\033[0m\n"} /^[a-zA-Z_0-9-]+:.*?##/ { printf "  \033[36m%-15s\033[0m %s\n", $$1, $$2 } /^##@/ { printf "\n\033[1m%s\033[0m\n", substr($$0, 5) } ' $(MAKEFILE_LIST)
