# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What this project is

AgentGo is a **Go framework for building agents** — a library, not an app. There are no binaries: no CLI, no UI, no servers. Consumers embed `pkg/agent` in their own programs (the reference consumer is superai-desktop).

Module path is `github.com/liliang-cn/agent-go/v3`.

Reason about the repo in this order:

1. `pkg/agent` — framework core (agent, loop, tools, context, hooks/lints, session/checkpoint, events)
2. `pkg/*` capability modules — `providers`, `mcp`, `skills`, `memory`, `rag`, `prompt`, `scheduler`, `sandbox`, `worktree`, `search`
3. Support modules — `pkg/store`, `pkg/config`, `pkg/log`, `pkg/cache`, `pkg/pool`, `pkg/poolsvc`, `pkg/domain`, `pkg/cortexbridge`, `pkg/otelobserver`
4. `eval/` — the behavioral eval harness; `examples/` — runnable library examples

Keep public package APIs intentional and embeddable.

### The seven concepts

v3 deliberately has exactly seven things in it. If a change does not fit one of these, it probably does not belong in `pkg/agent`.

1. **Agent** — a name, a system prompt, a model, a tool set, optional sub-agents.
2. **Loop** — one streaming state machine: assemble context → call the model → execute tools → lint the answer → terminate. `Runtime.loop` is the only implementation.
3. **Tool** — everything the agent can do is a tool: built-ins, MCP, skills, and sub-agents.
4. **Context** — message assembly, task-scoped history filtering, compaction, skill reminders.
5. **Hooks + Lints** — the deterministic layer. Hooks bracket the run and each tool; lints reject a final answer and force a retry.
6. **Session + Checkpoint** — a session UUID owns the conversation; every terminal state writes a replayable checkpoint.
7. **Events** — the single output channel. `Run()` is `RunStream()` plus a collector.

There is **no** team, dispatcher, router, handoff or built-in role hierarchy. Composition happens inside one agent via `WithSubagents(...)`, which exposes a single `task(agent_name, prompt)` tool.

## Development commands

```bash
make test           # go test ./...
make check          # fmt + vet + test
make coverage-core  # focused coverage report for the packages in $CORE_COVERAGE_PKGS
make deps           # go mod download && tidy
make clean          # removes .agentgo/data/*.db (local dev databases)

# Behavioral eval harness — scenarios under eval/scenarios/
make eval           # mock profile, deterministic, CI-safe
make eval-verbose   # same with -v
make eval-live      # AGENTGO_EVAL_LIVE=1 go test ./eval/runner -run TestLiveScenarios
```

### Running tests

```bash
go test ./pkg/agent/...                              # one package tree
go test ./pkg/agent -run TestX -v -count=1           # force re-run, verbose
go test ./pkg/agent -race                            # race detector — useful here, see "Concurrency"
```

### Quick smoke run

```bash
go run ./examples/quickstart      # minimal agent.New(...).Build() + RunStream
```

## Architecture notes that aren't obvious from reading one file

### There is exactly one loop

`Runtime.loop` (`pkg/agent/runtime.go`) is the only execution path in the framework.

- `Service.RunStream` starts it and returns the event channel.
- `Service.Run` calls `RunStream` and collects the events into an `ExecutionResult`. There is no second, non-streaming implementation to keep in sync.
- A sub-agent (`pkg/agent/subagent.go`) builds a **child `Runtime` over the same `Service`** — its own session, a narrower tool surface (`RunConfig.ToolAllowlist` / `ToolDenylist`), a different event sink. It is not a second engine.

When extending runtime behavior, push it into the shared helpers rather than forking a branch. They are grouped by concern, one file each:

| file | owns |
|---|---|
| `runtime.go` | the loop state machine, round advance, terminal states, event emission |
| `loop_context.go` | what the model sees before its turn: memory/RAG retrieval, history filtering, the recent/older split, skill reminders, compaction |
| `tool_prep.go` | what a turn may call: tool collection, allow/deny lists, constraints, skill-first policy, generation options |
| `tool_round.go` | what happens inside one tool round: normalise, dedupe, execute, decide terminal |
| `service.go` | lifecycle and the public entry points (`Run`, `RunStream`, `Ask`, `Chat`, structured output) |

Tool execution has its own state model — `ReadOnly`, `ConcurrencySafe`, `Destructive`, `InterruptBehavior`, plus `queued`/`executing`/`completed` lifecycle. New tools should declare these honestly so batching, permissioning, and cancel work.

### Sub-agents are tools

`WithSubagents(specs...)` registers one tool: `task(agent_name, prompt)`. Calling it runs the named sub-agent through the same loop and returns only its final answer; its events bubble up nested. That is the whole composition story — there is no team, no dispatcher, no router, no handoff.

`agent.Manager` is the application-level host, not an orchestrator: it owns the `Store` (agent definitions, sessions, tasks, checkpoints), caches one `*Service` per named agent, and exposes the task surface. `Manager.SetStreamOverride` is the single dispatch seam if an embedder needs to intercept runs.

### Task is a first-class object

Tasks (`task_id` in `pkg/domain/types.go` and `pkg/agent/types.go`) are propagated through async dispatch and used for history filtering. Treat `task_id` as load-bearing — when adding a new piece of state (history, memory, discovered tools, retries), scope it by task where possible.

### A long task is many runs, not a long run

`Service.RunSegments(ctx, goal, LongRunConfig{...})` (`pkg/agent/long_run.go`) drives one task across many runs. It is **not** a second engine and not orchestration — it calls `Run`, reads why it stopped, and calls `Run` again. The rule that there is exactly one loop is unchanged.

