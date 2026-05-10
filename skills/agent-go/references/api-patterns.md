# AgentGo API Patterns

## Minimal Agent

```go
svc, err := agent.New("assistant").
	WithPrompt("You are a concise assistant.").
	Build()
if err != nil {
	return err
}
defer svc.Close()

reply, err := svc.Ask(ctx, "What is AgentGo?")
```

## Chat With Memory

```go
svc, err := agent.New("assistant").
	WithMemory().
	Build()
if err != nil {
	return err
}
defer svc.Close()

_, _ = svc.Chat(ctx, "My name is Alice.")
result, err := svc.Chat(ctx, "What do you know about me?")
```

## Team Manager

```go
store, err := agent.NewStore("agentgo.db")
if err != nil {
	return err
}

manager := agent.NewTeamManager(store)
if err := manager.SeedDefaultMembers(); err != nil {
	return err
}

task, err := manager.Tasks().Submit(ctx, agent.TaskSubmitOptions{
	SessionID: "demo-session",
	AgentName: "Operator",
	Input:     "Check the repository status.",
})
if err != nil {
	return err
}

done, err := manager.Tasks().Await(ctx, task.ID)
```

## Task Plans

Task plans coordinate work. Tasks execute work.

```go
plan, err := manager.Plans().Create(ctx, agent.TaskPlanCreateOptions{
	SessionID: "demo-session",
	Goal:      "Validate CLI task planning",
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
if err != nil {
	return err
}

task, err := manager.Plans().SubmitItem(ctx, plan.ID, "inspect", agent.TaskPlanSubmitItemOptions{})
```

## Streaming a run

Use `RunStream` when you want live tokens / tool events:

```go
events, err := svc.RunStream(ctx, "summarize the README in three bullets")
if err != nil { return err }
for evt := range events {
    switch evt.Type {
    case agent.EventTypePartial:
        fmt.Print(evt.Content)             // streaming token
    case agent.EventTypeToolCall:
        log.Printf("tool: %s", evt.ToolName)
    case agent.EventTypeComplete:
        // final aggregated text in evt.Content
    case agent.EventTypeBlocked, agent.EventTypeError:
        // task ended without a normal completion
    }
}
```

For TeamManager-submitted tasks, subscribe at the task layer:

```go
events, unsub, _ := manager.SubscribeTask(submitted.ID)
defer unsub()
for evt := range events {
    if evt.Type == agent.TaskEventTypeRuntime && evt.Runtime != nil {
        // evt.Runtime is the same *agent.Event as the per-Service stream
    }
}
```

## Output lints

Register a deterministic check that fires on the model's free-form
final answer; failure re-prompts the model with structured feedback.

```go
svc.RegisterOutputLint(agent.LintFunc{
    NameValue: "must_cite_source",
    Fn: func(text string, ctx agent.LintContext) (bool, string) {
        if !strings.Contains(text, "source:") {
            return false, "response must include 'source:' citation"
        }
        return true, ""
    },
}, "Researcher")  // empty agents... = global
```

Use the three built-ins as a starting set:

```go
agent.RegisterDefaultOutputLints(svc)
```

Built-ins covered: `dispatcher_no_bounce_back`,
`archivist_no_relative_time`, `no_planning_only_finish`. See
`harness.md` for details and discipline.

## Task checkpoint + replay

Every `completeRun` / `blockRun` writes a snapshot when a
`CheckpointSink` is wired. TeamManager wires itself automatically;
`agent.New(...).Build()` services need:

```go
svc.SetCheckpointSink(manager) // or any CheckpointSink implementation
```

Resume a crashed task from its latest snapshot:

```go
resumed, err := manager.Tasks().ResumeFromCheckpoint(ctx, taskID,
    agent.CheckpointResumeOptions{
        FollowUp: "and now also do X", // optional
    })
```

## Custom tools

Add a Go function as a tool the model can call:

```go
svc.AddToolWithMetadata(
    "fetch_status",
    "Fetch the system status (read-only).",
    map[string]interface{}{
        "type":       "object",
        "properties": map[string]interface{}{},
    },
    func(ctx context.Context, args map[string]interface{}) (interface{}, error) {
        return fetchStatus(ctx)
    },
    agent.ToolMetadata{
        ReadOnly:        true,
        ConcurrencySafe: true,
        InterruptBehavior: agent.InterruptBehaviorCancel,
    },
)
```

Tool metadata flags (`ReadOnly`, `ConcurrencySafe`, `Destructive`,
`InterruptBehavior`) drive runtime batching, permission prompts, and
cancellation behavior. Set them honestly.

## Run options

Pass to `RunStream` / `Run` to override defaults per call:

```go
events, _ := svc.RunStreamWithOptions(ctx, "do the thing",
    agent.WithSessionID("custom-session-id"),
    agent.WithTaskID("custom-task-id"),
    agent.WithParentTaskID("upstream-task-id"),
    agent.WithMaxTurns(8),
    agent.WithTemperature(0.1),
    agent.WithPTCEnabled(false),                    // disable PTC for this run
    agent.WithResumeMessages(savedHistory),         // start from a checkpoint
    agent.WithInheritedMemoryScope(agentID, teamID, userID),
)
```

## Where to look in the codebase

- Builder + Service public API: `pkg/agent/builder.go`, `pkg/agent/service.go`
- Runtime loop, lint hook, checkpoint hook: `pkg/agent/runtime.go`
- TaskService (Submit / Get / Await / Resume / ResumeFromCheckpoint): `pkg/agent/task_service.go`, `pkg/agent/task_checkpoint_manager.go`
- TaskPlanService: `pkg/agent/task_plan.go`
- Output lints: `pkg/agent/output_lint.go`, `output_lints_builtin.go`
- Provider compatibility (DeepSeek reasoner fallback): `pkg/pool/client.go`
- Eval runner (library API): `eval/runner/`

