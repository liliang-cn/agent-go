# Getting started: embedding AgentGo in a real host

The README's quick start is one model and one question. A host usually wants
more: memory that survives the session, a knowledge graph, web search, plans
that outlive the process. Each of those has one right way to wire it and at
least one wrong way that looks fine until a real run. This page is the right
ways, in the order they go.

The runnable version of everything here is `examples/integration`:

```bash
LLM_BASE_URL=https://api.deepseek.com/v1 LLM_API_KEY=$DEEPSEEK_API_KEY \
LLM_MODEL=deepseek-v4-flash go run ./examples/integration
```

It remembers a fact, replaces it when it changes, recalls it in a fresh
session, and records and traverses a dependency graph in CortexDB.

## 1. Give the service a home

Everything AgentGo persists — `agentgo.db` (sessions, checkpoints, plans, tool
reliability) and file memory — lives under `AGENTGO_HOME`, default
`~/.agentgo`. Set it for anything that is not your main install: tests,
examples, a second app on the same machine. Two apps sharing one home share
one database.

## 2. The model

Any OpenAI-compatible endpoint:

```go
llm, _ := pool.NewClient("main", baseURL, apiKey, "deepseek-v4-flash")
svc, _ := agent.New("assistant").WithLLM(llm).Build()
defer svc.Close()
```

`pool.NewClient` is what `WithConfig` installs use underneath, and the one to
prefer: it carries the native-search verdict, cache metering and the provider
fallbacks. Without `WithLLM`, providers come from `agentgo.db` in the home.

Reasoning models (deepseek-v4-flash, qwen with thinking) spend part of every
token budget on reasoning you never see. The framework raises a budget that ran
out before any text was written — for turns and for structured calls — so you
should not need to size one yourself. The framework reports tokens
(`ExecutionResult.Usage`, `LongRunResult.TotalUsage`), never money: if you want
a cost figure, multiply those by your provider's rates.

## 3. Web search a provider has built in

Say so; it is never guessed from a model name:

```go
llm.SetNativeWebSearch(domain.NativeWebSearchDashScope,
	map[string]interface{}{"forced_search": true})
```

or `native_web_search = "dashscope"` on a configured provider. Values: `none`,
`openai`, `dashscope`, `google_search`. The options go inside the provider's
own field; for DashScope `forced_search` is the difference between a model
that searches and one that is allowed to and does not.

A declaration keeps any MCP search tools as a fallback — declared says the
field works, not that the model uses it. Only a response that shows the model
searching hides them. Undeclared, the provider is probed on every tool round
until a response settles it.

## 4. Memory

```go
agent.New("assistant").WithLLM(llm).
	WithMemory(agent.WithMemoryStoreType("file")).
	Build()
```

`file` needs no embedder and no server. Memory is retrieved and stored on every
turn; each side has one switch (`WithMemoryRetrieval(false)`,
`WithMemoryAutoStore(false)`). Saves — automatic and the agent's own
`memory_save` — are reconciled against what is already known: a fact that
changed replaces the old one instead of sitting beside it.

For a memory shared between machines use `cortex-remote`; for another backend,
see "Builder options" in the README. A backend you write should pass
`pkg/memory/memorystoretest`.

Whose memory is it? For a single-user service, once:
`svc.SetMemoryScope("assistant", "", "owner")`. For a service serving several
people, per run: `svc.Run(ctx, goal, agent.WithMemoryUser(userID))` — the
session remembers it, so later runs in that session need not repeat it.

## 5. CortexDB as a knowledge graph

```go
cortex, _ := cortexdb.Open(cortexdb.DefaultConfig(filepath.Join(home, "cortex.db")))
defer cortex.Close()
cortexbridge.Register(svc, cortex)
```

Register after `Build`, because it reads what the service already has:

- **CortexDB's own `memory_*` tools are left out** when the service has memory.
  Beside agent-go's they were a second, same-named path that skipped
  reconciliation and wrote to the local file even with a shared brain
  configured. `WithCortexMemoryTools()` puts them back (with
  `WithNamePrefix` to keep both); a service without memory gets them anyway.
- **A tool the service already has is never replaced.**
- **`user_id` is filled from the run's memory user** (`WithMemoryUser`, else
  `SetMemoryScope`) when a call omits it, so two people on one service stay
  apart. `WithArgDefaults` sets a fixed one instead.

`WithAllow` / `WithDeny` narrow the surface; 60-odd tools is a lot of schema on
every turn, and most hosts want the graph and search tools only.

## 6. Plans

Nothing to do: `Build` stores plans in the service's own database, so a task
interrupted at hour nine resumes with the steps it reached. A plan store you
write (`WithPlanStore`) should pass `pkg/agent/planstoretest` — a store that
keeps text and done flags but drops step IDs and dependencies looks healthy and
resumes a flat checklist.

## 7. What to watch

- `agent.NewActivityLog(os.Stderr)` as an observer: one greppable line per
  model turn, tool call and checkpoint.
- `svc.StatusSnapshot()`: what every run in flight is doing, without being its
  caller.
- `agent.Doctor(ctx, ...)`: providers configured, context windows known, store
  writable.

Logs you will see and can ignore: "constraint extraction timed out" means the
pre-run classification took longer than its 20s bound on a long prompt, and the
run went ahead without it. It becomes a problem only if it happens on most
runs — then pass what you already know (`WithRequiredDeliverables`,
`WithToolsDisabled`) or turn it off with `WithConstraintExtraction(false)`.
