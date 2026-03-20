---
name: agentgo-pkg
description: Use AgentGo as a Go library to build AI agents, RAG pipelines, and multi-agent squads. Use when writing Go code that needs AI agent capabilities, document ingestion, semantic search, or multi-agent orchestration.
---

# AgentGo Package Guide

AgentGo is a Go library for building AI applications with agents, RAG, memory, and multi-agent squads.

## Core Concepts

### 1. Agent (Single AI Assistant)
A single AI agent with optional RAG, memory, MCP tools, and skills.

### 2. Squad (Multi-Agent Team)
A team of agents coordinated by a Captain, with roles like Concierge, Assistant, Operator, Archivist.

### 3. LongRun (Autonomous Agent)
An agent running continuously with heartbeat scheduling and task queues.

---

## Quick Start

### Simple Agent with RAG

```go
import "github.com/liliang-cn/agent-go/v2/pkg/agent"

svc, err := agent.New("my-agent").
    WithRAG().
    WithMemory().
    Build()

response, err := svc.Run(ctx, "Your question", sessionID)
```

### Agent with Full Features

```go
svc, err := agent.New("my-agent").
    WithRAG(
        agent.WithRAGEmbeddingModel("your-embedding-model"),
    ).
    WithMemory(
        agent.WithMemoryPath("/path/to/memory"),
        agent.WithMemoryHybrid(),  // File + Vector hybrid mode
    ).
    WithMCP("/path/to/mcpServers.json").
    WithSkills(
        agent.WithSkillsPaths("/path/to/skills"),
    ).
    WithProgressCallback(func(progress agent.SubAgentProgress) {
        fmt.Printf("Progress: %s\n", progress.Message)
    }).
    Build()
```

---

## Builder API Reference

### agent.New(name) → *Builder → *Service

Creates a single agent with configurable capabilities.

| Method | Description |
|--------|-------------|
| `WithRAG(opts...)` | Enable RAG with optional embedding model |
| `WithMemory(opts...)` | Enable memory (file/vector/hybrid) |
| `WithMCP(paths...)` | Enable MCP tools from config files |
| `WithRouter()` | Enable semantic routing |
| `WithSkills(opts...)` | Enable skills system |
| `WithPTC(opts...)` | Enable Programmatic Tool Calling |
| `WithLLM(llm)` | Use custom LLM instead of global pool |
| `WithSystemPrompt(prompt)` | Custom system prompt |
| `WithDebug()` | Enable debug logging |
| `WithConfig(cfg)` | Pass agentgo config |
| `WithAgentName(name)` | Set brand name in prompts (e.g., "MyApp") |

### Memory Options

```go
agent.WithMemoryPath("/data/memory")           // File storage path
agent.WithMemoryStoreType("file")              // "file", "vector", or "hybrid"
agent.WithMemoryHybrid()                       // FileMemoryStore + RAG shadow
agent.WithMemoryReflect(threshold)             // Auto-reflect after N facts
agent.WithMemoryBank(mission, directives)      // Long-term mission statement
```

---

## Squad API (Multi-Agent)

### agent.NewSquad(dbPath) → *SquadBuilder → *SquadManager

Creates a squad manager with built-in agents.

```go
mgr, err := agent.NewSquad("~/.agentgo/data/agentgo.db").
    WithAgentName("MyApp").      // Brand name in prompts
    WithSquadName("Dev Team").   // Custom squad name
    Build()

// Get the built-in agents
concierge := mgr.GetAgent("Concierge")
assistant := mgr.GetAgent("Assistant")
captain := mgr.GetAgent("Captain")
```

### Enqueue Squad Task

```go
task, err := mgr.EnqueueSharedTask(ctx,
    "Captain",
    []string{"Assistant", "Operator"},
    "Implement user authentication",
)

// Check status
status := mgr.GetTaskStatus(task.ID)
```

---

## LongRun API (Autonomous Agent)

### agent.NewLongRun(agent) → *LongRunBuilder → *LongRunService

Creates a continuously running agent with heartbeat.

```go
svc, _ := agent.New("worker").
    WithMemory().
    WithRAG().
    Build()

longrun, err := agent.NewLongRun(svc).
    WithInterval(30 * time.Second).
    WithWorkDir("/data/longrun").
    WithMaxActions(100).
    WithApproval(true).   // Require approval for actions
    Build()

ctx := context.Background()
longrun.Start(ctx)

// Add tasks
longrun.AddTask(ctx, "Check system health", nil)
```

---

## Configuration (TOML)

~/.agentgo/config/agentgo.toml:

```toml
[agent]
name = "MyApp"        # Brand name

[squad]
name = "Dev Team"    # Squad display name

[llm]
enabled = true
strategy = "round_robin"
providers = [
    { name = "openai", model = "gpt-4o" },
    { name = "anthropic", model = "claude-3-5-sonnet" },
]

[rag]
enabled = true
embedding_model = "your-embedding-model"

[memory]
store_type = "hybrid"    # file, vector, hybrid
memory_path = "~/.agentgo/memory"

[skills]
enabled = true
paths = ["~/.agentgo/skills"]
```

---

## RAG Pipeline

### Ingest Documents

```go
// Simple file ingestion
err := svc.IngestFile(ctx, "/path/to/doc.pdf")

// Or use processor directly
processor := rag.NewProcessor(ragConfig, embedder, store)
err = processor.Ingest(ctx, reader, metadata)
```

### Query

```go
results, err := svc.Query(ctx, "Your question", sessionID)
for _, result := range results {
    fmt.Printf("Score: %.2f\n", result.Score)
    fmt.Printf("Content: %s\n", result.Content)
}
```

---

## Complete Examples

### AskDocs SaaS

```go
// Create RAG-enabled agent for document Q&A
svc, err := agent.New("ask-docs").
    WithRAG(
        agent.WithRAGEmbeddingModel("your-embedding-model"),
    ).
    WithMemory().
    Build()

// Ingest documents (background)
go svc.IngestFile(ctx, "/docs/user-manual.pdf")

// Answer questions
answer, err := svc.Run(ctx, "How do I reset my password?", sessionID)
```

### Multi-Tenant SaaS

```go
// Each tenant gets own store and squad
mgr, err := agent.NewSquad(store).
    WithAgentName("YourBrand").
    WithSquadName(tenantName).
    Build()

// Tenant's agents with brand name in prompts
task := mgr.EnqueueSharedTask(ctx,
    "Captain",
    []string{"Assistant", "Operator"},
    userRequest,
)
```

### Autonomous Worker

```go
svc, _ := agent.New("worker").
    WithMemory(agent.WithMemoryHybrid()).
    WithMCP().
    Build()

longrun, _ := agent.NewLongRun(svc).
    WithInterval(1 * time.Minute).
    WithWorkDir("/data/worker").
    Build()

longrun.Start(ctx)
longrun.AddTask(ctx, "Monitor /tmp/alerts", nil)
```

---

## Project Structure

```
pkg/
├── agent/           # Core agent, squad, longrun
├── config/          # Configuration loading
├── domain/          # Interfaces (Generator, Processor, etc.)
├── memory/          # Memory implementations
├── mcp/             # MCP server and tools
├── pool/            # LLM provider pool
├── rag/             # RAG pipeline (chunking, embedding, storage)
├── skills/          # Skills system
└── store/           # SQLite persistence
```

---

## See Also

- `pkg/agent/builder.go` - Builder pattern source
- `pkg/agent/squad_defaults.go` - Built-in agents
- `pkg/config/config.go` - Configuration reference
