# AgentGo

**Local-first RAG + Agent framework for Go.**

> “AgentGo? It's useless and it consumes a lot of tokens.” -- some guy on the internet

[中文文档](README_zh-CN.md) · [API Reference](references/API.md) · [Architecture](references/ARCHITECTURE.md)

AgentGo is a Go library for building AI applications that keep your data local. Start with semantic search over your documents, add agent automation when you need it.

```bash
go get github.com/liliang-cn/agent-go
```

---

## What AgentGo does

| Capability    | Details                                                                                                    |
| ------------- | ---------------------------------------------------------------------------------------------------------- |
| **RAG**       | Ingest docs → chunk → embed → SQLite vector store → hybrid search                                          |
| **Agent**     | Multi-turn reasoning loop with tool calls, planning, and session memory                                    |
| **Memory**    | **Cognitive Layer**: Hindsight-style evolution (Fact → Observation) + PageIndex-style reasoning navigation |
| **Tools**     | MCP (Model Context Protocol), Skills (YAML+Markdown), custom inline tools                                  |
| **PTC**       | LLM writes JavaScript; tools run in a Goja sandbox — cuts round-trips                                      |
| **Streaming** | Token-by-token via channel; full event stream with tool call visibility                                    |
| **Providers** | OpenAI, Anthropic, Azure, DeepSeek, Ollama — switchable at runtime                                         |
| **Teams**     | Persistent captains + specialists, async team task queues, runtime status, and cross-process task tracking |
| **Operator**  | Built-in execution agent with filesystem/web tools plus PTY and coding-agent session tooling               |

---

## Conceptual Model

AgentGo is easiest to understand as eight collaborating subsystems:

### 1. LLM

LLM is the execution core. Everything else is built around it.

- It provides the base generation interface used by agents, RAG answers, PTC, and tool selection.
- Providers are runtime-selectable through the global pool.
- Standalone agents, captains, specialists, and built-in agents all eventually run on the same LLM abstraction.

Think of it as: `prompt + tools + policy -> model call`.

### 2. RAG

RAG is the knowledge retrieval layer.

- It ingests documents, chunks them, embeds them, and stores them in SQLite/vector storage.
- At query time it retrieves relevant context and injects it into the agent or query flow.
- It is for external or project knowledge, not for durable user preferences.

Think of it as: `documents -> retrieval context`.

### 3. Memory

Memory is the durable internal context layer.

- It stores facts, preferences, observations, and other reusable knowledge learned from interaction.
- It is separate from cache and separate from RAG documents.
- File memory works even when no embedder is available.

Think of it as: `what the system has learned over time`.

### 4. MCP

MCP is the tool transport layer.

- It standardizes tool access, whether the tool is built-in or external.
- AgentGo always includes built-in filesystem and websearch servers.
- MCP is how agents interact with files, web pages, and other process-like capabilities without hardcoding every operation into the model prompt.

Think of it as: `how agents touch the outside world`.

### 5. Skills

Skills are reusable workflows expressed as Markdown/YAML.

- They are higher-level than raw tools.
- They encode domain-specific procedures, instructions, and reusable operator playbooks.
- They can be user-invocable or model-invocable depending on configuration.

Think of them as: `portable expert workflows`.

### 6. PTC

PTC (Programmatic Tool Calling) is the structured orchestration layer.

- Instead of emitting one tool call at a time, the model writes JavaScript to coordinate tools.
- This reduces round-trips for multi-step logic and data shaping.
- It is best for tool-heavy workflows where the model needs procedural control.

Think of it as: `LLM-authored tool orchestration code`.

### 7. Agent

An Agent is the basic runtime unit.

- It has instructions, tool access, optional RAG/memory/PTC/skills, and a session-aware execution loop.
- Agents can be built-in or user-defined.
- Built-in standalone agents include `Concierge`, `Assistant`, `Operator`, and `Stakeholder`.

Key standalone patterns:

