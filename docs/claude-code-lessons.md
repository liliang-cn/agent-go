# Claude Code Lessons For AgentGo

This document captures what AgentGo can borrow from Claude Code at the framework level.

The goal is not to copy Claude Code as a product.

The goal is to strengthen AgentGo as a Go framework centered on `Agent` and `Team`.

## What To Borrow

Claude Code is worth studying mainly for its runtime design:

1. a clear agent execution loop
2. a unified capability dispatch model
3. session-first execution and recovery
4. explicit permission and trust boundaries
5. structured handling of multi-agent work
6. first-class extensibility

These ideas map well to AgentGo.

They should be applied to `pkg/`, especially `pkg/agent`, instead of being treated as CLI or UI features.

## What Not To Copy

AgentGo should not copy Claude Code's product shape.

Do not optimize the architecture around:

- terminal UI
- CLI affordances
- product-specific flows
- app-shell concerns

AgentGo is a framework first.

Its architectural center is:

- `pkg/agent`
- `pkg/*` capability modules
- framework composition and embeddability

## Main Borrowing Areas

### 1. Single Runtime Core

Claude Code benefits from having a recognizable orchestration center for an agent turn:

- gather context
- build prompt
- call model
- inspect tool calls
- execute tools
- append results
- continue or stop

AgentGo already has most of this behavior, but it is spread across multiple files:

- `pkg/agent/runtime.go`
- `pkg/agent/service_execution.go`
- `pkg/agent/executor.go`
- `pkg/agent/planner.go`
- `pkg/agent/subagent.go`

#### Recommendation

Make one runtime path the canonical orchestration core for an agent turn.

Suggested shape:

- `service.go`: public facade and dependency wiring
- `runtime.go`: canonical turn loop
- `planner.go`: planning helper, not a parallel execution center
- `executor.go`: tool execution helper, not a second runtime

#### Why It Matters

This reduces drift across:

- streaming vs non-streaming paths
- tool execution semantics
- history persistence
- handoff behavior
- task completion rules
- error handling

### 2. Unified Capability Dispatch

Claude Code treats many powers as part of one controlled execution model.

AgentGo should push further in that direction.

Today, AgentGo already has a strong start:

- `pkg/agent/tool_registry.go`
- `pkg/agent/tool.go`
- `pkg/mcp/tools.go`
- `pkg/mcp/memory_tools.go`

But the framework still benefits from a stricter capability model.

#### Recommendation

Treat these as first-class capabilities hanging off the framework core:

- tools
- MCP
- skills
- memory
- RAG
- subagents
- team tasks

The runtime should invoke them through one consistent dispatch model with shared:

- tracing
- permission checks
- event emission
- error wrapping
- session scoping

#### Why It Matters

If every capability path has slightly different rules, the framework becomes harder to embed and reason about.

### 3. Session-First Execution Model

Claude Code is strong at treating the session as a real execution boundary.

AgentGo should continue to lean into that.

Relevant files:

- `pkg/agent/session.go`
- `pkg/agent/history.go`
- `pkg/agent/store.go`
- `pkg/agent/store_sqlite.go`
- `pkg/agent/service_session.go`

#### Recommendation

Keep the session as the primary boundary for:

- conversation history
- execution history
- runtime state
- memory lookup context
- task linkage
- recovery and resume

Every conversation should have a UUID-based session identity.

Do not let `userID` become the main unit of conversation identity.

#### Why It Matters

This keeps the framework clean for:

- local apps
- server apps
- embedded SDK usage
- multi-agent orchestration

### 4. Explicit Permission Boundaries

Claude Code is careful about trust and side effects.

AgentGo should borrow that principle at the framework level.

Relevant files:

- `pkg/agent/permission.go`
- `pkg/agent/guardrail.go`
- `pkg/agent/guardrails_builtin.go`
- `pkg/agent/hooks.go`
- `pkg/agent/hooks_builtin.go`

#### Recommendation

Model capability permissions more explicitly.

At minimum, the framework should distinguish:

- read-only actions
- local mutating actions
- external side-effecting actions
- cross-agent or cross-session actions

This classification should affect:

- auto-execution rules
- confirmation hooks
- logging and trace detail
- scheduling and concurrency

#### Why It Matters

A framework needs predictable safety rules, not just best-effort behavior.

### 5. Stronger Multi-Agent Orchestration Semantics

Claude Code treats multi-agent behavior as an organized system rather than ad hoc recursion.

That idea is highly relevant to AgentGo because AgentGo is explicitly Agent / Team centered.

Relevant files:

- `pkg/agent/team_manager.go`
- `pkg/agent/team_tasks.go`
- `pkg/agent/subagent.go`
- `pkg/agent/runtime_subagent.go`
- `pkg/agent/builtin_agent_delegation.go`
- `pkg/agent/handoff.go`
- `pkg/agent/handoff_executor.go`

#### Recommendation

Define stricter semantics for:

- delegation
- handoff
- follow-up tasks
- ownership of a task
- parent/child session relationships
- result propagation

Recommended invariant:

- a parent runtime can delegate
- a child runtime gets a scoped capability set
- child work returns structured results
- session lineage remains traceable

#### Why It Matters

This is one of the main areas where AgentGo can become stronger as a framework than a typical agent SDK.

### 6. First-Class Extensibility

Claude Code treats MCP and extension mechanisms as integral to its system.

AgentGo should keep doing this from a framework perspective.

Relevant modules:

- `pkg/mcp`
- `pkg/skills`
- `pkg/providers`
- `pkg/rag`
- `pkg/router`
- `pkg/ptc`

#### Recommendation

Keep capability modules as first-class, but force them to integrate through stable framework contracts.

That means:

- clear registration paths
- stable lifecycle hooks
- consistent session access
- consistent event interfaces
- consistent configuration patterns

#### Why It Matters

This avoids framework drift where each extension family behaves like its own mini-framework.

## Concrete Refactoring Priorities

### Priority 1: Canonical Runtime

Target:

- define one canonical turn loop under `pkg/agent`

Candidate files to consolidate around:

- `pkg/agent/runtime.go`
- `pkg/agent/service_execution.go`

Supporting helpers:

- `pkg/agent/executor.go`
- `pkg/agent/planner.go`
- `pkg/agent/service_tools.go`
- `pkg/agent/service_memory_context.go`

### Priority 2: Capability Contract

Target:

- make capability dispatch consistent across tools, MCP, skills, memory, RAG, and subagent execution

Candidate anchors:

- `pkg/agent/tool_registry.go`
- `pkg/agent/tool.go`
- `pkg/agent/module.go`

### Priority 3: Session And Scope Cleanup

Target:

- make session UUID the primary conversation boundary
- keep scope metadata separate from conversation identity

Candidate files:

- `pkg/agent/session.go`
- `pkg/memory/scope.go`
- `pkg/memory/service.go`
- `pkg/store/memory.go`
- `pkg/store/file_memory.go`

### Priority 4: Permission Model Cleanup

Target:

- classify capability side effects and let runtime policy depend on that

Candidate files:

- `pkg/agent/permission.go`
- `pkg/agent/guardrail.go`
- `pkg/agent/operator_tools.go`
- `pkg/mcp/filesystem_guard.go`

### Priority 5: Team Runtime Semantics

Target:

- make Team behavior feel like a first-class orchestration system instead of multiple independent shortcuts

Candidate files:

- `pkg/agent/team_manager.go`
- `pkg/agent/team_tasks.go`
- `pkg/agent/subagent.go`
- `pkg/agent/followup_agents.go`

## Recommended Mental Model

AgentGo should think about itself like this:

```text
Framework Core
  -> Agent
  -> Team
  -> Session
  -> Runtime
  -> Task / Delegation / Handoff

Capability Modules
  -> Providers
  -> MCP
  -> Skills
  -> Memory
  -> RAG
  -> Router / PTC

Support Layers
  -> Store
  -> History
  -> Config
  -> Logging
  -> Usage / Pool / Cache

Optional Adapters
  -> CLI
  -> UI
  -> API
```

This is the key borrowing lesson:

Claude Code is useful as a reference for runtime organization.

AgentGo should borrow that systems discipline while staying true to its own identity as a Go framework centered on Agent and Team abstractions.
