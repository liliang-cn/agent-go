package agent

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"github.com/liliang-cn/agent-go/v3/pkg/trace"
)

// runSpan is a run's place in its flow and how it ended, kept on the Runtime
// so callbacks fired from a context that does not carry the run's span (a
// checkpoint, a lint verdict) still land in the right place.
type runSpan struct {
	sc trace.SpanContext

	mu       sync.Mutex
	outcome  string
	reason   StopReason
	errText  string
	finished bool

	// aux holds the ModelInfo announced at a call's start, so its end
	// repeats it: the round a call began in, not the one it ended in.
	aux sync.Map // span id -> ModelInfo
	// decided is the last detail reported for each decision, so a choice
	// re-made every round is reported once, and again only when it changes.
	decided sync.Map // what -> string
}

// spanCtx returns ctx carrying the run's span, for callbacks given a bare
// context.
func (r *Runtime) spanCtx(ctx context.Context) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if r == nil || r.span == nil {
		return ctx
	}
	if cur, ok := trace.SpanFromContext(ctx); ok && cur.SpanID == r.span.sc.SpanID {
		return ctx
	}
	return trace.ContextWithSpan(ctx, r.span.sc)
}

// noteEnd records how the run ended, for the run-end observers. The first
// terminal path wins.
func (r *Runtime) noteEnd(outcome string, reason StopReason, errText string) {
	if r == nil || r.span == nil {
		return
	}
	r.span.mu.Lock()
	defer r.span.mu.Unlock()
	if r.span.finished {
		return
	}
	r.span.finished = true
	r.span.outcome, r.span.reason, r.span.errText = outcome, reason, errText
}

func (r *Runtime) runStartInfo() RunStartInfo {
	info := RunStartInfo{
		TaskID:    currentTaskID(r.session),
		RunID:     r.runID(),
		SessionID: r.sessionID(),
		AgentName: r.currentAgentName(),
		Model:     r.svc.Info().Model,
		Goal:      r.goal,
	}
	if r.span != nil {
		info.TraceID, info.SpanID, info.ParentSpanID = r.span.sc.TraceID, r.span.sc.SpanID, r.span.sc.ParentSpanID
	}
	return info
}

func (r *Runtime) emitRunBegin(ctx context.Context) {
	info := r.runStartInfo()
	r.svc.emitObserver(func(o Observer) {
		if ro, ok := o.(RunObserver); ok {
			ro.OnRunBegin(ctx, info)
		}
	})
}

func (r *Runtime) emitRunFinish(ctx context.Context) {
	end := RunEndInfo{RunStartInfo: r.runStartInfo(), Rounds: int(r.currentRound.Load())}
	if !r.startedAt.IsZero() {
		end.Duration = time.Since(r.startedAt)
	}
	if r.span != nil {
		r.span.mu.Lock()
		end.Outcome, end.StopReason, end.Err = r.span.outcome, r.span.reason, r.span.errText
		r.span.mu.Unlock()
	}
	if end.Outcome == "" {
		end.Outcome = "ended"
	}
	r.svc.emitObserver(func(o Observer) {
		if ro, ok := o.(RunObserver); ok {
			ro.OnRunFinish(ctx, end)
		}
	})
}

// Decision forwards a runtime decision to the observers, once per change.
func (s runtimeSink) Decision(ctx context.Context, d trace.Decision) {
	r := s.r
	if r.span != nil {
		key, _ := json.Marshal(d.Detail)
		if prev, ok := r.span.decided.Load(d.What); ok && prev.(string) == d.Reason+string(key) {
			return
		}
		r.span.decided.Store(d.What, d.Reason+string(key))
	}
	info := RuntimeDecisionInfo{
		TaskID:    currentTaskID(r.session),
		RunID:     r.runID(),
		SessionID: r.sessionID(),
		AgentName: r.currentAgentName(),
		Round:     int(r.currentRound.Load()),
		What:      d.What,
		Reason:    d.Reason,
		Detail:    d.Detail,
	}
	r.svc.emitObserver(func(o Observer) {
		if do, ok := o.(RuntimeDecisionObserver); ok {
			do.OnRuntimeDecision(ctx, info)
		}
	})
}