- use `Assistant` for general-purpose direct work
- use `Operator` for execution, validation, PTY sessions, and coding-agent invocation
- use `Stakeholder` for product/business framing
- use `Concierge` for intake and orchestration

### 8. Team

A Team is the persistent team layer on top of agents.

- A team has one `captain` and multiple `specialists`.
- The captain is still an agent, but with team-oriented orchestration rules.
- Captains prefer async team work for implementation-heavy tasks.
- Team task state is persisted, so new CLI processes can inspect or continue work.

Think of it as: `persistent multi-agent coordination with queueing and status`.

### API Shape

At a high level the APIs map to those concepts like this:

- **LLM / Agent runtime**
  - `Ask`, `Chat`, `Run`, `RunStream`
- **RAG**
  - `WithRAG`, `rag_query`, document ingest/query flows
- **Memory**
  - `WithMemory`, `memory_save`, `memory_recall`
- **MCP**
  - `WithMCP`, built-in filesystem/websearch tools, external MCP servers
- **Skills**
  - `WithSkills`, skill registration and invocation
- **PTC**
  - `WithPTC`, `execute_javascript`, `callTool()`
- **Team**
  - `CreateTeam`, `JoinTeam`, `DispatchTask`, `SubmitTeamTask`, `GetTask`

The practical layering is:

`LLM -> tools/PTC -> Agent -> Team`

with `RAG`, `Memory`, `MCP`, and `Skills` acting as capabilities that can be attached to an agent.

---

## Quick Start

### Simple Q&A

```go
svc, _ := agent.New("assistant").
    WithPrompt("You are a helpful assistant.").
    Build()
defer svc.Close()

reply, _ := svc.Ask(ctx, "What is Go?")
fmt.Println(reply)
```

### With RAG (document knowledge base)

```go
svc, _ := agent.New("assistant").
    WithPrompt("Answer questions based on the provided documents.").
    WithRAG().
    WithDBPath("~/.agentgo/data/agent.db").
    Build()
defer svc.Close()

// Ingest once
svc.Run(ctx, "Ingest ./docs/")

// Query
reply, _ := svc.Ask(ctx, "What does the spec say about error handling?")
```

### Multi-turn chat with cognitive memory

```go
svc, _ := agent.New("assistant").
    WithMemory().
    Build()
defer svc.Close()

svc.Chat(ctx, "My name is Alice and I work on the Go team.")
reply, _ := svc.Chat(ctx, "What team am I on?")
// → "You're on the Go team, Alice." (Recall via hybrid vector/index search)
```

### CLI Interface

Run the interactive chat with memory visibility:

```bash
# Start interactive chat showing retrieved memories and reasoning
go run ./cmd/agentgo-cli chat --show-memory

# Enable JavaScript sandbox for complex logic
go run ./cmd/agentgo-cli chat --with-ptc
```

Run team workflows from the CLI:

```bash
# Create a standalone agent
agentgo agent add Scout --description "Independent field agent" \
  --instructions "Work independently, answer directly, and use tools when needed."

# Inspect or update that agent
agentgo agent show Scout
agentgo agent update Scout --model openai/gpt-5-mini

# Run a stored agent directly
agentgo agent run --agent Scout "Summarize the current repo structure"

# Built-in standalone agents are always available
agentgo agent show Concierge
agentgo agent show Operator

# Create a team (a default captain is created automatically)
agentgo team add "Docs Team" --description "Documentation and release notes"

# Join the standalone agent to a team
agentgo agent join Scout --team "Docs Team" --role specialist

# Run a task through the default captain and a specialist
agentgo team go "@Captain @Scout summarize the UI/backend relationship and write workspace/ui_backend_overview.md"

# Inspect runtime task state; follows while tasks are still running or queued
agentgo team status "Docs Team"

# Run direct execution work through the built-in Operator
agentgo agent run --agent Operator "Write workspace/operator_probe.txt with the text: OPERATOR_OK"

# Leave the team again
agentgo agent leave Scout

# Delete the team when you're done
agentgo team delete "Docs Team"
```

