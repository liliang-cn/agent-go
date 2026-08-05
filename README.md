# AgentGo

[![CI](https://github.com/liliang-cn/agent-go/actions/workflows/ci.yml/badge.svg)](https://github.com/liliang-cn/agent-go/actions/workflows/ci.yml)
[![Go Reference](https://pkg.go.dev/badge/github.com/liliang-cn/agent-go/v3.svg)](https://pkg.go.dev/github.com/liliang-cn/agent-go/v3)
[![Go Report Card](https://goreportcard.com/badge/github.com/liliang-cn/agent-go/v3)](https://goreportcard.com/report/github.com/liliang-cn/agent-go/v3)
[![Release](https://img.shields.io/github/v/release/liliang-cn/agent-go)](https://github.com/liliang-cn/agent-go/releases)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)

**Agent framework for Go with local-first AI capabilities.**

AgentGo is a Go framework for building agents that run locally, use tools, keep memory, and compose with each other.

It is centered on `pkg/agent`: one transparent loop, everything-is-a-tool, and determinism enforced by lints rather than by longer prompts. AgentGo is a library — there is no CLI, no UI, no server. You embed it.

## Install

```bash
go get github.com/liliang-cn/agent-go/v3
```

## Core Ideas

- **Agent**: a named runtime with instructions, tools, memory, and sessions.
- **Loop**: one streaming state machine. `Run()` is `RunStream()` plus a collector; sub-agents reuse the same loop.
- **Tool**: everything the agent can do — built-ins, MCP, skills, PTC, and sub-agents.
- **Sub-agent**: registered with `WithSubagents(...)` and reached through a single `task(agent_name, prompt)` tool. There is no team, dispatcher or router.
- **Task**: a first-class unit of work with status, events, frames, and output.
- **Memory**: durable local context, separate from cache and RAG.
- **MCP**: tool integration layer for filesystem, web, and external capabilities.
- **Skills**: reusable Markdown/YAML workflows.
- **PTC**: optional JavaScript tool orchestration in a Goja sandbox.
- **RAG**: optional document retrieval when embeddings are configured.
- **Output lints**: deterministic post-output checks that re-prompt the model on violation (instead of "please remember to..." paragraphs).
- **Checkpoint + replay**: every terminal task writes a snapshot; crashed/cancelled runs can be re-played from the latest checkpoint.
- **Eval harness**: scenario-driven behavioral evaluation, mock or live LLM, JSON output for cross-commit diffs.

## Minimal Agent

```go
package main

import (
	"context"
	"fmt"

	"github.com/liliang-cn/agent-go/v3/pkg/agent"
)

func main() {
	ctx := context.Background()

	svc, err := agent.New("assistant").
		WithPrompt("You are a concise Go assistant.").
		Build()
	if err != nil {
		panic(err)
	}
	defer svc.Close()

	reply, err := svc.Ask(ctx, "What is AgentGo?")
	if err != nil {
		panic(err)
	}
	fmt.Println(reply)
}
```

## Agent With Memory

```go
svc, _ := agent.New("assistant").
	WithMemory().
	Build()
defer svc.Close()

svc.Chat(ctx, "My name is Alice and I prefer short answers.")
result, _ := svc.Chat(ctx, "What do you know about me?")

fmt.Println(result.Text())
```

## Sub-agents are just a tool

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
defer svc.Close()

result, _ := svc.Run(ctx, "Research X and write two paragraphs about it.")
fmt.Println(result.Text())
```

The model reaches a sub-agent by calling `task(agent_name, prompt)`. It runs through the same loop, so its events bubble up, its answer is linted, and its terminal state is checkpointed.

## Tasks

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
fmt.Println(done.Status)
fmt.Println(done.Output)
```

## Checkpoint + replay

```go
// Crashed or cancelled task? Re-play it from the last snapshot.
resumed, _ := manager.Tasks().ResumeFromCheckpoint(ctx, task.ID, agent.CheckpointResumeOptions{
	FollowUp: "and now also do X",
})
```

Every terminal state writes a `TaskCheckpoint`; `WithResumeMessages` is the low-level `RunOption` underneath.

## Output lints — moving "please don't" out of prompts

When an agent keeps making the same mistake (ending with "Next steps:...", claiming it sent an email it never sent, answering with nothing at all), don't add another sentence to its instruction. Register a lint:

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

Every service built with `agent.New(...).Build()` gets the built-ins automatically:

- `no_planning_only_finish` — reject planning-only endings
- `file_task_must_write` — a task that asked for a file must have produced one
- `no_raw_ptc_code` — sandbox JS must never be the user-facing answer
- `non_empty_final_answer` — a run cannot terminate with no text
- `task_delivery_contract` — a goal naming a delivery action (send the mail, post the message, write the file) cannot complete unless a matching tool was actually called

Related: a task that says "without using any tools" is given **zero** tools, and any tool call it makes anyway is refused. Hard constraints are enforced by the runtime, not requested in the prompt.

## Storage

By default AgentGo uses:

```text
~/.agentgo/
├── data/
│   ├── agentgo.db     # config, providers, agents, tasks, checkpoints
│   └── cortex.db      # optional memory/vector/graph storage
├── memories/          # file memory when enabled
├── skills/            # local skills
└── workspace/         # agent working directory
```

Override the home directory with the `AGENTGO_HOME` environment variable.

## Repository Layout

```text
pkg/agent      framework core: agent, loop, tools, context, hooks/lints, sessions, checkpoints
pkg/mcp        MCP tools and servers
pkg/memory     durable memory
pkg/rag        optional retrieval
pkg/skills     skill loading
pkg/providers  LLM providers (with reasoner-model fallbacks)
pkg/pool       provider pool + token/cost accounting
pkg/poolsvc    process-global pool service for embedders
pkg/ptc        Programmatic Tool Calling — JS sandbox
pkg/store      SQLite storage
eval/          behavioral eval harness (scenarios + runner)
examples/      runnable examples
```

## Development

```bash
make test           # go test ./...
make check          # fmt + vet + test
make eval           # behavioral eval, mock-LLM, CI-safe
make eval-live      # behavioral eval, real provider
```

See `CLAUDE.md` and `docs/dev/PLAN.md` for the harness-engineering roadmap and operational guidance.

## License

MIT
