# AgentGo

[![CI](https://github.com/liliang-cn/agent-go/actions/workflows/ci.yml/badge.svg)](https://github.com/liliang-cn/agent-go/actions/workflows/ci.yml)
[![Go Reference](https://pkg.go.dev/badge/github.com/liliang-cn/agent-go/v3.svg)](https://pkg.go.dev/github.com/liliang-cn/agent-go/v3)
[![Go Report Card](https://goreportcard.com/badge/github.com/liliang-cn/agent-go/v3)](https://goreportcard.com/report/github.com/liliang-cn/agent-go/v3)
[![Release](https://img.shields.io/github/v/release/liliang-cn/agent-go)](https://github.com/liliang-cn/agent-go/releases)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)

**Agent framework for Go with local-first AI capabilities.**

AgentGo is a Go framework for building agents that run locally, use tools, keep memory, and compose with each other.

It is centered on `pkg/agent`: one transparent loop, everything-is-a-tool, and determinism enforced by lints rather than by longer prompts. AgentGo is a library — there is no CLI, no UI, no server. You embed it.

## Install

```bash
go get github.com/liliang-cn/agent-go/v3
```

## Core Ideas

- **Agent**: a named runtime with instructions, tools, memory, and sessions.
- **Loop**: one streaming state machine. `Run()` is `RunStream()` plus a collector; sub-agents reuse the same loop.
- **Tool**: everything the agent can do — built-ins, MCP, skills, and sub-agents.
- **Sub-agent**: registered with `WithSubagents(...)` and reached through a single `task(agent_name, prompt)` tool. There is no team, dispatcher or router.
- **Task**: a first-class unit of work with status, events, frames, and output.
- **Memory**: durable local context, separate from cache and RAG.
- **MCP**: tool integration layer for filesystem, web, and external capabilities.
- **Skills**: reusable Markdown/YAML workflows.
- **RAG**: optional document retrieval when embeddings are configured.
- **Output lints**: deterministic post-output checks that re-prompt the model on violation (instead of "please remember to..." paragraphs).
- **Checkpoint + replay**: every terminal task writes a snapshot; crashed/cancelled runs can be re-played from the latest checkpoint.
- **Eval harness**: scenario-driven behavioral evaluation, mock or live LLM, JSON output for cross-commit diffs.

## Minimal Agent

```go
package main

import (
	"context"
	"fmt"

	"github.com/liliang-cn/agent-go/v3/pkg/agent"
)

func main() {
	ctx := context.Background()

	svc, err := agent.New("assistant").
		WithPrompt("You are a concise Go assistant.").
		Build()
	if err != nil {
		panic(err)
	}
	defer svc.Close()

	reply, err := svc.Ask(ctx, "What is AgentGo?")
	if err != nil {
		panic(err)
	}
	fmt.Println(reply)
}
```

## Agent With Memory

```go
svc, _ := agent.New("assistant").
	WithMemory().
	Build()
defer svc.Close()

svc.Chat(ctx, "My name is Alice and I prefer short answers.")
result, _ := svc.Chat(ctx, "What do you know about me?")

fmt.Println(result.Text())
```

## Pluggable memory backends

The four built-in `store_type` values are `file`, `cortex`, `memoryflow` and
`graphflow`. Two plugins ship with the framework: `cortex-remote` for a shared
CortexDB reached over gRPC, and `mcp-memory` for **any** memory service that
speaks MCP. Anything else is a plugin too. There are three seams, in decreasing
order of how much the framework still does for you:

**1. Register a backend by name** (the one to reach for). After registering,
`store_type = "<name>"` in `agentgo.toml` selects it:

```go
func init() {
	agent.MustRegisterMemoryStore("redis", func(cfg agent.MemoryStoreConfig) (domain.MemoryStore, error) {
		// cfg carries Name, Path, DSN, Options, Embedder and Generator.
		return newRedisStore(cfg.DSN, cfg.OptionOr("namespace", "agentgo"))
	})
}

svc, _ := agent.New("assistant").
	WithMemory(
		agent.WithMemoryStoreType("redis"),
		agent.WithMemoryDSN("redis://localhost:43510"),
		agent.WithMemoryOption("namespace", "team"),
	).
	Build()
```

Registration is concurrency-safe and strict: a blank name, a nil factory, a
built-in name, or a duplicate is an error, never a silent overwrite. Use
`agent.UnregisterMemoryStore(name)` to replace one on purpose.

**2. Inject an instance** — skips the registry and the factory:

```go
agent.New("assistant").WithMemory(agent.WithMemoryStore(myStore))
```

**3. Replace the service** — the escape hatch; you own retrieval and injection
policy too:

```go
agent.New("assistant").WithMemoryService(myMemoryService)
```

### `memory.BaseStore`

`domain.MemoryStore` has eighteen methods. Embed `memory.BaseStore` and override
only the ones your backend can actually serve; the rest return
`memory.ErrMemoryStoreUnsupported`, which callers degrade on rather than fail:

```go
type MyStore struct{ memory.BaseStore }

func (s *MyStore) Store(ctx context.Context, m *domain.Memory) error { ... }
func (s *MyStore) SearchByText(ctx context.Context, q string, k int) ([]*domain.MemoryWithScore, error) { ... }
func (s *MyStore) Get(ctx context.Context, id string) (*domain.Memory, error) { ... }
```

Never fake a method you cannot implement — return
`domain.ErrMemoryStoreUnsupported` and let the caller degrade.

### `store_type = "mcp-memory"` — any MCP memory service

`mcp-memory` turns any MCP server with memory tools into an agent's memory
store. It is not written for one product: no tool name and no argument name is
assumed, so the mapping is the integration.

```toml
[memory]
store_type = "mcp-memory"
dsn        = "https://memory.example.com/mcp"   # or a stdio command line

[memory.options]
"tool.store"  = "save_memory"
"tool.search" = "find_memories"
"tool.get"    = "read_memory"
"tool.delete" = "forget_memory"
"tool.list"   = "all_memories"

"arg.store.content" = "text"          # arg.<op>.<canonical> = <remote param>
"arg.store.tags"    = "labels"
"arg.search.query"  = "q"
"arg.search.limit"  = "max_results"

"result.search.items" = "matches"     # dot path to the array ("" = the root)
"result.search.hit"   = "memory"      # the record inside one hit
"result.search.score" = "relevance"

"field.id"      = "uuid"              # field.<canonical> = <remote field>
"field.content" = "text"
```

Presets exist for convenience — `profile = "cortexdb"` expands to a full mapping
for CortexDB's MCP tools, and your own options still win. A profile is always
named explicitly; nothing is ever inferred from a server's tool names. Register
your own with `store.RegisterMCPMemoryProfile(name, options)`.

Covered: `Store`, `StoreWithScope`, `Get`, `Update`, `Delete`, `SearchByText`,
`List` (each only when its `tool.<op>` is configured — an unmapped operation
returns `ErrMemoryStoreUnsupported` rather than pretending). `InitSchema` is a
no-op; the server owns its storage.

Degraded on purpose: the vector `Search` / `SearchBySession` / `SearchByScope`
return **empty, not an error**, because an MCP memory tool takes a query string
and the server owns the embedding model. That makes `memory.Service` fall
through to `SearchByText`, which is the route that actually reaches the server.

Unsupported: `IncrementAccess`, `GetByType`, `Clear`, `DeleteBySession`,
`ConfigureBank`, `Reflect`, `AddMentalModel`.

The store opens its own MCP connection, so it does not depend on the agent's MCP
service being assembled first. If the same server is also listed in
`mcpServers.json`, the process holds two sessions to it — deduplicate on the
host side if that matters. Connection is lazy: building an agent never requires
the memory server to be up. Call `Close()` when you own the store;
`memory.Service.Close()` does not cascade.

Runnable: `examples/memory-custom-store` (seams 1 and 2 + BaseStore),
`examples/memory-remote-cortex` (the shared-CortexDB backend, live),
`examples/memory-mcp` (the MCP backend against an in-process server, no external
dependency).

## Sub-agents are just a tool

```go
svc, _ := agent.New("lead").
	WithSubagents(
		agent.SubagentSpec{
			Name:         "researcher",
			Description:  "Gathers and summarises background information.",
			Instructions: "You research a topic and return a tight, factual brief.",
		},
		agent.SubagentSpec{
			Name:         "writer",
			Description:  "Turns notes into finished prose.",
			Instructions: "You write clear, plain prose from the notes you are given.",
		},
	).
	Build()
defer svc.Close()

result, _ := svc.Run(ctx, "Research X and write two paragraphs about it.")
fmt.Println(result.Text())
```

The model reaches a sub-agent by calling `task(agent_name, prompt)`. It runs through the same loop, so its events bubble up, its answer is linted, and its terminal state is checkpointed.

## Tasks

```go
store, _ := agent.NewStore("agentgo.db")
manager := agent.NewManager(store)
_ = manager.SeedDefaultAgent()

task, _ := manager.Tasks().Submit(ctx, agent.TaskSubmitOptions{
	SessionID: "demo-session",
	AgentName: "Assistant",
	Input:     "Check the current repository status.",
})

done, _ := manager.Tasks().Await(ctx, task.ID)
fmt.Println(done.Status)
fmt.Println(done.Output)
```

## Checkpoint + replay

```go
// Crashed or cancelled task? Re-play it from the last snapshot.
resumed, _ := manager.Tasks().ResumeFromCheckpoint(ctx, task.ID, agent.CheckpointResumeOptions{
	FollowUp: "and now also do X",
})
```

Every terminal state writes a `TaskCheckpoint`; `WithResumeMessages` is the low-level `RunOption` underneath.

## Output lints — moving "please don't" out of prompts

When an agent keeps making the same mistake (ending with "Next steps:...", claiming it sent an email it never sent, answering with nothing at all), don't add another sentence to its instruction. Register a lint:

```go
svc.RegisterOutputLint(agent.LintFunc{
    NameValue: "no_planning_only_finish",
    Fn: func(text string, ctx agent.LintContext) (bool, string) {
        if strings.HasSuffix(strings.TrimSpace(text), "Next steps:") {
            return false, "response reads like a plan; deliver the work or call task_blocked"
        }
        return true, ""
    },
})
```

Every service built with `agent.New(...).Build()` gets the built-ins automatically:

- `no_planning_only_finish` — reject planning-only endings
- `file_task_must_write` — a task that asked for a file must have produced one
- `non_empty_final_answer` — a run cannot terminate with no text
- `task_delivery_contract` — a goal naming a delivery action (send the mail, post the message, write the file) cannot complete unless a matching tool was actually called

Related: a request that refuses tool use is given **zero** tools, and any tool call it makes anyway is refused. Hard constraints are enforced by the runtime, not requested in the prompt.

The runtime works out what the user asked for with one small structured call per run — not by matching phrases, so it behaves the same in every language:

```go
// Declare it yourself and the extraction call is skipped entirely.
result, _ := svc.Run(ctx, "Name the largest planet.", agent.WithToolsDisabled())

// Or state what the run must deliver, and the contract lint enforces it.
result, _ = svc.Run(ctx, goal, agent.WithRequiredDeliverables(
    agent.DeliverableRequirement{Kind: "email", Description: "the summary"},
))

// Off entirely: only what you declared is enforced.
result, _ = svc.Run(ctx, goal, agent.WithConstraintExtraction(false))
```

A blocked run is an outcome, not an error: `result.Err()` stays nil and `result.Text()` carries the agent's explanation, so a caller checking `err` first can't silently discard it. Branch on `result.Blocked`.

## Storage

By default AgentGo uses:

```text
~/.agentgo/
├── data/
│   ├── agentgo.db     # config, providers, agents, tasks, checkpoints
│   └── cortex.db      # optional memory/vector/graph storage
├── memories/          # file memory when enabled
├── skills/            # local skills
└── workspace/         # agent working directory
```

Override the home directory with the `AGENTGO_HOME` environment variable.

## Repository Layout

```text
pkg/agent      framework core: agent, loop, tools, context, hooks/lints, sessions, checkpoints
pkg/mcp        MCP tools and servers
pkg/memory     durable memory
pkg/rag        optional retrieval
pkg/skills     skill loading
pkg/providers  LLM providers (with reasoner-model fallbacks)
pkg/pool       provider pool + token/cost accounting
pkg/poolsvc    process-global pool service for embedders
pkg/store      SQLite storage
eval/          behavioral eval harness (scenarios + runner)
examples/      runnable examples
```

## Development

```bash
make test           # go test ./...
make check          # fmt + vet + test
make eval           # behavioral eval, mock-LLM, CI-safe
make eval-live      # behavioral eval, real provider
```

See `CLAUDE.md` and `docs/dev/PLAN.md` for the harness-engineering roadmap and operational guidance.

## License

MIT