What it does *not* carry across segments is the conversation. Each segment gets a **fresh session id** (one **task id** spans them all, so checkpoints and the plan stay coherent), which is what keeps context length from growing across a task that runs for hours. Continuity comes from what was actually established:

- the plan, injected at run start by `planSummaryForRun` — `PlanItem.Note` is its whole bandwidth, so an agent that records lazily pays to re-walk ground it covered;
- the workspace — same sandbox, same `Service`;
- run memory, recalled per segment like any other run.

Two gates decide a segment *finished the task* rather than merely ending: `StopReason != max_turns`, and (unless `AllowIncompletePlan`) the stored plan has no unchecked steps. `ExecutionResult.StopReason` exists for the first one — `Success` is true both when the model concluded and when the round budget ran out and `forceFinalSynthesis` assembled something from however far it got, and only one of those is an answer.

`LongRunStop` is a separate type from `StopReason` on purpose: "the task finished" and "I stopped asking" are different statements, and only `LongRunStopFinished` (`result.Done()`) means the work is done.

### Long-horizon knobs, and the failure each one answers

| knob | what used to happen without it |
|---|---|
| `AutonomyProfile.MaxRounds` / `WithMaxTurns` | `const maxRounds = 20` sat in the loop and both options were dead; a run stopped at 20 rounds and synthesised an answer that looked like success |
| `WithLLMRetries` | one 502 or rate limit ended the run, with no checkpoint written |
| `AutonomyProfile.CheckpointEveryRounds` | `round_end` snapshots were declared but never written by a live run — the run worth resuming was the only one not on disk |
| `WithPromptCache` | no explicit cache breakpoints at all; on a marker-only provider every round re-paid for the whole history |

Transient-vs-permanent classification lives in `transientLLMError` (`llm_retry.go`). It reads a **provider's error**, which is the output side and therefore fair game — a permanent rejection beats a transient word inside it, so a 400 mentioning "timeout" is still a 400, and `context.Canceled` is never transient.

### Watching a long run, and what a soak found

A run in flight is nearly invisible: its conversation reaches the store only when it ends, its events go to whoever called `RunStream`, and the framework logs nothing of its own accord. `agent.NewActivityLog(w)` is an `Observer` that narrates it — one line per model turn, tool call, sub-agent and checkpoint, flat and greppable because what you do with a long run's log is `grep` and `awk` it. Attach it to anything you cannot watch interactively.

Three bugs found by pointing a real coding task (a RISC-V kernel, then an e-commerce API) at the loop for half an hour. All three are invisible to unit tests and all three only bite an agent that edits its own files for many rounds:

- **A tool call that returns nothing is one the model can only repeat.** `prepareToolRound` used to overwrite `result.ToolCalls` with the deduped list, deleting the repeated call from the transcript. Its result — real or the "already called" note — is addressed to that call's id, so both became orphans and the pairing sanitiser dropped them. The model asked and *nothing came back*: it repeated the same `fs_read` 124 times. Keep every call the model made in the assistant message; every one must have exactly one result.
- **A re-read after a write is a different read.** The duplicate record is per-run and nothing invalidated it, so re-reading a file the agent had just written was answered "its result is unchanged". Any state-changing call in a batch now clears the record.
- **Compaction and the duplicate record deadlock.** At the default `CompactionDefaultThresholdTokens` (8000) a coding agent compacts nearly every round — reading a small workspace once is already ~6k tokens. Compaction deleted the result, the model re-read, the dedupe refused it as a repeat. Compaction now clears the record too, for the same reason a write does.

Two things worth knowing before running one of these yourself: **verify the toolchain by hand first** (a broken build environment burns hours of agent time and tests nothing), and **make every milestone binary** — `go test ./...` exiting 0, a banner appearing under QEMU. A model's own report of its progress is not evidence; on the first kernel soak it was accurate, and it is the one time you would not have known.

`PlanItem.Note` is the whole bandwidth of a segment hand-off, and the soak measured what that costs. Notes naming test functions and files ("Passed AUTH-OK. Tests: TestAuthService in internal/auth/auth_test.go") let the next segment open two files and carry on. Notes naming only files sent it re-reading all thirteen, one per round, spending most of a 20-round segment on rediscovery.

### What the gateway was hiding

A second round of soaks, this time against **DeepSeek direct** rather than through an OpenAI-compatible gateway, found four more. Every one of them had been invisible for the same reason: the gateway did not pass `usage` cache fields through, so the one number that would have exposed them read zero and looked like "not measured".

**Point a long-run measurement at a provider that reports its cache split.** If `ExecutionResult.Usage.CachedPromptTokens` is zero on a run whose history grows every round, find out whether the cache is broken or the reporting is, before concluding anything about the loop.

