# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What this project is

AgentGo is a **Go framework for Agent / Team based systems**, not a CLI app, not a UI app, not a RAG app. The architectural center is `pkg/agent`. `cmd/agentgo-cli`, `cmd/agentgo-ui`, and `ui/` are adapters around the framework.

Reason about the repo in this order:

1. `pkg/agent` — framework core (agents, teams, tasks, task plans, runtime)
2. `pkg/*` capability modules — `providers`, `mcp`, `skills`, `memory`, `rag`, `router`, `ptc`, `prompt`, `scheduler`, `a2a`, `acp`
3. Support modules — `pkg/store`, `pkg/config`, `pkg/log`, `pkg/cache`, `pkg/pool`, `pkg/usage`, `pkg/domain`
4. Optional adapters — `cmd/agentgo-cli`, `cmd/agentgo-ui`, `ui/`

Prefer changes in `pkg/` over `cmd/`. New capabilities plug into the framework core, not the adapters. Keep public package APIs intentional and embeddable.

## Development commands

Use the Makefile. The repo has two binaries — `agentgo-cli` (CLI) and `agentgo-ui` (Go API + embedded React UI).

```bash
make build          # build both binaries into bin/
make agentgo-cli    # CLI only
make agentgo-ui     # builds UI assets, syncs to cmd/agentgo-ui/dist, then builds Go binary
make test           # go test ./...
make check          # fmt + vet + test
make coverage-core  # focused coverage report for the core packages listed in $CORE_COVERAGE_PKGS
make deps           # go mod download && tidy
make clean          # removes bin/, cmd/agentgo-ui/dist, .agentgo/data/*.db

# UI dev (Vite + Go API together; API is air-reloaded on :7127)
make ui-dev
make ui-api-dev     # Go API only with air
make ui-web-dev     # Vite only
make ui-deps        # npm ci in ui/
```

### Running tests

```bash
go test ./pkg/agent/...                              # one package tree
go test ./pkg/agent -run TestTaskPlanItem            # one test
go test ./pkg/agent -run TestX -v -count=1           # force re-run, verbose
go test ./pkg/agent -race                            # race detector — useful here, see "Concurrency"
```

`make test` and `make check` run a `fix-embed` step that touches `cmd/agentgo-ui/dist/index.html` so the Go embed in `cmd/agentgo-ui` doesn't fail when UI assets aren't built. Don't delete that file unconditionally in scripts.

### Quick smoke run

```bash
go run ./cmd/agentgo-cli chat            # interactive Dispatcher chat
go run ./cmd/agentgo-cli agent list
go run ./cmd/agentgo-cli task list
```

## Architecture notes that aren't obvious from reading one file

### Runtime is a kernel, not a loop

`pkg/agent/runtime.go`, `pkg/agent/service_execution.go`, `pkg/agent/subagent.go` are converging on a single state machine with shared helpers (`prepareToolRound`, `executeToolCallsWithOptions`, `executePreparedToolRound`, `streamToolTurn`, …). Streaming, non-streaming, and subagent execution should differ only in output mode. When extending runtime behavior, push it into the shared helpers; don't fork a new branch.

Tool execution has its own state model — `ReadOnly`, `ConcurrencySafe`, `Destructive`, `InterruptBehavior`, plus `queued`/`executing`/`completed` lifecycle. New tools should declare these honestly so batching, permissioning, and cancel work.

### Task is becoming a first-class object

Sessions used to be the only boundary; tasks (`task_id` in `pkg/domain/types.go` and `pkg/agent/types.go`) are now propagated through async/team dispatch and used for history filtering. Treat `task_id` as load-bearing — when adding a new piece of state (history, memory, discovered tools, retries), scope it by task where possible.

### PTC = Programmatic Tool Calling

`pkg/ptc/` runs model-written JavaScript in a Goja sandbox so the model can `callTool(name, args)` inside loops/filters instead of doing N tool-call rounds through the chat protocol. PTC is **default-on**. See `PTC.md` for the design rationale; the short version: large intermediate data stays in the sandbox, only the small final result re-enters context. Tools used by PTC must return stable structured shapes (`{ ok, data, error }`-ish), not freeform strings.

