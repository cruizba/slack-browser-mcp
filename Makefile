# Two binaries from one module, built CGO-free so the release can cross-compile
# every target from a single Linux runner. Tests still need cgo for -race.
GO ?= go
BIN_DIR ?= bin
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)

.PHONY: all build build-all test test-short lint lint-fix fmt vet audit clean run help

all: lint test build-all

## build: Build the slack-browser binary
build:
	CGO_ENABLED=0 $(GO) build -trimpath -ldflags "$(LDFLAGS)" -o $(BIN_DIR)/slack-browser ./cmd/slack-browser

## build-all: Build every binary in cmd/
build-all:
	CGO_ENABLED=0 $(GO) build -trimpath -ldflags "$(LDFLAGS)" -o $(BIN_DIR)/ ./cmd/...

## test: Run the tests with the race detector
test:
	CGO_ENABLED=1 $(GO) test -race -coverprofile=coverage.out ./...

## test-short: Run the tests that do not touch a real filesystem layout
test-short:
	$(GO) test -short ./...

## lint: Run golangci-lint
lint:
	golangci-lint run ./...

## lint-fix: Apply the linters that can fix themselves
lint-fix:
	golangci-lint run --fix ./...

## fmt: Format and vet
fmt:
	$(GO) fmt ./...
	$(GO) vet ./...

## vet: Run the vet pass alone
vet:
	$(GO) vet ./...

## audit: Check the dependencies for known vulnerabilities
audit:
	govulncheck ./...

## clean: Remove build artifacts
clean:
	rm -rf $(BIN_DIR) coverage.out coverage.html

## run: Build and run slack-browser
run: build
	./$(BIN_DIR)/slack-browser

## help: Show this help
help:
	@sed -n 's/^##//p' $(MAKEFILE_LIST) | column -t -s ':'