---

## Cognitive Memory (Hindsight & PageIndex)

AgentGo implements an evolving memory layer inspired by **Hindsight** (Cognitive Hierarchy) and **PageIndex** (Structural Navigation).

| Concept                | Description                                                                                                     |
| ---------------------- | --------------------------------------------------------------------------------------------------------------- |
| **Facts**              | Raw atomic data points extracted from conversations (e.g., "User likes Go").                                    |
| **Observations**       | LLM-consolidated insights synthesized from multiple facts via **Reflect()**.                                    |
| **Hierarchical Index** | A `_index/` directory with Markdown summaries for lightning-fast reasoning navigation.                          |
| **Hybrid Search**      | Parallel **Vector Search** (similarity) + **Index Navigator** (reasoning) fused via RRF.                        |
| **Traceability**       | Every observation tracks its **EvidenceIDs**, providing a clear audit trail of why the agent "knows" something. |

### Memory Evolution

1. **Extraction**: Agent identifies a fact during chat.
2. **Indexing**: Fact is stored in a Markdown file with YAML metadata (Confidence, SourceType).
3. **Reflection**: Periodically (e.g., every 5 facts), a background worker triggers `Reflect()` to merge facts into high-level Observations.
4. **Superseded**: When information changes, old memories are marked as `stale` and linked to new ones via `SupersededBy`.

---

## Builder

```go
// Implement your own module
type Module interface {
    ID() string
    RegisterTools(registry *ToolRegistry) error
}

svc, _ := agent.New("agent").
    WithModule(NewMyCustomModule()).
    Build()
```

---

## Agent APIs

### Runtime Invocation

| Method                    | Returns                     | Session         | Use case              |
| ------------------------- | --------------------------- | --------------- | --------------------- |
| `Ask(ctx, prompt)`        | `(string, error)`           | no              | one-shot Q&A          |
| `Chat(ctx, prompt)`       | `(*ExecutionResult, error)` | yes (auto UUID) | conversational        |
| `Run(ctx, goal, ...opts)` | `(*ExecutionResult, error)` | optional        | full agent loop       |
| `Stream(ctx, prompt)`     | `<-chan string`             | no              | live token output     |
| `ChatStream(ctx, prompt)` | `<-chan string`             | yes             | conversational + live |
| `RunStream(ctx, goal)`    | `(<-chan *Event, error)`    | optional        | full event visibility |

```go
result, _ := svc.Run(ctx, "goal",
    agent.WithMaxTurns(20),
    agent.WithTemperature(0.7),
    agent.WithSessionID("my-session"),
    agent.WithStoreHistory(true),
)

result.Text()        // final answer as string
result.Err()         // non-nil if agent reported an error
result.HasSources()  // true when RAG chunks were used
```

### Standalone Agent Management

At the manager level, standalone agents are persistent named runtimes:

- `CreateAgent`, `UpdateAgent`, `DeleteAgent`
- `GetAgentByName`, `ListAgents`, `ListStandaloneAgents`
- `GetAgentService`
- `GetAgentStatus`, `ListAgentStatuses`

Built-in standalone agents (`Concierge`, `Assistant`, `Operator`, `Stakeholder`) are seeded automatically and can be treated like normal named agents.

### Built-in Agent Delegation APIs

User-created standalone agents automatically receive a small built-in delegation surface:

- `list_builtin_agents`
- `delegate_builtin_agent`
- `submit_builtin_agent_task`
- `get_delegated_task_status`

This is the primary way a custom agent can keep its own role while offloading execution to `Operator`, general work to `Assistant`, or business clarification to `Stakeholder`.

---

## Programmatic Tool Calling (PTC)

With `WithPTC()`, the LLM generates JavaScript instead of JSON tool calls. The code runs in a Goja sandbox where `callTool()` is available:

