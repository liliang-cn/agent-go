.PHONY: help test check clean deps coverage-core eval eval-verbose eval-live

CORE_COVERAGE_PKGS := ./pkg/config ./pkg/cache ./pkg/prompt ./pkg/ptc/runtime/goja ./pkg/ptc/store ./pkg/rag/embedder ./pkg/scheduler/executors

GIT_TAG := $(shell git describe --tags --abbrev=0 2>/dev/null || echo "v0.0.0")

all: help

help:
	@echo "AgentGo - Go agent framework (library only, no binaries)"
	@echo ""
	@echo "  test          - Run tests"
	@echo "  check         - Run format, vet and tests"
	@echo "  coverage-core - Run core unit-test coverage report"
	@echo "  eval          - Run behavioral eval harness (mock-LLM scenarios, CI-safe)"
	@echo "  eval-verbose  - Same, with -v output"
	@echo "  eval-live     - Run live-LLM scenarios against the configured provider pool"
	@echo "  clean         - Clean local dev databases"
	@echo "  deps          - go mod download && tidy"
	@echo ""
	@echo "Version: $(GIT_TAG)"

test:
	@go test ./...

check:
	@echo "Running format check..."
	@go fmt ./...
	@echo "Running vet..."
	@go vet ./...
	@echo "Running tests..."
	@go test ./...

coverage-core:
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

# Live-LLM eval. Uses the configured agentgo provider pool (agentgo.toml /
# SQLite store). Non-deterministic — scenarios with `mode: live` are expected
# to declare a `runs:` count and use loose assertions. NOT CI-runnable; for
# local sanity / regression checks. Results are saved to eval/results/.
eval-live:
	@AGENTGO_EVAL_LIVE=1 go test ./eval/runner/ -run TestLiveScenarios -count=1 -v -timeout 1200s

clean:
	@rm -rf .agentgo/data/*.db

deps:
	@go mod download && go mod tidy
