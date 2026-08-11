# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What this project is

AgentGo is a **Go framework for building agents** — a library, not an app. There are no binaries: no CLI, no UI, no servers. Consumers embed `pkg/agent` in their own programs (the reference consumer is superai-desktop).

Module path is `github.com/liliang-cn/agent-go/v3`.

Reason about the repo in this order:

1. `pkg/agent` — framework core (agent, loop, tools, context, hooks/lints, session/checkpoint, events)
2. `pkg/*` capability modules — `providers`, `mcp`, `skills`, `memory`, `rag`, `prompt`, `scheduler`, `sandbox`, `worktree`, `search`
3. Support modules — `pkg/store`, `pkg/config`, `pkg/log`, `pkg/cache`, `pkg/pool`, `pkg/poolsvc`, `pkg/domain`, `pkg/cortexbridge`, `pkg/otelobserver`
4. `eval/` — the behavioral eval harness; `examples/` — runnable library examples

Keep public package APIs intentional and embeddable.

### The seven concepts

v3 deliberately has exactly seven things in it. If a change does not fit one of these, it probably does not belong in `pkg/agent`.

1. **Agent** — a name, a system prompt, a model, a tool set, optional sub-agents.
2. **Loop** — one streaming state machine: assemble context → call the model → execute tools → lint the answer → terminate. `Runtime.loop` is the only implementation.
3. **Tool** — everything the agent can do is a tool: built-ins, MCP, skills, and sub-agents.
4. **Context** — message assembly, task-scoped history filtering, compaction, skill reminders.
5. **Hooks + Lints** — the deterministic layer. Hooks bracket the run and each tool; lints reject a final answer and force a retry.
6. **Session + Checkpoint** — a session UUID owns the conversation; every terminal state writes a replayable checkpoint.
7. **Events** — the single output channel. `Run()` is `RunStream()` plus a collector.

There is **no** team, dispatcher, router, handoff or built-in role hierarchy. Composition happens inside one agent via `WithSubagents(...)`, which exposes a single `task(agent_name, prompt)` tool.

## Development commands

```bash
make test           # go test ./...
make check          # fmt + vet + test
make coverage-core  # focused coverage report for the packages in $CORE_COVERAGE_PKGS
make deps           # go mod download && tidy
make clean          # removes .agentgo/data/*.db (local dev databases)

# Behavioral eval harness — scenarios under eval/scenarios/
make eval           # mock profile, deterministic, CI-safe
make eval-verbose   # same with -v
make eval-live      # AGENTGO_EVAL_LIVE=1 go test ./eval/runner -run TestLiveScenarios
```

### Running tests

```bash
go test ./pkg/agent/...                              # one package tree
go test ./pkg/agent -run TestX -v -count=1           # force re-run, verbose
go test ./pkg/agent -race                            # race detector — useful here, see "Concurrency"
```

### Quick smoke run

```bash
go run ./examples/quickstart      # minimal agent.New(...).Build() + RunStream
```

## Architecture notes that aren't obvious from reading one file

### There is exactly one loop

`Runtime.loop` (`pkg/agent/runtime.go`) is the only execution path in the framework.

- `Service.RunStream` starts it and returns the event channel.
- `Service.Run` calls `RunStream` and collects the events into an `ExecutionResult`. There is no second, non-streaming implementation to keep in sync.
- A sub-agent (`pkg/agent/subagent.go`) builds a **child `Runtime` over the same `Service`** — its own session, a narrower tool surface (`RunConfig.ToolAllowlist` / `ToolDenylist`), a different event sink. It is not a second engine.

When extending runtime behavior, push it into the shared helpers rather than forking a branch. They are grouped by concern, one file each:

| file | owns |
|---|---|
| `runtime.go` | the loop state machine, round advance, terminal states, event emission |
| `loop_context.go` | what the model sees before its turn: memory/RAG retrieval, history filtering, the recent/older split, skill reminders, compaction |
| `tool_prep.go` | what a turn may call: tool collection, allow/deny lists, constraints, skill-first policy, generation options |
| `tool_round.go` | what happens inside one tool round: normalise, dedupe, execute, decide terminal |
| `service.go` | lifecycle and the public entry points (`Run`, `RunStream`, `Ask`, `Chat`, structured output) |