- **A tool list in map order rewrote the cache prefix every round.** A request serialises system, then tools, then messages, and a prompt cache matches on a prefix — so tool schemas sit inside the bytes each round is trying to reuse. `collectTools` built its slice by ranging a Go map. Measured: a run whose history grew from 5.6k to 10.9k tokens held its hits at *exactly 1024 tokens*, round after round — the head of the system prompt, and not one byte past the first tool. Sorting by name took the same ten rounds from 9.3% to 83.4%, and 92% over fifteen. The same randomness also moved `markPromptCacheBreakpoints`'s prefix mark to a different tool every round, so explicit breakpoints were dead too. **Anything that reaches the prompt prefix must be byte-stable across a run**; `context.go` already carries two comments about this (hour-granularity timestamps, sorted env) — the tool list was the third.
- **A truncated turn was judged as a refusal to answer.** The loop capped responses and nothing read `finish_reason`. On a model that reasons before it writes, that budget goes to reasoning the caller never sees: deepseek-v4-flash returned `finish_reason="length"` with zero characters at 400, 2000 *and* 8000 tokens. The empty draft reached `non_empty_final_answer`, which read it exactly as designed — the model refused — and three rejections later the run was blocked. Truncation is now its own outcome (`token_budget.go`): raise the budget, ask again, bounded at two 4x steps. `defaultRunMaxTokens` is 8192 because 2000 also silently truncated any file write larger than a couple of hundred lines.
- **`DefaultRunConfig` shadowed the budget it was supposed to default.** `MaxTokens: 2000` sat three lines under `MaxTurns: 0` and its comment explaining why that one must be left unset. `r.maxTokens()` reads `cfg.MaxTokens` first, so raising `defaultRunMaxTokens` changed nothing a run could see. **Two defaults for one knob, the nearer one winning silently** — check for this shape whenever you add a resolver.
- **The system prompt named the host's directory, not the sandbox.** `buildSystemContext` used `os.Getwd()` unconditionally. With a sandbox configured, file tools are jailed under its workspace and bash runs there — so the first line of context named a directory the agent's own tools could not reach. A model does what it is told: given `Dir: /somewhere/else`, the agent opens round one with `cd /somewhere/else` and works there, the jail bypassed by a shell builtin. Observed directly — a soak launched from this checkout created its project *inside this checkout*. `cd` still leaves a `LocalSandbox`, which never claimed to isolate; the bug was telling the model to.

And one about money, which is a stop condition here and not just a readout: **`CalculateCost` returned 0 for any model missing from its table.** Silently. `LongRunConfig.MaxTotalCostUSD` is built on that number, so an unlisted model removed the only spending ceiling a multi-hour run has. `pool.RegisterModelPricing` now lets an operator state their own rates (they win over the bundled table, which is an explicitly-fallible fallback), `CalculateCostDetailed` prices the cache split, and "nothing could price this model" is a `known bool` the caller reads instead of a zero it cannot tell from free. The runtime warns once per run when it hits one.

**When you add a retry, add its observer callback in the same commit.** Both re-asks inside a model turn — transient provider error, and budget escalation — happen inside one span, so a turn that took three attempts looked identical to one that took one. `Observer.OnModelRetry` closes that, the way `OnLint` closed the lint layer. A unit test on the escalation function alone stays green whether or not the runtime ever calls it; the loop-level test is what caught the shadowed default above.

### What one real run on the desktop found

The unit suite was green and the Doctor said healthy; the first real long run
driven through superai on the apps VM (a Go CLI plus tests, gemini via a
gateway) still ended `blocked` after 31 rounds and 1.4M tokens, with the work
finished at round 7. Every one of these is a property of a real run, not of a
scenario, so run one before believing a release is fine.

- **A directory is a deliverable.** The goal said "create a directory
  `wordfreq`"; `fileArtifactExistsIn` skipped directories, so
  `task_delivery_contract` rejected the finished answer three times and the
  run died `lint_exhausted`. A non-empty directory now satisfies a file
  deliverable (`dirHasContent`).
- **Never name a lint to the model.** The feedback said `Lint:
  task_delivery_contract`, and the model spent a round on `grep -rn
  task_delivery_contract /` looking for the rule. `FormatLintFeedback` carries
  the reason only; the name is for observers and logs.
- **A plan belongs to its task.** `planSummaryForRun` fell through to the one
  shared `default` scratchpad list, so a fresh session's first turn opened
  with another task's finished plan and "carry on from the first unchecked
  step". An unnamed plan is now keyed `default:<task_id>` on every path
  (`taskScopedPlanKey`); the bare list serves only a run with no task at all.
- **"$0" is not a price.** The bundled table prices OpenAI, Anthropic and
  DeepSeek names; a gateway alias like `gemini-3.7-flash-high` is unpriced, so
  cost read 0 for 1.4M tokens and `MaxTotalCostUSD` could never fire. The
  state is now visible instead of silent: `Event.CostUnpriced`,
  `ExecutionResult.CostUnpriced`, `SegmentInfo.Unpriced`,
  `LongRunResult.CostUnpriced`, and a Doctor check `llm.provider.<name>.pricing`
  that warns with the `RegisterModelPricing` call to make. A host should show
  "unpriced", not `$0.00`.

Two things it confirmed rather than broke: the prompt cache held at ~80% hit
rate across 30 rounds on the gateway, and the trace, checkpoint and segment
events all landed where the panel expected them.

### Watching the process, not only the agent

Every observer before this one reported what the *agent* did. Nothing reported
what the *program* was using, and on a task measured in hours that is the half
that decides whether it finishes: a goroutine leaked per tool call, or a
history nothing ever drops, fails no lint and no test. It fails at 03:00 with
the OOM killer, and every agent metric right up to the last one looks healthy.

`agent.SampleProcess()` returns a `ProcessStats` — live heap, heap objects,
stack, goroutines, GC, current RSS, peak RSS, cumulative CPU, uptime. No cgo
and no dependency: `getrusage` for CPU and peak RSS on unix, `/proc/self/statm`
for current RSS on Linux. **What cannot be read is reported as unknown, not as
zero** (`RSSKnown`, `CPUKnown`) — a zero looks like a process using no memory.

Three ways it reaches you, all the same reading:

- **`ResourceObserver`** — an *optional* interface, deliberately not a
  fourteenth method on `Observer`: hosts outside this repo implement `Observer`
  in full, and adding a method would break every one that does not embed
  `BaseObserver`. The runtime emits one sample per round plus a final one at
  every terminal path, and **samples nothing at all when no observer asks**
  (`ReadMemStats` stops the world briefly, so an unwatched service must not
  pay for it).
