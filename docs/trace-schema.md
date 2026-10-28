# Traces and logs

What a run did, why, and where its time and tokens went — on the record,
without reconstructing it by hand. This page is the schema of the JSONL trace
`agent.TraceWriter` writes, the flow ids that join runs across sub-agents,
resumes and processes, the log levels, and the switches that turn it all up on
a deployed binary. `pkg/agent/trace_schema_test.go` fails when a field of the
trace is missing from this page.

## Switches

| Variable | What it does |
| --- | --- |
| `AGENTGO_LOG_LEVEL` | `error`, `warn` (default), `info`, `debug`, `payload` — the framework logger's level |
| `AGENTGO_LOG_FORMAT` | `text` (default) or `json` |
| `AGENTGO_LOG_FILE` | append log lines to this file instead of stderr |
| `AGENTGO_TRACE_EVENTS` | append the JSONL trace below, for every run of every Service in the process, to this file |
| `AGENTGO_TRACE_BODIES=1` | payload mode: model requests and responses on `model_end`, tool arguments and results whole, server tool result URLs; also turns on bodies in the `pkg/trace` record file |
| `AGENTGO_TRACE=1` | `pkg/trace`'s one-line-per-step timeline on stderr (`agentgo-trace run=… model purpose=turn …`) |
| `AGENTGO_TRACE_FILE` | `pkg/trace`'s timeline records (`kind`/`name`/`dur_ms`) as JSONL — a different, older schema than this page's; `agentgo-trace` skips its lines |

In code: `agent.New(...).WithLogger(l)` or `svc.SetLogger(l)` for the logger,
`agent.NewTraceWriter(w)` as an observer for the trace (`tw.Payloads = true`
for payload mode), `WithObserverPayloads()` for requests and responses.

## Flows

A **flow** is one thing a person asked for and everything it fanned out into:
the run, its sub-agents and theirs, tool calls, model calls, long-run
segments, a resume after a restart, and work handed to another process.

