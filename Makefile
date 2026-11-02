# Two binaries from one module, built CGO-free so the release can cross-compile
# every target from a single Linux runner. Tests still need cgo for -race.
GO ?= go
BIN_DIR ?= bin
# `version` reports these three, so a release binary never has to shell out to git.
VERSION ?= $(shell git describe --tags --always 2>/dev/null | sed 's/^v//' || echo 0.0.0)
DESCRIBE ?= $(shell git describe --tags --always 2>/dev/null)
DIRTY ?= $(shell git status --porcelain 2>/dev/null | head -1)
BUILDINFO := github.com/cruizba/slack-browser-mcp/internal/buildinfo
LDFLAGS := -s -w -X $(BUILDINFO).Version=$(VERSION) -X $(BUILDINFO).Describe=$(DESCRIBE)
ifneq ($(DIRTY),)
LDFLAGS += -X $(BUILDINFO).Dirty=1
endif

.PHONY: all build build-all test test-short test-integration lint lint-fix fmt vet audit clean run help golden diff

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

## fuzz: Fuzz the parsers that read somebody else's text, 30s per target
fuzz:
	$(GO) test -run XXX -fuzz=FuzzDecodeJSON -fuzztime 30s ./internal/forum/
	$(GO) test -run XXX -fuzz=FuzzDecodeMarkdown -fuzztime 30s ./internal/forum/
	$(GO) test -run XXX -fuzz=FuzzDecodeFence -fuzztime 30s ./internal/forum/
	$(GO) test -run XXX -fuzz=FuzzReadFrontmatter -fuzztime 30s ./internal/forum/

test-integration: Build both binaries and diff them against the goldens
test-integration:
	$(GO) test -tags integration -count=1 ./internal/difftest/

## golden: Re-record the goldens from this build; review the diff before committing
golden:
	$(GO) test -tags integration -count=1 -run TestDifferential ./internal/difftest/ -update

## diff: Check this build against the behaviour recorded from the TypeScript one
diff:
	$(GO) test -tags integration -count=1 -run TestDifferential ./internal/difftest/

## lint: Run golangci-lint, integration files included
lint:
	golangci-lint run --build-tags=integration ./...

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
