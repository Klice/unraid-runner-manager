BINARY := unraid-runner-manager
MODULE := ./cmd/$(BINARY)
IMAGE ?= ghcr.io/klice/$(BINARY)
TAG ?= dev
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)
GOFLAGS ?= -trimpath

DEV_DATA ?= $(CURDIR)/.dev/config
DEV_RUNNERS ?= $(CURDIR)/.dev/runners
DEV_ADMIN_PASSWORD ?= change-me-please

.DEFAULT_GOAL := help

.PHONY: help
help:
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-16s\033[0m %s\n", $$1, $$2}'

.PHONY: build
build: ## Build the binary into bin/
	CGO_ENABLED=0 go build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o bin/$(BINARY) $(MODULE)

.PHONY: run
run: ## Run locally against the local Docker socket with a dev admin (see DEV_* vars)
	@mkdir -p $(DEV_DATA) $(DEV_RUNNERS)
	DATA_DIR=$(DEV_DATA) RUNNER_DATA_DIR=$(DEV_RUNNERS) RUNNER_DATA_HOST_ROOT=$(DEV_RUNNERS) \
	ADMIN_USERNAME=admin ADMIN_PASSWORD=$(DEV_ADMIN_PASSWORD) UNRAID_HOSTNAME=dev \
	go run $(MODULE)

.PHONY: test
test: ## Run the full test suite with the race detector
	go test -race -count=1 ./...

.PHONY: test-cover
test-cover: ## Run tests and open an HTML coverage report
	go test -race -count=1 -coverprofile=coverage.out ./...
	go tool cover -html=coverage.out -o coverage.html
	@echo "coverage report: coverage.html"

.PHONY: lint
lint: ## Run go vet and golangci-lint
	go vet ./...
	golangci-lint run ./...

.PHONY: fmt
fmt: ## Format code with golangci-lint formatters (gofmt, goimports)
	golangci-lint fmt ./...

.PHONY: tidy
tidy: ## Tidy go.mod and go.sum
	go mod tidy

.PHONY: vuln
vuln: ## Scan dependencies for known vulnerabilities
	go run golang.org/x/vuln/cmd/govulncheck@latest ./...

.PHONY: check
check: lint test ## Lint and test, same as CI

.PHONY: docker-build
docker-build: ## Build the container image locally as $(IMAGE):$(TAG)
	docker build --build-arg VERSION=$(VERSION) -t $(IMAGE):$(TAG) .

.PHONY: docker-run
docker-run: ## Run the locally built image with the Docker socket and dev folders mounted
	@mkdir -p $(DEV_DATA) $(DEV_RUNNERS)
	docker run --rm -p 8080:8080 \
		-v /var/run/docker.sock:/var/run/docker.sock \
		-v $(DEV_DATA):/config \
		-v $(DEV_RUNNERS):/runners \
		-e RUNNER_DATA_HOST_ROOT=$(DEV_RUNNERS) \
		-e ADMIN_PASSWORD=$(DEV_ADMIN_PASSWORD) \
		$(IMAGE):$(TAG)

.PHONY: clean
clean: ## Remove build artifacts and dev data
	rm -rf bin coverage.out coverage.html .dev