- **`TraceWriter`** — one `"event":"resource"` JSONL line per round. This is
  where a long run's memory curve lives; by the time a process is killed the
  curve is the only evidence left.
- **`pkg/otelobserver`** — observable gauges (`agentgo.process.heap.bytes`,
  `.heap.objects`, `.goroutines`, `.rss.bytes`, `.rss.peak_bytes`,
  `.cpu.seconds`). Observable, not pushed, so they keep reporting while the
  service sits idle between runs — which is when a leak is easiest to see and
  when nothing else emits anything.

`ActivityLog` prints a `res` line on the first reading, the last, every tenth
round, and any round where heap or goroutines grew by a quarter — a leak
detector in a log a human greps. `Doctor` reports `process.resources`
informationally: in the CLI the numbers are dull, in a host that has been up
for days they are the first thing worth pasting into a bug report.

### Many callers through one Service

`Service` was always safe to run many tasks through at once; what it had no
notion of was *whose* run a run is. On a server that is the difference between
a product and an incident.

`WithTenant(id)` attaches an opaque owner label. The rules that keep it honest:

- **Nothing in the loop may read it.** It exists for exactly three things:
  admission control, bulk cancellation, and attributing spend. A tenant string
  that changes what an agent does is configuration by string matching — the
  same disease `constraints.go` removed.
- **It is not an identity.** Identity is still the session UUID (memory scopes
  by session, history filters by task). Tenant sits alongside as ownership.

The surface: `WithMaxConcurrentRuns(n)` and `WithMaxRunsPerTenant(n)` (0 =
unlimited, which every service built before these keeps); `Capacity()` for what
the process is carrying; `ActiveRunsForTenant`; `CancelTenant` — the operator's
verb, which previously required a host to keep its own run-to-tenant map and
race every run that started meanwhile. `ActiveRun.Tenant` and
`ExecutionResult.Tenant` carry the label back.

Two decisions worth not relitigating. **Admission is decided under the same
lock that records the run** — a limit checked anywhere else is one two
simultaneous callers both pass, and there is a test that starts 32 runs at once
against a limit of 4. And **it refuses rather than queues**
(`ErrAtCapacity` / `ErrTenantAtCapacity`, with a `CapacityError` carrying the
numbers): a library that blocks its caller for an unbounded time turns a
capacity problem into a latency mystery, and shedding, queueing or answering
503 are the host's decisions to make.

### Time is relative to when it was said, and stored text outlives that

`pkg/timeaware`. A memory written on the 1st saying "明天要去医院" describes the
2nd — but the stored text still says "明天", and a model recalling it on the
2nd with no anchor books the appointment for the 3rd. The sentence is correct
and it is correct about a day that has passed. Same for "next Friday", "月末",
"after the holiday", and every other way of naming a day without naming it.

**There is no phrase table, and there must never be one.** A map of 明天 → +1,
tomorrow → +1 serves exactly the languages someone enumerated and silently does
nothing for everyone else — which reads identically to "this text mentioned no
date". Deciding what a person meant by a day is understanding, so the model
does it. The old `timeExpressionPatterns` regexp in `structured_schedule.go` is
gone for this reason; `MemoryEventMetadata.TimeExpression` is now filled from
the model's answer, alongside `OccursOn`.

The two halves sit on opposite sides of the latency budget:

- **Writing** resolves. It rides on the extraction call the memory writer
  already makes — `timeaware.SchemaFields()` grafts the fields onto that
  schema, `PromptRules(anchor)` onto that prompt, `ReferenceFromFields` parses
  what comes back — so time costs **zero extra model calls**. It happens on the
  durable worker, where nothing is waiting. `Resolver.Resolve` is the
  standalone route (one call, however many texts) for a caller with no
  structured pass to graft onto; it must never be called on an agent's turn.
- **Reading** never calls anything. `timeaware.Note(writtenAt, ref, now)` is
  arithmetic on two timestamps, so every recalled memory carries
  `(written 2026-09-01, yesterday; "明天" = 2026-09-02, today)`.

**Local time is the part most likely to be wrong and least likely to look
wrong.** `Builder.WithTimezone(loc)` sets the person's zone; unset it is the
machine's, which is right for a desktop app and a guess for a server. The
anchor is converted into that zone before the model sees it, the prompt states
the offset *and* the IANA name (an offset cannot express a DST rule), and
`DaysBetween` converts both endpoints before comparing — a memory written at
23:30 in Tokyo is 16:30 the same afternoon in Vienna, and comparing them as
stored puts "today" and "yesterday" a day apart.

Degradation is the design, not an afterthought: no model, a timeout or an
unparsable answer leaves the memory unresolved, and it still carries the moment
it was written, which is true in every language and enough to reckon from.

**Three things only a live check found**, all of them invisible to a suite with
a scripted model:

- **A model does not fill an optional field.** Asked with the time fields
  optional, gemini omitted them entirely. With `time_kind` required and
  `occurs_on` still optional, it answered `time_kind: "point"` and wrote the
  resolved date into the free-text field as prose — `time_text: "明天下午三点
  特别指2026年9月3日下午15:00"` — leaving `occurs_on` empty. Both answers are
  indistinguishable from "this text named no time" downstream. `RequiredFields()`
  now demands both, and the descriptions say the words field holds the input's
  words and nothing else.
- **A twenty-second timeout was too short.** A reasoning model behind a gateway
  took longer on a six-text batch, and every reference came back unresolved.
  `DefaultTimeout` is 60s: this runs in the background, where a slow answer
  costs nothing and a timeout costs the whole resolution.
- **A failed extraction was silent.** `storeIfWorthwhileSync` returned nil for
  both "the model said there is nothing to remember" and "the call failed", so a
  provider outage stored nothing and said nothing for as long as it lasted. It
  warns now.

