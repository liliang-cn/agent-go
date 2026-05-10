# Harness Engineering With AgentGo

This file is the in-depth companion to the Harness section in `SKILL.md`.
Read it when the user wants reliability — lints to catch recurring agent
mistakes, checkpoints to recover crashed runs, behavioral eval to measure
"is my change actually better".

## Output lints

### Why they exist

The model keeps making the same mistake (Dispatcher narrating "I will
route this..." instead of doing it; Archivist storing 明天 / tomorrow
instead of an absolute date; an agent ending with "Next steps:..."
instead of finishing). The reflexive fix is to add another sentence to
the system prompt. That doesn't scale: prompts grow, instructions get
ignored, and you have no signal when the rule fires.

A lint is the alternative: a deterministic check that runs on the
model's free-form final answer, before the runtime emits the
`EventTypeComplete` event. On violation, the runtime appends structured
feedback as a system message and re-prompts the model — bounded by
`defaultLintRetryBudget = 2`. Exhaust the budget → the task blocks
with the lint name in the message.

### Registering one

```go
import "github.com/liliang-cn/agent-go/v2/pkg/agent"

svc.RegisterOutputLint(agent.LintFunc{
    NameValue: "must_cite_source",
    Fn: func(text string, ctx agent.LintContext) (bool, string) {
        if !strings.Contains(text, "source:") {
            return false, "response must include 'source:' citation"
        }
        return true, ""
    },
}, "Researcher")  // empty agents... → global
```

`LintContext` carries `AgentName / TaskID / SessionID / TurnIndex /
ToolCalls / IsRetry / RetryCount` so a lint can be context-aware.

### Built-ins (`pkg/agent/output_lints_builtin.go`)

| Lint                          | Scope     | What it rejects                                                            |
| ----------------------------- | --------- | -------------------------------------------------------------------------- |
| `dispatcher_no_bounce_back`   | Dispatcher| "I will route this to ..." / "我会让 X 处理"                                |
| `archivist_no_relative_time`  | Archivist | 明天 / tomorrow / next Monday without an absolute date in the response     |
| `no_planning_only_finish`     | global    | endings like "Next steps:" / "我会..." / "I will ..."                      |

Wire all three at once: `agent.RegisterDefaultOutputLints(svc)`.

### Auto-wiring on built-in agents

When `TeamManager.buildServiceForModel` constructs a Dispatcher or
Archivist service, it calls `applyBuiltInOutputLints(svc, model)` which
registers the matching agent-scoped lint. Services constructed
directly via `agent.New(...).Build()` are NOT auto-wired — you opt in.
This keeps user-controlled services backward-compatible.

### Discipline

When you observe a recurring mistake in a real run:

1. Reproduce with a mock-LLM eval scenario that fails today.
2. Write the lint in `pkg/agent/output_lints_builtin.go` (or in your
   own app code).
3. Wire the lint, re-run the scenario; it should self-heal on retry.
4. Optionally: cut the corresponding sentence from the system prompt
   now that the runtime enforces it.

## Task checkpoint + replay

### What gets saved

`pkg/agent/runtime.go` calls `persistTerminalCheckpoint(...)` from both
`completeRun` and `blockRun`. The snapshot captures the entire message
history at that moment plus `final_text`, `agent_name`, `session_id`,
`task_id`, `round`. Stored in the `task_checkpoints` SQLite table.

Snapshots beyond `MaxCheckpointsPerTask = 32` are pruned by the
in-process `checkpointWriter` after each write.

### Resume API

```go
resumed, err := manager.Tasks().ResumeFromCheckpoint(ctx, taskID,
    agent.CheckpointResumeOptions{
        // CheckpointID: "<id>",  // optional; default = latest
        FollowUp: "and now also confirm with 'replay confirmed'",
    })
```

`ResumeFromCheckpoint` looks up the latest snapshot for `taskID`,
rebuilds the agent's message history with `WithResumeMessages(...)`,
and re-runs through the normal streaming pipeline. The original
`taskID` is reused so existing `SubscribeTask` clients keep working.

