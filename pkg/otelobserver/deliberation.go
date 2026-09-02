package otelobserver

import (
	"context"
	"strconv"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/liliang-cn/agent-go/v3/pkg/agent"
)

// The five callbacks in this file are the ones that describe what the runtime
// decided rather than what it called: a lint that rejected an answer, a turn
// asked twice, history folded away, an error nobody was watching for, and the
// boundary between two segments of a long task.
//
// Four of them are span EVENTS rather than spans. They are instants, not
// intervals, and the thing they belong on is the run they happened inside —
// a trace backend renders them on the timeline of the root span, which is
// exactly where an operator looks when asking "what happened at minute nine".
// The fifth, a segment, is an interval and gets a span.

// rootSpanFor returns the task's root span, opening it if this is the first
// thing seen for the task.
func (o *Observer) rootSpanFor(ctx context.Context, taskID, sessionID string) trace.Span {
	if o == nil || o.tracer == nil {
		return nil
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.rootForLocked(ctx, taskID, sessionID).span
}

// OnLint records a lint rejection as an event on the run's root span.
//
// The verdict is the part worth keeping: the same lint firing twice and being
// retried is a run correcting itself, while one firing and blocking is a run
// that ended on the framework's judgement rather than the model's.
func (o *Observer) OnLint(ctx context.Context, info agent.LintInfo) {
	if o == nil {
		return
	}
	o.recordLint(ctx, info)
	span := o.rootSpanFor(ctx, info.TaskID, info.SessionID)
	if span == nil {
		return
	}
	span.AddEvent("lint.rejected", trace.WithAttributes(
		attribute.String("lint.name", info.Lint),
		attribute.String("lint.verdict", lintVerdict(info.Retrying)),
		attribute.String("lint.reason", info.Reason),
		attribute.String(attrAgentName, info.AgentName),
		attribute.Int(attrRound, info.Round),
	))
}

// OnModelRetry records a re-ask as an event on the model span it happened
// inside, so a turn that took three attempts no longer looks like one that
// took one. If the turn's span is gone the event falls back to the root.
func (o *Observer) OnModelRetry(ctx context.Context, info agent.ModelRetryInfo) {
	if o == nil {
		return
	}
	o.recordModelRetry(ctx, info)

	o.mu.Lock()
	span := o.spans[info.SpanID]
	o.mu.Unlock()
	if span == nil {
		span = o.rootSpanFor(ctx, info.TaskID, info.SessionID)
	}
	if span == nil {
		return
	}
	attrs := []attribute.KeyValue{
		attribute.String("retry.kind", info.Kind),
		attribute.Int("retry.attempt", info.Attempt),
		attribute.Int64("retry.delay_ms", info.Delay.Milliseconds()),
		attribute.String("retry.reason", info.Reason),
		attribute.String(attrAgentName, info.AgentName),
		attribute.Int(attrRound, info.Round),
	}
	if info.MaxTokensTo > 0 {
		attrs = append(attrs,
			attribute.Int("retry.max_tokens.from", info.MaxTokensFrom),
			attribute.Int("retry.max_tokens.to", info.MaxTokensTo),
		)
	}
	span.AddEvent("model.retry", trace.WithAttributes(attrs...))
}

// OnCompaction records a history fold as an event on the run's root span.
//
// It is the largest thing that happens to a run without anyone being told, and
// on a timeline it explains the shape of everything after it: the model
// re-reads what compaction deleted.
func (o *Observer) OnCompaction(ctx context.Context, info agent.CompactionInfo) {
	if o == nil {
		return
	}
	o.recordCompaction(ctx, info)
	span := o.rootSpanFor(ctx, info.TaskID, info.SessionID)
	if span == nil {
		return
	}
	span.AddEvent("context.compaction", trace.WithAttributes(
		attribute.String("compaction.trigger", info.Trigger),
		attribute.Int("compaction.messages.before", info.MessagesBefore),
		attribute.Int("compaction.messages.after", info.MessagesAfter),
		attribute.Int("compaction.tokens.context", info.ContextTokens),
		attribute.Int("compaction.tokens.estimated", info.EstimatedTokens),
		attribute.String(attrAgentName, info.AgentName),
		attribute.Int(attrRound, info.Round),
	))
}

// OnError records a runtime error as an event on the run's root span.
//
// It deliberately does not set the span's status: most of these are recovered
// — a tool that failed and was reported back to the model, a lint retry — and
// a run that recovered is not a failed run. The terminal outcome is carried by
// the checkpoint reason.
func (o *Observer) OnError(ctx context.Context, info agent.ErrorInfo) {
	if o == nil {
		return
	}
	o.recordError(ctx, info)
	span := o.rootSpanFor(ctx, info.TaskID, info.SessionID)
	if span == nil {
		return
	}
	span.AddEvent("agentgo.error", trace.WithAttributes(
		attribute.String("error.kind", errorKind(info.Marker)),
		attribute.String("error.message", info.Message),
		attribute.String(attrAgentName, info.AgentName),
		attribute.Int(attrRound, info.Round),
	))
}

// OnSegment opens and closes a span per RunSegments segment, and — while one
// is open — makes it the parent of that segment's model and tool spans.
//
// The segment's own run checkpoints before the segment ends, so the root has
// to survive its own checkpoint until the segment span closes; rootSpan's
// openSegments counter is what holds it. Segments correlate by
// agentgo.task_id, not by sharing one span (see rootSpan for why).
func (o *Observer) OnSegment(ctx context.Context, info agent.SegmentInfo) {
	if o == nil || o.tracer == nil {
		return
	}
	key := "seg:" + info.TaskID + ":" + strconv.Itoa(info.Index)

	if !info.Ending {
		o.mu.Lock()
		root := o.rootForLocked(ctx, info.TaskID, info.SessionID)
		sctx, span := o.tracer.Start(root.ctx, "segment "+strconv.Itoa(info.Index))
		span.SetAttributes(
			attribute.String(attrSpanKind, spanKindChain),
			attribute.String(attrTaskID, info.TaskID),
			attribute.String(attrSessionID, info.SessionID),
			attribute.Int("agentgo.segment.index", info.Index),
			attribute.Int("agentgo.segment.total", info.Total),
		)
		root.childCtx = sctx
		root.openSegments++
		o.spans[key] = span
		o.mu.Unlock()
		return
	}

	o.mu.Lock()
	span := o.spans[key]
	delete(o.spans, key)
	root := o.roots[info.TaskID]
	var closeRoot *rootSpan
	if root != nil {
		if root.openSegments > 0 {
			root.openSegments--
		}
		if root.openSegments == 0 {
			root.childCtx = root.ctx
			if root.pendingEnd {
				delete(o.roots, info.TaskID)
				closeRoot = root
			}
		}
	}
	o.mu.Unlock()

	if span != nil {
		span.SetAttributes(
			attribute.String("agentgo.segment.stop_reason", string(info.StopReason)),
			attribute.Int64("agentgo.segment.duration_ms", info.Duration.Milliseconds()),
			attribute.Bool("agentgo.segment.productive", info.Productive),
			attribute.Float64("agentgo.segment.cost_usd", info.CostUSD),
		)
		if info.Err != "" {
			span.SetAttributes(attribute.String("agentgo.segment.error", info.Err))
		}
		span.End()
	}
	if closeRoot != nil {
		closeRoot.span.End()
	}
}

func lintVerdict(retrying bool) string {
	if retrying {
		return "retrying"
	}
	return "blocked"
}

// errorKind normalises ErrorInfo.Marker, which is empty for the error paths
// that never had a sub-kind. A metric attribute that is sometimes absent is
// worse than one that is always present.
func errorKind(marker string) string {
	if marker == "" {
		return "unmarked"
	}
	return marker
}
