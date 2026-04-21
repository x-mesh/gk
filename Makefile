.PHONY: build test lint fmt vet tidy clean release-snapshot help

BINARY := gk
LDFLAGS := -s -w -X main.version=$(shell git describe --tags --always --dirty 2>/dev/null || echo dev) -X main.commit=$(shell git rev-parse --short HEAD 2>/dev/null || echo none) -X main.date=$(shell date -u +%Y-%m-%dT%H:%M:%SZ)

build:
	go build -ldflags='$(LDFLAGS)' -o bin/$(BINARY) ./cmd/gk

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
	@echo "Targets: build, test, lint, fmt, vet, tidy, clean, release-snapshot"
