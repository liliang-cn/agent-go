# Copilot Instructions — AgentGo

## Build, Test, and Lint

All commands use the Makefile. The `fix-embed` target (creating a placeholder `cmd/agentgo-ui/dist/index.html`) runs automatically before tests/checks to satisfy Go's `//go:embed` directive.

```bash
make build          # Build both binaries (agentgo-cli + agentgo-ui with embedded UI)
make agentgo-cli    # Build CLI only
make agentgo-ui     # Build UI binary (builds frontend first)
make test           # Run all Go tests (go test ./...)
make check          # Format + vet + test
make coverage-core  # Coverage for core packages only

# Run a single test
go test ./pkg/pool/... -run TestRoundRobin -v

# Frontend
make ui-deps        # Install UI dependencies (npm ci)
make ui-build       # Build React app and sync into cmd/agentgo-ui/dist/
make ui-dev         # Start both Vite dev server + Go API with hot reload
cd ui && npm run build  # Build frontend standalone
```

The project produces **two binaries**:
- `bin/agentgo-cli` — CLI tool for RAG, agent, MCP, skills commands
- `bin/agentgo-ui` — HTTP server with embedded React SPA (port 7127)

## Architecture

AgentGo is a **local-first RAG system with optional agent automation**. Feature priority: RAG → Multi-Provider LLM → MCP Tools → Agent Automation → HTTP API.

### Core Subsystems

**LLM Pool** (`pkg/pool/`): Manages multiple LLM/embedding providers behind a unified interface. Supports selection strategies: round_robin, random, least_load, capability (1-5 scale), failover. The pool itself implements `domain.Generator` and `domain.Embedder`, so consumers don't know about individual providers. Clients must be `Release()`d after use.

**RAG Pipeline** (`pkg/rag/`): Document ingestion → chunking → embedding → SQLite vector storage → semantic search → LLM-augmented answers. Key flow:
- Ingest: `processor.Ingest()` → `chunker.Split()` → `embedder.EmbedBatch()` → `vectorStore.Store()`
- Query: `embedder.Embed(query)` → `vectorStore.Search()` → `generator.GenerateWithTools()`
- Advanced search: reranking (keyword boost, MMR diversity), metadata filtering

**Agent System** (`pkg/agent/`): Planner/Executor pattern. The Planner asks the LLM to create a `Plan` with `Step`s (each has tool, arguments, dependencies). The Executor runs steps in dependency order, calling MCP tools, skills, RAG, or memory. Built using a builder pattern:
```go
agent.New("name").WithRAG().WithMemory().WithMCP().WithSkills().Build()
```

**MCP** (`pkg/mcp/`): Model Context Protocol tool integration. Server types: stdio (subprocess), HTTP, SSE, and in-process (built-in filesystem + websearch). External servers configured via `mcpServers.json`. Built-in servers require no configuration.

**Skills** (`pkg/skills/`): SKILL.md format (YAML frontmatter + Markdown body) for specialized agent workflows. Skills have progressive disclosure steps, variables, and can run in fork mode (subprocess).

**PTC** (`pkg/ptc/`): Programmatic Tool Calling — LLM writes JavaScript executed in a Goja sandbox to reduce round-trips.

### Data Storage

All data lives under the configured `home` directory (default `~/.agentgo/`):
- `data/cortex.db` — SQLite with CortexDB extension for vectors, graph store, chat history
- `data/agentgo.db` — Agent execution history, plans, memory records
- `data/cache/`, `data/memories/` — File-based caches and memory
- `workspace/` — Filesystem MCP sandbox
- `skills/`, `intents/` — Skill and intent definitions

### Domain Interfaces

Core interfaces in `pkg/domain/types.go` define the system contracts:
- `Generator` — LLM generation (Generate, Stream, GenerateWithTools, GenerateStructured)
- `Embedder` — Vector embedding (Embed, EmbedBatch)
- `VectorStore` — Vector persistence (Store, Search, SearchWithReranker, SearchWithDiversity)
- `Chunker` — Text splitting (Split with sentence/paragraph/token methods)
- `Processor` / `RAGProcessor` — RAG pipeline orchestration
- `GraphStore`, `ChatStore` — Knowledge graph and conversation persistence

### UI

React 18 + TypeScript + Vite + Tailwind CSS + React Router v7 + TanStack Query. The built frontend is embedded into the Go binary via `//go:embed dist` in `cmd/agentgo-ui/main.go`. i18n support for Chinese/English via i18next.

## Conventions

- **IDs**: Use UUIDs (`github.com/google/uuid`) for all entity identification — documents, chunks, plans, steps, sessions. Never use sequential IDs.
- **Context**: Always pass `context.Context` as the first parameter through service calls.
- **Error handling**: Standard Go error returns. Wrap with `fmt.Errorf()` for context. Named sentinel errors for provider operations (`ErrProviderNotFound`, etc.). No `panic` in library code.
- **Config**: TOML-based (`agentgo.toml`). Paths support `~` expansion. `Config.Load()` via Viper, validated with `Config.Validate()`.
- **Concurrency**: `sync.Mutex` for shared state, `atomic` operations for pool counters, channels for streaming responses.
- **Builder pattern**: Complex services use `New().With*().Build()` (see `pkg/agent/builder.go`).
- **Dependency injection**: Constructors take all dependencies explicitly. No global state. Enables straightforward test mocking.
- **Testing**: Standard `testing` package. Fake implementations for interfaces (e.g., `fakeDirectChatLLM`). `t.Helper()` + `t.TempDir()` for test setup. HTTP handlers tested with `httptest`.
- **Streaming**: `Stream()` methods use callbacks `func(string)`; `RunStream()` returns `<-chan *Event`. SSE for real-time UI updates.
- **CLI**: Cobra command framework. Root command in `cmd/agentgo-cli/root.go` with `PersistentPreRunE` for config loading and pool initialization.
- **Struct tags**: `json:"field,omitempty"` for API, `toml:"field"` for config, `mapstructure:"field"` for Viper.

## MCP Servers

Playwright MCP is available for browser automation and UI testing against the React frontend (default port 7127 in dev):

```json
{
  "mcpServers": {
    "playwright": {
      "command": "npx",
      "args": ["@playwright/mcp@latest"]
    }
  }
}
```