```go
svc, _ := agent.New("analyst").
    WithPTC().
    WithTool(teamDef, teamHandler, "data").
    WithTool(expenseDef, expenseHandler, "data").
    Build()

// The LLM can now write:
//   const team = callTool("get_team", { dept: "eng" });
//   return team.members.map(m => ({
//     name: m.name,
//     spend: callTool("get_expenses", { id: m.id }).total
//   }));
```

**When to use PTC:** multiple dependent tool calls in one shot, data transformation before it hits the context window, conditional tool logic.

---

## Memory

Memory and cache are different subsystems:

| Subsystem  | Storage                                 | What for                                                           |
| ---------- | --------------------------------------- | ------------------------------------------------------------------ |
| **Memory** | Markdown/YAML files or SQLite + vectors | Durable facts, observations, preferences, and reasoning context    |
| **Cache**  | In-memory or file-backed JSON entries   | Disposable acceleration artifacts for query/vector/LLM/chunk reuse |

```go
// Enable cognitive memory
svc, _ := agent.New("agent").WithMemory().Build()

// LongRun agents share the same memory automatically
lr, _ := agent.NewLongRun(svc).
    WithInterval(5 * time.Minute).
    WithWorkDir("~/.agentgo/longrun").
    Build()
```

Memory degrades gracefully:

- no embedder -> file memory still works
- file-backed memory uses Markdown + YAML frontmatter and PageIndex-style retrieval
- `remember:` prompts can be written directly to memory
- ordinary dialogue can also be extracted into memory via `StoreIfWorthwhile`

Cache is separate from memory:

- use `agentgo cache status|put|get|delete|clear`
- configure `cache.store_type = "memory"` or `cache.store_type = "file"`

---

## Autonomous Agents (LongRun)

LongRun runs an agent on a schedule with a persistent task queue:

```go
lr, _ := agent.NewLongRun(svc).
    WithInterval(10 * time.Minute).
    WithMaxActions(5).
    Build()

lr.AddTask(ctx, "Monitor RSS feeds and summarize new entries", nil)
lr.Start(ctx)
// ...
lr.Stop()
```

Features: SQLite task queue, heartbeat file, cron-style scheduling, shared DB memory with the parent agent.

---

## Multi-Agent Orchestration

```go
// Handoffs — specialist agents
orchestrator.RegisterAgent(researchAgent)
orchestrator.RegisterAgent(writerAgent)
// The LLM routes to the right agent via transfer_to_* tool calls

// SubAgents — scoped delegation
coordinator := agent.NewSubAgentCoordinator()
resultChan  := coordinator.RunAsync(ctx, subAgent)
results     := coordinator.WaitAll(ctx)
```

### Agent Model

AgentGo has three layers of agent concepts:

- **Standalone agents**: long-lived named agents with their own role and tool budget
- **Teams**: a persistent team with one `captain` and multiple `specialists`
- **Built-in agents**: system-provided standalone agents that are always available

The default built-ins are:

- `Concierge`: intake/orchestration for `agentgo chat`
- `Assistant`: general-purpose direct worker
- `Operator`: execution/validation agent
- `Stakeholder`: product/business representative

Inspect them directly:

```bash
agentgo agent show Concierge
agentgo agent show Assistant
agentgo agent show Operator
agentgo agent show Stakeholder
```

### Delegation Model

AgentGo now supports two delegation axes:

- **Team delegation**
  - `captain -> specialists`
  - supports synchronous dispatch and persisted async team tasks
- **Built-in delegation**
  - `custom agent -> Assistant / Operator / Stakeholder`
  - useful when the custom agent should keep its own role but offload execution or business clarification

Conceptually:

- use **Assistant** when you want a general-purpose built-in doer
- use **Operator** when the task is about execution, validation, files, PTY-backed commands, or coding-agent invocation
- use **Stakeholder** when the task is about requirements, scope, tradeoffs, or acceptance criteria

### Operator Concept

`Operator` is the built-in execution layer.

At a concept level, it provides two API families:

- **PTY session APIs**
  - start a command session
  - send more input
  - inspect output/status
  - interrupt or stop the session
