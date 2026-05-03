# AgentGo

**Agent / Team framework for Go with local-first AI capabilities.**

> “AgentGo? It's useless and it consumes a lot of tokens.” -- some guy on the internet

AgentGo is a Go framework for building agent systems that can run locally, use tools, keep memory, and coordinate work through teams.

It is centered on `pkg/agent`. The CLI and UI are adapters around the framework, not the core.

## Install

```bash
go get github.com/liliang-cn/agent-go/v2
```

## Core Ideas

- **Agent**: a named runtime with instructions, tools, memory, and sessions.
- **Team**: a persistent group of agents with an orchestrator and specialists.
- **Task**: a first-class unit of work with status, events, frames, and output.
- **Task plan**: a lightweight work plan whose items can be submitted as real tasks.
- **Memory**: durable local context, separate from cache and RAG.
- **MCP**: tool integration layer for filesystem, web, and external capabilities.
- **Skills**: reusable Markdown/YAML workflows.
- **PTC**: optional JavaScript tool orchestration in a Goja sandbox.
- **RAG**: optional document retrieval when embeddings are configured.

## Minimal Agent

```go
package main

import (
	"context"
	"fmt"

	"github.com/liliang-cn/agent-go/v2/pkg/agent"
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

## Team Manager

```go
store, _ := agent.NewStore("agentgo.db")
manager := agent.NewTeamManager(store)
_ = manager.SeedDefaultMembers()

task, _ := manager.Tasks().Submit(ctx, agent.TaskSubmitOptions{
	SessionID: "demo-session",
	AgentName: "Operator",
	Input:     "Check the current repository status.",
})

done, _ := manager.Tasks().Await(ctx, task.ID)
fmt.Println(done.Status)
fmt.Println(done.Output)
```

## Task Plans

Task plans are coordination records. Actual execution still happens through tasks.

```go
plan, _ := manager.Plans().Create(ctx, agent.TaskPlanCreateOptions{
	SessionID: "demo-session",
	Goal:      "Verify the CLI task-plan flow",
	Items: []agent.TaskPlanItem{
		{
			ID:         "inspect",
			Subject:    "Inspect CLI output",
			OwnerAgent: "Operator",
			Blocks:     []string{"summarize"},
		},
		{
			ID:         "summarize",
			Subject:    "Summarize result",
			OwnerAgent: "Responder",
			BlockedBy:  []string{"inspect"},
		},
	},
})

task, _ := manager.Plans().SubmitItem(ctx, plan.ID, "inspect", agent.TaskPlanSubmitItemOptions{})
fmt.Println(task.ID)
```

## CLI

```bash
# Chat with Dispatcher
agentgo chat

# Ask once
agentgo chat "Create a small task plan for validating this repo"

# Inspect plans in the current chat session
agentgo chat --session my-session
# then type:
# /plans
# /plan ready <plan_id>
# /plan submit <plan_id> <item_id> [agent_name]

# Manage agents
agentgo agent list
agentgo agent show Dispatcher
agentgo agent run --agent Operator "Run git status and summarize it"

# Manage teams
agentgo team list
agentgo team add "Docs Team" --description "Documentation work"

# Inspect tasks
agentgo task list
agentgo task get <task_id>
agentgo task trace <task_id>

# Manage LLM providers
agentgo llm list
agentgo llm add --name local --url http://localhost:11434/v1 --model qwen2.5
```

## Storage

By default AgentGo uses:

```text
~/.agentgo/
├── data/
│   ├── agentgo.db     # config, providers, agents, teams, tasks, plans
│   └── cortex.db      # optional memory/vector/graph storage
├── memories/          # file memory when enabled
├── skills/            # local skills
└── workspace/         # agent working directory
```

Override the home directory with:

```bash
AGENTGO_HOME=/path/to/home agentgo chat
```

## Repository Layout

```text
pkg/agent      framework core: agents, teams, tasks, task plans
pkg/mcp        MCP tools and servers
pkg/memory     durable memory
pkg/rag        optional retrieval
pkg/skills     skill loading
pkg/providers  LLM provider pool
pkg/store      SQLite storage
cmd/           CLI and UI adapters
examples/      runnable examples
```

## Development

```bash
make test
```

## License

MIT
