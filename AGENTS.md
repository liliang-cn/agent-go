# AGENTS.md

This file provides guidance to Codex (Codex.ai/code) when working with code in this repository.

## Architecture Priority (Important!)

**AgentGo's True Nature**: Primarily a **local RAG system** with **optional agent automation**. Core: document ingestion → chunking → vector storage → semantic search → Q&A. Agents are a secondary feature leveraging MCP tools.

**Feature Priority**: RAG System → Multi-Provider LLM → MCP Tools → Agent Automation → HTTP API → Local-First

**Evidence**: Core packages (`pkg/store/`, `pkg/chunker/`, `pkg/processor/`) vs optional agents (`pkg/agents/`). Most commands are RAG-focused (`ingest`, `query`) vs agent additions (`agent run`).

## Development Commands

### Build and Test

use makefile

## Common Development Tasks

### Examples Generation

Use the `/examples` command to create runnable examples:

```bash
# This will:
# 1. Analyze the requested feature/task
# 2. Create appropriate folder structure in /examples/
# 3. Generate complete, runnable Go file with imports and error handling
# 4. Run the example with: go run examples/$feature/$filename.go
```

**Example Structure**:

```
examples/
├── basic_rag_usage/main.go          # Simple RAG operations
├── provider_switching/main.go       # Multi-provider examples
├── mcp_integration/main.go          # MCP tool usage
├── agent_workflows/main.go          # Agent automation
├── custom_chunking/main.go          # Chunking strategies
└── http_api_client/main.go          # API usage examples
```

**Best Practices for Examples**:

- Each example in its own folder with `main.go`
- Include proper imports, error handling, and cleanup
- Add comments explaining key concepts
- Use realistic data and scenarios
- Follow Go best practices and project patterns

### Release Workflow

Use the `/release` command for automated releases:

```bash
# Custom slash command available: /release [optional commit message]
# This will:
# 1. Check git status and recent tags
# 2. Add non-binary files to git
# 3. Analyze commit content to determine semantic version bump
# 4. Create commit without co-author
# 5. Create and push tag (auto-determined from changes)
# 6. Push changes to remote
```

**Custom Command**: The `/release` slash command is implemented in `.Codex/commands/release.md`

**Tag Version Determination**:

- **MAJOR** (x.0.0): Breaking changes, API changes, architecture changes
- **MINOR** (x.y.0): New features, new commands, significant enhancements
- **PATCH** (x.y.z): Bug fixes, documentation updates, small improvements

**Manual Release Steps** (if needed):

```bash
# Check current status and analyze changes
git status
git tag --sort=-version:refname | head -5
git diff HEAD~1..HEAD --stat  # Review changes for version determination

# Add files (excluding binaries)
git add README.md README_zh-CN.md docs/index.html
git add examples/ pkg/ cmd/
git add *.json *.md mcpServers.json

# Create commit (message affects version determination)
git commit -m "feat: your commit message here"

# Create and push tag (version determined by content analysis)
# PATCH: "fix:", "docs:", "style:", "refactor:", "test:", "chore:"
# MINOR: "feat:", new functionality, new commands
# MAJOR: "BREAKING CHANGE:", API changes, architectural changes
git tag -a v2.x.x -m "Release v2.x.x: description"
git push origin main --tags
```

### Debugging Provider Issues

1. Check provider connectivity: `./agentgo status --verbose`
2. Test with minimal query: `./agentgo query "test" --verbose`
3. Verify model availability on provider (Ollama: `ollama list`)

### Debugging MCP Issues

1. Check server status: `./agentgo mcp status`
2. Test tool directly: `./agentgo mcp tools call filesystem read_file '{"path": "test.txt"}'`
3. Review server logs in `~/.agentgo/logs/`

### Debugging Skills Issues

1. List available skills: `./agentgo skills list`
2. Show skill details: `./agentgo skills show [skill-id]`
3. Test skill execution: `./agentgo skills run [skill-id] --var key=value`
4. Check for `SKILL.md` format errors in logs.

### Adding New Commands

1. Create command file in `cmd/agentgo/`
2. Add to root command in `cmd/agentgo/root.go`
3. Follow existing patterns for config loading and error handling
4. Add corresponding API endpoint in `api/handlers/` if needed

The codebase follows Go best practices with clear separation of concerns, comprehensive error handling, and extensive configuration options for different deployment scenarios.

- go run . to test it
- what the fuck is this userID??? use UUID!!!!每一个对话都有一个 uuid，没有 userid ufck！！！！！！
