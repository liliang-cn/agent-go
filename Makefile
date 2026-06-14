.PHONY: help build agentgo-cli agentgo-ui claw ui-build sync-ui-dist ui-dev ui-api-dev ui-web-dev ui-deps test check clean deps coverage-core eval eval-verbose eval-live eval-all

CORE_COVERAGE_PKGS := ./pkg/config ./pkg/cache ./cmd/agentgo-ui/internal/handler ./pkg/prompt ./pkg/ptc/runtime/goja ./pkg/ptc/store ./pkg/rag/embedder ./pkg/scheduler/executors
UI_RUNNER := $(shell if command -v fnm >/dev/null 2>&1; then printf 'fnm exec --using=24'; else printf 'env'; fi)

GIT_TAG := $(shell git describe --tags --abbrev=0 2>/dev/null || echo "v0.0.0")
LDFLAGS := -ldflags="-X 'main.version=$(GIT_TAG)'"

all: help

help:
	@echo "AgentGo - AI Agent SDK"
	@echo ""
	@echo "  build       - Build all (agentgo-cli + agentgo-ui)"
	@echo "  agentgo-cli    - Build agentgo-cli only"
	@echo "  agentgo-ui     - Build agentgo-ui only"
	@echo "  claw        - Build claw, the interactive autonomous agent CLI"
	@echo "  test        - Run tests"
	@echo "  check       - Run format, vet and tests"
	@echo "  coverage-core - Run core unit-test coverage report"
	@echo "  eval        - Run behavioral eval harness (mock-LLM scenarios, CI-safe)"
	@echo "  eval-verbose - Same, with -v output"
	@echo "  eval-live   - Run live-LLM scenarios via agentgo eval --profile=live"
	@echo "  eval-all    - Run mock + live scenarios via agentgo eval --profile=all"
	@echo "  clean       - Clean"
	@echo "  deps        - Install deps"
	@echo ""
	@echo "UI:"
	@echo "  ui-build    - Build UI assets and sync embedded dist"
	@echo "  sync-ui-dist - Sync built UI assets into cmd/agentgo-ui/dist"
	@echo "  ui-dev      - Start Vite and Go API dev servers together"
	@echo "  ui-api-dev  - Start Go UI API with air hot reload"
	@echo "  ui-web-dev  - Start Vite dev server only"
	@echo "  ui-deps     - Install UI deps"
	@echo ""
	@echo "Version: $(GIT_TAG)"

build: agentgo-cli agentgo-ui claw
	@echo "✅ Done"

agentgo-cli:
	@echo "Building agentgo-cli..."
	@mkdir -p bin
	@go build $(LDFLAGS) -o bin/agentgo-cli ./cmd/agentgo-cli

claw:
	@echo "Building claw (autonomous agent CLI)..."
	@mkdir -p bin
	@go build $(LDFLAGS) -o bin/claw ./cmd/claw

agentgo-ui: ui-build
	@echo "Building agentgo-ui..."
	@mkdir -p bin
	@go build $(LDFLAGS) -o bin/agentgo-ui ./cmd/agentgo-ui

ui-build: sync-ui-dist

ui/node_modules: ui/package.json ui/package-lock.json
	@echo "Installing UI deps..."
	@cd ui && $(UI_RUNNER) npm ci

sync-ui-dist: ui/node_modules
	@echo "Building UI assets..."
	@cd ui && $(UI_RUNNER) npm run build
	@mkdir -p cmd/agentgo-ui/dist
	@cp -R ui/dist/. cmd/agentgo-ui/dist/

ui-dev:
	@mkdir -p /tmp/go-build-cache
	@mkdir -p /tmp/go-mod-cache
	@/usr/bin/env sh -c 'env GOCACHE=/tmp/go-build-cache GOMODCACHE=/tmp/go-mod-cache go tool air -c .air.toml & api_pid=$$!; trap "kill $$api_pid" EXIT INT TERM; until curl -fsS http://127.0.0.1:7127/api/status >/dev/null 2>&1; do sleep 1; done; cd ui && npm run dev'

ui-api-dev:
	@mkdir -p /tmp/go-build-cache
	@mkdir -p /tmp/go-mod-cache
	@env GOCACHE=/tmp/go-build-cache GOMODCACHE=/tmp/go-mod-cache go tool air -c .air.toml

ui-web-dev:
	@cd ui && $(UI_RUNNER) npm run dev

ui-deps:
	@cd ui && $(UI_RUNNER) npm ci

test: fix-embed
	@go test ./...

check: fix-embed
	@echo "Running format check..."
	@go fmt ./...
	@echo "Running vet..."
	@go vet ./...
	@echo "Running tests..."
	@go test ./...

coverage-core: fix-embed
	@echo "Running core unit-test coverage..."
	@go test $(CORE_COVERAGE_PKGS) -coverprofile=/tmp/agentgo-core.cover.out
	@go tool cover -func=/tmp/agentgo-core.cover.out | tail -n 1

# Behavioral eval harness — runs every scenario in eval/scenarios/ against a
# mock LLM driven by the scenario's reply script. See eval/runner/scenario.go
# for the YAML schema. CI-runnable; deterministic.
eval:
	@go test ./eval/runner/ -count=1 -timeout 120s

eval-verbose:
	@go test ./eval/runner/ -count=1 -v -timeout 120s

# Live-LLM eval. Uses the configured agentgo provider pool (same as
# `agentgo chat`). Non-deterministic — scenarios with `mode: live` are
# expected to declare a `runs:` count and use loose assertions. This
# target is NOT CI-runnable; it is for local sanity / regression checks.
eval-live: agentgo-cli
	@./bin/agentgo-cli eval --profile=live --save

eval-all: agentgo-cli
	@./bin/agentgo-cli eval --profile=all --save

fix-embed:
	@mkdir -p cmd/agentgo-ui/dist && touch cmd/agentgo-ui/dist/index.html

clean:
	@rm -rf bin/ cmd/agentgo-ui/dist .agentgo/data/*.db

deps:
	@go mod download && go mod tidy