The proof itself: a real model resolving one batch of six texts in Chinese,
English, Korean, Portuguese and Arabic against a stated anchor, every date
correct and the text with no time in it correctly reporting none; then the read
path rendering `(written 2026-09-02, today; "明天下午三点" = 2026-09-03 15:00,
tomorrow)` with no model call at all.

### Task checkpoint + replay

Every terminal `completeRun` / `blockRun` writes a `TaskCheckpoint` snapshot of the message history to `task_checkpoints` (capped at `MaxCheckpointsPerTask=32`, pruned by `checkpointWriter`). The wiring lives in `pkg/agent/task_checkpoint.go` + `task_checkpoint_manager.go`; `Service.SetCheckpointSink(...)` is what the runtime calls — `Manager.buildServiceForModel` auto-wires this, services built directly via `agent.New(...).Build()` skip persistence.

To re-run a crashed/cancelled task from its latest snapshot: `manager.Tasks().ResumeFromCheckpoint(ctx, taskID, CheckpointResumeOptions{FollowUp: "..."})`.

The `WithResumeMessages([]domain.Message)` `RunOption` is what makes the runtime skip its normal context-prep step and start the loop from a snapshot.

### Output lint registry — the deterministic layer

`pkg/agent/output_lint.go` ships a deterministic post-output-check layer. When the model produces a free-form final answer, the runtime consults `Service.OutputLints()`; on violation it appends structured feedback and re-prompts (bounded by `defaultLintRetryBudget = 2`; exhaustion → `task_blocked`).

Every service built through `agent.New(...).Build()` gets the built-in set automatically — there are no agent-scoped or role-scoped lints in v3:

- `no_planning_only_finish` — final text mustn't read like "Next steps:" / "I will..."
- `file_task_must_write` — a task that asked for a file must have produced one
- `non_empty_final_answer` — a run cannot terminate with no text at all
- `task_delivery_contract` — a goal that names a delivery action (send the mail, post the message, write the file) cannot complete unless a matching tool was actually called *and* such a tool was available

`LintContext` carries `ToolCalls` (what ran) and `AvailableTools` (what could have run), so a lint can tell "the agent skipped a capability it had" from "the agent never had it".

The discipline: when a model keeps making the same mistake, **don't add another sentence to the prompt — write a lint** in `output_lints_builtin.go` / `output_lints_delivery.go` and the runtime will reject + retry deterministically.

### Extensions are bundles over the seams, not a middleware chain

`pkg/agent/extension.go`. An `Extension` is one concern (PII, usage metering,
logging, a budget gate) that implements `Name()` plus whichever optional
capabilities it needs — `Observer`, `OutputLint`, `Module`, `HookProvider`,
`ContextContributor`, `ToolCallFilter`, `ToolResultFilter`, `RunLifecycle`,
`Lifecycle`. `Build()` detects each by type assertion and wires it into the
existing seam (`installExtensions`); nothing new was added to the loop. Order
is registration order at every seam (hook sorting is stable now; extension
hooks sit at priority 1000+i, after user hooks at 100).

Two rules keep this from becoming v2 again. **There is no `next()`**: an
extension cannot wrap the loop, skip a stage or call the model, so the loop
stays one loop. **Context contribution is additive**: an extension appends
system messages and never rewrites the goal, because rewriting is one phrase
table away from deciding behaviour from the user's wording.

Two seams were changed to make this honest. `post_tool_use` now runs through
`EmitWithResult`, so a handler can replace the result the model sees and an
erroring handler fails closed; `HookEventPostExecution` is now actually
emitted, once, on every terminal path (`emitRunEnd`), carrying stop reason,
text and duration. And a bug found by the refusal test: a tool call that
returned an error reached the model as an *empty* result — `Error` rode
alongside `Result` in `ToolExecutionResult` but only `Result` was written into
the tool message. `toolResultForModel` fixes that; an empty tool result is the
one thing a model can only respond to by repeating the call.

Shipped extensions live in `pkg/extensions/{logging,pii,usage}`; a `Service`
runs many tasks concurrently and shares its extensions between them, so an
extension's methods must be safe for that (`extension_concurrency_test.go`).

`pkg/extensions/exec` is the same seams over a pipe: a plugin in any language,
run as a subprocess, speaking newline-delimited JSON (protocol 1, documented in
docs/extensions.md). Two things about it are load-bearing. **The capability
gate is ours** — `installExtensions` detects seams by type assertion and the
one exec type implements all of them, so each method must check the handshake's
declared set and no-op before touching the pipe. **Every failure is the seam's
own fail-closed answer**: a timeout or broken pipe on `after_tool` keeps the
result from the model, on `before_tool` refuses the call, on `lint` rejects, on
`run_start` blocks the run; `context` and `run_end` only log. A transport
failure retires that process rather than reusing it — the reply we gave up on
would otherwise be read as the answer to the next request.

### Hard constraints live in the runtime, not the prompt

A user who refuses tool use is obeyed by *withholding the tools* — `prepareTurnInputsWithConfig` empties the list, including `search_available_tools` — and any tool call the model emits anyway is refused with structured feedback. Forbidding a capability means not offering it, not offering it and then arguing about it.

**Never decide this by matching phrases in the goal.** `constraints.go` resolves each run's constraints once, in `Runtime.loop`, via one temperature-0 structured call with a keyword-free prompt:

```go
type RunConstraints struct {
    ForbidTools  bool
    Deliverables []DeliverableRequirement // kind: email|file|message|other
}
```