### Wiring for custom services

`Service` exposes `SetCheckpointSink(sink CheckpointSink)`. TeamManager
implements `CheckpointSink` and wires itself in
`buildServiceForModel`. Direct `agent.New(...).Build()` services skip
this — call `svc.SetCheckpointSink(myImpl)` if you want persistence.

A minimal sink:

```go
type checkpointSink struct{ store *agent.Store }

func (s checkpointSink) WriteCheckpoint(taskID string, reason agent.CheckpointReason, round int, sessionID, agentName, finalText, afterTool string, messages []domain.Message) error {
    return s.store.SaveTaskCheckpoint(&agent.TaskCheckpoint{
        TaskID: taskID, Round: round, AfterTool: afterTool,
        SessionID: sessionID, AgentName: agentName,
        Messages: messages, FinalText: finalText, CreatedAt: time.Now(),
    })
}
```

### CLI surface

```bash
agentgo task checkpoints <task_id>             # list snapshots
agentgo task replay <task_id>                  # rerun from latest
agentgo task replay <task_id> --checkpoint X   # rerun from a specific snapshot
agentgo task replay <task_id> --follow-up "..." # append a user instruction to the resumed history
```

## Behavioral eval

### Scenario YAML

```yaml
# eval/scenarios/lint_dispatcher_no_bounce_back_self_heals.yaml
name: lint_dispatcher_no_bounce_back_self_heals
description: dispatcher first response narrates routing; lint rejects; second answer passes.
agent: Dispatcher
register_lints:
  - dispatcher_no_bounce_back
input: "what's the current status?"
llm_replies:
  - "I will route this to Operator now."
  - "Current status: all systems green."
expect:
  status: completed
  final_text_match: "Current status"
  final_text_must_not_match: "I will route"
  llm_calls: 2
  lint_violations:
    - lint: dispatcher_no_bounce_back
      count: 1
```

Required fields: `name`, `description`, `input`, `llm_replies` (mock
mode) **or** `mode: live` (real provider). Optional: `runs: N` for
live, `register_lints`, `max_lint_violations` (loose ceilings for
live), `expect.final_text_match` / `expect.final_text_must_not_match`
(prefix `re:` for regex), `expect.llm_calls`.

### Profiles

- **mock** (default, CI-safe): `MockLLM` plays back `llm_replies` in
  order. Deterministic. `make eval` runs every mock scenario.
- **live**: scenarios with `mode: live` use the configured provider
  pool (`agentgo eval --profile=live`). Use `runs: 2` or `runs: 3`
  to amortize non-determinism.

### Library API

```go
import evalrunner "github.com/liliang-cn/agent-go/v2/eval/runner"

opts := evalrunner.RunOptions{
    Live: func(scenarioName, agentName string, lints []string, home string) (*agent.Service, error) {
        return buildLiveService(home, agentName) // your code, returns a real-LLM Service
    },
}
results, err := evalrunner.RunAll(ctx, "eval/scenarios", opts)
fmt.Print(evalrunner.FormatSummary(results))
_, _ = evalrunner.SaveResults(results, "live", "eval/results")
```

`results` is `[]*RunResult`; each has `PassCount / FailCount /
AvgLLMCalls / AvgDurationMs / LintViolations` so you can diff between
runs.

### Discipline (the harness loop)

1. Make a runtime / lint / prompt change.
2. `make eval` (mock) — must stay green.
3. `make eval-live` — runs against the real provider.
4. `git diff eval/results/<prev>.json eval/results/<latest>.json` —
   look at `pass_count` per scenario, `avg_llm_calls`,
   `avg_duration_ms`, `lint_violations` deltas.
5. If pass rate dropped or LLM-call cost rose: revert or investigate
   before merging.

This is the loop OpenAI's "harness engineering" piece argues for —
the test isn't whether the code compiles, the test is whether the
agent's behavior actually improved.
