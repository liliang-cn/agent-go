# AgentGo

[![CI](https://github.com/liliang-cn/agent-go/actions/workflows/ci.yml/badge.svg)](https://github.com/liliang-cn/agent-go/actions/workflows/ci.yml)
[![Go Reference](https://pkg.go.dev/badge/github.com/liliang-cn/agent-go/v3.svg)](https://pkg.go.dev/github.com/liliang-cn/agent-go/v3)
[![Go Report Card](https://goreportcard.com/badge/github.com/liliang-cn/agent-go/v3)](https://goreportcard.com/report/github.com/liliang-cn/agent-go/v3)
[![Release](https://img.shields.io/github/v/release/liliang-cn/agent-go)](https://github.com/liliang-cn/agent-go/releases)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)

**A Go library for running an agent loop that can be trusted to run for a long time.**

AgentGo is not a chat wrapper and not an orchestration product. It is one streaming
loop — assemble context, call the model, execute tools, lint the answer, terminate —
with everything an agent can do exposed to that loop as a tool, and with the things a
prompt cannot guarantee (a file that must exist, an email that must have been sent, a
tool the user forbade) enforced by the runtime instead. Around the loop sit the parts a
run needs to survive hours rather than minutes: sessions and replayable checkpoints,
context compaction, a persisted plan, retries, cost ceilings, and a supervisor that
drives one task across many runs.

There is no CLI, no UI and no server. You embed `pkg/agent` in your own program.

[中文文档](README_zh-CN.md)

## Install

```bash
go get github.com/liliang-cn/agent-go/v3
```

Requires Go 1.25+.

## Quick start

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

Without `WithLLM`, the provider comes from `AGENTGO_HOME` (default `~/.agentgo`), where
providers live in `data/agentgo.db`. `examples/quickstart` is the config-driven variant.

Every entry point runs the same loop:

| call | returns | use when |
| --- | --- | --- |
| `svc.Ask(ctx, q)` | `(string, error)` | one question, one answer |
| `svc.Chat(ctx, q)` | `*ExecutionResult` | multi-turn, session state kept |
| `svc.Stream(ctx, q)` | `<-chan string` | tokens as they arrive |
| `svc.Run(ctx, goal, opts...)` | `*ExecutionResult` | a goal, with tools and run options |
| `svc.RunStream(ctx, goal)` / `RunStreamWithOptions(ctx, goal, opts...)` | `<-chan *Event` | every runtime event: state, tool calls, tool results, partials, checkpoints |
| `svc.RunSegments(ctx, goal, LongRunConfig{...})` | `*LongRunResult` | one task that needs many runs |

`Run` is `RunStream` plus a collector. There is no second, non-streaming implementation.

## The seven concepts

`pkg/agent` deliberately contains exactly seven things. A change that does not fit one of
them probably does not belong there.

| concept | what it is |
| --- | --- |
| **Agent** | a name, a system prompt, a model, a tool set, optional sub-agents — `agent.New(name).With...().Build()` |
| **Loop** | one streaming state machine: assemble context → call the model → execute tools → lint the answer → terminate. `Runtime.loop` is the only implementation; sub-agents run a child runtime over the same service |
| **Tool** | everything the agent can do: built-ins, your Go functions, MCP servers, skills, sub-agents. Each declares `ReadOnly` / `ConcurrencySafe` / `Destructive` / `InterruptBehavior`, which is what batching, permissioning and cancel act on |
| **Context** | what the model sees each turn: message assembly, task-scoped history, the recent/older split, compaction, skill reminders, recalled memory |
| **Hooks + Lints** | the deterministic layer. Hooks bracket the run and each tool call; a lint rejects a final answer and forces a retry |
| **Session + Checkpoint** | a session UUID owns a conversation; every terminal state writes a replayable checkpoint, and a long run can write one every N rounds |
| **Events** | the single output channel. Observers, activity logs and UIs all read the same stream |

There is no team, dispatcher, router, handoff or role hierarchy. Composition is
`WithSubagents(...)`, which gives the model one tool: `task(agent_name, prompt)`.

## How a run ends

A run has outcomes, and only some of them are errors:

```go
result, err := svc.Run(ctx, goal)
switch {
case err != nil:                       // the runtime itself failed
case result.Cancelled:                 // someone pressed stop; err is nil
case result.Blocked:                   // the agent could not proceed; result.Text() says why
case result.StopReason == agent.StopReasonMaxTurns:
	// the round budget ran out; Success may still be true because the
	// runtime synthesised an answer from what it had — not the same thing
case result.Success:
}
```

`StopReason` distinguishes `end_turn`, `max_turns`, `max_tokens`, `max_budget_usd`,
`refusal`, `lint_exhausted`, `stop_hook`, `error_during_execution` and `cancelled`.
`result.Usage` carries the provider's token accounting including the prompt-cache split;
`result.EstimatedCostUSD` prices it.

## Capabilities

### Tools

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

Built-ins cover web search, URL fetch, date/time resolution, a scratchpad plan, tool
search and, with a sandbox attached (`WithSandbox`), shell and file access inside that
sandbox plus deliverable scanning. Tools are offered to the model sorted by name, because
tool schemas sit inside the prompt prefix and a prefix that changes between rounds
defeats the provider's prompt cache.

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

A sub-agent is a child runtime over the same `Service`: its own session, a narrower tool
surface, a different event sink. It returns only its final answer; its events bubble up
nested. Configuring sub-agents also exposes the generic delegation tools
(`delegate_to_subagent`, `delegate_async`, `subagent_send_message`); an agent with none
configured is not offered them. `WithDelegation(bool)` overrides that either way.
Runnable variants: `examples/subagent/`.

### Memory

```go
svc, _ := agent.New("assistant").WithMemory().Build()

svc.Chat(ctx, "My name is Alice and I prefer short answers.")
result, _ := svc.Chat(ctx, "What do you know about me?")
```

Both sides of memory run on every turn and each has exactly one switch:
`WithMemoryRetrieval(false)` and `WithMemoryAutoStore(false)` (as `MemoryOption`s).
Auto-store asks the model once whether the turn is worth keeping; nothing inspects the
wording of the request to decide either side.

Built-in `store_type` values: `file` (no embedder needed), `cortex`, `memoryflow`,
`graphflow` (`WithGraphMemory()`, needs `WithEmbedder`). Two more ship as plugins:
`cortex-remote` (a shared CortexDB over gRPC) and `mcp-memory` (any MCP server with
memory tools, mapped entirely by `tool.*` / `arg.*` / `result.*` options — no tool name
is assumed and there is no synonym table).

Your own backend, in decreasing order of what the framework still does for you:

1. `agent.MustRegisterMemoryStore("redis", factory)`, then `WithMemoryStoreType("redis")`.
   Registration is strict: duplicates and built-in names are errors.
2. `WithMemory(agent.WithMemoryStore(myStore))` — inject an instance.
3. `WithMemoryService(mySvc)` — replace the whole service; retrieval and injection are yours.

`domain.MemoryStore` has eighteen methods; embed `memory.BaseStore` and implement what
your backend can serve — the rest return `ErrMemoryStoreUnsupported`, and callers degrade
on it instead of failing. Runnable: `examples/memory-custom-store`,
`examples/memory-remote-cortex`, `examples/memory-mcp`.

### Run memory

`RunMemory` brackets a run for an external long-term memory: recall injects a "Recalled
context" section into the system prompt (bounded: 5s, ~10k chars, failures only log);
capture runs after the run completes and never blocks it.

```go
type RunMemory interface {
	RecallForRun(ctx context.Context, goal string) (string, error)
	CaptureRun(ctx context.Context, goal, finalText string) error
}

svc, _ := agent.New("ops").
	WithRunMemory(cortexbridge.NewRunMemory(cortexDB)). // or your own
	Build()
```

`pkg/cortexbridge.NewRunMemory` is the CortexDB implementation. Demo:
`examples/graph-memory-experiment`.

### MCP, skills, RAG

```go
svc, _ := agent.New("assistant").
	WithMCP(agent.WithMCPConfigPaths("./mcpServers.json")). // tools from MCP servers
	WithSkills().                                          // SKILL.md workflows from AGENTGO_HOME/skills
	WithEmbedder(embedder).WithRAG().                      // document retrieval; only active with an embedder
	Build()
```

Skills are not all dumped into the prompt: the runtime surfaces a small relevant subset
per turn through `<skill-discovery>` reminders and activates the matching `skill_*`
tools. RAG is optional everywhere — an install with no embedding model still has agent,
tools, MCP and file memory. Runnable: `examples/mcp/basic`, `examples/mcp/advanced`.

### Structured output

```go
type Brief struct {
	Ticker    string   `json:"ticker"    desc:"uppercase stock symbol"`
	KeyPoints []string `json:"key_points" desc:"3-4 short, factual takeaways"`
}

brief, err := agent.RunTyped[Brief](ctx, svc, "Summarize NVDA.")
```

`RunTyped[T]` derives the JSON Schema from the struct and returns a parsed `T`;
`WithStructuredOutput(spec)` / `WithStructuredOutputType[T]()` are the run options.
Enforced natively through `response_format` where the provider supports it (with
fallback), and by a post-validation lint that re-prompts on mismatch. Runnable:
`examples/structured-output`.

## The deterministic layer

When a model keeps making the same mistake, the fix is not another sentence in the
prompt. It is a lint the runtime enforces:

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

A failing lint appends structured feedback and re-prompts, bounded by a retry budget;
exhaustion blocks the run. Every service gets four built-ins: `no_planning_only_finish`,
`file_task_must_write`, `non_empty_final_answer` and `task_delivery_contract` (a goal
that names a delivery action cannot complete unless a matching tool was actually called
*and* such a tool was available). `LintContext` carries both what ran and what could
have run, so a lint can tell "skipped a capability it had" from "never had it".

**Hard constraints live in the runtime, not the prompt.** A user who forbids tools is
obeyed by offering none — the tool list is emptied, and any call the model emits anyway
is refused with structured feedback. What a run must deliver is resolved once, at the
start of the loop, by one small temperature-0 structured model call with a keyword-free
prompt. It is never decided by matching phrases in the goal, so it behaves the same in
every language. Declare the contract yourself and that call is skipped:

```go
svc.Run(ctx, "Name the largest planet.", agent.WithToolsDisabled())
svc.Run(ctx, goal, agent.WithRequiredDeliverables(
	agent.DeliverableRequirement{Kind: "email", Description: "the summary"}))
svc.Run(ctx, goal, agent.WithConstraintExtraction(false)) // off entirely
```

The rule generalises: **no hardcoded phrase or regexp table anywhere in the framework
reads the user's request and changes behaviour.** Ranking (tool search, BM25) reads the
request; verdicts do not. Checks on the *model's* output — the lints above, refusal
detection, planning-only endings — are the output side, which is where deterministic
checks belong.

## Extensions

One concern often touches several seams. PII handling masks tool results,
rejects a final answer that leaks, and wants to appear in the run's telemetry —
three interfaces, three registrations, and nothing that says they belong
together. An `Extension` is that bundle:

```go
svc, _ := agent.New("support").
	WithExtensions(
		logging.New(os.Stderr), // pkg/extensions/logging — the activity log
		pii.New(),              // pkg/extensions/pii     — mask tool results, lint the answer
		usage.New(),            // pkg/extensions/usage   — tokens by model, priced
	).
	Build()
```

An extension implements `Name()` and whichever of the optional capabilities it
needs; `Build()` detects each one by type assertion and wires it into the right
seam. Extensions run in the order listed, at every seam.

| capability | seam | what it may do |
| --- | --- | --- |
| `Observer` | model turns, tool calls, retries, compaction, checkpoints, segments | see |
| `OutputLint` | the final answer | reject and force a retry |
| `Module` | the tool registry | add tools |
| `ContextContributor` | before the first turn | append system messages — additive only, never rewrite the goal |
| `ToolCallFilter` | before a tool runs | rewrite its arguments, or refuse it with a reason the model sees |
| `ToolResultFilter` | after a tool runs | replace what the model sees; an error fails closed |
| `RunLifecycle` | run start / run end | veto a run before its first turn; see how every run ended |
| `Lifecycle` | `Build()` / `Close()` | open and release a resource, started in order and stopped in reverse |
| `HookProvider` | any `HookEvent` | the escape hatch for `stop`, `pre_compact`, sub-agent events |

This is deliberately not a middleware chain. There is no `next()`: an extension
cannot wrap the loop, skip a stage, or call the model itself, which is what keeps
one loop one loop. A `Service` runs many tasks at once and every extension is
shared by all of them, so its methods must be safe to call concurrently; the
three shipped ones are, and `go test -race` covers twelve runs through every
seam of one extension. Runnable: `examples/extensions`.

Anyone can write one: it is a Go type in your own module, and nothing in the
framework has to know about it. The capability interfaces and their argument
types are the extension API and follow the module's semantic version.
`pkg/extensiontest` builds a real service over a scripted model so an extension
is tested at the seams the loop actually calls, with no model behind it.
[docs/extensions.md](docs/extensions.md) is the contract;
`examples/extensions-thirdparty` is a complete extension in a separate module.

It does not have to be Go either. `pkg/extensions/exec` runs a plugin as a
subprocess speaking a small versioned JSON protocol over stdio —
`exec.New("redact", []string{"python3", "plugins/redact.py"})` is an ordinary
extension, so nothing in the loop changes. The plugin names the capabilities it
implements in a handshake and only those are ever sent to it; one that hangs or
dies fails closed at every seam, so an unchecked tool result never reaches the
model. Runnable: `examples/extensions-exec`, whose reference plugin is 90 lines
of stdlib Python.

## Long-running work

A run that must last hours is not a longer run. It is many runs, and the framework is
built so that the parts which make that survivable are the runtime's job rather than the
caller's.

### One task, many runs

```go
result, err := svc.RunSegments(ctx,
	"Work through this in steps. Keep a scratchpad plan, and when you finish a "+
		"step record what it produced as its note — that note is all the next "+
		"stretch of work will have to go on.",
	agent.LongRunConfig{
		MaxSegments:            40,
		RoundsPerSegment:       60,
		MaxConsecutiveFailures: 3,
		MaxTotalCostUSD:        5,
		MaxDuration:            8 * time.Hour,
	})
if result.Done() { /* LongRunStopFinished: the task itself is complete */ }
```

`RunSegments` is not a second engine: it calls `Run`, reads why the run stopped, and
calls `Run` again. Each segment gets a **fresh session** — that is what keeps context
from growing across a task that runs all day — while one **task id** spans them all, so
checkpoints and the plan stay coherent. What carries across segments is what was actually
established: the plan (persisted through `PlanStore`, SQLite by default when a store is
present), the workspace, and run memory.

Two gates decide that a segment *finished the task* rather than merely ended: its
`StopReason` was not `max_turns`, and (unless `AllowIncompletePlan`) the stored plan has
no unchecked steps. `LongRunStop` is a separate type from `StopReason` on purpose: "the
task finished" and "the supervisor stopped asking" are different statements. The
supervisor also stops on `consecutive_failures`, `time_limit`, `cost_limit`, `blocked`,
`cancelled`, `segment_budget_exhausted`, and `unproductive_segments` — segments that ran
out of rounds having changed nothing, which a failure budget never sees.
`RunSegmentsStream` is the same with the event channel exposed.

### What a long run needs from the runtime

| concern | mechanism |
| --- | --- |
| round budget | `AutonomyProfile.MaxRounds` / `WithMaxTurns`; a run that hits it reports `max_turns`, not success |
| provider blinks | `WithLLMRetries(n)` — 502s, rate limits and region refusals are retried; a permanent 4xx that merely mentions "timeout" is not, and `context.Canceled` never is |
| truncation | a reasoning model can spend the whole output budget before writing a byte. An empty turn with `finish_reason=length` is its own outcome, and the budget is raised and asked again (bounded), instead of being judged a refusal |
| context growth | compaction at a token threshold (default 60k, counting tool arguments), with a summary budget that escalates when the summary would not fit |
| prompt cache | `WithPromptCache(true)` places explicit breakpoints on the prefix and the history tail; independently, the system context and the tool list are kept byte-stable across rounds so implicit caches hit |
| crash | `AutonomyProfile.CheckpointEveryRounds` writes snapshots while the run is still going; `ResumeFromCheckpoint` continues from the latest with the same task id |
| money | `WithMaxBudgetUSD` per run, `MaxTotalCostUSD` across a task. Pricing comes from `pool.RegisterModelPricing` (yours) over a bundled table (fallible); an unpriced model is reported as *unknown*, not as free, and the run warns once |
| duplicate calls | a repeated identical tool call is answered "unchanged" — and the record is cleared by any state-changing call and by compaction, because a re-read after a write is a different read |

`examples/long-run` configures one run for hours; `examples/segmented-run` drives a task
across many.

### Watching it

A run in flight is nearly invisible by default: its conversation reaches the store when it
ends and its events go to whoever called `RunStream`. `agent.NewActivityLog(w)` is an
`Observer` that narrates a run as flat, greppable lines — one per model turn, tool call,
sub-agent, retry, compaction and checkpoint:

```go
logf, _ := os.Create("run.log")
svc, _ := agent.New("worker").WithObserver(agent.NewActivityLog(logf)).Build()
```

Attach it to anything you cannot watch interactively. A model's own report of its
progress is not evidence; the log is.

## Cancellation and lifecycle

A stop is an outcome, not an error. `Cancel()` stops every run on the service,
`CancelRun(id)` one run (name it with `WithRunID`), `CancelSession(id)` one conversation;
`ActiveRuns()` lists what is in flight. The run terminates with `workflow_cancelled`,
writes a checkpoint so it can be resumed, and returns `result.Cancelled = true` with a nil
error. All three defer while a tool declared `InterruptBehavior: block` is mid-execution.
Scheduled executions (`pkg/scheduler`) have their own per-execution registry so one run
can be stopped without touching the timers. Runnable: `examples/cancel`.

A `Service` owns exactly one long-lived resource, its store. `Close()` is idempotent,
cancels in-flight runs, and every entry point afterwards fails with `ErrServiceClosed` —
a scheduler pointed at a closed service cannot keep firing turns whose history silently
fails to persist. A failed history write is an ERROR log plus an `EventTypeError` event,
not a warning.

## Tasks, checkpoints and replay

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

resumed, _ := manager.Tasks().ResumeFromCheckpoint(ctx, task.ID,
	agent.CheckpointResumeOptions{FollowUp: "and now also do X"})
```

`Manager` is the application-level host, not an orchestrator: it owns the store, caches
one `Service` per named agent, and exposes the task surface. Every terminal state writes a
`TaskCheckpoint` (capped per task and pruned); `WithResumeMessages` is the run option
underneath replay. Task history, plan, memory and discovered tools are all scoped by
`task_id`. Runnable: `examples/task-store`.

## Run options

Passed to `Run` / `RunStreamWithOptions` / `RunSegments` per call:

| option | effect |
| --- | --- |
| `WithMaxTurns(n)` | round budget for this run |
| `WithMaxTokens(n)` / `WithTemperature(t)` / `WithThinking(bool)` | sampling; `WithThinking(false)` cuts latency on tool-heavy runs |
| `WithLLMRetries(n)` | retry transient provider errors before giving up |
| `WithMaxBudgetUSD(x)` | stop when estimated spend exceeds the budget |
| `WithToolsDisabled()` / `WithToolAllowlist(names)` / `WithToolDenylist(names)` | the tool surface |
| `WithStructuredOutput(spec)` / `WithStructuredOutputType[T]()` | enforce a JSON shape |
| `WithTenant(id)` | label the run's owner for limits, cancellation and billing |
| `WithRequiredDeliverables(...)` / `WithRequestedActions(...)` / `WithConstraintExtraction(bool)` | the delivery contract |
| `WithSessionID` / `WithTaskID` / `WithRunID` / `WithParentTaskID` / `WithPlanKey` | identity, lineage, which plan |
| `WithResumeMessages(msgs)` / `WithPriorToolCalls(names)` | continue from history |
| `WithInputParts(...)` / `WithInputImages(paths...)` | multimodal input |
| `WithAutoCompaction(threshold, keep)` / `WithoutAutoCompaction()` | compaction policy |
| `WithDebug(bool)` | verbose logging for one run |

Builder options: `WithLLM`, `WithEmbedder`, `WithConfig`, `WithPrompt` /
`WithSystemPrompt`, `WithMemory` / `WithGraphMemory` / `WithMemoryService`,
`WithRunMemory`, `WithMCP`, `WithSkills`, `WithRAG`, `WithSubagents`, `WithDelegation`,
`WithLengthLimits`, `WithSandbox`, `WithAutonomy`, `WithPlanStore`, `WithTaskStore`,
`WithPromptCache`, `WithExtensions`, `WithTool(s)`, `WithObserver`, `WithProgress`,
`WithDBPath`, `WithDebug`, and `WithOptions(agent.Options{...})` for the low-frequency knobs
(permission policy, tool-execution policy, required skills, extra modules, observers).

## Providers

`pkg/providers` speaks the OpenAI-compatible API — OpenAI, DeepSeek, Ollama, LM Studio,
vLLM, DashScope/Qwen and most gateways:

```go
llm, _ := providers.NewOpenAILLMProvider(&domain.OpenAIProviderConfig{
	BaseURL:  "http://localhost:11434/v1", // Ollama
	LLMModel: "qwen3",
})
```

Provider quirks are handled in the library: a reasoner that rejects a pinned
`tool_choice`, `response_format` fallbacks, `reasoning_content`, split streaming tool-call
deltas, servers that omit usage on streams. Optional request fields (`web_search_options`,
`tool_choice`, cache markers) follow one shape: send, detect the rejection, strip, retry
once. Native web search is **detected, never assumed**: a rejection proves unsupported,
grounding evidence in a response proves supported, acceptance alone proves nothing —
there is no model-name capability table.

**Usage and cache metering.** Every `domain.GenerationResult` carries the provider's
`Usage`: `PromptTokens`, `CompletionTokens`, `CachedPromptTokens` (the cache-hit portion,
billed at a deep discount) and `CacheWriteTokens`. It is nil when the provider reported
nothing, which is an honest unknown rather than a zero. If cached tokens read zero on a run
whose history grows every round, find out whether the cache is broken or the reporting is
before concluding anything about the loop.

**Multi-provider pool.** `pool.NewPool` load-balances across providers (round-robin,
random, least-load; per-provider concurrency; failover) and implements the generator
interface, so it drops straight into `WithLLM`. `pkg/providers.LLMPool` is the lower-level
pool over `domain.LLMProvider` instances.

## Observability

Implement `agent.Observer` (embed `agent.BaseObserver`, override what you need) and
register with `WithObserver`. Callbacks: `OnModelStart/Delta/End`, `OnToolStart/End`,
`OnSubAgentStart/End`, `OnCheckpoint`, `OnLint`, `OnModelRetry`, `OnCompaction`,
`OnError`, `OnSegment` — with stable span and call ids for pairing. Every retry inside a
model turn has its own callback, so a turn that took three attempts does not look like one
that took one.

```go
type usage struct{ agent.BaseObserver }

func (u *usage) OnModelEnd(ctx context.Context, info agent.ModelInfo, res *agent.ModelResult, err error) {
	if res != nil {
		log.Printf("round=%d tokens=%d cached=%d dur=%dms",
			info.Round, res.TokensUsed, res.CachedTokens, res.DurationMs)
	}
}
```

`pkg/otelobserver` bridges every callback to OpenTelemetry: spans for model
turns, tools and sub-agents, span events for lints, retries, compaction, errors
and segments, and — with `WithMeterProvider` — metrics for calls, tokens (with
the cache split), cost, retries and lint rejections.

**Before the run.** `svc.Preview(ctx, goal, opts...)` returns the messages and
tools the first model turn would receive, without calling the model or writing
anything: the dry run for "what will it actually see".

**During the run.** `agent.NewActivityLog(w)` narrates for a person;
`agent.NewTraceWriter(w)` writes the same events as JSONL for a program, one
object per line with run, task and session ids. Every log line the loop writes
carries those ids too, and `log.SetLogger` routes the framework's logging
through your own `slog` handler.

**The process itself.** Every callback above reports what the *agent* did.
`agent.SampleProcess()` reports what the *program* is using — live heap, heap
objects, goroutines, GC, current and peak RSS, cumulative CPU, uptime — with no
cgo and no dependency. A goroutine leaked per tool call fails no lint and no
test; it fails at 03:00 with the OOM killer, and every agent metric right up to
the last one looks healthy.

```go
type watch struct{ agent.BaseObserver }

// Optional interface: one reading per round, plus a final one at every
// terminal path. Nothing is sampled when no observer asks for it.
func (watch) OnResourceSample(_ context.Context, s agent.ResourceSample) {
	log.Printf("round=%d heap=%d goroutines=%d rss=%d",
		s.Round, s.Stats.HeapAllocBytes, s.Stats.Goroutines, s.Stats.RSSBytes)
}
```

The same readings reach `TraceWriter` as one `"event":"resource"` line per
round — where a long run's memory curve lives — and `pkg/otelobserver`
publishes them as observable gauges (`agentgo.process.heap.bytes`,
`.goroutines`, `.rss.bytes`, `.cpu.seconds` and the rest), which keep reporting
while the service sits idle between runs. What cannot be read on a platform is
reported as unknown rather than zero: a zero looks like a process using no
memory.

**Before anything.** `agent.Doctor(ctx, ...)` inspects a home — database,
providers, memory store type, MCP config, skills — and reports what is wrong
and how to fix it, without calling a model. It is what the removed CLI's status
command used to be. Runnable: `examples/preview`, `examples/trace`,
`examples/otel`, `examples/doctor`, `examples/resources`.

## Time is relative to when it was said

A memory written on the 1st saying "明天要去医院" describes the 2nd. Recalled on
the 2nd, the text still says "明天" — and a model with no anchor books the
appointment for the 3rd. The sentence is correct, and it is correct about a day
that has passed.

`pkg/timeaware` resolves this with the model and **no phrase table**. A map of
`明天 → +1`, `tomorrow → +1` serves exactly the languages someone enumerated and
silently does nothing for everyone else, which reads identically to "this text
mentioned no date".

```go
svc, _ := agent.New("assistant").
	WithTimezone(shanghai).   // the person's zone, not the server's
	Build()
```

- **Writing resolves, at no extra cost.** The fields ride on the extraction
  call the memory writer already makes, on a background worker — nothing on the
  agent's turn waits for it. `timeaware.SchemaFields()` and `PromptRules(anchor)`
  graft the same contract onto any structured call you already make;
  `Resolver.Resolve` is the standalone route, one call for however many texts.
- **Reading calls nothing.** Every recalled memory carries
  `(written 2026-09-01, yesterday; "明天" = 2026-09-02, today)`, computed from
  two timestamps.
- **Local time is the part most likely to be wrong.** The anchor is converted
  into the person's zone before the model sees it, the prompt states the offset
  *and* the IANA name (an offset cannot express a daylight-saving rule), and day
  arithmetic converts both endpoints first — 23:30 in Tokyo is 16:30 the same
  afternoon in Vienna.
- **Degradation is the design.** No model, a timeout or an unparsable answer
  leaves the memory unresolved and still carrying when it was written, which is
  true in every language. Runnable: `examples/timeaware`.

## Memory backends

Seven shipped plugins besides the four built-ins, selected by one string:

```go
svc, _ := agent.New("assistant").
	WithMemory(
		agent.WithMemoryStoreType("qdrant"),
		agent.WithMemoryDSN("http://192.168.1.10:6333"),
	).Build()
```

| store_type | needs an embedder? | needs another service? |
| --- | --- | --- |
| `qdrant` | no, BM25 server-side | no |
| `meilisearch` | no | no |
| `weaviate` | no, `vectorizer: none` | no |
| `surrealdb` | no | no |
| `mem0` | yes, any OpenAI-compatible URL | Postgres |
| `cortex-remote` | no, the server owns it | a CortexDB |
| `mcp-memory` | depends on the server | an MCP server |

The four in the first block are a single downloaded binary each and need no API
key of any kind. All seven were verified against a live server through
`pkg/memory/memorystoretest`, which asserts on the text the agent is actually
shown — a Store/Search round trip passes while the agent sees nothing.

Both sides of this — picking one, and writing one — are in `examples/memory-backends`.

Adding your own is a registration, never a new case in a switch:

```go
agent.RegisterMemoryStore("my-store", func(cfg domain.MemoryStoreConfig) (domain.MemoryStore, error) {
	return newMyStore(cfg.DSN), nil
})
```

Implement only what your backend can do: embed `memory.BaseStore` and the rest
return `ErrMemoryStoreUnsupported`, which callers degrade past. An honest
unsupported beats a fake implementation.

## Swapping memory at runtime

Which backend a service uses was decided once, at construction. Moving a user
from local file memory to a shared brain meant building a second Service and
throwing the first away, with its conversation and its in-flight runs.

```go
previous := svc.SetMemoryService(shared) // already drained and closed
svc.SetMemoryService(nil)                // turns memory off; runs still work
```

The outgoing service is closed on purpose: it holds a background writer with
extractions not yet persisted, and dropping the pointer would strand them
silently. Swap when the service is idle — a run mid-turn can retrieve from the
old backend and store into the new. Runnable: `examples/memory-swap`.

## Multimodal

Images go in and come back out:

```go
res, _ := svc.Run(ctx, "What is in this image?", agent.WithInputImages("photo.png"))

// A model asked to draw returns no text at all — the picture arrives here.
for _, part := range res.OutputParts {
	if part.Image != nil {
		raw, _ := base64.StdEncoding.DecodeString(part.Image.Base64)
		os.WriteFile("drawn.jpg", raw, 0o644)
	}
}
```

`WithInputAudio` and `WithInputFiles` attach recorded audio and documents the
same way, using OpenAI's `input_audio` and `file` blocks. Those two follow the
documented wire format but were not exercised against a live endpoint here,
which their doc comments say plainly; the image path was verified in both
directions against a real model.

## Background work

Some things a person would never stand and wait for: a crawl, a build, a report
over a week of logs. Making the model wait stops the conversation and burns the
round budget on a tool that is merely slow.

A host could always start detached work. Now the agent can too:

```go
svc, _ := agent.New("assistant").
	WithBackgroundTasks(4).   // gives it background_start / _check / _cancel
	Build()

task, _ := svc.StartBackgroundTask(ctx, goal, agent.WithBackgroundLabel("crawl"))
// … later, in another turn
if t, ok := svc.BackgroundTask(task.ID); ok && t.Status.Done() {
	fmt.Println(t.Result)
}
```

It **does not inherit the caller's context** — that is the whole difference
from a sub-agent, which runs under its parent and dies with it. It is still one
loop: another run on the same Service, with its own session and run id, so every
observer, lint, hook and extension applies to it, and it inherits the caller's
tenant so `CancelTenant` reaches it. `Close` cancels and drains anything still
running before releasing the store.

The tools are opt-in because a background task is a whole run with its own
budget, and `background_check` never reports a result for a task still in
flight. Runnable: `examples/background`.

## Delegating to an agent CLI

The most capable agent runtime on a developer's machine is often not the one you
are writing. `claude`, `codex`, `gemini` and `cursor-agent` are whole agents with
their own tools and their own subscriptions; two tools let yours hand one of them
a task and get the answer back, with its output streaming into the parent run's
event channel and its tokens accounted separately.

```go
svc, _ := agent.New("assistant").Build()

err := agent.RegisterCLIAgentTools(svc, agent.CLIAgentConfig{
	AllowedRoots:   []string{workdir}, // bounds cwd; defaults to the workspace
	DefaultTimeout: 3 * time.Minute,
})
// the agent now has cli_agent_list and cli_agent_run
```

Two things to know before relying on it. **Listed means installed, not logged
in** — the only honest probe is a real, billable turn, so discovery reports the
binaries it found and a run reports what came back. And **`failed` is not the
exit code**: a `claude` whose OAuth token has been revoked writes "Failed to
authenticate" as an assistant message, sets `is_error`, and exits zero, so a
caller reading the summary and the status hands the model an authentication
error as if it were the answer. `cli_agent_run` treats that verdict as a
failure regardless of the exit code.

The delegated run is bracketed by `OnSubAgentStart` / `OnSubAgentEnd` with
`SubAgentInfo.Kind == "cli"`, and the end carries a `CLIAgentRunResult` so an
observer can bill it to the right account. Command building, stream parsing and
usage accounting come from `github.com/liliang-cn/agentexec`. Runnable:
`examples/cli-agents`.

## Many callers through one Service

A `Service` has always been safe to run many tasks through at once. What it had
no notion of was *whose* run a run is — and on a shared server that is the
difference between a product and an incident.

```go
svc, _ := agent.New("support").
	WithMaxConcurrentRuns(64).  // the process's ceiling
	WithMaxRunsPerTenant(4).    // one customer's share of it
	Build()

res, err := svc.Run(ctx, goal, agent.WithTenant("acme"))
if errors.Is(err, agent.ErrTenantAtCapacity) {
	// refused immediately, with the numbers on the error — shed, queue, or 503
}
```

| what | for |
| --- | --- |
| `WithTenant(id)` | attach an opaque owner label to a run |
| `WithMaxConcurrentRuns(n)` / `WithMaxRunsPerTenant(n)` | ceilings; 0 = unlimited, the default |
| `Capacity()` | runs in flight, the ceilings, the split by tenant |
| `ActiveRunsForTenant(id)` / `CancelTenant(id)` | see and stop one customer's work |
| `ActiveRun.Tenant` / `ExecutionResult.Tenant` | attribute a run and what it cost |

Two things the label deliberately is not. It is **not an identity** — identity
is still the session UUID, memory scopes by session and history filters by task
— and **nothing in the loop reads it**: a tenant string that changed what an
agent does would be configuration by string matching. It exists for admission
control, bulk cancellation and attributing spend, and for nothing else.

Admission is decided under the same lock that records the run, so two callers
arriving together cannot both take the last slot, and it **refuses rather than
queues**: a library that blocks its caller for an unbounded time turns a
capacity problem into a latency mystery. Runnable: `examples/multitenant`.

## Storage

```text
~/.agentgo/                  # override with AGENTGO_HOME
├── data/
│   ├── agentgo.db           # config, providers, agents, tasks, checkpoints, plans (SQLite)
│   └── cortex.db            # optional memory / vector / graph storage
├── memories/                # file memory when enabled
├── skills/                  # local skills (SKILL.md)
└── workspace/               # agent working directory
```

Identity is the session UUID. There is no user id in the chat or task APIs.

## Repository layout

```text
pkg/agent         the framework: agent, loop, tools, context, hooks/lints, extensions, sessions, checkpoints, long runs
pkg/extensions    shipped extensions: logging, pii, usage, exec (out-of-process plugins)
pkg/extensiontest test an extension through the real loop with a scripted model
pkg/domain        shared types: messages, generation results, token usage, provider and store interfaces
pkg/providers     OpenAI-compatible providers + LLMPool
pkg/pool          provider pool, token estimation, pricing and cost
pkg/mcp           MCP client, tools and servers
pkg/memory        durable memory service + BaseStore
pkg/cortexbridge  CortexDB-backed knowledge graph / RAG / RunMemory
pkg/rag           optional retrieval
pkg/skills        skill loading and ranking
pkg/sandbox       local / Docker execution sandboxes
pkg/scheduler     cron-style scheduling with per-execution cancellation
pkg/otelobserver  Observer -> OpenTelemetry
pkg/store         SQLite storage, memory store plugins
pkg/worktree      git worktree helpers
eval/             behavioural eval harness: YAML scenarios, mock and live runners
examples/         runnable examples, one folder each
```

## Development

```bash
make check          # fmt + vet + test — the release gate
go test -race ./pkg/agent/...
make eval           # behavioural eval against a scripted mock model, CI-safe
make eval-live      # the same scenarios against a real provider
```

Eval scenarios live in `eval/scenarios/`; a harness change (a lint, a prompt cut, a
tool-prep tweak) is checked by running `make eval-live` and diffing the result JSON
against the previous run. `CLAUDE.md` records the architecture decisions and the bugs
that only a long soak found — read it before changing the loop.

## License

MIT
