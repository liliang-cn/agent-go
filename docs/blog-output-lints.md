# Stop begging your agent in prompts. Lint its output instead.

*A deterministic alternative to "please remember to…" prompt engineering, with a self-healing retry loop and regression evals. Examples in Go, but the idea is language-agnostic.*

---

## The problem: every agent prompt becomes a graveyard of pleas

If you've built an LLM agent that runs in a loop, your system prompt probably looks like mine used to:

```
You are a helpful operator agent.
IMPORTANT: Do not just describe what you are going to do — actually do it.
IMPORTANT: Always resolve relative dates ("tomorrow", "next week") to an
           absolute YYYY-MM-DD before storing them.
IMPORTANT: If you are a dispatcher, do not narrate "I will route this" —
           return the routed result inline.
IMPORTANT: Never end your turn with "Next steps: ...".
...
```

Every one of those `IMPORTANT:` lines is a scar. Each was added the day the model did the dumb thing again. And each has the same three problems:

1. **It's probabilistic.** The model *usually* remembers. Until the context gets long, or the task gets weird, and it forgets. You can't put a paragraph on a leash.
2. **It's invisible in tests.** "Did the agent stop stalling?" isn't something you can assert. You eyeball a few runs and hope.
3. **It taxes every single call.** That paragraph is in the context window on turn 1 and turn 40, burning tokens whether or not the failure mode is even relevant to this task.

Prompt engineering treats a **control problem** as a **persuasion problem**. We keep writing more elaborate pleas to a system that has no obligation to obey them.

## The reframe: a lint is a deterministic post-output check

Here's the move. Take the rule out of the prompt and make it a **function that inspects the model's final answer**. If the answer violates the rule, you don't ship it — you reject it and re-prompt the model with the reason.

That's it. That's an output lint. It's the agent equivalent of ESLint or `go vet`: a deterministic gate that runs *after* generation, not a hope you bake in *before*.

