---
name: agentgo-pkg
description: Use AgentGo as a Go library to build AI agents, RAG pipelines, and multi-agent teams. Use when writing Go code that needs AI agent capabilities, document ingestion, semantic search, or multi-agent orchestration.
---

# AgentGo Package Guide

AgentGo is a Go library for building AI applications with agents, RAG, memory, MCP tools, and multi-agent teams.

## CLI Usage

```bash
# Build
go build -o agentgo ./cmd/agentgo
go build -o agentgo-cli ./cmd/agentgo-cli

# Agent commands
./agentgo-cli agent run "your goal"           # Run agent with streaming
./agentgo-cli agent plan create "goal"        # Create plan without execution
./agentgo-cli agent plan list                 # List plans
./agentgo-cli agent plan get [plan-id]        # Get plan details
./agentgo-cli agent execute [plan-id]        # Execute existing plan
./agentgo-cli agent revise [plan-id] [inst]  # Revise plan
./agentgo-cli agent ptc-chat [message]       # PTC-enabled chat

# RAG commands
./agentgo-cli rag ingest [file]               # Ingest documents
./agentgo-cli rag query [query]              # Query RAG
./agentgo-cli rag list                        # List documents
./agentgo-cli rag reset                       # Reset RAG

# MCP commands
./agentgo-cli mcp status                      # MCP server status
./agentgo-cli mcp chat [message]             # Chat with MCP tools

# Skills commands
./agentgo-cli skills list                     # List skills
./agentgo-cli skills run [skill-id]           # Run skill
./agentgo-cli skills add [name] [path]         # Add skill

# Other
./agentgo-cli status                          # Show status
./agentgo-cli config show                     # Show config
```

---

## Go API: Agent (Single Agent)

### Quick Start

```go
import "github.com/liliang-cn/agent-go/v2/pkg/agent"

svc, err := agent.New("my-agent").
    WithRAG().
    WithMemory().
    Build()

result, err := svc.Run(ctx, "Your question")
fmt.Println(result.Text())
```

### Builder API

```go
// Create agent
svc, err := agent.New(name string).Build()

// Or with options
svc, err := agent.New("my-agent").
    WithRAG(
        agent.WithRAGEmbeddingModel("text-embedding-3-small"),
    ).
    WithMemory(
        agent.WithMemoryPath("/path/to/memory"),
        agent.WithMemoryHybrid(),  // File + Vector hybrid mode
    ).
    WithMCP("/path/to/mcpServers.json").
    WithSkills("/path/to/skills").
    WithPTC().
    WithRouter().
    WithSystemPrompt("You are helpful").
    WithDebug().
    WithProgressCallback(func(progress agent.SubAgentProgress) {
        fmt.Printf("Progress: %s\n", progress.Message)
    }).
    Build()
```

### Builder Methods

| Method                     | Description                              |
| -------------------------- | ---------------------------------------- |
| `WithRAG(opts...)`         | Enable RAG with optional embedding model |
| `WithMemory(opts...)`      | Enable memory (file/vector/hybrid)       |
| `WithMCP(paths...)`        | Enable MCP tools from config files       |
| `WithRouter(opts...)`      | Enable semantic routing                  |
| `WithSkills(opts...)`      | Enable skills system                     |
| `WithPTC(opts...)`         | Enable Programmatic Tool Calling         |
| `WithLLM(llm)`             | Use custom LLM instead of global pool    |
| `WithEmbedder(embedder)`   | Use custom embedder for RAG              |
| `WithSystemPrompt(prompt)` | Custom system prompt                     |
| `WithDebug(on ...bool)`    | Enable debug logging                     |
| `WithProgressCallback(cb)` | Set progress callback                    |
| `WithTool(tool)`           | Register a tool                          |
| `WithDBPath(path)`         | Set database path                        |
| `WithAgentName(name)`      | Set brand name in prompts                |

### Run Options

```go
// Basic run
result, err := svc.Run(ctx, "goal")

// With options
result, err := svc.Run(ctx, "goal",
    agent.WithMaxTurns(10),
    agent.WithTemperature(0.7),
    agent.WithSessionID("session-id"),
)

// Streaming
events, err := svc.RunStream(ctx, "goal")
for event := range events {
    fmt.Printf("Event: %s\n", event.Type)
}

// Result helpers
result.Text()           // Get response text
result.Err()           // Get error
result.HasSources()    // Has RAG sources
```

