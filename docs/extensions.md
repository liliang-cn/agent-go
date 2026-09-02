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

## Out-of-process plugins

None of the seams above is Go-specific: each one is a question with a small
answer. `pkg/extensions/exec` asks them over a pipe, so a plugin can be
written in any language.

```go
svc, err := agent.New("assistant").
	WithExtensions(exec.New("redact", []string{"python3", "plugins/redact.py"})).
	Build()
```

`exec.New(name, command, opts...)` returns an ordinary `agent.Extension`. The
process starts at `Build()` and is asked to leave at `Close()`. Options:
`WithTimeout` (per request, default 5s), `WithShutdownGrace` (default 2s),
`WithConcurrency` (processes, default 1), `WithEnv`, `WithDir`, `WithLogger`.

### Framing

Newline-delimited JSON. One request object per line on the plugin's **stdin**,
one reply object per line on its **stdout**. Every request carries an `id`
that increases by one per process; every reply echoes the `id` it answers.
Requests to one process are serialised — the framework never has two in flight
on the same pipe — so a plugin can be a plain read-handle-write loop with no
concurrency of its own.

**stdout is the protocol.** Anything else printed there is an undecodable
reply. Diagnostics go to **stderr**, which is forwarded line by line to the
framework's logger, tagged with the plugin's name.

### Handshake

The first request is always:

```json
{"id":1,"type":"hello","protocol":1,"name":"redact"}
```

The plugin answers with the version it speaks and the capabilities it
implements:

```json
{"id":1,"type":"hello","protocol":1,"capabilities":["after_tool","lint"]}
```

Capabilities are exactly `"context"`, `"before_tool"`, `"after_tool"`,
`"lint"`, `"run_start"`, `"run_end"`. A mismatched `protocol` or an unknown
capability name fails the build — loudly, because a typo that silently
disables a seam looks installed and does nothing. Only declared capabilities
are ever sent.

### Requests

Every request has `id` and `type`. The payload sits under a field named for
its shape, and mirrors the Go type of the seam it serves in snake_case.

| type | payload | fields |
| --- | --- | --- |
| `context` | `context` | `goal`, `session_id`, `agent_id` |
| `before_tool` | `call` | `name`, `args` (object), `session_id`, `agent_id` |
| `after_tool` | `result` | `name`, `args`, `result` (any JSON), `error` (string, empty when the tool succeeded), `session_id`, `agent_id` |
| `lint` | `lint` | `text`, `agent_name`, `task_id`, `session_id`, `turn_index`, `goal`, `tool_calls` (array of names), `available_tools`, `deliverables`, `requested_actions`, `workspace`, `is_retry`, `retry_count` |
| `run_start` | `run` | `goal`, `session_id`, `agent_id`, `task_id` |
| `run_end` | `run` + `outcome` | outcome: `stop_reason`, `text`, `blocked`, `cancelled`, `duration_ms` |
| `shutdown` | — | sent once at `Close()`; no reply is read |

### Replies

| for | reply | meaning |
| --- | --- | --- |
| `context` | `{"messages":[{"role":"system","content":"..."}]}` | messages appended to the run's context; `role` defaults to `system`, empty content is dropped |
| `before_tool` | `{"args":{...}\|null,"block":""}` | `args` non-null replaces the call's arguments; `block` non-empty refuses the call and the model reads the reason as the tool's error |
| `after_tool` | `{"result":<any JSON>,"replaced":true}` | `replaced` false leaves the result alone; true substitutes `result` |
| `lint` | `{"ok":true}` / `{"ok":false,"reason":"..."}` | `ok` false rejects the answer and re-prompts with `reason`. `ok` is absent-means-false: a reply that omits it rejects |
| `run_start` | `{}` | anything but an error lets the run proceed |
| `run_end` | `{}` | acknowledgement only; the outcome already happened |

A reply with a non-empty `"error"` is an error to the framework whatever the
type was — that is how a plugin blocks a run from `run_start`, or reports that
it could not do its job. It is a verdict, not a crash: the process stays.

### Failure is not optional

A plugin is a process, and a process can hang or die. A request that times out
or hits a broken pipe produces the same outcome the seam's own contract
already gives:

| seam | failure |
| --- | --- |
| `after_tool` | the model does not see the result — a filter that could not inspect a result must not let it through |
| `before_tool` | the call is refused |
| `lint` | the answer is rejected; exhausting the lint budget blocks the run |
| `context` | nothing is contributed, and it is logged |
| `run_start` | the run is blocked before its first model turn |
| `run_end` | logged only |

A transport failure retires that process rather than reusing it: the reply
that was given up on may still arrive and would be read as the answer to the
next request. There is no restart — a retired process answers every later
request instantly with its failure, still closed.

### Concurrency

One process serves one request at a time. A `Service` runs many tasks at once,
so a plugin that takes 200ms per call makes every concurrent run queue behind
every other. `WithConcurrency(n)` runs n identical processes and hands each
request the first free one; they must declare the same capabilities. Anything
a plugin remembers between requests is then per-process, which is a reason to
keep plugins stateless.

### One caveat about the Go side

`Build()` detects capabilities by type assertion, and the one Go type behind
every exec plugin implements all of them — so the framework calls every seam
of every plugin, whatever that plugin is for. The gate is therefore in this
package: each method checks the declared capability set and returns
"unchanged" without touching the pipe. The cost is a map lookup per seam per
turn on a plugin that declared nothing there.

`examples/extensions-exec` is a complete reference: a Python plugin
(stdlib only) that masks email addresses in tool results and refuses an answer
that quotes one, plus a `main.go` that installs it.

## Stability

The types named on this page — `Extension`, the nine capability interfaces,
`HookSpec`, `ContextInput`, `ToolCallInfo`, `ToolVerdict`, `ToolResultInfo`,
`RunInfo`, `RunOutcome`, `ModelInfo`, `ModelResult`, `LintContext`, and the
`extensiontest` package — are the extension API. They follow the module's
semantic version: fields may be added in a minor release, nothing is removed
or changed in meaning without a major one.

The out-of-process protocol is versioned separately and stated in the
handshake. Version 1 follows the same rule: fields may be added, and a plugin
that speaks it keeps working until a version 2 says otherwise.

## A complete third-party example

`examples/extensions-thirdparty` is a separate module with its own `go.mod`: a
budget gate that refuses runs once a service has spent its ceiling, a test
through `extensiontest`, and a `main.go` that installs it. Start from there.
