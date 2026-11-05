BIN     := droot
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)

# Run privileged targets with sudo unless already root (e.g. in a container
# without sudo).
SUDO := $(if $(filter 0,$(shell id -u)),,sudo)

GOLANGCI_LINT_VERSION := v2.15.0
GORELEASER_VERSION    := v2.20.0

.DEFAULT_GOAL := build

.PHONY: build
build: ## Build a static droot binary
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o $(BIN) ./cmd/droot

.PHONY: test
test: ## Run unit tests (tests that need root are skipped)
	go test -race ./...

.PHONY: test-root
test-root: ## Run unit tests as root, including the ones that mount file systems
	$(SUDO) env "PATH=$$PATH" "GOTOOLCHAIN=$$(go env GOTOOLCHAIN)" "GOFLAGS=$$(go env GOFLAGS)" go test -race -count=1 ./...

.PHONY: integration-test
integration-test: build ## Run end-to-end tests against the local Docker daemon (needs root)
	$(SUDO) ./script/integration-test.sh ./$(BIN)

.PHONY: cover
cover: ## Write a coverage report to coverage.out
	go test -coverprofile=coverage.out ./...
	go tool cover -func=coverage.out | tail -1

.PHONY: vet
vet: ## Run go vet for every supported platform
	go vet ./...
	GOOS=darwin go vet ./...

.PHONY: lint
lint: ## Run golangci-lint
	go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION) run

.PHONY: fmt
fmt: ## Format the code
	go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION) fmt

.PHONY: tidy
tidy: ## Tidy go.mod and go.sum
	go mod tidy

.PHONY: cross
cross: ## Check that droot builds for every released platform
	for p in linux/amd64 linux/arm64 linux/arm darwin/amd64 darwin/arm64; do \
		GOOS=$${p%/*} GOARCH=$${p#*/} GOARM=7 CGO_ENABLED=0 go build -o /dev/null ./cmd/droot || exit 1; \
	done

.PHONY: snapshot
snapshot: ## Build release archives locally into dist/ without publishing
	go run github.com/goreleaser/goreleaser/v2@$(GORELEASER_VERSION) release --snapshot --clean

.PHONY: clean
clean: ## Remove build outputs
	rm -f $(BIN) coverage.out
	rm -rf dist

.PHONY: help
help: ## Show this help
	@grep -E '^[a-z-]+:.*## ' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*## "}; {printf "  %-18s %s\n", $$1, $$2}'