In [AgentGo](https://github.com/liliang-cn/agent-go) the interface is exactly this small:

```go
type OutputLint interface {
    // Name returns a stable identifier used in events and retry feedback.
    Name() string
    // Check inspects the final text. ok=true passes; ok=false with a
    // human-readable reason rejects (the reason is shown to the model on retry).
    Check(text string, ctx LintContext) (ok bool, reason string)
}
```

The `LintContext` carries what a smart check needs — the agent name, the task `Goal`, session/turn info — so a lint can judge the answer *against what was actually asked*:

```go
type LintContext struct {
    AgentName string
    TaskID    string
    SessionID string
    TurnIndex int
    Goal      string // the task input, for goal-aware checks
}
```

## Three lints that used to be prompt paragraphs

AgentGo ships three built-ins. Each one encodes a rule that lived in instruction strings for months. The file comment says it best:

> Moving them into deterministic checks shifts enforcement from "the model has to remember a paragraph" to "the runtime rejects and re-prompts on violation."

**1. `no_planning_only_finish`** — the universal failure mode. The agent ends its turn with *"Next steps: I'll read the README and summarize it"* instead of actually summarizing. By the time lints run, the runtime already knows the model did **not** call `task_complete` — so if the final text reads like a plan, the model is stalling. Reject it.

**2. `dispatcher_no_bounce_back`** — a routing agent writes *"I will route this to the specialist…"* as its final answer, without ever calling the routing tool. The intention is not the action. Reject it.

**3. `archivist_no_relative_time`** — a memory agent tries to store *"meeting moved to tomorrow"* without resolving "tomorrow" to a date. Six months later that memory is garbage. The lint fails the response if it contains a relative-time keyword (`tomorrow`, `next Monday`, `明天`, `下周`…) with no absolute `YYYY-MM-DD` nearby. Reject it.

None of these are in the system prompt anymore. They're code.

## The part that makes it actually work: self-healing retry

A gate that just says "no" would be annoying. The point is what happens next: the rejection reason goes **back to the model as feedback**, and it tries again. Most of the time, the second attempt is correct — because the model didn't lack capability, it just needed to be told it stalled.

The best part is that this behavior is **executable and tested**. Here's a real eval scenario from the repo (`eval/scenarios/lint_no_planning_only_finish_self_heals.yaml`):

```yaml
name: lint_no_planning_only_finish_self_heals
description: >
  When the model ends with a planning phrase ("Next steps: ...") instead
  of delivering a result, no_planning_only_finish rejects it. The model
  retries with a substantive answer and the run completes.
agent: Operator
register_lints:
  - no_planning_only_finish
input: "summarize the README"
llm_replies:
  - "Next steps: I'll read the README and summarize it."   # ← rejected
  - "README documents three CLI subcommands: build, test, deploy."  # ← passes
expect:
  status: completed
  final_text_match: "three CLI subcommands"
  final_text_must_not_match: "Next steps"
  llm_calls: 2
  lint_violations:
    - lint: no_planning_only_finish
      count: 1
```

Read what that asserts: the first (stalling) reply is rejected, the model self-corrects on the second, the run completes, and it took exactly **2 LLM calls with 1 lint violation**. That's not a vibe. That's a regression test for agent behavior.

## Writing your own lint

The interface is one method, so an inline lint is a few lines. Say you never want the agent to leak an internal ID prefix:

```go
noInternalIDs := agent.LintFunc{
    NameValue: "no_internal_ids",
    Fn: func(text string, ctx agent.LintContext) (bool, string) {
        if strings.Contains(text, "usr_internal_") {
            return false, "response leaked an internal user id; return the public handle instead"
        }
        return true, ""
    },
}

svc.RegisterOutputLint(noInternalIDs)            // global: every agent
svc.RegisterOutputLint(dispatcherCheck, "Dispatcher") // scoped to one agent
```

Lints are opt-in and composable. A global bucket runs for every agent; you can also scope a lint to specific agents by name. The registry stops at the first violation, so ordering is cheap and predictable.

## Why this is strictly better than another prompt line

- **Deterministic.** A regex either matched or it didn't. No temperature, no "usually."
- **Testable.** Behavior becomes an assertion (`llm_calls: 2`, `final_text_must_not_match`), so you can diff agent behavior across commits in CI — which is exactly what an [eval harness](https://github.com/liliang-cn/agent-go) is for.
- **Cheap.** A lint costs a string scan, not context-window real estate on every turn. It only spends tokens (one retry) on the runs that actually fail.
- **Targeted.** A lint scoped to the Dispatcher doesn't tax the Archivist. Try doing *that* with a shared system prompt.
- **Self-documenting.** `no_planning_only_finish` in an event log tells you precisely why a turn was retried. "The model felt like behaving" does not.

## What it's *not*

Lints are guardrails for **known, detectable failure modes** — stalling, format violations, leaked tokens, un-resolved dates. They're regex-and-rules deterministic checks, so they catch *shape*, not *truth*. They won't tell you the summary is wrong, only that it's a summary and not a promise to summarize. For semantic correctness you still want evals (and, if you must, an LLM-judge). Think of lints as the `go vet` layer, with evals as your test suite on top. Used together, "did my agent regress?" stops being a feeling and starts being a CI check.

## Try it

The whole thing — output lints, the self-healing loop, checkpoint/replay for crash recovery, and the eval harness — is open source and Go-native:

**https://github.com/liliang-cn/agent-go**

If you've been adding `IMPORTANT:` lines to a prompt for the last six months, pull one of them out and write it as a lint. It's a strange feeling the first time the runtime enforces a rule instead of asking the model to.

---

*AgentGo is a production-grade Go framework for agents, multi-agent teams, and RAG. As one guy on the internet put it: "It's useless and it consumes a lot of tokens." We left the quote at the top of the README.*
