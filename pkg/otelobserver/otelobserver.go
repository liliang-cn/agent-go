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

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/liliang-cn/agent-go/v2/pkg/agent"
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

	mu    sync.Mutex
	roots map[string]*rootSpan  // taskID -> lazy root span
	spans map[string]trace.Span // per-event key (SpanID / tool:CallID / sub:SubAgentID)
}

type rootSpan struct {
	span trace.Span
	ctx  context.Context // carries the root span, so children nest under it
}

// Option configures an Observer.
type Option func(*Observer)

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
	}
	for _, opt := range opts {
		opt(o)
	}
	if tp == nil {
		tp = trace.NewNoopTracerProvider()
	}
	o.tracer = tp.Tracer("github.com/liliang-cn/agent-go/v2/pkg/otelobserver")
	return o
}

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
	r := &rootSpan{span: span, ctx: rctx}
	o.roots[taskID] = r
	return r
}

func (o *Observer) track(key string, span trace.Span) {
	if key == "" || span == nil {
		return
	}
	o.mu.Lock()
	o.spans[key] = span
	o.mu.Unlock()
}

func (o *Observer) pop(key string) trace.Span {
	o.mu.Lock()
	defer o.mu.Unlock()
	span := o.spans[key]
	delete(o.spans, key)
	return span
}

// OnModelStart opens an LLM span as a child of the task root.
func (o *Observer) OnModelStart(ctx context.Context, info agent.ModelInfo) {
	if o == nil || o.tracer == nil {
		return
	}
	o.mu.Lock()
	root := o.rootForLocked(ctx, info.TaskID, info.SessionID)
	rctx := root.ctx
	o.mu.Unlock()

	_, span := o.tracer.Start(rctx, "llm."+nonEmpty(info.AgentName, "model"))
	span.SetAttributes(
		attribute.String(attrSpanKind, spanKindLLM),
		attribute.String(attrLLMModelName, info.AgentName),
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
func (o *Observer) OnModelEnd(_ context.Context, info agent.ModelInfo, res *agent.ModelResult, err error) {
	if o == nil {
		return
	}
	span := o.pop(info.SpanID)
	if span == nil {
		return
	}
	if res != nil {
		span.SetAttributes(
			attribute.String(attrOutputValue, res.Content),
			attribute.Int(attrLLMTokenTotal, res.TokensUsed),
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
	rctx := root.ctx
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
func (o *Observer) OnToolEnd(_ context.Context, info agent.ToolInfo, result any, err error) {
	if o == nil {
		return
	}
	span := o.pop("tool:" + info.CallID)
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
	rctx := root.ctx
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
	delete(o.roots, info.TaskID)
	o.mu.Unlock()
	if root == nil {
		return
	}
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
	root.span.End()
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
