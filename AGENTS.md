# AGENTS.md

This file provides guidance for working in this repository.

## Core Identity

AgentGo is a Go framework for building Agent / Team based systems.

The core of the project is `pkg/`.

`cmd/`, `ui`, and other entrypoints are optional adapters. They are not the architectural center of the project.

## Architecture Priority

Always reason about the repository in this order:

1. `pkg/agent` is the framework core
2. `pkg/*` capability modules extend the framework core
3. storage / config / infra support the framework core
4. CLI / UI / API are optional shells around the framework

Do not describe this project as:

- a CLI-first app
- a UI-first app
- a RAG-first app

Describe it as:

- a Go framework
- centered on Agent / Team abstractions
- extensible through providers, MCP, skills, memory, RAG, router, and related modules

## Core Modules

Primary framework core:

- `pkg/agent`

Primary capability modules:

- `pkg/providers`
- `pkg/mcp`
- `pkg/skills`
- `pkg/memory`
- `pkg/rag`
- `pkg/router`
- `pkg/ptc`

Support modules:

- `pkg/store`
- `pkg/config`
- `pkg/log`
- `pkg/cache`
- `pkg/pool`
- `pkg/usage`
- `pkg/domain`

Optional adapters:

- `cmd/agentgo-cli`
- `cmd/agentgo-ui`
- `api/`

## Design Rules

When making changes:

1. Prefer improving `pkg/` over adding behavior in `cmd/`
2. Keep Agent / Team abstractions clean and reusable
3. Treat CLI/UI behavior as adapter logic, not framework logic
4. New capabilities should plug into the framework core instead of bypassing it
5. Avoid coupling capability modules too tightly to one adapter

## Session Identity

Every conversation should have its own UUID-based session identity.

Session identity is the primary conversation boundary.

Do not introduce `userID` as the main identity for a conversation.

## Development Notes

- Use the Makefile for common development tasks
- Prefer changes that improve library ergonomics and composability
- Keep public package APIs stable and intentional
- Think in terms of embeddable framework design, not only local app behavior
