// Package otelobserver bridges agent-go's agent.Observer callbacks to
// OpenTelemetry spans that follow the OpenInference semantic conventions, so
// agent runs render as trace trees in Arize Phoenix (or any OTLP backend), and
// — when a MeterProvider is supplied — to a small set of metrics that answer
// the questions a trace cannot: how much, how often, how many tokens.
//
// All OpenTelemetry imports are confined to this package; the framework core
// (pkg/agent) stays dependency-lean. Wire an Observer onto a Service with
// agent.WithObserver(obs) / svc.RegisterObserver(obs).
//
// The Observer API exposes no explicit run-start/run-end hook, so this bridge
// lazily opens a ROOT span the first time it sees any event for a TaskID and
// closes it on the first TERMINAL OnCheckpoint. Every model / tool /
// sub-agent span is created as a CHILD of that root so Phoenix shows a correct
// trace tree. Tasks that never checkpoint are cleaned up by Shutdown.
//
// # What the bridge emits
//
// Everything below is stable enough to build a dashboard on. The scope name on
// both the tracer and the meter is
// "github.com/liliang-cn/agent-go/v3/pkg/otelobserver".
//
// ## Spans
//
//	name                 kind    opened by            closed by
//	"task <task_id>"     CHAIN   first event of a run terminal OnCheckpoint (or Shutdown)
//	"segment <index>"    CHAIN   OnSegment            OnSegment(Ending)
//	"llm.<agent>"        LLM     OnModelStart         OnModelEnd
//	"tool.<tool>"        TOOL    OnToolStart          OnToolEnd
//	"agent.<name>"       AGENT   OnSubAgentStart      OnSubAgentEnd
//
// The kind is carried in openinference.span.kind; WithRootKind swaps the root's
// CHAIN for AGENT. While a segment span is open it — not the root — is the
// parent of the model, tool and sub-agent spans, so a long task reads as
// task → segment → turns. One trace is emitted per segment rather than one per
// task: a span exports nothing until it ends, and a task-wide root would show
// nothing for the hours it ran. Segments correlate through agentgo.task_id.
//
// Span attributes, by span:
//
//	root      openinference.span.kind, session.id, agentgo.task_id,
//	          agentgo.checkpoint.reason, agentgo.round, agentgo.messages,
//	          output.value (the final answer)
//	segment   openinference.span.kind, agentgo.task_id, session.id,
//	          agentgo.segment.index, .total, .stop_reason, .duration_ms,
//	          .productive, .error
//	llm       openinference.span.kind, llm.model_name, session.id,
//	          agentgo.agent_name, agentgo.round, output.value,
//	          llm.token_count.total, .prompt, .completion,
//	          llm.token_count.prompt_details.cache_read, llm.tool_calls;
//	          with agent.WithObserverPayloads also input.value,
//	          llm.input_messages.<i>.message.*, llm.tools.<i>.tool.json_schema
//
// Every llm, tool and agent span also carries the OpenTelemetry GenAI
// semantic conventions (gen_ai.*) beside the OpenInference set; see genai.go.
//
//	tool      openinference.span.kind, tool.name, tool.parameters,
//	          input.value, output.value, session.id, agentgo.agent_name,
//	          agentgo.tool.inner
//	agent     openinference.span.kind, session.id, agentgo.agent_name,
//	          agentgo.goal, input.value, output.value
//
// A span whose callback carried an error gets RecordError plus status Error;
// everything else ends Ok. The root is deliberately left Unset: most runtime
// errors are recovered, and the terminal verdict is agentgo.checkpoint.reason.
//
// ## Span events
//
//	event                on          attributes
//	"model.delta"        llm         delta.kind ("reasoning"|"partial"), delta.text
//	"model.retry"        llm         retry.kind ("transient_error"|"max_tokens_truncation"),
//	                                 retry.attempt, retry.delay_ms, retry.reason,
//	                                 retry.max_tokens.from/.to (budget escalation only),
//	                                 agentgo.agent_name, agentgo.round
//	"lint.rejected"      root        lint.name, lint.verdict ("retrying"|"blocked"),
//	                                 lint.reason, agentgo.agent_name, agentgo.round
//	"context.compaction" root        compaction.trigger, compaction.messages.before/.after,
//	                                 compaction.tokens.context/.estimated,
//	                                 agentgo.agent_name, agentgo.round
//	"agentgo.error"      root        error.kind (ErrorInfo.Marker, "unmarked" when empty),
//	                                 error.message, agentgo.agent_name, agentgo.round
//	"checkpoint"         root        reason; plus agentgo.round and agentgo.messages
//	                                 on the non-terminal ones
//
// model.delta is one event per streamed fragment and carries the fragment text:
// it is by far the highest-volume thing here, and a backend with an event cap
// will drop the rest of a turn's events before it drops these.
//
// ## Metrics
//
//	name                            instrument  unit      attributes
//	agentgo.model.calls             counter     1         agent, model, status
//	agentgo.model.duration          histogram   s         agent, model
//	agentgo.model.retries           counter     1         agent, kind
//	agentgo.tool.calls              counter     1         agent, tool, inner, status
//	agentgo.tool.duration           histogram   s         agent, tool, inner
//	agentgo.lint.rejections         counter     1         agent, lint, verdict
//	agentgo.compactions             counter     1         agent, trigger
//	agentgo.errors                  counter     1         agent, kind
//	agentgo.tokens.prompt           counter     {token}   agent, model
//	agentgo.tokens.completion       counter     {token}   agent, model
//	agentgo.tokens.cached           counter     {token}   agent, model
//
// Attribute keys are prefixed agentgo.: agentgo.agent, .model, .tool, .status
// ("ok"|"error"), .lint, .verdict, .kind, .trigger, .inner.
//
// No metric carries a task, session, run or span id, and none ever should: a
// metric's storage is the product of its label cardinalities, and a per-run label
// turns one time series into one per run — which is a trace, badly, at a
// hundred times the storage.
//
// ## What the numbers are guaranteed to add up to
//
// Verified against a live provider by examples/otel-probe, which runs the loop
// and compares these counters with the run's own accounting:
//
//   - tokens.prompt / .completion / .cached sum, over a run, to
//     ExecutionResult.Usage.PromptTokens / CompletionTokens / CachedPromptTokens
//     — and over a RunSegments task to LongRunResult.TotalUsage.
//   - model.duration's count equals model.calls, and tool.duration's equals
//     tool.calls: a turn that errored with no result is still timed, by the
//     bridge's own clock.
//
// Three things are outside those sums, by construction:
//
//   - Cache WRITE tokens. agent.ModelResult carries no equivalent of
//     TokenUsage.CacheWriteTokens, so the bridge cannot report the tokens written
//     to establish a cache entry. Read it from ExecutionResult.Usage.
//   - The terminal control tools. task_complete / task_blocked are intercepted
//     in the stream and never dispatched, so they produce no tool span and no
//     tool.calls — which is why ExecutionResult.ToolCalls can exceed the tool
//     counters by one.
//   - agentgo.model is only as good as Service.Info().Model. A generator
//     injected with Builder.WithLLM that implements neither GetModelName nor
//     GetBaseURL leaves it empty.
package otelobserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"

	"github.com/liliang-cn/agent-go/v3/pkg/agent"
	"github.com/liliang-cn/agent-go/v3/pkg/domain"
	agtrace "github.com/liliang-cn/agent-go/v3/pkg/trace"
)