Tool execution has its own state model — `ReadOnly`, `ConcurrencySafe`, `Destructive`, `InterruptBehavior`, plus `queued`/`executing`/`completed` lifecycle. New tools should declare these honestly so batching, permissioning, and cancel work.

### Sub-agents are tools

`WithSubagents(specs...)` registers one tool: `task(agent_name, prompt)`. Calling it runs the named sub-agent through the same loop and returns only its final answer; its events bubble up nested. That is the whole composition story — there is no team, no dispatcher, no router, no handoff.

`agent.Manager` is the application-level host, not an orchestrator: it owns the `Store` (agent definitions, sessions, tasks, checkpoints), caches one `*Service` per named agent, and exposes the task surface. `Manager.SetStreamOverride` is the single dispatch seam if an embedder needs to intercept runs.

### Task is a first-class object

Tasks (`task_id` in `pkg/domain/types.go` and `pkg/agent/types.go`) are propagated through async dispatch and used for history filtering. Treat `task_id` as load-bearing — when adding a new piece of state (history, memory, discovered tools, retries), scope it by task where possible.

### Task checkpoint + replay

Every terminal `completeRun` / `blockRun` writes a `TaskCheckpoint` snapshot of the message history to `task_checkpoints` (capped at `MaxCheckpointsPerTask=32`, pruned by `checkpointWriter`). The wiring lives in `pkg/agent/task_checkpoint.go` + `task_checkpoint_manager.go`; `Service.SetCheckpointSink(...)` is what the runtime calls — `Manager.buildServiceForModel` auto-wires this, services built directly via `agent.New(...).Build()` skip persistence.

To re-run a crashed/cancelled task from its latest snapshot: `manager.Tasks().ResumeFromCheckpoint(ctx, taskID, CheckpointResumeOptions{FollowUp: "..."})`.

The `WithResumeMessages([]domain.Message)` `RunOption` is what makes the runtime skip its normal context-prep step and start the loop from a snapshot.

### Output lint registry — the deterministic layer

`pkg/agent/output_lint.go` ships a deterministic post-output-check layer. When the model produces a free-form final answer, the runtime consults `Service.OutputLints()`; on violation it appends structured feedback and re-prompts (bounded by `defaultLintRetryBudget = 2`; exhaustion → `task_blocked`).

Every service built through `agent.New(...).Build()` gets the built-in set automatically — there are no agent-scoped or role-scoped lints in v3:

- `no_planning_only_finish` — final text mustn't read like "Next steps:" / "I will..."
- `file_task_must_write` — a task that asked for a file must have produced one
- `non_empty_final_answer` — a run cannot terminate with no text at all
- `task_delivery_contract` — a goal that names a delivery action (send the mail, post the message, write the file) cannot complete unless a matching tool was actually called *and* such a tool was available

`LintContext` carries `ToolCalls` (what ran) and `AvailableTools` (what could have run), so a lint can tell "the agent skipped a capability it had" from "the agent never had it".

The discipline: when a model keeps making the same mistake, **don't add another sentence to the prompt — write a lint** in `output_lints_builtin.go` / `output_lints_delivery.go` and the runtime will reject + retry deterministically.

### Hard constraints live in the runtime, not the prompt

A user who refuses tool use is obeyed by *withholding the tools* — `prepareTurnInputsWithConfig` empties the list, including `search_available_tools` — and any tool call the model emits anyway is refused with structured feedback. Forbidding a capability means not offering it, not offering it and then arguing about it.

**Never decide this by matching phrases in the goal.** `constraints.go` resolves each run's constraints once, in `Runtime.loop`, via one temperature-0 structured call with a keyword-free prompt:

```go
type RunConstraints struct {
    ForbidTools  bool
    Deliverables []DeliverableRequirement // kind: email|file|message|other
}
```

`ForbidTools` empties the tool list; `Deliverables` reach `task_delivery_contract` and `file_task_must_write` through `LintContext`. Callers who already know skip the call entirely with `WithToolsDisabled()` / `WithRequiredDeliverables(...)`; `WithConstraintExtraction(false)` turns the pass off. Extraction failure degrades to "no constraints" and warns — it must never block an ordinary run.

This replaced four hardcoded phrase tables (`noToolInstructionPhrases`, delivery `GoalMarkers`, `fileOutputIntentPatterns`, the auto-memory hook). They are the same disease as the deleted `isExplicitMemoryRecallQuery`: a list only enforces the languages someone thought to enumerate, and silently does nothing for everyone else. If you find yourself adding a phrase to a list to change runtime behavior, that is the signal to extract a constraint instead.

