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

# The vendored skills are a copy, not a dependency, so the commit they came from is
# pinned in .agents/upstream.json and `skills-sync` re-takes it. Only skills/ and the
# license come across: rules/ serves harnesses this project does not vendor for.
SKILLS_MANIFEST := .agents/upstream.json
SKILLS_DIR := .agents/skills
SKILLS_REPO := $(shell test -f $(SKILLS_MANIFEST) && sed -n 's/.*"repo"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' $(SKILLS_MANIFEST))
SKILLS_REF := $(shell test -f $(SKILLS_MANIFEST) && sed -n 's/.*"ref"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' $(SKILLS_MANIFEST))
ifneq ($(DIRTY),)
LDFLAGS += -X $(BUILDINFO).Dirty=1
endif

.PHONY: all build build-all test test-short test-integration lint lint-fix fmt vet audit clean run help golden diff skills-sync

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

## mutation: Mutation-test a package with gremlins (go install github.com/go-gremlins/gremlins/cmd/gremlins@latest)
mutation:
	gremlins unleash --workers 4 ./internal/config ./internal/update ./internal/platform

## bench: Run the benchmarks and report allocations
bench:
	$(GO) test -run XXX -bench . -benchtime 200x -benchmem ./internal/archive/ ./internal/forum/

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

## skills-sync: Re-vendor .agents/skills from the upstream commit pinned in .agents/upstream.json
skills-sync:
	@test -n "$(SKILLS_REPO)" && test -n "$(SKILLS_REF)" || { echo "skills-sync: $(SKILLS_MANIFEST) must define repo and ref" >&2; exit 1; }
	@set -eu; tmp=$$(mktemp -d); trap 'rm -rf "$$tmp"' EXIT INT TERM; \
	git init -q "$$tmp"; \
	git -C "$$tmp" remote add origin "$(SKILLS_REPO)"; \
	if ! git -C "$$tmp" fetch -q --depth 1 origin "$(SKILLS_REF)"; then \
		echo "skills-sync: cannot fetch $(SKILLS_REF) from $(SKILLS_REPO); the ref must be a full 40-character commit id" >&2; \
		exit 1; \
	fi; \
	git -C "$$tmp" checkout -q FETCH_HEAD; \
	test -d "$$tmp/skills"; \
	rm -rf "$(SKILLS_DIR)"; \
	cp -R "$$tmp/skills" "$(SKILLS_DIR)"; \
	cp "$$tmp/LICENSE" .agents/LICENSE; \
	echo "skills-sync: $(SKILLS_DIR) at $(SKILLS_REF), $$(find "$(SKILLS_DIR)" -type f | wc -l | tr -d ' ') files"

## clean: Remove build artifacts
clean:
	rm -rf $(BIN_DIR) coverage.out coverage.html

## run: Build and run slack-browser
run: build
	./$(BIN_DIR)/slack-browser

## help: Show this help
help:
	@sed -n 's/^##//p' $(MAKEFILE_LIST) | column -t -s ':'