// OpenInference semantic-convention attribute keys.
// See https://github.com/Arize-ai/openinference/blob/main/spec/semantic_conventions.md
const (
	attrSpanKind      = "openinference.span.kind"
	attrSessionID     = "session.id"
	attrInputValue    = "input.value"
	attrOutputValue   = "output.value"
	attrLLMModelName  = "llm.model_name"
	attrLLMTokenTotal = "llm.token_count.total"
	attrToolName      = "tool.name"
	attrToolParams    = "tool.parameters"

	attrLLMTokenPrompt     = "llm.token_count.prompt"
	attrLLMTokenCompletion = "llm.token_count.completion"
	attrLLMTokenCached     = "llm.token_count.prompt_details.cache_read"
	attrLLMTokenCacheWrite = "llm.token_count.prompt_details.cache_write"

	// Extra (non-OpenInference) attributes that help debugging in Phoenix.
	attrTaskID    = "agentgo.task_id"
	attrAgentName = "agentgo.agent_name"
	attrRound     = "agentgo.round"
	attrGoal      = "agentgo.goal"
)

// OpenInference span kinds.
const (
	spanKindChain = "CHAIN"
	spanKindAgent = "AGENT"
	spanKindLLM   = "LLM"
	spanKindTool  = "TOOL"
)

