package agent

import (
	"context"
	"sync"
)

// TracingObserver is a built-in Observer that drives a Tracer: it opens a Span
// at each Start callback and closes it at the matching End, so a completed run
// leaves a populated Tracer that trace_export.go / `agentgo task trace` can
// render. Spans are correlated by the stable SpanID / CallID / SubAgentID
// carried on the observer payloads.
//
// All spans for a single observer instance share one trace id, so a run's
// model / tool / sub-agent spans group together. Checkpoints are recorded as
// short-lived spans carrying a single event.
type TracingObserver struct {
	tracer  *Tracer
	baseCtx context.Context

	mu    sync.Mutex
	spans map[string]*Span
}

// NewTracingObserver returns an Observer that records spans into t. Pass the
// returned observer to WithObserver / RegisterObserver.
func NewTracingObserver(t *Tracer) Observer {
	return &TracingObserver{
		tracer:  t,
		baseCtx: withTraceID(context.Background(), newID()),
		spans:   make(map[string]*Span),
	}
}

func (o *TracingObserver) track(key string, span *Span) {
	if span == nil || key == "" {
		return
	}
	o.mu.Lock()
	o.spans[key] = span
	o.mu.Unlock()
}

func (o *TracingObserver) pop(key string) *Span {
	o.mu.Lock()
	defer o.mu.Unlock()
	span := o.spans[key]
	delete(o.spans, key)
	return span
}

func (o *TracingObserver) OnModelStart(_ context.Context, info ModelInfo) {
	if o == nil || o.tracer == nil {
		return
	}
	span, _ := o.tracer.StartSpan(o.baseCtx, "llm."+nonEmpty(info.AgentName, "model"), SpanKindLLM,
		WithSpanAgentID(info.AgentName), WithSpanSessionID(info.SessionID))
	o.track(info.SpanID, span)
}

func (o *TracingObserver) OnModelDelta(context.Context, ModelDelta) {}

func (o *TracingObserver) OnModelEnd(_ context.Context, info ModelInfo, res *ModelResult, err error) {
	if o == nil || o.tracer == nil {
		return
	}
	span := o.pop(info.SpanID)
	if span != nil && res != nil {
		o.tracer.SetAttribute(span, "tool_calls", itoa(res.ToolCalls))
		o.tracer.SetAttribute(span, "tokens", itoa(res.TokensUsed))
	}
	o.tracer.EndSpan(span, err)
}

func (o *TracingObserver) OnToolStart(_ context.Context, info ToolInfo) {
	if o == nil || o.tracer == nil {
		return
	}
	span, _ := o.tracer.StartSpan(o.baseCtx, "tool."+info.Tool, SpanKindTool,
		WithSpanAgentID(info.AgentName), WithSpanSessionID(info.SessionID))
	o.track("tool:"+info.CallID, span)
}

func (o *TracingObserver) OnToolEnd(_ context.Context, info ToolInfo, _ any, err error) {
	if o == nil || o.tracer == nil {
		return
	}
	o.tracer.EndSpan(o.pop("tool:"+info.CallID), err)
}

func (o *TracingObserver) OnSubAgentStart(_ context.Context, info SubAgentInfo) {
	if o == nil || o.tracer == nil {
		return
	}
	span, _ := o.tracer.StartSpan(o.baseCtx, "subagent."+nonEmpty(info.Name, "agent"), SpanKindAgent,
		WithSpanSessionID(info.SessionID))
	o.track("sub:"+info.SubAgentID, span)
}

func (o *TracingObserver) OnSubAgentEnd(_ context.Context, info SubAgentInfo, _ any, err error) {
	if o == nil || o.tracer == nil {
		return
	}
	o.tracer.EndSpan(o.pop("sub:"+info.SubAgentID), err)
}

func (o *TracingObserver) OnCheckpoint(_ context.Context, info CheckpointInfo) {
	if o == nil || o.tracer == nil {
		return
	}
	span, _ := o.tracer.StartSpan(o.baseCtx, "checkpoint", SpanKindInternal,
		WithSpanSessionID(info.SessionID))
	o.tracer.AddEvent(span, "checkpoint", map[string]string{
		"reason":   info.Reason,
		"round":    itoa(info.Round),
		"messages": itoa(info.Messages),
	})
	o.tracer.EndSpan(span, nil)
}

var _ Observer = (*TracingObserver)(nil)

func nonEmpty(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}