`ForbidTools` empties the tool list; `Deliverables` reach `task_delivery_contract` and `file_task_must_write` through `LintContext`. Callers who already know skip the call entirely with `WithToolsDisabled()` / `WithRequiredDeliverables(...)`; `WithConstraintExtraction(false)` turns the pass off. Extraction failure degrades to "no constraints" and warns — it must never block an ordinary run.

This replaced four hardcoded phrase tables (`noToolInstructionPhrases`, delivery `GoalMarkers`, `fileOutputIntentPatterns`, the auto-memory hook). They are the same disease as the deleted `isExplicitMemoryRecallQuery`: a list only enforces the languages someone thought to enumerate, and silently does nothing for everyone else. If you find yourself adding a phrase to a list to change runtime behavior, that is the signal to extract a constraint instead.

**The input side is now clear: no hardcoded phrase or regexp table anywhere in the framework reads the user's request and changes behavior.** (Tokenizing a query to *rank* something — `service_tools.go` tool search, `pkg/search` BM25 — is relevance, not a verdict, and is fine. Matching the *model's* output — `planningEndingPatterns`, `looksLikeRefusalText` — is the output side, which is where deterministic checks belong.) Seven tables were removed — the four above, plus `pkg/memory`'s `QueryClassifier` (retrieval gate), `shouldSkipAutoStoreForTaskGoal` and friends (auto-store gate), and `FilterMemoriesForQuery`/`detectRecallFilterMode` (post-retrieval schedule filter). Every remaining regexp in `pkg/memory` reads *stored memory content* — noise filtering in `filter.go`, event extraction in `structured_schedule.go` — which is a different thing: content the model wrote, not wording the user chose. Adding a new list that inspects a goal, a query or a prompt is a regression, not a feature; the two supported ways to change behavior from the request are a structured constraint extraction and an explicit option.

Resolve constraints in the **loop**, never in a helper that only sees a goal string — that mistake is why the gate fired for `Run` but not `Ask`.

### Cancellation is a registry, and it is an outcome

Two separate registries, because the two callers hold different things.

- **A run.** `startRun` derives a cancellable context per run and records it in
  `Service.runs` (`pkg/agent/run_cancel.go`). `Cancel()` stops **every** run in
  flight on the service; `CancelRun(runID)` stops one (name it with
  `WithRunID`); `CancelSession(sessionID)` stops one conversation;
  `ActiveRuns()` lists them. Registration is released when the event stream
  closes, so a stale ID can never answer "ok" to a stop. All three defer — and
  return false — while a tool with `InterruptBehavior: block` is mid-execution.
  Sub-agents are not registered: they run under the parent's context.
- **A scheduled execution.** `pkg/scheduler` gives every execution its own
  context (`beginRun`) instead of handing out the scheduler root, so
  `CancelRun(runID)` / `CancelTaskRuns(taskID)` stop one run without touching
  the timers. `Stop()` is unchanged: it still tears everything down.
  `RunTaskAsync` / `PromptScheduler.RunNowAsync` exist because `RunNow` blocks
  for the whole run — a host stuck inside it cannot draw the cancel button.

A stop is **not** an error. The runtime terminates with `EventTypeCancelled`
(`workflow_cancelled`) + `StopReasonCancelled`, writes a
`CheckpointReasonTaskCancelled` snapshot so `ResumeFromCheckpoint` can pick the
work back up, and persists the task as `cancelled`. `ExecutionResult.Cancelled`
is true with `Err() == nil`, exactly like `Blocked` — a caller branching on err
must never mistake its own stop button for a crash.

### A Service owns its store; everything else only borrows the Service

`Service` opens exactly one long-lived resource — the `Store`, and through it
the `*sql.DB` behind `agentgo.db` — so `Service.Close()` is the only thing
allowed to release it. A `PromptExecutor` on a timer, a `Manager` cache entry, a
host's window: all borrowers. A borrower never closes, and must not keep running
turns through a Service that has been closed.

That second half is enforced now (`pkg/agent/service_close.go`):

- `Close()` is idempotent, marks the service closed *first*, cancels the runs
  still in flight (bounded by `closeDrainTimeout`), then releases memory + store.
- `startRun` — the single entry point into the loop — refuses a closed service
  with `ErrServiceClosed`, so `Run` / `RunStream` / `Ask` / `Chat` / structured
  output / the prompt scheduler all fail the same way. `Service.Closed()` lets a
  host ask first.
- `Manager.getOrBuildService` drops a cached service somebody closed and rebuilds.

Why: a host that rebuilt its agent and left a `PromptScheduler` pointed at the
old one kept firing schedules against a closed store. The model answered, the
answer reached the UI, the run reported success — and every write of the
conversation failed with `sql: database is closed`, logged as a warning and
dropped. **A failed history write is now an ERROR log plus an `EventTypeError`
event marked `history_persist_failed`**, not a warning nobody reads.

Same rule one layer down: `scheduler.Storage` mirrors into a canonical
`AgentGoDB` it *borrows*; `TaskScheduler.Stop` closes the handle
`TaskScheduler.start` opened. A borrower closing its lender's handle is the same
bug in miniature.

### Eval harness

`eval/runner/` is a behavioral eval driver: every YAML in `eval/scenarios/` defines an `input`, an entry agent, optional lint registrations, expected `status`/`final_text_match` constraints, and optional `lint_violations` counts. Two profiles:

- **mock** (default, CI-safe): `MockLLM` plays back a scripted `llm_replies` sequence — deterministic. `make eval`.
- **live**: scenarios with `mode: live` run against the configured provider pool via `eval/runner/live.go` (`BuildPoolLiveBuilder`). `make eval-live` (gated on `AGENTGO_EVAL_LIVE=1`); results are saved as timestamped JSON in `eval/results/` (gitignored).