// Observer implements agent.Observer by emitting OpenTelemetry spans following
// the OpenInference conventions. Construct it with New.
type Observer struct {
	agent.BaseObserver

	tracer   trace.Tracer
	rootKind string

	meterProvider metric.MeterProvider
	metrics       *instruments

	mu     sync.Mutex
	roots  map[string]*rootSpan  // taskID -> lazy root span
	spans  map[string]trace.Span // per-event key (SpanID / tool:CallID / sub:SubAgentID / seg:TaskID:Index)
	starts map[string]time.Time  // same keys, for the duration histograms
}

type rootSpan struct {
	span trace.Span
	ctx  context.Context // carries the root span, so children nest under it

	// childCtx is where the next model / tool / sub-agent span attaches. It is
	// the root context normally and the open segment's context while a
	// RunSegments segment is running, so a long task renders as
	// task → segment → turns rather than one flat list of every turn it ever
	// took.
	childCtx context.Context

	// openSegments is how many segment spans are currently open under this
	// root (0 or 1 in practice; segments are sequential).
	//
	// A segment outlives the run inside it: the run's terminal checkpoint
	// fires first, OnSegment(Ending) second. Without the count the root would
	// end while its own segment child was still open, which is a child that
	// outlives its parent — legal OTel, unreadable trace.
	//
	// One trace per segment rather than one per task is deliberate. The end
	// of a long run has no callback (RunSegments can stop early for six
	// different reasons), so a task-wide root could only be closed by
	// Shutdown — and a span exports nothing until it ends, which would mean
	// seeing nothing at all from a task until the hour was up. Segments are
	// correlated by agentgo.task_id instead.
	openSegments int
	// pendingEnd records that a checkpoint asked to end the root while a
	// segment was still open.
	pendingEnd bool
}

// Option configures an Observer.
type Option func(*Observer)

// WithMeterProvider makes the Observer record metrics through mp. Without it
// the metric side is a no-op and only spans are produced, so New(tp) keeps its
// original behaviour exactly.
func WithMeterProvider(mp metric.MeterProvider) Option {
	return func(o *Observer) { o.meterProvider = mp }
}

// WithRootKind overrides the OpenInference span kind used for the per-task
// root span. Defaults to "CHAIN"; "AGENT" is also common.
func WithRootKind(kind string) Option {
	return func(o *Observer) {
		if kind != "" {
			o.rootKind = kind
		}
	}
}

// WithServiceName is accepted for API symmetry with the Phoenix helper. The
// service name is a Resource-level concern owned by the TracerProvider, so this
// option is a no-op here; set it on the provider (Phoenix does this for you).
func WithServiceName(string) Option { return func(*Observer) {} }

// New returns an Observer that emits spans via a Tracer obtained from tp. The
// caller supplies (and owns) the TracerProvider, so the exporter / endpoint is
// the caller's choice. Shutdown ends dangling root spans but does NOT shut down
// tp.
func New(tp trace.TracerProvider, opts ...Option) *Observer {
	o := &Observer{
		rootKind: spanKindChain,
		roots:    make(map[string]*rootSpan),
		spans:    make(map[string]trace.Span),
		starts:   make(map[string]time.Time),
	}
	for _, opt := range opts {
		opt(o)
	}
	if tp == nil {
		tp = trace.NewNoopTracerProvider()
	}
	o.tracer = tp.Tracer(instrumentationName)
	o.metrics = newInstruments(o.meterProvider)
	return o
}

// instrumentationName is the scope both the tracer and the meter report under.
const instrumentationName = "github.com/liliang-cn/agent-go/v3/pkg/otelobserver"

var _ agent.Observer = (*Observer)(nil)