**The input side is now clear: no hardcoded phrase or regexp table anywhere in the framework reads the user's request and changes behavior.** (Tokenizing a query to *rank* something — `service_tools.go` tool search, `pkg/search` BM25 — is relevance, not a verdict, and is fine. Matching the *model's* output — `planningEndingPatterns`, `looksLikeRefusalText` — is the output side, which is where deterministic checks belong.) Seven tables were removed — the four above, plus `pkg/memory`'s `QueryClassifier` (retrieval gate), `shouldSkipAutoStoreForTaskGoal` and friends (auto-store gate), and `FilterMemoriesForQuery`/`detectRecallFilterMode` (post-retrieval schedule filter). Every remaining regexp in `pkg/memory` reads *stored memory content* — noise filtering in `filter.go`, event extraction in `structured_schedule.go` — which is a different thing: content the model wrote, not wording the user chose. Adding a new list that inspects a goal, a query or a prompt is a regression, not a feature; the two supported ways to change behavior from the request are a structured constraint extraction and an explicit option.

Resolve constraints in the **loop**, never in a helper that only sees a goal string — that mistake is why the gate fired for `Run` but not `Ask`.

### Cancellation is a registry, and it is an outcome

Two separate registries, because the two callers hold different things.

- **A run.** `startRun` derives a cancellable context per run and records it in
  `Service.runs` (`pkg/agent/run_cancel.go`). `Cancel()` stops **every** run in
  flight on the service; `CancelRun(runID)` stops one (name it with
  `WithRunID`); `CancelSession(sessionID)` stops one conversation;
  `ActiveRuns()` lists them. Registration is released when the event stream
  closes, so a stale ID can never answer "ok" to a stop. All three defer — and
  return false — while a tool with `InterruptBehavior: block` is mid-execution.
  Sub-agents are not registered: they run under the parent's context.
- **A scheduled execution.** `pkg/scheduler` gives every execution its own
  context (`beginRun`) instead of handing out the scheduler root, so
  `CancelRun(runID)` / `CancelTaskRuns(taskID)` stop one run without touching
  the timers. `Stop()` is unchanged: it still tears everything down.
  `RunTaskAsync` / `PromptScheduler.RunNowAsync` exist because `RunNow` blocks
  for the whole run — a host stuck inside it cannot draw the cancel button.

A stop is **not** an error. The runtime terminates with `EventTypeCancelled`
(`workflow_cancelled`) + `StopReasonCancelled`, writes a
`CheckpointReasonTaskCancelled` snapshot so `ResumeFromCheckpoint` can pick the
work back up, and persists the task as `cancelled`. `ExecutionResult.Cancelled`
is true with `Err() == nil`, exactly like `Blocked` — a caller branching on err
must never mistake its own stop button for a crash.

### A Service owns its store; everything else only borrows the Service

`Service` opens exactly one long-lived resource — the `Store`, and through it
the `*sql.DB` behind `agentgo.db` — so `Service.Close()` is the only thing
allowed to release it. A `PromptExecutor` on a timer, a `Manager` cache entry, a
host's window: all borrowers. A borrower never closes, and must not keep running
turns through a Service that has been closed.

That second half is enforced now (`pkg/agent/service_close.go`):

- `Close()` is idempotent, marks the service closed *first*, cancels the runs
  still in flight (bounded by `closeDrainTimeout`), then releases memory + store.
- `startRun` — the single entry point into the loop — refuses a closed service
  with `ErrServiceClosed`, so `Run` / `RunStream` / `Ask` / `Chat` / structured
  output / the prompt scheduler all fail the same way. `Service.Closed()` lets a
  host ask first.
- `Manager.getOrBuildService` drops a cached service somebody closed and rebuilds.

Why: a host that rebuilt its agent and left a `PromptScheduler` pointed at the
old one kept firing schedules against a closed store. The model answered, the
answer reached the UI, the run reported success — and every write of the
conversation failed with `sql: database is closed`, logged as a warning and
dropped. **A failed history write is now an ERROR log plus an `EventTypeError`
event marked `history_persist_failed`**, not a warning nobody reads.