- **Coding-agent APIs**
  - start or inspect provider-aware sessions for `claude`, `gemini`, `codex`, and `opencode`
  - run one-shot coding-agent calls without forcing the model to guess shell commands

In practice, `Operator` is what `QA`, `PM`, or custom agents should delegate to when they need actual execution instead of just reasoning.

Simple CLI examples:

```bash
agentgo agent run --agent Operator "Write workspace/operator_probe.txt with the text: OPERATOR_OK"
agentgo agent run --agent Operator "Call codex and make it output exactly: RES_FROM_CODEX"
```

### Custom Agents + Built-ins

User-created standalone agents automatically get a small built-in delegation API:

- `list_builtin_agents`
- `delegate_builtin_agent`
- `submit_builtin_agent_task`
- `get_delegated_task_status`

This means a custom agent can keep its own role and capabilities, but still delegate:

- execution to `Operator`
- general work to `Assistant`
- product/business clarification to `Stakeholder`

## Team APIs

AgentGo exposes a team-oriented manager API for standalone agents and team agents. A `captain` is just an agent role inside a team.

```go
store, err := agent.NewStore(filepath.Join(cfg.DataDir(), "agent.db"))
if err != nil {
    panic(err)
}

manager := agent.NewTeamManager(store)
if err := manager.SeedDefaultMembers(); err != nil {
    panic(err)
}

scout, err := manager.CreateAgent(ctx, &agent.AgentModel{
    Name:         "Scout",
    Kind:         agent.AgentKindAgent,
    Description:  "Independent field agent",
    Instructions: "Work independently and answer directly.",
})
if err != nil {
    panic(err)
}

docsTeam, err := manager.CreateTeam(ctx, &agent.Team{
    Name:        "Docs Team",
    Description: "Documentation and release notes",
})
if err != nil {
    panic(err)
}

writer, err := manager.JoinTeam(ctx, scout.Name, docsTeam.ID, agent.AgentKindSpecialist)
if err != nil {
    panic(err)
}

result, err := manager.DispatchTask(ctx, writer.Name, "Write workspace/ui_backend_overview.md")
if err != nil {
    panic(err)
}
fmt.Println(result)
```

### Captain Runtime Model

- A custom team created via `CreateTeam()` or `agentgo team add` automatically gets a default captain.
- The captain receives team roster and role summaries in its system prompt.
- Captains prefer async team work for implementation-heavy tasks.
- Shared team tasks are persisted and can be inspected from new CLI processes.
- Captains do not use generic `delegate_to_subagent` by default.

### Core Team-Manager APIs

- `CreateAgent`, `UpdateAgent`, `DeleteAgent`, `GetAgentByName`, `ListAgents`, `ListStandaloneAgents`
- `JoinTeam`, `LeaveTeam`, `GetAgentService`
- `CreateTeam`, `ListTeams`, `GetTeamByName`
- `AddTeamAgent`, `CreateTeamAgent`, `ListTeamAgents`, `GetTeamAgentByName`
- `AddCaptain`, `AddSpecialist`, `ListCaptains`, `ListSpecialists` (role-specific helpers)
- `DispatchTask`, `DispatchTaskStream`
- `EnqueueSharedTask`, `ListSharedTasks`
- `SubmitAgentTask`, `SubmitTeamTask`, `GetTask`, `ListSessionTasks`

### Team Runtime / Status APIs

For runtime orchestration and monitoring:

- `GetTeamStatus`, `ListTeamStatuses`
- `GetAgentStatus`, `ListAgentStatuses`
- `GetLeadAgentForTeam`
- `SubscribeTask` for async task progress streams
- `DispatchTaskStreamWithOptions`, `ChatWithMemberStream`, `ChatWithMemberStreamWithOptions`

In practice, the API layers look like this:

- **Standalone agent APIs**: create, run, inspect, update
- **Team APIs**: create teams, join agents, dispatch tasks, track async work
- **Built-in delegation APIs**: let a custom agent explicitly call `Assistant`, `Operator`, or `Stakeholder`

