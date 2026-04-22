.PHONY: build test lint fmt vet tidy clean install uninstall release-snapshot help

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
	@echo "Targets: build, install, uninstall, test, lint, fmt, vet, tidy, clean, release-snapshot"
	@echo ""
	@echo "install writes: $(BINDIR)/$(INSTALL_NAME)"
	@echo "  (the Homebrew 'gk' in /opt/homebrew/bin stays untouched)"
	@echo "  override the installed name with: make install INSTALL_NAME=gk"
