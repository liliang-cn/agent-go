---
name: agent-go
description: Use AgentGo (github.com/liliang-cn/agent-go/v2) as a Go library to build AI agents, RAG pipelines, and multi-agent teams. Use when the user is writing Go code that imports github.com/liliang-cn/agent-go/v2 and needs help with agent.New() / TeamManager / Tasks / TaskPlan / output lints / task checkpoints / memory / MCP / PTC, or when configuring providers in agentgo.toml or via the agentgo CLI.
license: MIT
compatibility: Go 1.21+ project that imports github.com/liliang-cn/agent-go/v2. SQLite via go-sqlite3 (CGO). Optional Goja for PTC. Optional embedding model for RAG.
metadata:
  project: agent-go
  version: "2.0"
  homepage: https://github.com/liliang-cn/agent-go
  import: github.com/liliang-cn/agent-go/v2
---

# AgentGo — Go library for AI agents

Use this skill when the user wants to **embed AgentGo in their Go project** — building an agent, calling tools, persisting memory, running multi-agent teams, or evaluating agent behavior. The skill covers the public API surface of `pkg/agent` and a few sibling packages most apps reach for.

## Install

```bash
go get github.com/liliang-cn/agent-go/v2
```

Pulls SQLite via `mattn/go-sqlite3` (needs CGO).

## 30-second mental model

- **Agent** = named runtime with prompt, LLM, tools, optional memory/RAG/PTC/skills. Build via `agent.New("name").With...().Build()`.
- **Service** = what `Build()` returns. The thing you call `.Chat(ctx, msg)`, `.Ask(ctx, q)`, `.Run(ctx, goal)`, `.RunStream(ctx, goal)` on.
- **TeamManager** = orchestrator over many agents, persistent task queue, event subscription. Built on a `*Store` (SQLite).
- **Task** = first-class execution unit (`task_id`). Submit via `manager.Tasks().Submit(...)`. Subscribe events via `manager.SubscribeTask(taskID)`.
- **TaskPlan** = lightweight coordination object (items with deps), submitted to real Tasks one at a time.
- **OutputLint** = deterministic post-output check; on violation runtime re-prompts the model.
- **TaskCheckpoint** = snapshot at every terminal `completeRun`/`blockRun`; `Tasks().ResumeFromCheckpoint(...)` re-runs from there.

## Minimal one-shot agent

```go
import "github.com/liliang-cn/agent-go/v2/pkg/agent"

svc, err := agent.New("assistant").
    WithPrompt("You are a concise Go assistant.").
    Build()
if err != nil { return err }
defer svc.Close()

reply, err := svc.Ask(ctx, "What is AgentGo?")
```

The builder picks up its LLM from the global pool service (configured via `agentgo.toml` / SQLite store). Use `WithLLM(...)` to inject a custom `domain.Generator` instead.

See [references/api-patterns.md](references/api-patterns.md) for: memory, RAG, PTC, MCP, skills, custom tools, system prompt sections.

## TeamManager + persistent tasks

```go
store, _ := agent.NewStore("agentgo.db")
manager := agent.NewTeamManager(store)
_ = manager.SeedDefaultMembers()  // Dispatcher, Operator, Responder, Archivist, ...

submitted, _ := manager.Tasks().Submit(ctx, agent.TaskSubmitOptions{
    SessionID: "demo-session",
    AgentName: "Operator",
    Input:     "Check the current repository status.",
})

// Either block:
done, _ := manager.Tasks().Await(ctx, submitted.ID)
// Or subscribe to live events:
events, unsub, _ := manager.SubscribeTask(submitted.ID)
defer unsub()
for evt := range events {
    // evt.Type == TaskEventTypeRuntime (model deltas / tool calls), TaskEventTypeCompleted, ...
}
```

See [references/api-patterns.md](references/api-patterns.md) `Team` section for: orchestrators, registering custom tools onto the dispatcher, A2A pipelines, follow-up agents.

## Output lints — moving "please don't" out of prompts

When the model keeps making the same mistake, register a lint instead of adding another instruction sentence:

```go
svc.RegisterOutputLint(agent.LintFunc{
    NameValue: "no_planning_only_finish",
    Fn: func(text string, ctx agent.LintContext) (bool, string) {
        if strings.HasSuffix(strings.TrimSpace(text), "Next steps:") {
            return false, "response reads like a plan; deliver the work or call task_blocked"
        }
        return true, ""
    },
}, "Operator")  // empty agentNames = global lint
```