### Tool Definition (Typed)

```go
// Define typed tool with struct parameters
type WeatherParams struct {
    Location string `json:"location" desc:"City name" required:"true"`
    Units    string `json:"units" desc:"Units (celsius/fahrenheit)" enum:"celsius,fahrenheit"`
}

tool := agent.NewTool[WeatherParams]("get_weather", "Get current weather",
    func(ctx context.Context, p *WeatherParams) (any, error) {
        return fetchWeather(p.Location, p.Units)
    },
)

svc, err := agent.New("my-agent").
    WithTool(tool).
    Build()
```

### Memory Options

```go
agent.WithMemoryPath("/data/memory")      // File storage path
agent.WithMemoryStoreType("file")         // "file", "vector", or "hybrid"
agent.WithMemoryHybrid()                  // FileMemoryStore + RAG shadow
agent.WithMemoryReflect(threshold)         // Auto-reflect after N facts
agent.WithMemoryBank(mission, directives)  // Long-term mission statement
```

---

## Go API: Team (Multi-Agent)

```go
// Create team manager
mgr, err := agent.NewTeam(dbPath).
    WithAgentName("MyApp").
    WithTeamName("Dev Team").
    Build()

// Built-in agents
captain := mgr.GetAgent("Captain")
assistant := mgr.GetAgent("Assistant")
concierge := mgr.GetAgent("Concierge")
operator := mgr.GetAgent("Operator")
archivist := mgr.GetAgent("Archivist")

// Enqueue task
task, err := mgr.EnqueueSharedTask(ctx,
    "Captain",
    []string{"Assistant", "Operator"},
    "Implement user authentication",
)

status := mgr.GetTaskStatus(task.ID)
```

---

## Go API: LongRun (Autonomous Agent)

```go
// Build agent first
svc, err := agent.New("worker").
    WithMemory().
    WithRAG().
    Build()

// Create LongRun
longrun, err := agent.NewLongRun(svc).
    WithInterval(30 * time.Second).
    WithWorkDir("/data/longrun").
    WithMaxActions(100).
    WithApproval(true).
    Build()

// Start and manage
longrun.Start(ctx)
longrun.AddTask(ctx, "Check system health", nil)
status := longrun.GetStatus()
longrun.Stop()
```

---

## Go API: RAG Pipeline

```go
import "github.com/liliang-cn/agent-go/v2/pkg/rag"

// Create client
client := rag.NewClient(cfg)

// Ingest
err := client.Ingest(ctx, &domain.IngestRequest{...})

// Query
resp, err := client.Query(ctx, &domain.QueryRequest{
    Query:       "Your question",
    TopK:        5,
    Temperature: 0.7,
})
fmt.Println(resp.Answer)
```

---

## Go API: MCP Tools

```go
import "github.com/liliang-cn/agent-go/v2/pkg/mcp"

// Create service
svc, err := mcp.NewService(cfg, llm)

// Start servers
err = svc.StartServers(ctx, []string{"filesystem", "websearch"})

// List tools
tools := svc.GetAvailableTools(ctx)

// Call tool
result, err := svc.CallTool(ctx, "filesystem.read_file", map[string]any{
    "path": "/path/to/file",
})
```

---

## Go API: Skills System

```go
import "github.com/liliang-cn/agent-go/v2/pkg/skills"

// Create service
svc, err := skills.NewService(cfg)

// Load skills
err = svc.LoadAll(ctx)

// List and run
skills, _ := svc.ListSkills(ctx, skills.SkillFilter{})
result, _ := svc.RunSkill(ctx, "skill-id", map[string]any{
    "query": "my question",
})
```

---

## Go API: Memory

```go
import "github.com/liliang-cn/agent-go/v2/pkg/memory"

// Create service (requires MemoryStore + LLM + Embedder)
svc := memory.NewService(memStore, llm, embedder, cfg)

// Retrieve with context
ctxText, memories, err := svc.RetrieveAndInject(ctx, query, sessionID)
```

---

## Go API: Pool (LLM Provider)

