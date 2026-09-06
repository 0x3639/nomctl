# nomctl build helpers. The module path is read from go.mod so it can be
# changed in one place; override REPO for install.sh/goreleaser if needed.
GOWORK      ?= off
export GOWORK
MODULE      := $(shell go list -m)
BINARY      := nomctl
VERSION     ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT      ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
DATE        ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS     := -s -w -X $(MODULE)/cmd.version=$(VERSION) -X $(MODULE)/cmd.commit=$(COMMIT) -X $(MODULE)/cmd.date=$(DATE)
GOFLAGS     := -trimpath
export CGO_ENABLED=0

.PHONY: all build test lint fmt vet cross clean tidy rename

all: build

build: ## Build for the host platform into ./bin
	go build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o bin/$(BINARY) .

cross: ## Cross-compile static linux/amd64 and linux/arm64 binaries
	GOOS=linux GOARCH=amd64 go build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o bin/$(BINARY)-linux-amd64 .
	GOOS=linux GOARCH=arm64 go build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o bin/$(BINARY)-linux-arm64 .

test: ## Run unit tests
	go test ./...

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
