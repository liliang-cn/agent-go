# AGENTS.md

Repository guidance for AI coding agents working in this tree.

## Project Identity

AgentGo is a Go framework for building Agent / Team based AI systems with local-first capabilities.

The architectural center is `pkg/`, especially `pkg/agent`.

`cmd/`, `ui/`, `api/`, and examples are adapters or entrypoints around the framework. They are useful for validation and product behavior, but they are not the architectural center.

Do not frame this repository as:

- a CLI-first application
- a UI-first application
- a RAG-first application
- a provider wrapper

Frame it as:

- a Go framework
- centered on Agent / Team abstractions
- extensible through providers, MCP, skills, memory, RAG, router, PTC, scheduler, task, A2A, ACP, and related modules

## Architecture Priority

When reasoning about a change, use this order:

1. `pkg/agent` defines the core framework concepts and orchestration behavior.
2. `pkg/*` capability modules extend or support that core.
3. Storage, config, logging, cache, scheduler, and domain modules provide infrastructure.
4. CLI, UI, API, and examples adapt the framework for specific surfaces.

Prefer framework improvements in `pkg/` when behavior should be reusable. Put only surface-specific parsing, display, commands, and transport concerns in adapters.

## Core Concepts

- `Agent`: a named runtime with instructions, tools, memory/session context, model preferences, permissions, hooks, and execution behavior.
- `Team`: a persistent coordination boundary with members, aliases, defaults, task routing, and an orchestrator/specialist model.
- `Task`: a first-class unit of work with status, events, frames, output, and execution state.
- `Task plan`: a coordination record for breaking down work. Plan items can be submitted as real tasks, but the plan itself is not the executor.
- `Session`: the primary conversation boundary. Every conversation should have a UUID-based session identity.
- `PTC`: programmatic tool calling support. Keep sandbox, runtime, and tool-adapter concerns reusable and independent from one CLI flow.

Do not introduce `userID` as the main identity for a conversation. If user identity is needed later, keep it separate from session identity.

## Module Map

Framework core:

- `pkg/agent`: agents, teams, tasks, task plans, sessions, routing, tools, hooks, memory integration, PTC integration, runtime execution

Primary capability modules:

- `pkg/providers`: LLM provider configuration and model access
- `pkg/mcp`: MCP clients, tools, and built-in tool servers
- `pkg/skills`: skill loading and execution context
- `pkg/memory`: durable memory behavior
- `pkg/rag`: retrieval, chunking, embedding, graph/vector storage integration
- `pkg/router`: intent/routing support
- `pkg/ptc`: programmatic tool calling runtimes, security, config, and store
- `pkg/a2a`: A2A protocol integration
- `pkg/acpserver`: ACP server integration

Support modules:

- `pkg/store`: SQLite-backed persistence primitives
- `pkg/config`: configuration loading and defaults
- `pkg/log`: logging helpers
- `pkg/cache`: local cache utilities
- `pkg/pool`: worker/resource pooling
- `pkg/usage`: token and usage accounting
- `pkg/domain`: shared domain types
- `pkg/scheduler`: scheduled tasks and executors
- `pkg/task`: task-related support outside the agent core
- `pkg/search`: search helpers
- `pkg/services`: service-layer helpers
- `pkg/resource`: resource abstractions
- `pkg/prompt`: prompt helpers
- `pkg/metadata`: metadata helpers

Adapters and examples:

- `cmd/agentgo-cli`: CLI adapter
- `cmd/agentgo-ui`: UI/API adapter with embedded web assets
- `ui/`: frontend source for the UI
- `api/`: optional API surface, if present
- `examples/`: runnable examples and integration demos
- `skills/`: repository-local skills and skill documentation

## Design Rules

When making changes:

1. Keep Agent / Team / Task / Session abstractions clean and reusable.
2. Add reusable behavior in `pkg/` instead of burying it in `cmd/`.
3. Treat CLI and UI behavior as adapter logic.
4. Avoid coupling capability modules to one adapter.
5. Keep public package APIs stable and intentional.
6. Prefer small, composable types and options over one-off special cases.
7. Persist durable framework state through the store layer instead of process globals.
8. Make task and plan behavior observable through events/status where practical.
9. Preserve local-first behavior and avoid requiring hosted services unless the feature explicitly needs them.

## Storage And Identity

Default local state lives under `~/.agentgo/` unless overridden by environment/config.

Use session identity as the conversation boundary. A task, team, memory record, or plan may reference a session, but should not replace it.

Task plans are durable coordination records. Task execution state belongs to tasks.

## Development Workflow

Use the Makefile for common validation:

```bash
make test
make check
make build
```

`make test` runs `go test ./...` after ensuring embedded UI assets have a placeholder.

For focused Go work, targeted package tests are acceptable during iteration, but run `make test` before considering broad framework changes complete.

For UI adapter changes, use the UI Makefile targets:

```bash
make ui-build
make ui-dev
make ui-api-dev
make ui-web-dev
```

## Change Guidance

- Read nearby code before introducing new patterns.
- Prefer existing builders, stores, managers, task services, and tool adapters.
- Keep adapters thin: parse input, call framework APIs, format output.
- Keep framework packages free of terminal/UI assumptions.
- Add tests near the changed package when behavior changes.
- For persistence changes, cover create/list/update or migration-sensitive behavior.
- For concurrency/task changes, check status transitions, cancellation, blocking, and event visibility.
- Do not rewrite unrelated files or generated assets unless the task requires it.