```go
import "github.com/liliang-cn/agent-go/v2/pkg/pool"

// Create pool
p, _ := pool.NewPool(pool.PoolConfig{
    Enabled:  true,
    Strategy: "round_robin",
    Providers: []pool.Provider{
        {Name: "openai", ModelName: "gpt-4o"},
        {Name: "anthropic", ModelName: "claude-3-5-sonnet"},
    },
})

// Get client and generate
client, _ := p.Get()
result, _ := client.Generate(ctx, prompt, nil)
```

---

## Go API: Router (Semantic Routing)

```go
import "github.com/liliang-cn/agent-go/v2/pkg/router"

// Create router
svc, _ := router.NewService(embedder, cfg)

// Route query to intent
result, _ := svc.Route(ctx, "What's the weather?")

// Register custom intents
svc.RegisterIntent(&router.Intent{
    Name:        "code_review",
    Description: "Review code for bugs",
    Examples:    []string{"review this code", "check for bugs"},
}, "codereview_tool")

svc.RegisterDefaultIntents()  // RAG, file, web, memory, code
```

---

## Go API: PTC (Programmatic Tool Calling)

```go
import "github.com/liliang-cn/agent-go/v2/pkg/ptc"

// Create router
router := ptc.NewAgentGoRouter(
    ptc.WithMCPService(mcpSvc),
    ptc.WithSkillsService(skillsSvc),
    ptc.WithRAGProcessor(ragProc),
)

// Register custom tool
router.RegisterTool("my_tool", &ptc.ToolInfo{
    Name:        "my_tool",
    Description: "Does something",
    Parameters:  schema,
}, func(ctx context.Context, args map[string]interface{}) (interface{}, error) {
    return "result", nil
})

// Execute
result, _ := router.Route(ctx, "my_tool", map[string]any{"arg": "value"})
```

---

## Go API: A2A (Agent-to-Agent)

```go
import "github.com/liliang-cn/agent-go/v2/pkg/a2a"

// Server (expose agent via A2A)
server, _ := a2a.NewServer(catalog, cfg)
server.Mount(mux)

// Client (connect to remote agent)
client, _ := a2a.Connect(ctx, "http://remote:8080/a2a", cfg)
card := client.Card()
result, _ := client.SendText(ctx, "Hello agent")
```

---

## Configuration (TOML)

```toml
[agent]
name = "MyApp"

[team]
name = "Dev Team"

[llm]
enabled = true
strategy = "round_robin"
providers = [
    { name = "openai", model = "gpt-4o" },
    { name = "anthropic", model = "claude-3-5-sonnet" },
]

[rag]
enabled = true
embedding_model = "text-embedding-3-small"

[memory]
store_type = "hybrid"
memory_path = "~/.agentgo/memory"

[skills]
enabled = true
paths = ["~/.agentgo/skills", ".skills"]

[mcp]
enabled = true
```

---

## Package Structure

```
pkg/
├── agent/       # Agent, Team, LongRun, Builder pattern
├── a2a/         # Agent-to-Agent protocol (server/client)
├── config/      # Configuration loading
├── domain/      # Core interfaces (Generator, MemoryStore, VectorStore, etc.)
├── memory/      # Memory implementations (file/vector/hybrid)
├── mcp/         # MCP server and tools
├── pool/        # LLM provider pool
├── ptc/         # Programmatic Tool Calling
├── rag/         # RAG pipeline
├── router/      # Semantic routing
├── skills/      # Skills system
└── store/       # SQLite persistence
```

---

## Key Interfaces (pkg/domain)

| Interface         | Methods                                                         |
| ----------------- | --------------------------------------------------------------- |
| `Generator`       | `Generate`, `Stream`, `GenerateWithTools`, `GenerateStructured` |
| `Embedder`        | `Embed`, `EmbedBatch`                                           |
| `MemoryStore`     | `Store`, `Search`, `Get`, `Update`, `Delete`, `Reflect`         |
| `VectorStore`     | `Store`, `Search`, `Delete`, `List`, `Reset`                    |
| `Chunker`         | `Split`                                                         |
| `Processor` (RAG) | `Ingest`, `Query`, `ListDocuments`, `DeleteDocument`            |