When a harness change (lint, prompt cut, tool-prep tweak) lands, run `make eval-live` and diff the result JSON against the previous run — that's the harness-engineering loop.

### Skill surfacing is reminder-based

Skills aren't all dumped into the prompt. The runtime surfaces a small relevant subset via `<skill-discovery>` reminders and activates matching `skill_*` tools per turn. When adding a skill, fill in `when_to_use` and `paths` in the SKILL.md frontmatter — that's what `ResolveForModel(...)` ranks on.

### Memory backends are pluggable — don't grow the switch

`buildMemoryService` (`pkg/agent/builder.go`) resolves `store_type` through a
switch of the four built-ins and then falls back to a registry. **A new backend
is a registration, not a new `case`.** The seams, in priority order:

1. `agent.RegisterMemoryStore(name, factory)` — `pkg/agent/memory_registry.go`,
   backed by `pkg/domain/memory_store_plugin.go`. The registry lives in
   `pkg/domain` because it is the only package both `pkg/config` (which must
   accept a plugin name in `MemoryStoreType.Valid()`) and `pkg/store` (which
   self-registers `cortex-remote` in `init()`) can import — `pkg/config` imports
   `pkg/store`, and `pkg/memory` imports `pkg/store`, so neither could host it.
   The factory receives `domain.MemoryStoreConfig{Name, Path, DSN, Options,
   Embedder, Generator}`; `WithMemoryDSN` / `WithMemoryOption(s)` and
   agentgo.toml's `[memory] dsn` / `[memory.options]` feed it.
   Registration is strict: blank name, nil factory, built-in name, or duplicate
   is an error. `UnregisterMemoryStore` replaces one deliberately.
2. `WithMemoryStore(domain.MemoryStore)` — inject an instance; wins over
   `store_type` entirely.
3. `memory.BaseStore` (= `domain.UnsupportedMemoryStore`) — embeddable base
   whose eighteen methods all return `domain.ErrMemoryStoreUnsupported`.
4. `Builder.WithMemoryService(domain.MemoryService)` — escape hatch; nothing in
   `buildMemoryService` runs.

Every resolved store funnels through `Builder.assembleMemoryService`, so
built-in, registered and injected backends get identical retrieval/injection
behaviour. Keep it that way.

Built-in `store_type` values: `file`, `cortex`, `memoryflow`, `graphflow`.

Shipped plugin 1: `cortex-remote` (`pkg/store/memory_cortex_remote.go`) — a
shared CortexDB over gRPC, the "shared brain". It covers Store/StoreWithScope/
Get/Update/Delete/SearchByText/List; vector `Search`/`SearchByScope`/
`SearchBySession` **degrade to empty** because the remote surface takes a query
string, not a vector (the server owns the embedder), and the memory service then
falls through to `SearchByText`; `IncrementAccess`, `GetByType`, `Clear`,
`DeleteBySession`, `ConfigureBank`, `Reflect`, `AddMentalModel` return
`ErrMemoryStoreUnsupported`. Endpoint and token come from the DSN/options or
`$CORTEXDB_REMOTE` / `$CORTEXDB_GRPC_TOKEN` — never from code.

Shipped plugin 2: `mcp-memory` (`pkg/store/memory_mcp.go`) — **any** MCP server
with memory tools, which is why nothing about it is product-shaped.

- **The mapping is the integration.** `MemoryStoreConfig.Options` names every
  tool (`tool.store` … `tool.list`), every request argument
  (`arg.<op>.<canonical> = <remote param>`, `"-"` suppresses one), every static
  extra (`const.<op>.<param>`), every response path (`result.search.items` /
  `.hit` / `.score`, `result.get.item`, `result.list.items`, `result.store.id` —
  dot paths, `""` = the root) and every record field
  (`field.<canonical> = <remote field>`). Only six identity defaults exist
  (`store.content`, `search.query`, `get.id`, `update.id`, `update.content`,
  `delete.id`) — every optional argument is opt-in, because sending a parameter
  a server never declared is how a generic client stops being generic. **There
  is no synonym table and there must never be one**: `content` vs `text` is a
  configuration fact, not something to infer from a tool name. `profile =
  "cortexdb"` (`memory_mcp_profiles.go`) expands to a full preset and explicit
  options still win — a profile is *named* in config, never detected.
  `store.RegisterMCPMemoryProfile` registers more.
- **Its own connection.** The store dials its own `pkg/mcp` client lazily on
  first use, so it neither depends on assembly order nor blocks agent
  construction when the server is down. `Close()` tears it down;
  `memory.Service.Close()` does not cascade, so hold the store if you want
  deterministic cleanup. A server also listed in `mcpServers.json` will have two
  sessions in the process — dedupe on the host side (superai's
  `backend/mcp_memory_route.go` already does).
- **Honest coverage.** Store/StoreWithScope/Get/Update/Delete/SearchByText/List,
  each only when its `tool.<op>` is configured; `InitSchema` is a no-op; vector
  `Search`/`SearchBySession`/`SearchByScope` degrade to empty exactly like
  cortex-remote; the other seven return `ErrMemoryStoreUnsupported`.
- **Scope is the trap.** Session/scope are taken *only* from our own metadata
  blob (`metadata_key`, default `agentgo`, the same blob cortex-remote writes,
  so both backends share a brain) or from an explicitly mapped
  `field.session`/`field.scope_type`/`field.scope_id`. A foreign record comes
  back scope-less → global → visible to every chain. Mapping a server's own
  bucket name into `SessionID` is exactly the `d0eb578` bug: the store looks
  healthy, `SearchByText` returns rows, and `RetrieveAndInject` injects nothing
  because the scope filter drops them all. `TestMCPMemoryForeignRecordsStayGlobal`
  fails with "0 memories" the moment someone maps it.