### Skill surfacing is reminder-based

Skills aren't all dumped into the prompt. The runtime surfaces a small relevant subset via `<skill-discovery>` reminders and activates matching `skill_*` tools per turn. When adding a skill, fill in `when_to_use` and `paths` in the SKILL.md frontmatter — that's what `ResolveForModel(...)` ranks on.

### Memory ≠ cache ≠ RAG

- `pkg/memory` — durable per-conversation/per-task memory, with file-backed `MEMORY.md` and `_session/*.md` writers in `pkg/store/file_memory.go`. Background durable writer.
- `pkg/cache` — ephemeral in-process caches.
- `pkg/rag` — optional document retrieval. **Only active when an embedding model is configured.** A bare AgentGo install (no embeddings) still has Agent + MCP + Memory + PTC working — don't gate basic features on RAG availability.

### Storage layout

```
~/.agentgo/                      # override with AGENTGO_HOME=...
├── data/
│   ├── agentgo.db               # config, providers, agents, teams, tasks, plans (SQLite)
│   └── cortex.db                # optional memory/vector/graph (cortexdb)
├── memories/                    # file memory when enabled
├── skills/                      # local skills (SKILL.md format)
└── workspace/                   # agent working directory
```

`agentgo.toml` at repo root is the dev config; `home = '/Users/.../.agentgo'` redirects all of the above.

### CLI structure

`cmd/agentgo-cli/root.go` registers cobra subcommands from sibling sub-packages: `agent/`, `team/`, `mcp/`, `memory/`, `ptc/`, `rag/`, `skills/`, `acp/`, `cache/`, plus top-level files for `chat`, `llm`, `embedding`, `status`, `tasks`, `config`, `explain`, `session`, `resources`. New commands go in those sub-packages. The root `PersistentPreRunE` loads config, sets log level from `--verbose/--debug/--quiet`, and lazily initializes the global pool service for any command except `cache`.

### UI

`cmd/agentgo-ui/` is a Go server with an embedded React/Vite SPA at `cmd/agentgo-ui/dist` (synced from `ui/dist`). Hot-reload dev: `make ui-dev` runs air on the Go API and Vite on the frontend together; the script waits for `http://127.0.0.1:7127/api/status` before starting Vite.

## Conventions that bite if you don't know them

- **Identity = session UUID, not userID.** Conversations are keyed by UUID. Don't introduce `userID` as a primary identity field for chat / task / plan APIs. Use `github.com/google/uuid`.
- **Concurrency.** Recent commits (`fix agent concurrency races`) tightened goroutine sharing in dispatcher/team manager paths. Run `go test -race` for changes touching `pkg/agent/dispatcher_*`, `pkg/agent/team_*`, `pkg/agent/async_tasks*`, or `pkg/agent/store.go`.
- **Use random high ports** (3000+, e.g. 3076, 6759, 43510) for any new dev port; avoid 8080 and other common defaults.
- **Releases:** the `/release` slash command in `.claude/commands/release.md` does the version bump. Manual: `git tag -a vX.Y.Z`, then `git push --tags`. Bump rules: `feat:` → minor, `fix:`/`docs:`/`chore:` → patch, `BREAKING CHANGE:` → major. **No co-author lines in commits.**
- **No summary docs** unless explicitly requested. Don't create `*_SUMMARY.md` / `NOTES.md` / `IMPLEMENTATION.md` after finishing work.
- **Examples:** new public features should ship with a runnable example under `examples/<feature>/main.go` (each in its own folder, full imports + cleanup).

## Debugging entry points

- Provider connectivity: `agentgo status --verbose`
- MCP servers: `agentgo mcp status`, then `agentgo mcp tools call <server> <tool> '<json>'`; logs in `~/.agentgo/logs/`
- Skills: `agentgo skills list` / `skills show <id>` / `skills run <id> --var k=v`
- Tasks: `agentgo task list`, `agentgo task get <id>`, `agentgo task trace <id>`
- Routing: `agentgo explain "..."` shows which agent/route would be picked
