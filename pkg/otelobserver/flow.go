package otelobserver

import (
	"context"
	crand "crypto/rand"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"

	agtrace "github.com/liliang-cn/agent-go/v3/pkg/trace"
)

// The spans this bridge emits use agent-go's own flow ids — the trace id a
// run's lines carry in TraceWriter and in the logs, and the span ids of its
// runs, model calls and tool calls — so a span found in Phoenix and a line
// found in a trace file are the same thing under the same id.
//
// OpenTelemetry mints ids in the TracerProvider's IDGenerator, which this
// package does not own. IDGenerator() is one that takes the ids the bridge
// asks for and mints the rest as the default would; Phoenix installs it, and
// a host with its own provider adds sdktrace.WithIDGenerator(IDGenerator()).
// Without it the bridge still parents its root under the run's remote parent
// when the flow came from elsewhere — so the trace id agrees — and every span
// carries agentgo.trace_id / agentgo.span_id to join on.

const (
	attrFlowTraceID = "agentgo.trace_id"
	attrFlowSpanID  = "agentgo.span_id"
)

type wantKey struct{}

type wantIDs struct {
	trace trace.TraceID
	span  trace.SpanID
}

// wantSpan asks the IDGenerator for these ids on the next span started with
// the returned context.
func wantSpan(ctx context.Context, traceID, spanID string) context.Context {
	tid, err1 := trace.TraceIDFromHex(traceID)
	sid, err2 := trace.SpanIDFromHex(spanID)
	if err1 != nil || err2 != nil {
		return ctx
	}
	return context.WithValue(ctx, wantKey{}, wantIDs{trace: tid, span: sid})
}

// rootContext is base prepared for a root span that is the run on ctx: its
// ids requested, and its parent — when the run has one — set as a remote
// parent, so the OTel trace is the flow's.
func rootContext(base, ctx context.Context) context.Context {
	sc, ok := agtrace.SpanFromContext(ctx)
	if !ok {
		return base
	}
	base = wantSpan(base, sc.TraceID, sc.SpanID)
	if sc.ParentSpanID == "" {
		return base
	}
	tid, _ := trace.TraceIDFromHex(sc.TraceID)
	pid, err := trace.SpanIDFromHex(sc.ParentSpanID)
	if err != nil {
		return base
	}
	return trace.ContextWithRemoteSpanContext(base, trace.NewSpanContext(trace.SpanContextConfig{
		TraceID: tid, SpanID: pid, TraceFlags: trace.FlagsSampled, Remote: true,
	}))
}

// IDGenerator returns an sdktrace.IDGenerator that uses agent-go's flow ids
// for the spans this bridge starts and random ids for every other span.
func IDGenerator() sdktrace.IDGenerator { return flowIDs{} }

type flowIDs struct{}

func (flowIDs) NewIDs(ctx context.Context) (trace.TraceID, trace.SpanID) {
	if w, ok := ctx.Value(wantKey{}).(wantIDs); ok {
		return w.trace, w.span
	}
	var tid trace.TraceID
	var sid trace.SpanID
	_, _ = crand.Read(tid[:])
	_, _ = crand.Read(sid[:])
	return tid, sid
}

func (flowIDs) NewSpanID(ctx context.Context, traceID trace.TraceID) trace.SpanID {
	if w, ok := ctx.Value(wantKey{}).(wantIDs); ok && w.trace == traceID {
		return w.span
	}
	var sid trace.SpanID
	_, _ = crand.Read(sid[:])
	return sid
}

// flowTraceID is the flow's trace id on ctx, for attributes.
func flowTraceID(ctx context.Context) string {
	return agtrace.TraceIDFromContext(ctx)
}
