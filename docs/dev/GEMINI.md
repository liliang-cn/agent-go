# GEMINI.md - AgentGo Project Context

## Project Overview
AgentGo (Retrieval-Augmented Generation Offline) is a modular, local-first RAG and Agent system written in Go. It is designed to provide powerful AI capabilities (document Q&A, task automation) while keeping data local and supporting a wide range of LLM providers.

### Core Architecture
- **Local-First RAG (pkg/rag):** Uses `cortexdb` (SQLite-based) for vector and knowledge graph storage. Implements hybrid search (Vector + GraphRAG).
- **Agentic Automation (pkg/agent):** Features a Planner/Executor model with multi-turn tool execution, supporting MCP (Model Context Protocol) and custom Skills.
- **Provider Management (pkg/providers):** Unified `Generator` and `Embedder` interfaces with an LLM Pool for resilience and model switching.
- **Modern UI (web/):** React-based management dashboard integrated via Go's `embed` (internal/web).
- **Extensibility:** Interface-driven design allows easy swapping of chunkers, embedders, and vector stores.

## Building and Running
The project uses a `Makefile` to manage common tasks.

- **Dependencies:** `make deps` (Go mod + npm install)
- **Development:** `make dev` (Builds web + starts backend on port 7127)
- **Frontend Dev:** `make frontend-dev` (Starts Vite dev server on port 5555)
- **Full Build:** `make build` (Produces `agentgo` binary with embedded UI)
- **Testing:** `make test` (Runs Go tests)
- **Clean:** `make clean` (Removes binaries, web dist, and local databases)

### CLI Entry Point
Run the CLI directly for tasks:
- `go run ./cmd/agentgo-cli serve --ui` - Start the server with UI.
- `go run ./cmd/agentgo-cli rag ingest <file>` - Index documents.
- `go run ./cmd/agentgo-cli rag query "<prompt>"` - Query the knowledge base.

## Development Conventions
- **Language:** Go (Backend), TypeScript/React (Frontend).
- **Error Handling:** Standard Go error patterns. Avoid `panic` in library code.
- **Context:** Always pass `context.Context` through service calls.
- **Database:** Use SQLite for persistence. Data is stored in `home/data/` (`~/.agentgo/data/` by default).
- **Unified Path:** Configure `home` in `agentgo.toml` to unify paths for `config/`, `data/`, `skills/`, `intents/`, and `workspace/`.
- `IDs:** Use UUIDs for session and conversation identification (avoid sequential IDs).
- **RAG Priority:** Focus on local document processing and semantic retrieval before agentic tool use.
- **Agent Skills:** Use the `SKILL.md` format (YAML frontmatter + Markdown) for specialized workflows. See `SKILLS.md` for details.
- **Examples:** New features should include a runnable example in the `examples/` directory.
- **MCP Integration:** Prefer standard MCP tools for external system interactions.

## Key File Locations
- `pkg/domain/types.go`: Core system interfaces.
- `pkg/rag/processor/service.go`: Main RAG pipeline logic.
- `pkg/agent/service.go`: Agent planning and execution loop.
- `cmd/agentgo-cli/`: CLI command definitions (using Cobra).
- `agentgo.toml.example`: Template for system configuration.
- `SKILLS.md`: Documentation for the Agent Skills system.
