.PHONY: build test test-pbt test-provider test-privacy test-cli test-integration lint fmt vet tidy clean install uninstall release-snapshot help

BINARY       := gk
# Installed as gk-dev so it never collides with the Homebrew-managed `gk`.
# Override via `make install INSTALL_NAME=gk` to replace both.
INSTALL_NAME ?= gk-dev
LDFLAGS := -s -w -X main.version=$(shell git describe --tags --always --dirty 2>/dev/null || echo dev) -X main.commit=$(shell git rev-parse --short HEAD 2>/dev/null || echo none) -X main.date=$(shell date -u +%Y-%m-%dT%H:%M:%SZ)
PREFIX    ?= $(HOME)/.local
BINDIR    := $(PREFIX)/bin

build:
	go build -ldflags='$(LDFLAGS)' -o bin/$(BINARY) ./cmd/gk

install: build
	install -d $(BINDIR)
	install -m 755 bin/$(BINARY) $(BINDIR)/$(INSTALL_NAME)
	@echo "installed: $(BINDIR)/$(INSTALL_NAME)"

uninstall:
	rm -f $(BINDIR)/$(INSTALL_NAME)
	@echo "removed:   $(BINDIR)/$(INSTALL_NAME)"

test:
	go test ./... -race -cover

test-pbt:
	go test ./internal/ai/provider/ -race -run "Property" -count=1 -v
	go test ./internal/aicommit/ -race -run "Property" -count=1 -v

test-provider:
	go test ./internal/ai/provider/ -race -cover -count=1 -v

test-privacy:
	go test ./internal/aicommit/ -race -run "Redact|Privacy|Gate" -count=1 -v

test-cli:
	go test ./internal/cli/ -race -cover -count=1

test-integration:
	go test ./internal/cli/ -race -run "Integration" -count=1 -v

lint:
	golangci-lint run

fmt:
	gofmt -s -w .
	go mod tidy

vet:
	go vet ./...

tidy:
	go mod tidy

clean:
	rm -rf bin/ dist/

release-snapshot:
	goreleaser release --snapshot --clean

help:
	@echo "Targets: build, install, uninstall, test, test-pbt, test-provider, test-privacy, test-cli, test-integration, lint, fmt, vet, tidy, clean, release-snapshot"
	@echo ""
	@echo "  test              run all tests"
	@echo "  test-pbt          property-based tests only (provider + privacy gate)"
	@echo "  test-provider     AI provider package tests (nvidia, fallback, factory)"
	@echo "  test-privacy      Privacy Gate tests (redaction, threshold, audit)"
	@echo "  test-cli          CLI package tests (all commands)"
	@echo "  test-integration  integration tests (privacy gate + fallback chain e2e)"
	@echo ""
	@echo "install writes: $(BINDIR)/$(INSTALL_NAME)"
	@echo "  (the Homebrew 'gk' in /opt/homebrew/bin stays untouched)"
	@echo "  override the installed name with: make install INSTALL_NAME=gk"