On violation the runtime appends structured feedback to history and re-prompts (bounded by `defaultLintRetryBudget=2`; exhausting → task blocks). Three built-ins ship in `pkg/agent/output_lints_builtin.go`:

- `dispatcher_no_bounce_back` — auto-wired when TeamManager builds Dispatcher
- `archivist_no_relative_time` — auto-wired for Archivist
- `no_planning_only_finish` — opt-in via `agent.RegisterDefaultOutputLints(svc)`

User-built services via `agent.New(...).Build()` skip auto-wire — opt in explicitly.

## Task checkpoint + replay

Every successful or blocked task writes a snapshot to SQLite (capped at `MaxCheckpointsPerTask=32`). Resume a crashed/cancelled run:

```go
resumed, err := manager.Tasks().ResumeFromCheckpoint(ctx, taskID, agent.CheckpointResumeOptions{
    FollowUp: "and now also do X",  // optional
    // CheckpointID: "<id>",          // pin to a specific checkpoint
})
```

Snapshot persistence is auto-wired when TeamManager builds a service. For `agent.New(...).Build()` services, you must call `svc.SetCheckpointSink(yourSink)` to persist.

## Behavioral eval (optional)

If your project ships its own scenarios:

```go
import evalrunner "github.com/liliang-cn/agent-go/v2/eval/runner"

scenarios, _ := evalrunner.LoadScenariosFromDir("eval/scenarios")
results, _ := evalrunner.RunAll(ctx, "eval/scenarios", evalrunner.RunOptions{
    Live: yourLiveBuilder, // nil → mock-only
})
fmt.Print(evalrunner.FormatSummary(results))
```

Scenario YAML format and the live-builder contract are documented in [references/harness.md](references/harness.md).

## Provider compatibility quirks

- **DeepSeek's reasoner** (e.g. `deepseek-v4-flash`) rejects `tool_choice` entirely. agent-go's `pkg/pool/client.go` retries once with `tool_choice` stripped — you don't have to handle this.
- **`tool_choice` JSON shape**: `"auto" / "required" / "none"` are plain strings; named-tool choice is `{"type":"function","function":{"name":"X"}}`.
- **`web_search_options`** is similarly stripped + retried when an upstream rejects it.

If you're integrating a new provider in your own code, follow `pkg/pool/client.go`'s pattern: detect "unsupported / does not support / invalid" + the param name, strip, retry once.

## Conventions to follow

- **Identity = session UUID, not userID.** Use `github.com/google/uuid` for conversation keys.
- **`task_id` is load-bearing.** Scope new state (history, memory entries, discovered tools) by task when possible.
- **PTC is default-on.** Tools you expose to the sandbox should return stable structured shapes (`{ok, data, error}`-ish), not freeform strings.
- **RAG is opt-in.** Don't gate basic features on embeddings being configured.
- **Use random high ports** (3000+, e.g. 3076, 6759, 43510) for any dev server.

## Storage layout (default `~/.agentgo/`)

```
~/.agentgo/
├── data/
│   ├── agentgo.db   # config, providers, agents, teams, tasks, plans, checkpoints
│   └── cortex.db    # optional memory/vector/graph
├── memories/        # file memory when enabled
├── skills/          # local skills (SKILL.md format)
└── workspace/       # agent working directory
```

Override with `AGENTGO_HOME=/path/to/home`. Each test should use `t.TempDir()` to keep state isolated.

## CLI is also available

The `agentgo` binary is an adapter around the same library — useful for ops, debugging, and one-shot tasks. See [references/cli.md](references/cli.md) for every subcommand: `chat`, `agent`, `team`, `task` (incl. `replay` and `checkpoints`), `eval`, `llm`, `mcp`, `skills`, `memory`, `ptc`, `rag`.

## Key References

- [API patterns](references/api-patterns.md) — code recipes for every public capability
- [Harness engineering](references/harness.md) — lints, eval scenarios, checkpoint+replay in depth
- [CLI usage](references/cli.md) — every `agentgo` subcommand with examples
- [Testing your AgentGo app](references/testing.md) — `*testing.T` helpers, mock LLM, fixtures