Same rule one layer down: `scheduler.Storage` mirrors into a canonical
`AgentGoDB` it *borrows*; `TaskScheduler.Stop` closes the handle
`TaskScheduler.start` opened. A borrower closing its lender's handle is the same
bug in miniature.

### Eval harness

`eval/runner/` is a behavioral eval driver: every YAML in `eval/scenarios/` defines an `input`, an entry agent, optional lint registrations, expected `status`/`final_text_match` constraints, and optional `lint_violations` counts. Two profiles:

- **mock** (default, CI-safe): `MockLLM` plays back a scripted `llm_replies` sequence — deterministic. `make eval`.
- **live**: scenarios with `mode: live` run against the configured provider pool via `eval/runner/live.go` (`BuildPoolLiveBuilder`). `make eval-live` (gated on `AGENTGO_EVAL_LIVE=1`); results are saved as timestamped JSON in `eval/results/` (gitignored).

When a harness change (lint, prompt cut, tool-prep tweak) lands, run `make eval-live` and diff the result JSON against the previous run — that's the harness-engineering loop.

### Skill surfacing is reminder-based

Skills aren't all dumped into the prompt. The runtime surfaces a small relevant subset via `<skill-discovery>` reminders and activates matching `skill_*` tools per turn. When adding a skill, fill in `when_to_use` and `paths` in the SKILL.md frontmatter — that's what `ResolveForModel(...)` ranks on.

### Memory backends are pluggable — don't grow the switch

`buildMemoryService` (`pkg/agent/builder.go`) resolves `store_type` through a
switch of the four built-ins and then falls back to a registry. **A new backend
is a registration, not a new `case`.** The seams, in priority order:

1. `agent.RegisterMemoryStore(name, factory)` — `pkg/agent/memory_registry.go`,
   backed by `pkg/domain/memory_store_plugin.go`. The registry lives in
   `pkg/domain` because it is the only package both `pkg/config` (which must
   accept a plugin name in `MemoryStoreType.Valid()`) and `pkg/store` (which
   self-registers `cortex-remote` in `init()`) can import — `pkg/config` imports
   `pkg/store`, and `pkg/memory` imports `pkg/store`, so neither could host it.
   The factory receives `domain.MemoryStoreConfig{Name, Path, DSN, Options,
   Embedder, Generator}`; `WithMemoryDSN` / `WithMemoryOption(s)` and
   agentgo.toml's `[memory] dsn` / `[memory.options]` feed it.
   Registration is strict: blank name, nil factory, built-in name, or duplicate
   is an error. `UnregisterMemoryStore` replaces one deliberately.
2. `WithMemoryStore(domain.MemoryStore)` — inject an instance; wins over
   `store_type` entirely.
3. `memory.BaseStore` (= `domain.UnsupportedMemoryStore`) — embeddable base
   whose eighteen methods all return `domain.ErrMemoryStoreUnsupported`.
4. `Builder.WithMemoryService(domain.MemoryService)` — escape hatch; nothing in
   `buildMemoryService` runs.

Every resolved store funnels through `Builder.assembleMemoryService`, so
built-in, registered and injected backends get identical retrieval/injection
behaviour. Keep it that way.

Built-in `store_type` values: `file`, `cortex`, `memoryflow`, `graphflow`.
Shipped plugin: `cortex-remote` (`pkg/store/memory_cortex_remote.go`) — a shared
CortexDB over gRPC, the "shared brain". It covers Store/StoreWithScope/Get/
Update/Delete/SearchByText/List; vector `Search`/`SearchByScope`/
`SearchBySession` **degrade to empty** because the remote surface takes a query
string, not a vector (the server owns the embedder), and the memory service then
falls through to `SearchByText`; `IncrementAccess`, `GetByType`, `Clear`,
`DeleteBySession`, `ConfigureBank`, `Reflect`, `AddMentalModel` return
`ErrMemoryStoreUnsupported`. Endpoint and token come from the DSN/options or
`$CORTEXDB_REMOTE` / `$CORTEXDB_GRPC_TOKEN` — never from code.

The discipline: **an honest `ErrMemoryStoreUnsupported` beats a fake
implementation.** If a backend cannot do something, say so and let the caller
degrade.

### Memory ≠ cache ≠ RAG

