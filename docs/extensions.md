# Writing an extension

An extension is one concern that takes part in the agent loop at one or more of
its seams. It is an ordinary Go type in your own module; the framework never
needs to know it exists. This page is the contract.

## The shape

```go
type Extension interface {
	Name() string   // non-empty, unique within a service
}
```

That is the whole required surface. Everything else is optional and detected by
type assertion when `Build()` runs, so implement only what you need:

| implement | seam | you may |
| --- | --- | --- |
| `agent.Observer` (embed `agent.BaseObserver`) | model turns, tool calls, retries, compaction, checkpoints, segments | observe |
| `agent.OutputLint` | the final answer | reject it with a reason; the loop re-prompts, bounded by a retry budget |
| `agent.Module` | the tool registry | add tools |
| `agent.ContextContributor` | before the first turn | append system messages |
| `agent.ToolCallFilter` | before a tool runs | replace its arguments, or refuse it |
| `agent.ToolResultFilter` | after a tool runs | replace what the model sees |
| `agent.RunLifecycle` | run start / run end | veto a run; see how every run ended |
| `agent.Lifecycle` | `Build()` / `Close()` | open and release a resource |
| `agent.HookProvider` | any `HookEvent` | register raw hooks for what the typed seams do not cover |

Install with `agent.New(...).WithExtensions(a, b, c)`. Extensions run in that
order at every seam. Registration is strict: an empty or duplicate name fails
the build.

## What each seam promises

**Observer** callbacks are informational. They carry stable span and call ids
so start/end pairs can be matched. `ModelInfo.Model` names the model and
`ModelResult` carries the prompt/completion/cached token split, so an observer
can price a turn with `pool.CalculateCostDetailed` without asking the service.

**OutputLint.Check** returns `(ok, reason)`. A `false` appends the reason as
feedback and asks the model again. The lint budget is small (two retries);
exhausting it blocks the run. Write the reason for the model: what is wrong
and what a passing answer looks like.

**ContextContributor** is additive. It returns messages to append; it cannot
see or change the goal beyond reading it. This is deliberate — rewriting the
goal is one phrase table away from deciding behaviour from the user's wording,
which the framework forbids everywhere. Anything you append reaches the prompt
prefix, so keep it byte-stable across rounds or you defeat the provider's
prompt cache.

**ToolCallFilter.BeforeTool** returns a `ToolVerdict`. `Args` non-nil replaces
the arguments; `Block` non-empty refuses the call and the model reads the
reason as the tool's error. An error from the filter itself also refuses the
call.

**ToolResultFilter.AfterTool** returns `(result, replaced, err)`. `replaced`
substitutes the result; an error replaces the result with the error. A filter
that could not inspect a result therefore fails closed — the model never sees
what the filter was supposed to check.

**RunLifecycle.OnRunStart** returning an error blocks the run before its first
model turn with that reason. `OnRunEnd` fires exactly once per run, on every
terminal path (completed, blocked, cancelled), with the stop reason, the final
text and the duration. It cannot change the outcome.

**Lifecycle.Start** runs at the end of a successful `Build()`, in extension
order; a failure fails the build and stops what already started. `Stop` runs
in `Close()`, in reverse order, after in-flight runs have been cancelled and
before the store is released.

## What an extension cannot do

There is no `next()`. An extension cannot wrap the loop, skip a stage, reorder
stages, or call the model itself. This is the difference between these seams
and a middleware chain, and it is what keeps one loop one loop. If a concern
needs to sit *between* stages rather than *at* a seam, it belongs in the
framework, not in an extension — open an issue.

## Concurrency

A `Service` runs many tasks at once and shares its extensions between them.
Every method above may be called concurrently from different runs. Guard
mutable state; the shipped extensions use a mutex or atomics. The framework's
own test drives twelve concurrent runs through every seam of one extension
under the race detector.

## Testing without a model

`pkg/extensiontest` builds a real service over a scripted model, so your test
drives an actual run and your extension is exercised at the seams the
framework calls, in the order it calls them:

```go
func TestRedactsToolResults(t *testing.T) {
	llm := extensiontest.Script(
		extensiontest.CallTool("echo", map[string]interface{}{"text": "the secret"}),
		extensiontest.Answer("done"),
	)
	svc := extensiontest.NewService(t, llm, extensiontest.EchoTool(), myredactor.New())
	out := extensiontest.Run(t, svc, "what did echo say?")

	tools := extensiontest.ToolMessages(llm.Rounds()[1])
	// assert on out.Final / out.Blocked and on what the model was shown
}
```

`Rounds()` is every message list the model received — the ground truth for
"what did the model actually see after my filter ran".

## Stability

The types named on this page — `Extension`, the nine capability interfaces,
`HookSpec`, `ContextInput`, `ToolCallInfo`, `ToolVerdict`, `ToolResultInfo`,
`RunInfo`, `RunOutcome`, `ModelInfo`, `ModelResult`, `LintContext`, and the
`extensiontest` package — are the extension API. They follow the module's
semantic version: fields may be added in a minor release, nothing is removed
or changed in meaning without a major one.

## A complete third-party example

`examples/extensions-thirdparty` is a separate module with its own `go.mod`: a
budget gate that refuses runs once a service has spent its ceiling, a test
through `extensiontest`, and a `main.go` that installs it. Start from there.
