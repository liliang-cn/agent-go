// Package otelobserver bridges agent-go's agent.Observer callbacks to
// OpenTelemetry spans that follow the OpenInference semantic conventions, so
// agent runs render as trace trees in Arize Phoenix (or any OTLP backend).
//
// All OpenTelemetry imports are confined to this package; the framework core
// (pkg/agent) stays dependency-lean. Wire an Observer onto a Service with
// agent.WithObserver(obs) / svc.RegisterObserver(obs).
//
// The Observer API exposes no explicit run-start/run-end hook, so this bridge
// lazily opens a ROOT span the first time it sees any event for a TaskID and
// closes it on OnCheckpoint (which fires on terminal complete/blocked). Every
// model / tool / sub-agent span is created as a CHILD of that root so Phoenix
// shows a correct trace tree. Tasks that never checkpoint are cleaned up by
// Shutdown.
package otelobserver

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"

	"github.com/liliang-cn/agent-go/v3/pkg/agent"
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
	rctx, span := o.tracer.Start(base, name)
	span.SetAttributes(
		attribute.String(attrSpanKind, o.rootKind),
		attribute.String(attrSessionID, sessionID),
		attribute.String(attrTaskID, taskID),
	)
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

	_, span := o.tracer.Start(rctx, "llm."+nonEmpty(info.AgentName, "model"))
	span.SetAttributes(
		attribute.String(attrSpanKind, spanKindLLM),
		attribute.String(attrLLMModelName, nonEmpty(info.Model, info.AgentName)),
		attribute.String(attrSessionID, info.SessionID),
		attribute.String(attrAgentName, info.AgentName),
		attribute.Int(attrRound, info.Round),
	)
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
	o.recordModelEnd(ctx, info, res, err)
	span := o.pop(info.SpanID)
	if span == nil {
		return
	}
	if res != nil {
		span.SetAttributes(
			attribute.String(attrOutputValue, res.Content),
			attribute.Int(attrLLMTokenTotal, res.TokensUsed),
			attribute.Int(attrLLMTokenPrompt, res.PromptTokens),
			attribute.Int(attrLLMTokenCompletion, res.CompletionTokens),
			attribute.Int(attrLLMTokenCached, res.CachedTokens),
			attribute.Int("llm.tool_calls", res.ToolCalls),
		)
	}
	finish(span, err)
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
	_, span := o.tracer.Start(rctx, "tool."+info.Tool)
	span.SetAttributes(
		attribute.String(attrSpanKind, spanKindTool),
		attribute.String(attrToolName, info.Tool),
		attribute.String(attrToolParams, argsJSON),
		attribute.String(attrInputValue, argsJSON),
		attribute.String(attrSessionID, info.SessionID),
		attribute.String(attrAgentName, info.AgentName),
		attribute.Bool("agentgo.tool.inner", info.Inner),
	)
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
		span.SetAttributes(attribute.String(attrOutputValue, toJSON(result)))
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

// OnCheckpoint is the run-end signal: it closes the task root span.
func (o *Observer) OnCheckpoint(_ context.Context, info agent.CheckpointInfo) {
	if o == nil {
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