// rootFor returns (creating if necessary) the lazy root span + context for a
// task. Must be called with o.mu held.
func (o *Observer) rootForLocked(ctx context.Context, taskID, sessionID string) *rootSpan {
	if r, ok := o.roots[taskID]; ok {
		return r
	}
	base := ctx
	if base == nil {
		base = context.Background()
	}
	name := "task"
	if taskID != "" {
		name = "task " + taskID
	}
	rctx, span := o.tracer.Start(rootContext(base, ctx), name)
	span.SetAttributes(
		attribute.String(attrSpanKind, o.rootKind),
		attribute.String(attrSessionID, sessionID),
		attribute.String(attrTaskID, taskID),
	)
	if sc, ok := agtrace.SpanFromContext(ctx); ok {
		span.SetAttributes(attribute.String(attrFlowTraceID, sc.TraceID), attribute.String(attrFlowSpanID, sc.SpanID))
	}
	r := &rootSpan{span: span, ctx: rctx, childCtx: rctx}
	o.roots[taskID] = r
	return r
}

func (o *Observer) track(key string, span trace.Span) {
	if key == "" || span == nil {
		return
	}
	o.mu.Lock()
	o.spans[key] = span
	o.starts[key] = time.Now()
	o.mu.Unlock()
}

func (o *Observer) pop(key string) trace.Span {
	span, _ := o.popTimed(key)
	return span
}

// popTimed removes a tracked span and reports how long it was open. A zero
// duration means the start was never recorded.
func (o *Observer) popTimed(key string) (trace.Span, time.Duration) {
	o.mu.Lock()
	defer o.mu.Unlock()
	span := o.spans[key]
	delete(o.spans, key)
	var elapsed time.Duration
	if start, ok := o.starts[key]; ok {
		elapsed = time.Since(start)
		delete(o.starts, key)
	}
	return span, elapsed
}

// OnModelStart opens an LLM span as a child of the task root.
func (o *Observer) OnModelStart(ctx context.Context, info agent.ModelInfo) {
	if o == nil || o.tracer == nil {
		return
	}
	o.mu.Lock()
	root := o.rootForLocked(ctx, info.TaskID, info.SessionID)
	rctx := root.childCtx
	o.mu.Unlock()

	name := "llm." + nonEmpty(info.AgentName, "model")
	if info.Purpose != "" && info.Purpose != "turn" {
		// A call made on the run's behalf, named for what it was for: the
		// spans that used to be missing from a slow run's trace.
		name = "llm." + info.Purpose
	}
	_, span := o.tracer.Start(wantSpan(rctx, flowTraceID(ctx), info.SpanID), name)
	span.SetAttributes(
		attribute.String(attrFlowTraceID, flowTraceID(ctx)),
		attribute.String(attrFlowSpanID, info.SpanID),
		attribute.String(attrSpanKind, spanKindLLM),
		attribute.String("agentgo.llm.purpose", nonEmpty(info.Purpose, "turn")),
		attribute.String(attrLLMModelName, nonEmpty(info.Model, info.AgentName)),
		attribute.String(attrSessionID, info.SessionID),
		attribute.String(attrAgentName, info.AgentName),
		attribute.Int(attrRound, info.Round),
	)
	span.SetAttributes(genAIModelStartAttrs(info)...)
	o.track(info.SpanID, span)
}

// OnModelDelta records streamed fragments as span events.
func (o *Observer) OnModelDelta(_ context.Context, delta agent.ModelDelta) {
	if o == nil {
		return
	}
	o.mu.Lock()
	span := o.spans[delta.SpanID]
	o.mu.Unlock()
	if span == nil || delta.Text == "" {
		return
	}
	span.AddEvent("model.delta", trace.WithAttributes(
		attribute.String("delta.kind", delta.Kind),
		attribute.String("delta.text", delta.Text),
	))
}

// OnModelEnd closes the LLM span, attaching output + token attributes.
func (o *Observer) OnModelEnd(ctx context.Context, info agent.ModelInfo, res *agent.ModelResult, err error) {
	if o == nil {
		return
	}
	span, elapsed := o.popTimed(info.SpanID)
	o.recordModelEnd(ctx, info, res, elapsed, err)
	if span == nil {
		return
	}
	setRequestAttributes(span, info.Request)
	if res != nil {
		span.SetAttributes(
			attribute.String(attrOutputValue, res.Content),
			attribute.Int(attrLLMTokenTotal, res.TokensUsed),
			attribute.Int(attrLLMTokenPrompt, res.PromptTokens),
			attribute.Int(attrLLMTokenCompletion, res.CompletionTokens),
			attribute.Int(attrLLMTokenCached, res.CachedTokens),
			attribute.Int(attrLLMTokenCacheWrite, res.CacheWriteTokens),
			attribute.Int("llm.tool_calls", res.ToolCalls),
			attribute.Bool("agentgo.llm.streamed", res.Streamed),
		)
		if res.Streamed && res.TTFTMs > 0 {
			span.SetAttributes(attribute.Int64("llm.ttft_ms", res.TTFTMs))
		}
		setServerToolAttributes(span, res.ServerTools)
	}
	span.SetAttributes(genAIModelEndAttrs(info, res, err)...)
	finish(span, err)
}