Every run is a span of a [W3C Trace Context](https://www.w3.org/TR/trace-context/)
trace:

- `trace_id` — 32 hex digits, shared by every span of the flow;
- `span_id` — 16 hex digits, the span a line is about;
- `parent_span_id` — the span that started it; absent on a flow's root.

| Span | Lines | Parent |
| --- | --- | --- |
| run | `run_start`, `run_end` (and the run's events) | the span on the context the run started from: a tool call (sub-agent), a segment's task span (long run), the run that wrote a checkpoint (resume), a remote caller's span (traceparent) |
| model call | `model_start`, `model_end` | the run (or the tool call it was made inside) |
| tool call | `tool_start`, `tool_end` | the run; it is the parent of whatever the tool starts |
| long task | `segment_start`, `segment_end` | the caller's span, or the flow of the task's last checkpoint |

Event lines — `decision`, `lint`, `checkpoint`, `compaction`, `error`,
`resource`, `permission`, `model_retry`, `model_wire`, `subagent_*` — carry the
span they are about (their run, or the model call a transport decision was
made in) and its parent.

### Propagation

- **In:** `trace.WithTraceparent(ctx, header)`, `trace.ExtractHTTP(ctx, r.Header)`,
  or `trace.Extract(ctx, get)` for any string-keyed carrier (gRPC metadata).
  Every run started from that context is a child of the span it names.
  `tracestate` is kept and passed on unchanged.
- **Out:** `trace.InjectHTTP(ctx, h)` / `trace.Inject(ctx, set)` write
  `traceparent` (and `tracestate`) for the span on ctx; inside a tool, that is
  the tool call's span. `trace.HTTPTransport(base)` does it for every request
  of an `http.Client`, and notes a `traceparent` the server answers with.
- **Read:** `trace.SpanFromContext(ctx)`, `trace.TraceIDFromContext(ctx)`,
  `trace.TraceparentFromContext(ctx)`.
- **Model requests** carry `traceparent` naming the model call's span.
- **MCP tool calls** carry it in `params._meta.traceparent` (and
  `_meta.tracestate`) on every transport, and as the HTTP header on
  HTTP/SSE transports. A server that answers with its own `traceparent` — a
  response header, or `result._meta.traceparent` — is recorded on the tool
  call's `tool_end` as `remote`, so the tree continues into it.
- **Checkpoints** store the run's traceparent (`task_checkpoints.traceparent`);
  `ResumeFromCheckpoint` and `RunSegments` on a task that has checkpoints join
  that flow when the context has none of its own.
- **OpenTelemetry:** `pkg/otelobserver` spans use the same ids when the
  provider has `sdktrace.WithIDGenerator(otelobserver.IDGenerator())`
  (`otelobserver.Phoenix` installs it); without it the root is still parented
  under the run's remote parent and every span has `agentgo.trace_id` /
  `agentgo.span_id`.

`x-request-id`-style per-call ids are not the flow id and are left alone.

### Reading a flow

```
agentgo-trace run.jsonl                 # one run: waterfall + totals; several: a list
agentgo-trace run.jsonl --run <id>      # one run (run id or span id prefix)
agentgo-trace a.jsonl logs/ --traces    # the flows in these files
agentgo-trace a.jsonl logs/ --trace <id># the whole flow as one tree, across files
```

A run whose parent is not in the files given is printed under
`(parent <id> not in these files)`, not dropped.

## Lines

Every line is one JSON object with `ts` (RFC 3339, nanoseconds), `event`, and
`elapsed_ms` (since the TraceWriter was made). All other fields are omitted
when empty.

### Correlation, on any line

| Field | Meaning |
| --- | --- |
| `trace_id`, `span_id`, `parent_span_id` | the flow ids above |
| `task_id` | the task |
| `run_id` | the id the run is registered under (`CancelRun`); a sub-agent's run has none |
| `session_id` | the conversation |
| `agent` | the agent's name |
| `round` | the round in progress when the thing *started*; absent (0) before round 1 |
| `call_id` | a tool call's id, as the model gave it |
| `parent_task_id` | a sub-agent's parent task |

### `run_start` / `run_end`

| Field | Meaning |
| --- | --- |
| `model` | the model the run was sent to |
| `goal` | what it was asked (clipped) |
| `host`, `pid` | where it ran |
| `outcome` | `run_end`: `completed`, `blocked`, `cancelled`, `failed` |
| `stop_reason` | `run_end`: the StopReason (`end_turn`, `max_turns`, `lint_exhausted`, …) |
| `duration_ms` | `run_end`: wall time |
| `rounds` | `run_end`: rounds run |
| `error` | `run_end`: the blocker or failure |
| `summary` | `run_end`: the run's totals, below |

`summary` is `trace.Summary`, added up by `trace.Tally` — the same arithmetic as
the in-memory timeline (`trace.Timeline`, the `trace` event):
`total_ms`, `first_model_call_ms`, `first_answer_token_ms`, `model_ms`,
`model_calls`, `memory_ms`, `tool_ms`, `tool_calls`, `tool_failures`,
`tokens {prompt, completion, cached, cache_write}`,
`by_purpose {<purpose>: {calls, ms, tokens}}`,
`tools {<name>: {calls, failures, ms}}`, `lint_retries`, `lint_blocks`,
`compactions`. Model and tool time can overlap, so parts may add up to more
than the whole.

### `model_start` / `model_end`

| Field | Meaning |
| --- | --- |
| `model` | model asked for |
| `purpose` | `turn` (the answering call), or one made on the run's behalf: `constraints`, `memory-pick`, `compaction`, `summary`, `final-synthesis`, … |
| `messages`, `tools` | `model_start`: how many messages and tools were sent |
| `duration_ms` | `model_end`: how long the call took |
| `streamed` | `model_end`: whether the answer arrived as a stream |
| `ttft_ms` | `model_end`: time to first output — written only when `streamed` |
| `tool_calls`, `text_len` | `model_end`: how many tool calls it asked for, how long its text was |
| `tokens` | `model_end`: `{total, prompt, completion, cached, cache_write}` — `cached` is prompt-cache reads, `cache_write` the prompt tokens written to the cache (Anthropic `cache_creation_input_tokens`) |
| `server_tools` | `model_end`: the provider's own work inside the call (below) |
| `request` | `model_end`, with payloads: `{messages: [{role, content, tool_call_id, tool_calls}], tools: [names], prompt}` |
| `response` | `model_end`, with payloads: `{content, reasoning, tool_calls: [{id, name, args}], finish_reason}` |
| `error` | `model_end`: why the call failed |

`server_tools` is `{uses: [{name, input, results, error_code, urls}], resumes,
request_ms: [...], citations}`: each server tool use (Anthropic's
`web_search`) with what it was asked and how many results came back, or its
error code; how many times a `pause_turn` was resumed; each HTTP request's
duration, the first and every resume; how many citations the text carried.
`urls` only in payload mode.

### `tool_start` / `tool_end`

| Field | Meaning |
| --- | --- |
| `tool` | the tool's name |
| `args` | `tool_start`: the arguments (string values clipped, unless payload mode) |
| `inner` | the call ran inside a sub-agent |
| `duration_ms` | `tool_end` |
| `result` / `error` | `tool_end`: the result (clipped unless payload mode), or the error |
| `remote` | `tool_end`: traceparents another process answered with |

### `decision`

A choice the runtime made that changes what the model sees or what is sent
for it, written once per run while it holds and again when it changes.

| Field | Meaning |
| --- | --- |
| `what` | `hide_mcp_search`, `hide_registry_web_search`, `defer_tools`, `eager_tools`, `drop_server_tool`, `retry_without_native_search`, `retry_without_tool_choice`, `retry_without_response_format`, `retry_without_prompt_cache` |
| `reason` | why, for a person |
| `detail` | what it was about: the tools, counts, modes |

### Other events

| Event | Fields |
| --- | --- |
| `model_delta` | (only with `IncludeDeltas`) `kind` (`reasoning`/`partial`), `delta` |
| `model_retry` | `kind` (`transient_error`, `max_tokens_truncation`), `attempt`, `reason`, `max_tokens_from`, `max_tokens_to`, `delay_ms` |
| `model_wire` | `purpose`, `kind` (queued, request, response, retry, first_byte, waiting, …), `attempt`, `status`, `duration_ms`, `delay_ms`, `bytes`, `reason` |
| `step` | `kind` (`memory.recall`, …), `duration_ms`, `error` |
| `permission` | `tool`, `args`, `decision`, `decider`, `decided_by`, `required`, `destructive`, `reason`, `error`, `metadata`, `duration_ms` |
| `subagent_start` / `subagent_end` | `subagent`, `subagent_id`, `goal`, `result`, `error` |
| `lint` | `lint`, `verdict` (`retry`/`blocked`), `reason` |
| `compaction` | `trigger`, `messages_before`, `messages_after`, `context_tokens`, `estimated_tokens`, `context_tokens_after`, `threshold`, `threshold_source`, `mode`, `clipped_results`, `trimmed_results`, `no_progress`, `summary_calls`, `degraded`, `error` |
| `error` | `marker`, `message` |
| `checkpoint` | `checkpoint_reason`, `messages`, `text_len` |
| `resource` | `res`: `{heap_bytes, heap_objects, stack_bytes, goroutines, rss_bytes, peak_rss_bytes, cpu_seconds, num_gc, uptime_ms}`, `final` |
| `segment_start` / `segment_end` | `segment_index`, `segment_total`; at the end `stop_reason`, `duration_ms`, `productive`, `error` |
| `tool_output_truncated` | `tool`, `original_bytes`, `kept_bytes`, `limit`, `structure_kept` |

Free-text fields (`text`, `result`, `reason`, `message`, `error`, `goal`) are
clipped to `MaxTextLen` (512) unless payload mode says otherwise for tool
results; request and response content to `MaxPayloadLen` (0 = whole).

## Logs

The framework logs through `pkg/log` (`log/slog`). Inside a run every line is
logged with the run's context, and carries `trace_id`, `span_id`,
`parent_span_id` — join a log line to its trace line on them; a line about a
model or tool call also names it as `call_span_id`.

| Level | A run says |
| --- | --- |
| `info` | `run start`, `run end` (outcome, stop reason, duration, rounds); one `model call` per model call (purpose, model, duration, streamed, tokens, ttft, server tools); one `tool call` per tool call (name, ok, error, remote); sub-agent start and end |
| `debug` | + `decision`, `model retry`, `model wire` (HTTP status, bytes, attempt), `lint rejected`, `compaction`, `permission`, `tool start` with arguments, tool results — clipped |
| `payload` | + `model call payload` (request, response, server tool report), tool arguments and results whole |

Warnings and errors always say what was being done and with which ids.

## Secrets

API keys and credentials never reach a log line or a trace line, at any level,
payload included: `Authorization` / `x-api-key` values, bearer and basic
tokens, provider key shapes (`sk-…`, `AIza…`, `ghp_…`, `xox…`, `AKIA…`), and
any argument or attribute whose name says it is a secret (`api_key`,
`password`, `token`, …) are replaced with `[REDACTED]` (`log.Redact`,
`log.RedactValue`).