- `pkg/memory` — durable per-conversation/per-task memory, with file-backed `MEMORY.md` and `_session/*.md` writers in `pkg/store/file_memory.go`. Background durable writer.

  Both sides of memory are unconditional, and both have exactly one switch. Retrieval runs on every turn unless `WithMemoryRetrieval(false)`. Auto-store runs on every turn unless `WithMemoryAutoStore(false)`: `storeIfWorthwhileSync` asks the model once (`should_store` + extracted items) and that verdict is final — there is no pre-filter on the goal's wording and no keyword fallback that overrides a "no". Neither side is ever decided by inspecting what the user typed; if you are tempted to skip a call because a request "looks like" a question, the answer is the explicit option, not a prefix list.

  Nothing narrows the retrieved set after ranking either. `FilterMemoriesForQuery` used to sit between the scorer and the `MaxMemories` cap and drop ranked memories whenever the query contained `安排`/`计划`/`plan`/`agenda` — it returned *nothing* for `帮我做一个学习计划`, and `…plan for the api service` took its "personal schedule" branch because `api ` contains `i `. It is gone. `pkg/memory/structured_schedule.go` keeps only the content-side half: it reads stored memory text to derive event metadata and to apply later corrections to earlier events, and it never sees the query.
- `pkg/cache` — ephemeral in-process caches.
- `pkg/rag` — optional document retrieval. **Only active when an embedding model is configured.** A bare AgentGo install (no embeddings) still has Agent + MCP + Memory working — don't gate basic features on RAG availability.

### Storage layout

```
~/.agentgo/                      # override with AGENTGO_HOME=...
├── data/
│   ├── agentgo.db               # config, providers, agents, tasks, checkpoints (SQLite)
│   └── cortex.db                # optional memory/vector/graph (cortexdb)
├── memories/                    # file memory when enabled
├── skills/                      # local skills (SKILL.md format)
└── workspace/                   # agent working directory
```

`agentgo.toml` at repo root is the dev config; `home = '/Users/.../.agentgo'` redirects all of the above.

## Conventions that bite if you don't know them

- **Identity = session UUID, not userID.** Conversations are keyed by UUID. Don't introduce `userID` as a primary identity field for chat or task APIs. Use `github.com/google/uuid`.
- **Concurrency.** Run `go test -race ./pkg/agent/...` for changes touching `pkg/agent/runtime.go`, `pkg/agent/manager.go`, `pkg/agent/subagent*.go`, `pkg/agent/async_tasks*`, or `pkg/agent/store.go`.
- **Provider compatibility fallbacks.** `pkg/pool/client.go` and `pkg/providers/openai.go` both have `applyRetryFallbacks` helpers that strip `web_search_options` or `tool_choice` and retry once when the upstream rejects them with "unsupported / does not support / invalid" errors. DeepSeek's reasoner (e.g. `deepseek-v4-flash`) needs the `tool_choice` fallback. When adding new optional params, mirror the same shape: detect the rejection, strip, retry once.
- **`tool_choice` JSON shape.** `"auto" / "required" / "none"` go in as plain strings; named-tool choice is `{"type":"function","function":{"name":"X"}}`. Don't reuse the named-tool form for `"required"` — DeepSeek and some OpenAI variants reject that.
- **Use random high ports** (3000+, e.g. 3076, 6759, 43510) for any new dev port; avoid 8080 and other common defaults.
- **Releases:** the `/release` slash command in `.claude/commands/release.md` does the version bump. Manual: `git tag -a vX.Y.Z`, then `git push --tags`. Bump rules: `feat:` → minor, `fix:`/`docs:`/`chore:` → patch, `BREAKING CHANGE:` → major. **No co-author lines in commits.**
- **No summary docs** unless explicitly requested. Don't create `*_SUMMARY.md` / `NOTES.md` / `IMPLEMENTATION.md` after finishing work.
- **Examples:** new public features should ship with a runnable example under `examples/<feature>/main.go` (each in its own folder, full imports + cleanup).

## Debugging entry points

- Behavioral eval: `make eval` (mock) / `make eval-live` (real provider)
- Provider connectivity / compat: `examples/` + `pkg/poolsvc` (`poolsvc.Global().Initialize(...)`); provider fallback behavior lives in `pkg/pool/client.go`
- MCP: `pkg/mcp` client logs land in `~/.agentgo/logs/`
- Tasks / checkpoints: `Manager.Tasks()` API — list, trace, `ResumeFromCheckpoint`
- Tracing: wire an observer (`pkg/otelobserver`) into the service to see per-round loop state