// OnStep records a memory lookup or another timed step as a finished span,
// backdated to when it started.
func (o *Observer) OnStep(ctx context.Context, info agent.StepInfo) {
	if o == nil || o.tracer == nil {
		return
	}
	o.mu.Lock()
	root := o.rootForLocked(ctx, info.TaskID, info.SessionID)
	rctx := root.childCtx
	o.mu.Unlock()
	end := time.Now()
	_, span := o.tracer.Start(rctx, info.Kind+"."+info.Name,
		trace.WithTimestamp(end.Add(-time.Duration(info.DurationMs)*time.Millisecond)))
	span.SetAttributes(attribute.String(attrSessionID, info.SessionID))
	var err error
	if info.Err != "" {
		err = errors.New(info.Err)
	}
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
	}
	span.End(trace.WithTimestamp(end))
}

// OnToolStart opens a TOOL span as a child of the task root.
func (o *Observer) OnToolStart(ctx context.Context, info agent.ToolInfo) {
	if o == nil || o.tracer == nil {
		return
	}
	o.mu.Lock()
	root := o.rootForLocked(ctx, info.TaskID, info.SessionID)
	rctx := root.childCtx
	o.mu.Unlock()

	argsJSON := toJSON(info.Args)
	_, span := o.tracer.Start(wantSpan(rctx, flowTraceID(ctx), info.SpanID), "tool."+info.Tool)
	span.SetAttributes(
		attribute.String(attrFlowTraceID, flowTraceID(ctx)),
		attribute.String(attrFlowSpanID, info.SpanID),
		attribute.String(attrSpanKind, spanKindTool),
		attribute.String(attrToolName, info.Tool),
		attribute.String(attrToolParams, argsJSON),
		attribute.String(attrInputValue, argsJSON),
		attribute.String(attrSessionID, info.SessionID),
		attribute.String(attrAgentName, info.AgentName),
		attribute.Bool("agentgo.tool.inner", info.Inner),
	)
	span.SetAttributes(genAIToolAttrs(info, argsJSON)...)
	o.track("tool:"+info.CallID, span)
}

// OnToolEnd closes the TOOL span, attaching the result as output.value.
func (o *Observer) OnToolEnd(ctx context.Context, info agent.ToolInfo, result any, err error) {
	if o == nil {
		return
	}
	span, elapsed := o.popTimed("tool:" + info.CallID)
	o.recordToolEnd(ctx, info, elapsed, err)
	if span == nil {
		return
	}
	if result != nil {
		out := toJSON(result)
		span.SetAttributes(
			attribute.String(attrOutputValue, out),
			attribute.String(genAIToolCallResult, out),
		)
	}
	finish(span, err)
}

// OnSubAgentStart opens an AGENT span as a child of the parent task root.
func (o *Observer) OnSubAgentStart(ctx context.Context, info agent.SubAgentInfo) {
	if o == nil || o.tracer == nil {
		return
	}
	o.mu.Lock()
	root := o.rootForLocked(ctx, info.ParentTaskID, info.SessionID)
	rctx := root.childCtx
	o.mu.Unlock()

	_, span := o.tracer.Start(rctx, "agent."+nonEmpty(info.Name, "subagent"))
	span.SetAttributes(
		attribute.String(attrSpanKind, spanKindAgent),
		attribute.String(attrSessionID, info.SessionID),
		attribute.String(attrAgentName, info.Name),
		attribute.String(attrGoal, info.Goal),
		attribute.String(attrInputValue, info.Goal),
	)
	span.SetAttributes(genAIAgentAttrs(info)...)
	o.track("sub:"+info.SubAgentID, span)
}

