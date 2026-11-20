# nomctl build helpers. The module path is read from go.mod so it can be
# changed in one place; override REPO for install.sh/goreleaser if needed.
GOWORK      ?= off
export GOWORK
MODULE      := $(shell go list -m)
BINARY      := nomctl
VERSION     ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT      ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
DATE        ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
RELAY_URL   ?=
LDFLAGS     := -s -w -X $(MODULE)/cmd.version=$(VERSION) -X $(MODULE)/cmd.commit=$(COMMIT) -X $(MODULE)/cmd.date=$(DATE) -X $(MODULE)/internal/relay.Version=$(VERSION)
ifneq ($(RELAY_URL),)
LDFLAGS     += -X $(MODULE)/internal/alerts.DefaultRelayURL=$(RELAY_URL)
endif
GOFLAGS     := -trimpath
export CGO_ENABLED=0

.PHONY: all build test lint fmt vet cross clean tidy rename bend-image law-images proof laws laws-inventory

all: build

build: ## Build both binaries for the host platform into ./bin
	go build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o bin/$(BINARY) .
	go build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o bin/nomctl-relay ./cmd/nomctl-relay

cross: ## Cross-compile static linux/amd64 and linux/arm64 binaries
	GOOS=linux GOARCH=amd64 go build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o bin/$(BINARY)-linux-amd64 .
	GOOS=linux GOARCH=arm64 go build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o bin/$(BINARY)-linux-arm64 .
	GOOS=linux GOARCH=amd64 go build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o bin/nomctl-relay-linux-amd64 ./cmd/nomctl-relay
	GOOS=linux GOARCH=arm64 go build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o bin/nomctl-relay-linux-arm64 ./cmd/nomctl-relay

test: ## Run unit tests
	go test ./...

bend-image: ## Build the pinned containerized Bend checker
	docker build -t nomctl-bend:2.0.40 test/bend

law-images: bend-image ## Build independent proof checker and offline Go test environment
	docker build -f test/bend/Dockerfile.verdict -t nomctl-bend:2.0.40-verdict test/bend
	docker build -f test/bend/Dockerfile.checks -t nomctl-law-checks:go1.26 .

proof: ## Check all Bend proofs in the container
	./scripts/bend PROOF.bend

laws-inventory: ## Check subsystem/law/test coverage without running dependencies
	python3 -B test/bend/run.py --inventory-only

laws: ## Full proof, comparison, race-test and website gate (see docs/BEND.md)
	python3 -B test/bend/run.py

fmt: ## gofmt check
	@out=$$(gofmt -l .); if [ -n "$$out" ]; then echo "gofmt needed:"; echo "$$out"; exit 1; fi

vet:
	go vet ./...

lint: fmt vet ## gofmt + go vet + golangci-lint
	golangci-lint run ./...

tidy:
	go mod tidy

rename: ## Change the module path everywhere: make rename NEW=github.com/you/nomctl
	@test -n "$(NEW)" || { echo "usage: make rename NEW=github.com/you/nomctl"; exit 1; }
	grep -rl --include='*.go' "$(MODULE)" . | xargs sed -i.bak 's|$(MODULE)|$(NEW)|g'
	sed -i.bak 's|^module $(MODULE)$$|module $(NEW)|' go.mod
	find . -name '*.bak' -delete
	@echo "module path is now $(NEW)"

clean:
	rm -rf bin dist
