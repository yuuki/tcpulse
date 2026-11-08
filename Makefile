BIN := tcpulse
PKG := ./...

# Tool versions. The workflows (.github/workflows) pin the same versions:
# bump them together.
GOLANGCI_LINT_VERSION ?= v2.15.0
GORELEASER_VERSION    ?= v2.20.0
GOVULNCHECK_VERSION   ?= v1.9.0
ACTIONLINT_VERSION    ?= v1.7.12
ZIZMOR_VERSION        ?= 1.32.0

FUZZTIME    ?= 30s
BENCH_COUNT ?= 6
CMD_DOCKER  ?= docker
OUT_DOCKER  ?= tcpulse

# golangci-lint and govulncheck type-check with the go/types they were built
# with, and `go run tool@version` builds a tool with the Go of the tool's own
# go.mod: build them with the Go of this module, or a newer one if the tool
# needs it (+auto).
GO_TOOLCHAIN  = $(shell go env GOVERSION)
GOLANGCI_LINT = GOTOOLCHAIN=$(GO_TOOLCHAIN)+auto go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION)
GORELEASER    = GOTOOLCHAIN=$(GO_TOOLCHAIN)+auto go run github.com/goreleaser/goreleaser/v2@$(GORELEASER_VERSION)
# uvx, or pipx (preinstalled on GitHub-hosted runners), runs the pinned zizmor.
ZIZMOR        = $(if $(shell command -v uvx 2>/dev/null),uvx zizmor@$(ZIZMOR_VERSION),pipx run zizmor==$(ZIZMOR_VERSION))
# Extra zizmor options, e.g. --format=github for annotations in CI.
ZIZMOR_FLAGS ?=

.DEFAULT_GOAL := build

# Phony on purpose: go build is incremental, and a file target without
# prerequisites would never rebuild a stale binary.
.PHONY: build
build: ## Build a static tcpulse binary
	CGO_ENABLED=0 go build -trimpath -o $(BIN) .

.PHONY: check
check: lint lint-actions vet test-race build ## Pre-PR gate: lint, workflow audit, vet, race tests and build

.PHONY: test
test: ## Run the tests
	go test $(PKG)

# What CI runs: the client and the server are concurrent.
.PHONY: test-race
test-race: ## Run the tests with the race detector
	go test -race $(PKG)

# Loading eBPF programs needs root, or CAP_BPF and CAP_PERFMON: without them,
# these tests skip (as they do in `make test`).
.PHONY: test-bpf
test-bpf: ## Run the tests that load the eBPF programs of --kernel-events (as root)
	go test -race -count=1 -run 'Tracer|KernelEvents' ./internal/kevents/ ./internal/cli/

# bpf2go compiles internal/kevents/bpf with clang; its output
# (internal/kevents/kevents_bpfel.go and .o) is committed, so that building
# tcpulse needs no C compiler.
.PHONY: generate
generate: ## Rebuild the eBPF object of --kernel-events (needs clang)
	go generate ./internal/kevents/

# go test -list runs on its own so that a package that does not build fails
# the target instead of silently having no fuzz targets. A failing input is
# written to testdata/fuzz/<target>/ of its package.
.PHONY: test-fuzz
test-fuzz: ## Run every fuzz target for FUZZTIME (default 30s)
	@set -e; for pkg in $$(go list $(PKG)); do \
		targets=$$(go test -list '^Fuzz' $$pkg); \
		for f in $$(printf '%s\n' "$$targets" | grep '^Fuzz' || true); do \
			echo "== $$pkg $$f"; \
			go test -run='^$$' -fuzz="^$$f\$$" -fuzztime=$(FUZZTIME) -fuzzminimizetime=5s $(FUZZFLAGS) $$pkg; \
		done; \
	done

# The output goes to bench.txt first: a pipe to tee would hide a failure.
.PHONY: bench
bench: ## Run the benchmarks BENCH_COUNT times (default 6) into bench.txt, for benchstat
	go test -run='^$$' -bench=. -benchmem -count=$(BENCH_COUNT) $(PKG) >bench.txt || { cat bench.txt; exit 1; }
	cat bench.txt

.PHONY: cover
cover: ## Write a coverage report to coverage.out
	go test -coverprofile=coverage.out $(PKG)
	go tool cover -func=coverage.out | tail -1

.PHONY: vet
vet: ## Run go vet for Linux and macOS, the released platforms
	go vet $(PKG)
	GOOS=darwin go vet $(PKG)

.PHONY: lint
lint: ## Run golangci-lint and check that go.mod and go.sum are tidy
	$(GOLANGCI_LINT) run
	go mod tidy -diff

# Without a GitHub token (GH_TOKEN), zizmor runs offline; CI gives it one for
# the audits that ask the GitHub API (impostor commits, known vulnerable
# actions, ...).
.PHONY: lint-actions
lint-actions: ## Lint (actionlint) and audit (zizmor) the GitHub Actions workflows
	GOTOOLCHAIN=$(GO_TOOLCHAIN)+auto go run github.com/rhysd/actionlint/cmd/actionlint@$(ACTIONLINT_VERSION)
	$(ZIZMOR) $(ZIZMOR_FLAGS) $(if $(GH_TOKEN),,--offline) .

.PHONY: vuln
vuln: ## Report known vulnerabilities reachable from tcpulse (govulncheck)
	GOTOOLCHAIN=$(GO_TOOLCHAIN)+auto go run golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION) $(PKG)

.PHONY: fmt
fmt: ## Format the code
	$(GOLANGCI_LINT) fmt

.PHONY: tidy
tidy: ## Tidy go.mod and go.sum
	go mod tidy

# Rank the most complex functions with cccc (https://github.com/moznion/cccc);
# what is analyzed is configured in cccc.toml.
.PHONY: complexity
complexity: ## Show the 20 most complex functions (needs cccc)
	cccc --table --top-cognitive 20 .

.PHONY: release-check
release-check: ## Validate .goreleaser.yaml
	$(GORELEASER) check

# GoReleaser refuses to push from a snapshot, so this also checks that the
# image still builds (with docker buildx).
.PHONY: snapshot
snapshot: ## Build the release archives and images into dist/ without publishing them
	$(GORELEASER) release --snapshot --clean

.PHONY: docker/build
docker/build: ## Build the container image from the source tree
	$(CMD_DOCKER) build -t $(OUT_DOCKER):latest .

.PHONY: clean
clean: ## Remove build outputs
	rm -f $(BIN) coverage.out bench.txt
	rm -rf dist

.PHONY: help
help: ## Show this help
	@grep -E '^[a-z/-]+:.*## ' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*## "}; {printf "  %-14s %s\n", $$1, $$2}'
