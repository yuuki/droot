BIN     := droot
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)

# Run privileged targets with sudo unless already root (e.g. in a container
# without sudo).
SUDO := $(if $(filter 0,$(shell id -u)),,sudo)

# Tool versions. The workflows (.github/workflows) pin the same versions:
# bump them together.
GOLANGCI_LINT_VERSION := v2.15.0
GORELEASER_VERSION    := v2.20.0
GOVULNCHECK_VERSION   := v1.9.0
ACTIONLINT_VERSION    := v1.7.12
ZIZMOR_VERSION        := 1.32.0

# golangci-lint refuses a module whose go line is newer than the Go it was
# built with, and `go run tool@version` builds a tool with the Go of the
# tool's own go.mod: build it with the Go of this module, or a newer one if
# the tool needs it (+auto).
GO_TOOLCHAIN  = $(shell go env GOVERSION)
GOLANGCI_LINT = GOTOOLCHAIN=$(GO_TOOLCHAIN)+auto go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION)
# uvx, or pipx (preinstalled on GitHub-hosted runners), runs the pinned zizmor.
ZIZMOR        = $(if $(shell command -v uvx 2>/dev/null),uvx zizmor@$(ZIZMOR_VERSION),pipx run zizmor==$(ZIZMOR_VERSION))
# Extra zizmor options, e.g. --format=github for annotations in CI.
ZIZMOR_FLAGS ?=

FUZZTIME    ?= 30s
BENCH_COUNT ?= 6

.DEFAULT_GOAL := build

.PHONY: build
build: ## Build a static droot binary
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o $(BIN) ./cmd/droot

.PHONY: check
check: lint lint-actions vet test-scripts test-race ## Pre-PR gate: lint, workflow audit, vet, script and race tests (no root or Docker)

.PHONY: test
test: ## Run unit tests (tests that need root are skipped)
	go test -race ./...

.PHONY: test-race
test-race: test ## Alias of test, which always uses the race detector

.PHONY: test-root
test-root: ## Run unit tests as root, including the ones that mount file systems
	$(SUDO) env "PATH=$$PATH" "GOTOOLCHAIN=$$(go env GOTOOLCHAIN)" "GOFLAGS=$$(go env GOFLAGS)" go test -race -count=1 ./...

.PHONY: integration-test
integration-test: build ## Run end-to-end tests against the local Docker daemon (needs root)
	$(SUDO) ./script/integration-test.sh ./$(BIN)

.PHONY: test-integration
test-integration: integration-test ## Alias of integration-test

.PHONY: docs
docs: ## Regenerate documentation/cli.md and documentation/droot.1 from the command definitions
	go run ./internal/cmd/gendocs documentation

# go test -list runs on its own so that a package that does not build fails
# the target instead of silently having no fuzz targets.
.PHONY: fuzz
fuzz: ## Run every fuzz target for FUZZTIME (default 30s)
	@set -e; for pkg in $$(go list ./...); do \
		targets=$$(go test -list '^Fuzz' $$pkg); \
		for f in $$(echo "$$targets" | grep '^Fuzz' || true); do \
			echo "== $$pkg $$f"; \
			go test -run='^$$' -fuzz="^$$f\$$" -fuzztime=$(FUZZTIME) -fuzzminimizetime=5s $$pkg; \
		done; \
	done

.PHONY: test-fuzz
test-fuzz: fuzz ## Alias of fuzz

# The output goes to bench.txt first: a pipe to tee would hide a failure.
.PHONY: bench
bench: ## Run the benchmarks BENCH_COUNT times (default 6) into bench.txt, for benchstat
	go test -run='^$$' -bench=. -benchmem -count=$(BENCH_COUNT) ./... >bench.txt || { cat bench.txt; exit 1; }
	cat bench.txt

.PHONY: cover
cover: ## Write a coverage report to coverage.out
	go test -coverprofile=coverage.out ./...
	go tool cover -func=coverage.out | tail -1

.PHONY: vet
vet: ## Run go vet for every supported platform
	go vet ./...
	GOOS=darwin go vet ./...

.PHONY: lint
lint: ## Run golangci-lint and check that go.mod and go.sum are tidy
	$(GOLANGCI_LINT) run
	go mod tidy -diff

# Without a GitHub token (GH_TOKEN), zizmor runs offline; CI gives it one for
# the audits that ask the GitHub API (impostor commits, known vulnerable
# actions, ...).
.PHONY: lint-actions
lint-actions: ## Lint (actionlint) and audit (zizmor) the GitHub Actions workflows
	go run github.com/rhysd/actionlint/cmd/actionlint@$(ACTIONLINT_VERSION)
	$(ZIZMOR) $(ZIZMOR_FLAGS) $(if $(GH_TOKEN),,--offline) .

.PHONY: fmt
fmt: ## Format the code
	$(GOLANGCI_LINT) fmt

.PHONY: tidy
tidy: ## Tidy go.mod and go.sum
	go mod tidy

.PHONY: vuln
vuln: ## Report known vulnerabilities reachable from droot (govulncheck)
	go run golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION) ./...

.PHONY: test-scripts
test-scripts: ## Test the release scripts
	./script/release-scripts-test.sh

.PHONY: cross
cross: ## Check that droot builds for every released platform
	for p in linux/amd64 linux/arm64 linux/arm darwin/amd64 darwin/arm64; do \
		GOOS=$${p%/*} GOARCH=$${p#*/} GOARM=7 CGO_ENABLED=0 go build -o /dev/null ./cmd/droot || exit 1; \
	done

.PHONY: snapshot
snapshot: ## Build the release assets, packages included, into dist/ without publishing (SBOMs need syft)
	go run github.com/goreleaser/goreleaser/v2@$(GORELEASER_VERSION) release --snapshot --clean $(if $(shell command -v syft),,--skip=sbom)

.PHONY: release-prep
release-prep: ## Move the Unreleased entries of CHANGELOG.md to a new version: make release-prep VERSION=x.y.z
	$(if $(filter command line environment,$(origin VERSION)),,$(error usage: make release-prep VERSION=x.y.z))
	./script/prepare-release.sh $(VERSION)

.PHONY: clean
clean: ## Remove build outputs
	rm -f $(BIN) coverage.out bench.txt
	rm -rf dist build

.PHONY: help
help: ## Show this help
	@grep -E '^[a-z-]+:.*## ' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*## "}; {printf "  %-18s %s\n", $$1, $$2}'