---

## Planning (deterministic workflow)

```go
plan, _   := svc.Plan(ctx, "Deploy the new service")
// inspect plan.Steps, edit if needed
result, _ := svc.Execute(ctx, plan.ID)
```

---

## Configuration & Storage

Runtime layout is derived from `AGENTGO_HOME` (default: `~/.agentgo`).
Structured runtime config lives in `data/agentgo.db`.

### Directory layout (default `home = ~/.agentgo`)

```
~/.agentgo/
├── mcpServers.json           ← MCP server definitions
├── data/
│   ├── agentgo.db            ← Control plane: providers, runtime config, agent/team metadata
│   ├── cortex.db             ← Brain store: memory, vectors, graph, knowledge
│   └── memories/             ← File memory store (Markdown + YAML frontmatter)
├── skills/                  ← SKILL.md files
├── intents/                 ← Intent YAML files
└── workspace/               ← Agent working directory
```

### SQLite files

| File                 | Default path              | Purpose                                                      |
| -------------------- | ------------------------- | ------------------------------------------------------------ |
| `agentgo.db`         | `$home/data/agentgo.db`   | Runtime config, providers, MCP/skills paths, agent/team data |
| `cortex.db`          | `$home/data/cortex.db`    | Brain store for memory, vectors, graph, and knowledge        |
| `history.db` _(opt)_ | via `WithHistoryDBPath()` | Detailed tool-call logs — only created when `WithStoreHistory(true)` |

### Memory store types

| `store_type`       | Storage                                                        | Requires embedder |
| ------------------ | -------------------------------------------------------------- | ----------------- |
| `file` _(default)_ | `data/memories/entities/*.md` and `data/memories/streams/*.md` | No                |
| `cortex`           | `data/cortex.db`                                               | No                |

### Cache store types

| `store_type`         | Storage                         | Purpose                            |
| -------------------- | ------------------------------- | ---------------------------------- |
| `memory` _(default)_ | in-process memory               | Fast ephemeral cache               |
| `file`               | `data/cache/<namespace>/*.json` | Restart-friendly cache persistence |

### Runtime settings

AgentGo derives the runtime storage layout automatically from `AGENTGO_HOME`:

- workspace: `$home/workspace`
- MCP filesystem allowlist: `$home/workspace`
- brain database: `$home/data/cortex.db`
- memory store: `$home/data/memories` or `$home/data/cortex.db` when `memory.store_type = "cortex"`
- cache directory: `$home/data/cache`

The remaining structured runtime values live in `agentgo.db`, including:

- LLM providers and pool strategy
- embedding providers and `rag.embedding_model`
- MCP config paths
- skills load paths
- per-agent preferred provider/model

### Cache CLI

```bash
agentgo cache status
agentgo cache put query my-key my-value --ttl 5m
agentgo cache get query my-key
agentgo cache clear query
```

---

## Providers

Providers are configured in `agentgo.db` and managed through the CLI/UI/runtime APIs.

Supported: OpenAI · Anthropic · Azure OpenAI · DeepSeek · Ollama (local)

---

## Examples

```
examples/
├── quickstart/               — simplest possible agent
├── agent/
│   ├── agent_usage/          — builder patterns, tool registration
│   ├── multi_agent_orchestration/ — handoffs + streaming
│   ├── longrun/              — autonomous scheduled agent
│   └── realtime_chat/        — WebSocket session
├── rag/                      — document ingestion + Q&A
├── memory/
│   ├── chat_with_memory/     — DB memory + chat
│   └── smart_fusion/         — memory merging
├── ptc/
│   ├── custom_tools/         — JS sandbox tool orchestration
│   └── memory_chat/          — PTC + memory
├── skills/                   — Skill files
└── mcp/                      — MCP tool servers
```

---

## License

MIT — Copyright (c) 2024–2026 AgentGo Authors
