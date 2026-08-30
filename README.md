# AgentGo

[![CI](https://github.com/liliang-cn/agent-go/actions/workflows/ci.yml/badge.svg)](https://github.com/liliang-cn/agent-go/actions/workflows/ci.yml)
[![Go Reference](https://pkg.go.dev/badge/github.com/liliang-cn/agent-go/v3.svg)](https://pkg.go.dev/github.com/liliang-cn/agent-go/v3)
[![Go Report Card](https://goreportcard.com/badge/github.com/liliang-cn/agent-go/v3)](https://goreportcard.com/report/github.com/liliang-cn/agent-go/v3)
[![Release](https://img.shields.io/github/v/release/liliang-cn/agent-go)](https://github.com/liliang-cn/agent-go/releases)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)

**Agent framework for Go with local-first AI capabilities.**

AgentGo is a Go library for building agents that run locally, use tools, keep memory, and compose with each other. It is centered on `pkg/agent`: one transparent streaming loop, everything-is-a-tool, and determinism enforced by lints and runtime contracts rather than by longer prompts. There is no required CLI, UI, or server — you embed it.

[中文文档](README_zh-CN.md)

## Install

```bash
go get github.com/liliang-cn/agent-go/v3
```

Requires Go 1.25+.

## Quick start

Inject any OpenAI-compatible provider and ask:

```go
package main

import (
	"context"
	"fmt"
	"log"
	"os"

	"github.com/liliang-cn/agent-go/v3/pkg/agent"
	"github.com/liliang-cn/agent-go/v3/pkg/domain"
	"github.com/liliang-cn/agent-go/v3/pkg/providers"
)

func main() {
	llm, err := providers.NewOpenAILLMProvider(&domain.OpenAIProviderConfig{
		BaseURL:  "https://api.deepseek.com/v1",
		APIKey:   os.Getenv("DEEPSEEK_API_KEY"),
		LLMModel: "deepseek-chat",
	})
	if err != nil {
		log.Fatal(err)
	}

	svc, err := agent.New("assistant").
		WithLLM(llm).
		WithPrompt("You are a concise Go assistant.").
		Build()
	if err != nil {
		log.Fatal(err)
	}
	defer svc.Close()

	reply, err := svc.Ask(context.Background(), "What is AgentGo?")
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(reply)
}
```

If you skip `WithLLM`, configuration is loaded from `AGENTGO_HOME` (default `~/.agentgo`), where providers live in `data/agentgo.db`. See `examples/quickstart` for the config-driven variant.

Four entry points, one loop underneath:

- `svc.Ask(ctx, q)` — one shot, returns `(string, error)`.
- `svc.Chat(ctx, q)` — multi-turn with session state, returns `*ExecutionResult`.
- `svc.Stream(ctx, q)` — token channel (`<-chan string`).
- `svc.RunStream(ctx, goal, opts...)` — full runtime events: state updates, tool calls, tool results, partials. `Run()` is `RunStream()` plus a collector.

## Core concepts

- **Agent** — a named runtime with instructions, tools, memory, and sessions, assembled by `agent.New(name).With...().Build()`.
- **Loop** — one streaming state machine. Sub-agents reuse the same loop, so their events bubble up, their answers are linted, and their terminal states are checkpointed.
- **Tool** — everything the agent can do: built-ins, your functions, MCP servers, skills, and sub-agents.
- **Sub-agent** — registered with `WithSubagents(...)`, reached by the model through a single `task(agent_name, prompt)` tool. There is no separate team/dispatcher/router layer.
- **Task** — a first-class unit of work with status, events, frames, checkpoints, and output, persisted in SQLite.
- **Memory** — durable local context, separate from cache and RAG. Pluggable backends.
- **Output lints** — deterministic post-output checks that re-prompt the model on violation, instead of "please remember to..." paragraphs.

## Capabilities

### Custom tools

Register a Go function; the schema is a plain JSON-Schema map:

```go
svc.AddTool("read_config", "Read a service's current configuration",
	map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"service": map[string]interface{}{"type": "string"},
		},
	},
	func(ctx context.Context, args map[string]interface{}) (interface{}, error) {
		return loadConfig(args["service"].(string))
	})
```

Built-in tools include web search, URL fetch, datetime, a scratchpad, and (when a sandbox is attached) command execution and deliverable scanning.

### Sub-agents

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

result, _ := svc.Run(ctx, "Research X and write two paragraphs about it.")
```

Runnable variants (basic, parallel, async, auto-delegation, filtering): `examples/subagent/`.

Configuring sub-agents is also what puts the generic delegation tools —
`delegate_to_subagent`, `delegate_async`, `subagent_send_message` — in the schema
the model sees. An agent with no sub-agents is not offered them, because
delegating with nothing configured only re-runs a clone of the same agent: three
tool schemas (~1.9 KB) on every request for a capability nobody asked for.
`WithDelegation(true)` asks for them anyway (running a sub-goal in an isolated
context and getting back only its result is a real use); `WithDelegation(false)`
withholds them even from an agent that has sub-agents, leaving `task` as the only
route. Either way they stay registered and callable by name — this is exposure,
not registration.

### Memory

```go
svc, _ := agent.New("assistant").WithMemory().Build()

svc.Chat(ctx, "My name is Alice and I prefer short answers.")
result, _ := svc.Chat(ctx, "What do you know about me?")
```

Built-in `store_type` values: `file` (no embedder needed), `cortex`, `memoryflow`, and `graphflow` (`WithGraphMemory()`, needs `WithEmbedder`). Two more ship as plugins: `cortex-remote` (a shared CortexDB over gRPC) and `mcp-memory` (any MCP server with memory tools, mapped via `tool.*` / `arg.*` / `result.*` options — no tool name is assumed).

Three seams for your own backend, in decreasing order of what the framework still does for you:

1. `agent.MustRegisterMemoryStore("redis", factory)` — register by name, then select with `agent.WithMemoryStoreType("redis")`. Registration is concurrency-safe and strict; duplicates are errors, never silent overwrites.
2. `WithMemory(agent.WithMemoryStore(myStore))` — inject an instance.
3. `WithMemoryService(mySvc)` — replace the whole service; you own retrieval and injection policy.

`domain.MemoryStore` has eighteen methods; embed `memory.BaseStore` and override only what your backend can serve — the rest return `memory.ErrMemoryStoreUnsupported`, which callers degrade on rather than fail.

Runnable: `examples/memory-custom-store`, `examples/memory-remote-cortex`, `examples/memory-mcp`.

### Run memory (automatic recall and capture)

`RunMemory` hooks a run's start and end for an external long-term memory system. Recall injects a "Recalled context" section into the system prompt (bounded: 5s timeout, ~10k chars, failures only log); capture runs asynchronously after the run completes, so a run is never blocked by its memory.

```go
type RunMemory interface {
	RecallForRun(ctx context.Context, goal string) (string, error)
	CaptureRun(ctx context.Context, goal, finalText string) error
}

svc, _ := agent.New("ops").
	WithRunMemory(cortexbridge.NewRunMemory(cortexDB)). // or your own impl
	Build()
```

`pkg/cortexbridge.NewRunMemory` is the CortexDB implementation: it captures `DECISION:`-style marker lines and entities into a typed graph and recalls them for later runs. End-to-end demo: `examples/graph-memory-experiment`.

### MCP

```go
svc, _ := agent.New("assistant").
	WithMCP(agent.WithMCPConfigPaths("./mcpServers.json")).
	Build()
```

Servers are declared in `mcpServers.json` (see the sample at the repo root); their tools register alongside built-ins. Runnable: `examples/mcp/basic`, `examples/mcp/advanced`.

### Skills

`WithSkills()` loads reusable Markdown/YAML workflows from `AGENTGO_HOME/skills` (or `agent.WithSkillsPaths(...)`). `Options.RequiredSkills` makes `Build()` fail unless every named skill is installed.

### RAG

Optional document retrieval, only in the path when an embedder is configured:

```go
svc, _ := agent.New("assistant").
	WithEmbedder(embedder). // e.g. providers.NewOpenAIEmbedderProvider(...)
	WithRAG().
	Build()
```

### Structured output

```go
type Brief struct {
	Ticker    string   `json:"ticker"    desc:"uppercase stock symbol"`
	KeyPoints []string `json:"key_points" desc:"3-4 short, factual takeaways"`
}

brief, err := agent.RunTyped[Brief](ctx, svc, "Summarize NVDA.")
```

`RunTyped[T]` derives a JSON Schema from the struct (tags drive names, `desc`, optionality) and returns a parsed `T`. `agent.WithStructuredOutput(spec)` is the hand-written-schema `RunOption`. Enforced two ways: natively via `response_format` on providers that support it (with automatic fallback), and by a deterministic post-validation lint that re-prompts on mismatch. Runnable: `examples/structured-output`.

### Output lints

When an agent keeps making the same mistake, don't add another instruction sentence — register a lint:

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

Every built service gets the built-ins: `no_planning_only_finish`, `file_task_must_write`, `non_empty_final_answer`, and `task_delivery_contract` (a goal naming a delivery action cannot complete unless a matching tool was actually called). The runtime works out what a run must deliver with one small structured call — not phrase matching, so it behaves the same in every language. Declare it yourself and the extraction call is skipped:

```go
result, _ := svc.Run(ctx, "Name the largest planet.", agent.WithToolsDisabled())

result, _ = svc.Run(ctx, goal, agent.WithRequiredDeliverables(
	agent.DeliverableRequirement{Kind: "email", Description: "the summary"},
))

result, _ = svc.Run(ctx, goal, agent.WithConstraintExtraction(false))
```

Constraint extraction is **on by default** and costs one extra structured model
call before the first turn of every run — a small one (temperature 0, 400 output
tokens, 20s cap), but a real round trip. Declaring the contract yourself with
`WithToolsDisabled` / `WithRequiredDeliverables` / `WithRequestedActions` skips
it, and `WithConstraintExtraction(false)` turns it off outright, leaving only
what you declared in force.

A blocked run is an outcome, not an error: `result.Err()` stays nil, `result.Text()` carries the explanation — branch on `result.Blocked`.

### Tasks, checkpoint and replay

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
```

Every terminal state writes a `TaskCheckpoint`. A crashed or cancelled task can be re-played from the latest snapshot:

```go
resumed, _ := manager.Tasks().ResumeFromCheckpoint(ctx, task.ID, agent.CheckpointResumeOptions{
	FollowUp: "and now also do X",
})
```

`agent.WithResumeMessages` is the low-level `RunOption` underneath.

### System prompt length limits

Every system prompt carries a length anchor by default — "keep text between tool
calls to ≤25 words. Keep final responses to ≤100 words unless the task requires
more detail." It is on by default and stays on, because agents in production are
tuned against those numbers. It is an opinion about the answer rather than a rule
about the runtime, though, and it can contradict your own instructions: an agent
told to cite a source for every statement cannot also stay under 100 words.
`agent.New(...).WithLengthLimits(false)` drops the section when your own prompt
owns the length.

### Sandbox and scheduler

`pkg/sandbox` provides isolated execution environments (local process and Docker); attach one with `WithSandbox(sb)` to enable the command-execution and deliverable tools. `pkg/scheduler` runs cron-style scheduled jobs with pluggable executors. `pkg/worktree` has dependency-free helpers for isolated git worktrees.

## Run options

Passed to `Run` / `RunStream` per call:

| Option | Effect |
| --- | --- |
| `WithMaxTurns(n)` | cap loop iterations |
| `WithTemperature(t)` / `WithMaxTokens(n)` | sampling controls |
| `WithThinking(bool)` | provider-side chain-of-thought on/off (DeepSeek reasoner `thinking.type` shape); `false` cuts latency on tool-heavy runs |
| `WithToolsDisabled()` | offer zero tools; any tool call is refused |
| `WithToolAllowlist(names)` / `WithToolDenylist(names)` | restrict the tool surface |
| `WithStructuredOutput(spec)` / `WithStructuredOutputType[T]()` | enforce a JSON shape |
| `WithRequiredDeliverables(...)` / `WithRequestedActions(...)` | declare the delivery contract |
| `WithConstraintExtraction(bool)` | toggle the per-run constraint-extraction call (default on; off saves one model call per run) |
| `WithSessionID(id)` / `WithTaskID(id)` / `WithRunID(id)` / `WithParentTaskID(id)` | identity and lineage |
| `WithResumeMessages(msgs)` | continue from prior history (checkpoint replay) |
| `WithInputParts(...)` / `WithInputImages(paths...)` | multimodal input |
| `WithMaxBudgetUSD(x)` | stop the run when estimated spend exceeds the budget |
| `WithAutoCompaction(threshold, keep)` / `WithoutAutoCompaction()` | context compaction policy |
| `WithDebug(bool)` | verbose logging for one run |

Builder-side options (`agent.New(...).With...`): `WithLLM`, `WithEmbedder`, `WithConfig`, `WithPrompt` / `WithSystemPrompt`, `WithMemory` / `WithGraphMemory` / `WithMemoryService`, `WithRunMemory`, `WithMCP`, `WithSkills`, `WithRAG`, `WithSubagents`, `WithDelegation`, `WithLengthLimits`, `WithSandbox`, `WithAutonomy`, `WithTool(s)`, `WithObserver`, `WithProgress`, `WithDBPath`, `WithDebug`, and `WithOptions(agent.Options{...})` for low-frequency knobs (permission policy, tool-execution policy, required skills, extra modules, observers).

## Providers

`pkg/providers` speaks the OpenAI-compatible API, which covers OpenAI, DeepSeek, Ollama, LM Studio, vLLM, DashScope/Qwen, and most proxies:

```go
llm, _ := providers.NewOpenAILLMProvider(&domain.OpenAIProviderConfig{
	BaseURL:  "http://localhost:11434/v1", // Ollama
	LLMModel: "qwen3",
})
```

Provider quirks are handled in the library, not your code: DeepSeek's reasoner rejecting pinned `tool_choice`, `response_format` fallbacks, `reasoning_content` from DeepSeek/Ollama, split streaming tool-call deltas, and servers that omit usage on streams.

**Usage and cache metering.** Every `domain.GenerationResult` carries provider-reported `Usage` (`domain.TokenUsage`): `PromptTokens`, `CompletionTokens`, and `CachedPromptTokens` — the prompt-cache-hit portion, billed at a deep discount (OpenAI ~0.5x, DeepSeek ~0.26x). Both providers cache automatically; the runtime keeps its context prefix byte-stable across turns so those hits actually happen. `Usage` is nil when the provider reported nothing.

**Multi-provider pool.** `pool.NewPool` load-balances across providers with selection strategies, per-provider concurrency limits, and capability levels; the pool implements the generator interface, so it drops straight into `WithLLM`:

```go
brain, _ := pool.NewPool(pool.PoolConfig{
	Enabled:  true,
	Strategy: pool.StrategyRoundRobin,
	Providers: []pool.Provider{
		{Name: "fast", BaseURL: base1, Key: key1, ModelName: "deepseek-chat", MaxConcurrency: 5},
		{Name: "local", BaseURL: "http://localhost:11434/v1", ModelName: "qwen3"},
	},
})
svc, _ := agent.New("assistant").WithLLM(brain).Build()
```

`pkg/providers.LLMPool` is the lower-level pool over `domain.LLMProvider` instances (round-robin / random / least-load strategies, failover, health checks).

## Observability

Implement `agent.Observer` (embed `agent.BaseObserver`, override what you need) and register with `WithObserver` or `Options.Observers`. Callbacks fire at the model / tool / sub-agent / checkpoint seams, with stable span and call IDs for pairing start/end:

```go
type usage struct{ agent.BaseObserver }

func (u *usage) OnModelEnd(ctx context.Context, info agent.ModelInfo, res *agent.ModelResult, err error) {
	if res != nil {
		log.Printf("round=%d tokens=%d cached=%d dur=%dms",
			info.Round, res.TokensUsed, res.CachedTokens, res.DurationMs)
	}
}
```

`ModelResult.CachedTokens` is the prompt-cache-hit portion of `TokensUsed` — cache hits are heavily discounted, so `TokensUsed` alone overstates cost. `pkg/otelobserver` bridges the same callbacks to OpenTelemetry spans (`otelobserver.New(tracerProvider)`).

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

## Repository layout

```text
pkg/agent         framework core: agent, loop, tools, context, hooks/lints, sessions, checkpoints, run memory
pkg/domain        shared types: messages, generation results, token usage, provider interfaces
pkg/providers     OpenAI-compatible providers + LLMPool (failover, health checks)
pkg/pool          provider pool + token/cost accounting
pkg/poolsvc       process-global pool service for embedders
pkg/mcp           MCP client, tools and servers
pkg/memory        durable memory service + BaseStore
pkg/cortexbridge  CortexDB-backed knowledge graph / RAG / RunMemory
pkg/rag           optional retrieval
pkg/skills        skill loading
pkg/sandbox       local / Docker execution sandboxes
pkg/scheduler     cron-style job scheduling
pkg/otelobserver  Observer -> OpenTelemetry bridge
pkg/store         SQLite storage
pkg/worktree      git worktree helpers
eval/             behavioral eval harness (scenarios + runner)
examples/         runnable examples
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
