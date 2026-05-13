.PHONY: build test test-pbt test-provider test-privacy test-cli test-integration lint fmt vet tidy clean install install-gk uninstall uninstall-gk release-snapshot check help

BINARY       := gk
# Installed as gk-dev so it never collides with the Homebrew-managed `gk`.
# Override via `make install INSTALL_NAME=gk` to replace both.
INSTALL_NAME ?= gk-dev
LDFLAGS := -s -w \
	-X main.version=$(shell git describe --tags --always --dirty 2>/dev/null || echo dev) \
	-X main.commit=$(shell git rev-parse --short HEAD 2>/dev/null || echo none) \
	-X main.date=$(shell date -u +%Y-%m-%dT%H:%M:%SZ) \
	-X main.branch=$(shell git rev-parse --abbrev-ref HEAD 2>/dev/null || echo unknown) \
	-X main.worktree=$(shell basename "$$(git rev-parse --show-toplevel 2>/dev/null)" 2>/dev/null || echo unknown)
PREFIX    ?= $(HOME)/.local
BINDIR    := $(PREFIX)/bin

build:
	go build -ldflags='$(LDFLAGS)' -o bin/$(BINARY) ./cmd/gk

install: build
	install -d $(BINDIR)
	install -m 755 bin/$(BINARY) $(BINDIR)/$(INSTALL_NAME)
	@echo "installed: $(BINDIR)/$(INSTALL_NAME)"

# install-gk forces the canonical `gk` name — convenient when the dev
# build IS the gk you use day-to-day (i.e. you maintain gk itself).
# Wraps `make install INSTALL_NAME=gk` so all install logic stays in one
# place. Note this can shadow a Homebrew-managed `gk` depending on PATH
# order; that's the intended trade-off here.
install-gk:
	$(MAKE) install INSTALL_NAME=gk

uninstall:
	rm -f $(BINDIR)/$(INSTALL_NAME)
	@echo "removed:   $(BINDIR)/$(INSTALL_NAME)"

uninstall-gk:
	$(MAKE) uninstall INSTALL_NAME=gk

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

# `make check` mirrors what the CI jobs run end-to-end (vet → build → test
# → lint). Run this before `gk ship` to catch lint/format regressions
# locally instead of waiting for the CI lint job to fail post-tag.
check: vet build test lint
	@echo "check: ok"

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
	@echo "Targets: build, install, install-gk, uninstall, uninstall-gk, test, test-pbt, test-provider, test-privacy, test-cli, test-integration, lint, fmt, vet, tidy, check, clean, release-snapshot"
	@echo ""
	@echo "  check             vet + build + test + lint — same gates the CI runs"
	@echo ""
	@echo "  test              run all tests"
	@echo "  test-pbt          property-based tests only (provider + privacy gate)"
	@echo "  test-provider     AI provider package tests (nvidia, fallback, factory)"
	@echo "  test-privacy      Privacy Gate tests (redaction, threshold, audit)"
	@echo "  test-cli          CLI package tests (all commands)"
	@echo "  test-integration  integration tests (privacy gate + fallback chain e2e)"
	@echo ""
	@echo "  install           writes $(BINDIR)/$(INSTALL_NAME)  (safe default: gk-dev)"
	@echo "  install-gk        writes $(BINDIR)/gk               (use the dev build AS gk)"
	@echo "  uninstall         remove $(BINDIR)/$(INSTALL_NAME)"
	@echo "  uninstall-gk      remove $(BINDIR)/gk"
	@echo ""
	@echo "  default install keeps the Homebrew 'gk' in /opt/homebrew/bin untouched."
	@echo "  install-gk overrides it (assuming \$$PATH puts ~/.local/bin first)."
	@echo "  or set it manually: make install INSTALL_NAME=<name>"