// OnSubAgentEnd closes the AGENT span.
func (o *Observer) OnSubAgentEnd(_ context.Context, info agent.SubAgentInfo, result any, err error) {
	if o == nil {
		return
	}
	span := o.pop("sub:" + info.SubAgentID)
	if span == nil {
		return
	}
	if result != nil {
		span.SetAttributes(attribute.String(attrOutputValue, toJSON(result)))
	}
	finish(span, err)
}

// checkpointEndsRun reports whether a checkpoint reason is a terminal one.
//
// Not every checkpoint is the end of a run. AutonomyProfile.CheckpointEveryRounds
// writes a round_end snapshot mid-run, and the after_tool sink writes one after
// a tool — both reach OnCheckpoint with the same shape as task_complete. Ending
// the root on those splits one run into a fresh trace per snapshot: a live probe
// with checkpoints every round produced two "task <id>" roots for a two-round
// run, the second holding the answer and neither holding the whole thing.
//
// Unknown reasons are treated as terminal on purpose. A root that is never
// ended exports nothing at all, so the failure mode of guessing wrong in this
// direction is a split trace; the other direction is no trace.
func checkpointEndsRun(reason string) bool {
	switch reason {
	case string(agent.CheckpointReasonRoundEnd), string(agent.CheckpointReasonAfterTool):
		return false
	}
	return true
}

// OnCheckpoint is the run-end signal: a terminal checkpoint closes the task
// root span. A non-terminal one only marks the timeline.
func (o *Observer) OnCheckpoint(_ context.Context, info agent.CheckpointInfo) {
	if o == nil {
		return
	}
	if !checkpointEndsRun(info.Reason) {
		o.mu.Lock()
		root := o.roots[info.TaskID]
		o.mu.Unlock()
		if root == nil {
			return
		}
		root.span.AddEvent("checkpoint", trace.WithAttributes(
			attribute.String("reason", info.Reason),
			attribute.Int(attrRound, info.Round),
			attribute.Int("agentgo.messages", info.Messages),
		))
		return
	}
	o.mu.Lock()
	root := o.roots[info.TaskID]
	if root == nil {
		o.mu.Unlock()
		return
	}
	// A segment still open means this checkpoint ends one run of a longer
	// task, not the task. Remember the ask and let the last segment close it.
	ending := root.openSegments == 0
	if ending {
		delete(o.roots, info.TaskID)
	} else {
		root.pendingEnd = true
	}
	o.mu.Unlock()

	root.span.SetAttributes(
		attribute.String("agentgo.checkpoint.reason", info.Reason),
		attribute.Int(attrRound, info.Round),
		attribute.Int("agentgo.messages", info.Messages),
	)
	if info.FinalText != "" {
		root.span.SetAttributes(attribute.String(attrOutputValue, info.FinalText))
	}
	root.span.AddEvent("checkpoint", trace.WithAttributes(
		attribute.String("reason", info.Reason),
	))
	if ending {
		root.span.End()
	}
}

// Shutdown ends any root spans whose task never checkpointed, plus any dangling
// child spans. It does NOT shut down the caller's TracerProvider — the caller
// owns that.
func (o *Observer) Shutdown(_ context.Context) error {
	if o == nil {
		return nil
	}
	o.mu.Lock()
	roots := o.roots
	spans := o.spans
	o.roots = make(map[string]*rootSpan)
	o.spans = make(map[string]trace.Span)
	o.starts = make(map[string]time.Time)
	o.mu.Unlock()

	for _, s := range spans {
		if s != nil {
			s.End()
		}
	}
	for _, r := range roots {
		if r != nil && r.span != nil {
			r.span.SetAttributes(attribute.String("agentgo.checkpoint.reason", "shutdown"))
			r.span.End()
		}
	}
	return nil
}

func finish(span trace.Span, err error) {
	if span == nil {
		return
	}
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
	} else {
		span.SetStatus(codes.Ok, "")
	}
	span.End()
}