The discipline: **an honest `ErrMemoryStoreUnsupported` beats a fake
implementation.** If a backend cannot do something, say so and let the caller
degrade. And when you add a memory backend, test it through
`memory.Service.RetrieveAndInject` with `embedder = nil`, asserting on the
injected *text* — a store-level Store/Search round trip passes while the agent
sees nothing.

### Memory ≠ cache ≠ RAG

- `pkg/memory` — durable per-conversation/per-task memory, with file-backed `MEMORY.md` and `_session/*.md` writers in `pkg/store/file_memory.go`. Background durable writer.

  Both sides of memory are unconditional, and both have exactly one switch. Retrieval runs on every turn unless `WithMemoryRetrieval(false)`. Auto-store runs on every turn unless `WithMemoryAutoStore(false)`: `storeIfWorthwhileSync` asks the model once (`should_store` + extracted items) and that verdict is final — there is no pre-filter on the goal's wording and no keyword fallback that overrides a "no". Neither side is ever decided by inspecting what the user typed; if you are tempted to skip a call because a request "looks like" a question, the answer is the explicit option, not a prefix list.

  Nothing narrows the retrieved set after ranking either. `FilterMemoriesForQuery` used to sit between the scorer and the `MaxMemories` cap and drop ranked memories whenever the query contained `安排`/`计划`/`plan`/`agenda` — it returned *nothing* for `帮我做一个学习计划`, and `…plan for the api service` took its "personal schedule" branch because `api ` contains `i `. It is gone. `pkg/memory/structured_schedule.go` keeps only the content-side half: it reads stored memory text to derive event metadata and to apply later corrections to earlier events, and it never sees the query.
- `pkg/cache` — ephemeral in-process caches.
- `pkg/rag` — optional document retrieval. **Only active when an embedding model is configured.** A bare AgentGo install (no embeddings) still has Agent + MCP + Memory working — don't gate basic features on RAG availability.

### Storage layout

```
~/.agentgo/                      # override with AGENTGO_HOME=...
├── data/
│   ├── agentgo.db               # config, providers, agents, tasks, checkpoints (SQLite)
│   └── cortex.db                # optional memory/vector/graph (cortexdb)
├── memories/                    # file memory when enabled
├── skills/                      # local skills (SKILL.md format)
└── workspace/                   # agent working directory
```

`agentgo.toml` at repo root is the dev config; `home = '/Users/.../.agentgo'` redirects all of the above.

## Conventions that bite if you don't know them

- **Identity = session UUID, not userID.** Conversations are keyed by UUID. Don't introduce `userID` as a primary identity field for chat or task APIs. Use `github.com/google/uuid`.
- **Concurrency.** Run `go test -race ./pkg/agent/...` for changes touching `pkg/agent/runtime.go`, `pkg/agent/manager.go`, `pkg/agent/subagent*.go`, `pkg/agent/async_tasks*`, or `pkg/agent/store.go`.
- **Provider compatibility fallbacks.** `pkg/pool/client.go` and `pkg/providers/openai.go` both have `applyRetryFallbacks` helpers that strip `web_search_options` or `tool_choice` and retry once when the upstream rejects them with "unsupported / does not support / invalid" errors. DeepSeek's reasoner (e.g. `deepseek-v4-flash`) needs the `tool_choice` fallback. When adding new optional params, mirror the same shape: detect the rejection, strip, retry once.
- **Native web search is detected, never assumed.** Web-search mode defaults to `auto`: tool rounds carry `web_search_options`/`enable_search`, and `pkg/pool/native_search.go` accumulates evidence per provider — a rejection proves *unsupported* (stop sending, keep MCP search tools), grounding evidence in a response (`url_citation` annotations, grounding metadata) proves *supported* (hide the redundant MCP search tools). Mere acceptance proves nothing: most servers silently ignore unknown fields, and treating acceptance as capability would hide real search behind a fake one. There is no model-name capability table and there must never be one — the verdict comes from `domain.NativeWebSearchReporter`, and explicit `native`/`mcp`/`off` config always wins.
- **`tool_choice` JSON shape.** `"auto" / "required" / "none"` go in as plain strings; named-tool choice is `{"type":"function","function":{"name":"X"}}`. Don't reuse the named-tool form for `"required"` — DeepSeek and some OpenAI variants reject that.
- **Use random high ports** (3000+, e.g. 3076, 6759, 43510) for any new dev port; avoid 8080 and other common defaults.
- **Releases:** the `/release` slash command in `.claude/commands/release.md` does the version bump. Manual: `git tag -a vX.Y.Z`, then `git push --tags`. Bump rules: `feat:` → minor, `fix:`/`docs:`/`chore:` → patch, `BREAKING CHANGE:` → major. **No co-author lines in commits.**
- **No summary docs** unless explicitly requested. Don't create `*_SUMMARY.md` / `NOTES.md` / `IMPLEMENTATION.md` after finishing work.
- **Examples:** new public features should ship with a runnable example under `examples/<feature>/main.go` (each in its own folder, full imports + cleanup).

## Debugging entry points

- Behavioral eval: `make eval` (mock) / `make eval-live` (real provider)
- Provider connectivity / compat: `examples/` + `pkg/poolsvc` (`poolsvc.Global().Initialize(...)`); provider fallback behavior lives in `pkg/pool/client.go`
- MCP: `pkg/mcp` client logs land in `~/.agentgo/logs/`
- Tasks / checkpoints: `Manager.Tasks()` API — list, trace, `ResumeFromCheckpoint`
- Tracing: wire an observer (`pkg/otelobserver`) into the service to see per-round loop state