// setRequestAttributes puts a model call's input on its span in the
// OpenInference shape Phoenix renders as a conversation: input.value (the
// whole request as JSON), one llm.input_messages.<i>.message.* group per
// message, and one llm.tools.<i>.tool.json_schema per tool offered. req is
// nil unless the service was built with agent.WithObserverPayloads, and then
// nothing here runs — the span keeps carrying counts and output only.
//
// Nothing is clipped. The point of carrying the request is reading it back
// whole; a collector that wants a ceiling sets one in its SDK limits.
func setRequestAttributes(span trace.Span, req *agent.ModelRequest) {
	if req == nil {
		return
	}
	if req.Prompt != "" && len(req.Messages) == 0 {
		span.SetAttributes(
			attribute.String(attrInputValue, req.Prompt),
			attribute.String("llm.input_messages.0.message.role", "user"),
			attribute.String("llm.input_messages.0.message.content", req.Prompt),
		)
		return
	}
	attrs := make([]attribute.KeyValue, 0, 2+3*len(req.Messages)+len(req.Tools))
	attrs = append(attrs,
		attribute.String(attrInputValue, toJSON(map[string]any{"messages": req.Messages, "tools": req.Tools})),
		attribute.String("input.mime_type", "application/json"),
	)
	for i, m := range req.Messages {
		p := fmt.Sprintf("llm.input_messages.%d.message.", i)
		attrs = append(attrs, attribute.String(p+"role", m.Role))
		if m.Content != "" || len(m.ToolCalls) == 0 {
			attrs = append(attrs, attribute.String(p+"content", m.Content))
		}
		if m.ToolCallID != "" {
			attrs = append(attrs, attribute.String(p+"tool_call_id", m.ToolCallID))
		}
		for j, tc := range m.ToolCalls {
			tp := fmt.Sprintf("%stool_calls.%d.tool_call.", p, j)
			attrs = append(attrs,
				attribute.String(tp+"id", tc.ID),
				attribute.String(tp+"function.name", tc.Function.Name),
				attribute.String(tp+"function.arguments", toJSON(tc.Function.Arguments)),
			)
		}
	}
	for i, t := range req.Tools {
		attrs = append(attrs, attribute.String(fmt.Sprintf("llm.tools.%d.tool.json_schema", i), toJSON(t)))
	}
	span.SetAttributes(attrs...)
}

func nonEmpty(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}

func toJSON(v any) string {
	if v == nil {
		return ""
	}
	b, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	return string(b)
}

// setServerToolAttributes puts a model call's server-side work on its span:
// counts and lists as attributes, one "server_tool.use" event per use.
func setServerToolAttributes(span trace.Span, r *domain.ServerToolReport) {
	if r == nil {
		return
	}
	var names, queries, errs []string
	for _, u := range r.Uses {
		names = append(names, u.Name)
		q, _ := u.Input["query"].(string)
		queries = append(queries, q)
		if u.ErrorCode != "" {
			errs = append(errs, u.ErrorCode)
		}
		span.AddEvent("server_tool.use", trace.WithAttributes(
			attribute.String("server_tool.name", u.Name),
			attribute.String("server_tool.input", toJSON(u.Input)),
			attribute.Int("server_tool.results", u.Results),
			attribute.String("server_tool.error_code", u.ErrorCode),
		))
	}
	span.SetAttributes(
		attribute.Int("agentgo.server_tools.uses", len(r.Uses)),
		attribute.StringSlice("agentgo.server_tools.names", names),
		attribute.StringSlice("agentgo.server_tools.queries", queries),
		attribute.StringSlice("agentgo.server_tools.errors", errs),
		attribute.Int("agentgo.server_tools.resumes", r.Resumes),
		attribute.Int64Slice("agentgo.server_tools.request_ms", r.RequestMs),
		attribute.Int("agentgo.server_tools.citations", r.Citations),
	)
}

// OnRuntimeDecision records a runtime decision as an "agentgo.decision"
// event on the task's root span (agent.RuntimeDecisionObserver).
func (o *Observer) OnRuntimeDecision(ctx context.Context, info agent.RuntimeDecisionInfo) {
	if o == nil || o.tracer == nil {
		return
	}
	o.mu.Lock()
	root := o.rootForLocked(ctx, info.TaskID, info.SessionID)
	o.mu.Unlock()
	root.span.AddEvent("agentgo.decision", trace.WithAttributes(
		attribute.String("decision.what", info.What),
		attribute.String("decision.reason", info.Reason),
		attribute.String("decision.detail", toJSON(info.Detail)),
		attribute.Int(attrRound, info.Round),
	))
}
