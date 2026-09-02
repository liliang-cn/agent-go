package agent

import (
	"context"
	"log/slog"
	"time"

	"github.com/liliang-cn/agent-go/v3/pkg/domain"
)

// Observer is a unified aspect/observability hook that brackets the four
// interesting seams of a run — model turns, tool calls, sub-agent runs, and
// terminal checkpoints — with paired Start/End callbacks. Unlike HookRegistry
// (which can mutate/​block a run) an Observer is strictly passive: it is fed
// stable correlation IDs so Start/End pair up without the kernel having to
// thread context back through every call site.
//
// Embed BaseObserver and override only the methods you care about. Register
// observers on a Service via RegisterObserver / WithObserver. A nil / empty
// observer set is zero-overhead.
type Observer interface {
	// OnModelStart fires immediately before an LLM turn is dispatched.
	OnModelStart(ctx context.Context, info ModelInfo)
	// OnModelDelta fires for each streamed reasoning / partial fragment.
	OnModelDelta(ctx context.Context, delta ModelDelta)
	// OnModelEnd fires after the LLM turn returns (res / err may be nil).
	OnModelEnd(ctx context.Context, info ModelInfo, res *ModelResult, err error)

	// OnToolStart fires before a tool call is dispatched.
	OnToolStart(ctx context.Context, info ToolInfo)
	// OnToolEnd fires after a tool call returns.
	OnToolEnd(ctx context.Context, info ToolInfo, result any, err error)

	// OnSubAgentStart fires when a goal-driven sub-agent begins.
	OnSubAgentStart(ctx context.Context, info SubAgentInfo)
	// OnSubAgentEnd fires when a goal-driven sub-agent finishes.
	OnSubAgentEnd(ctx context.Context, info SubAgentInfo, result any, err error)

	// OnCheckpoint fires when the runtime writes a terminal checkpoint.
	OnCheckpoint(ctx context.Context, info CheckpointInfo)

	// OnLint fires when an output lint rejects a draft answer — once per
	// rejection, whether the run is re-prompted or blocked.
	//
	// It is here because the lint layer is the one part of the runtime that
	// can end a run on its own judgement, and its verdict was the one thing
	// nothing recorded. A soak run finished all thirteen of its milestones,
	// passed every test, and was reported blocked; the events said which lint
	// did it, but nothing an observer could see, so the answer was
	// unavailable to anyone watching from outside the event stream.
	OnLint(ctx context.Context, info LintInfo)

	// OnModelRetry fires when the runtime asks the model again for the same
	// turn — because the provider erred transiently, or because the answer
	// came back truncated before it produced anything.
	//
	// Both retries happen inside a single model span, so from the outside a
	// turn that took three attempts looked exactly like one that took one:
	// the span opened, time passed, an answer arrived. That is the same gap
	// OnLint was added to close, one layer down. A run that silently
	// escalates its token budget every round is paying for reasoning it
	// never uses, and its operator could not see it happening.
	OnModelRetry(ctx context.Context, info ModelRetryInfo)

	// OnCompaction fires when the runtime folds older history into a
	// summary.
	//
	// This is the largest thing that happens to a run without anyone
	// watching being told. Compaction deletes the model's working memory
	// mid-segment: tool results it was about to use, files it had already
	// read. A soak showed a segment's prompt halving twice in forty-six
	// rounds, each drop followed by the agent re-reading the same seven
	// files — and the only way to see it was to plot the token counts and
	// infer backwards. `EventTypeCompactBoundary` has always been on the
	// event stream, but a run's events go to whoever called RunStream, and
	// the thing you attach to a run you cannot watch is an Observer.
	OnCompaction(ctx context.Context, info CompactionInfo)

	// OnError fires for every error the runtime puts on its event stream:
	// a tool that failed, a compaction that could not summarise, a history
	// write that did not land.
	//
	// These were the events most worth seeing and the ones with no observer
	// at all — eighteen event types against twelve callbacks, and this was
	// the gap. A run nobody is watching interactively is exactly the run
	// whose tool failures need to reach a log file.
	OnError(ctx context.Context, info ErrorInfo)

	// OnSegment fires at the start and end of each RunSegments segment.
	//
	// long_run.go emitted nothing at all: a supervisor driving a task across
	// eleven segments and an hour was, from outside, one opaque call. Segment
	// boundaries had to be reverse-engineered from round numbers restarting
	// at 1.
	OnSegment(ctx context.Context, info SegmentInfo)
}

// ModelInfo identifies a single model turn. SpanID is a stable per-turn id;
// the SAME SpanID is passed to the matching OnModelEnd so listeners can pair
// start/end without threading context.
type ModelInfo struct {
	TaskID    string
	SessionID string
	AgentName string
	Round     int
	SpanID    string
	Messages  int // number of messages sent to the model
	Tools     int // number of tools offered to the model
	// Model is the model name the turn was sent to, so an observer can
	// price it without asking the service.
	Model string
}

// ModelDelta carries a streamed fragment. Kind is "reasoning" or "partial".
type ModelDelta struct {
	SpanID string
	Kind   string
	Text   string
}

// ModelResult summarizes the outcome of a model turn.
type ModelResult struct {
	Content    string
	ToolCalls  int
	DurationMs int64
	TokensUsed int
	// CachedTokens is the prompt-cache hit portion of TokensUsed, when the
	// provider reported one (0 otherwise). Cache hits are billed at a deep
	// discount, so TokensUsed alone overstates what the turn cost.
	CachedTokens int
	// PromptTokens and CompletionTokens are the two halves of TokensUsed,
	// which is all a price list needs: input and output are billed at
	// different rates, and the cached share of the input at a third.
	PromptTokens     int
	CompletionTokens int
}

// ToolInfo identifies a single tool call. CallID is a stable per-call id; the
// SAME CallID is passed to the matching OnToolEnd.
type ToolInfo struct {
	TaskID    string
	SessionID string
	AgentName string
	Tool      string
	CallID    string
	Args      map[string]any
	// Inner is true when the tool executes inside a sub-agent rather than at
	// the top-level runtime loop.
	Inner bool
}

// SubAgentInfo identifies a goal-driven sub-agent run.
type SubAgentInfo struct {
	ParentTaskID string
	SubAgentID   string
	Name         string
	Goal         string
	SessionID    string
}

// LintInfo describes one output-lint rejection.
type LintInfo struct {
	TaskID    string
	SessionID string
	AgentName string
	Round     int
	// Lint is the rejecting lint's name, matching its Name().
	Lint string
	// Reason is the rejection text, written to be read by the model.
	Reason string
	// Retrying is true when the run was re-prompted, false when the retry
	// budget was spent and the run is being blocked on this rejection.
	Retrying bool
}

// ModelRetryInfo describes one re-ask of a model turn.
type ModelRetryInfo struct {
	TaskID    string
	SessionID string
	AgentName string
	Round     int
	// SpanID matches the OnModelStart / OnModelEnd pair this retry happened
	// inside, so a listener can attribute it to the right turn.
	SpanID string
	// Kind is why: "transient_error" or "max_tokens_truncation".
	Kind string
	// Attempt is 1-based within its kind.
	Attempt int
	// Reason carries the provider error text, or the finish_reason that
	// showed the answer had been cut off.
	Reason string
	// MaxTokensFrom / MaxTokensTo are set for a budget escalation and zero
	// otherwise.
	MaxTokensFrom int
	MaxTokensTo   int
	// Delay is how long the runtime waited before re-asking.
	Delay time.Duration
}

// CompactionInfo describes one history-folding step.
type CompactionInfo struct {
	TaskID    string
	SessionID string
	AgentName string
	Round     int
	// Trigger is why compaction ran: the token threshold, or the budget's
	// diminishing-returns signal.
	Trigger string
	// MessagesBefore / MessagesAfter bracket what was folded away.
	MessagesBefore int
	MessagesAfter  int
	// ContextTokens is the runtime's own estimate of the conversation being
	// folded — the number that crossed the threshold. It is an estimate, not
	// the provider's count.
	ContextTokens int
	// EstimatedTokens is the run's cumulative token spend so far. It is a
	// different quantity from ContextTokens and much larger; they were the
	// same field once, and the log line read "est 317280 tokens" while
	// compacting a 25k conversation.
	EstimatedTokens int
}

// ErrorInfo describes one error the runtime reported.
type ErrorInfo struct {
	TaskID    string
	SessionID string
	AgentName string
	Round     int
	// Marker distinguishes sub-kinds that share EventTypeError — "lint_retry",
	// "history_persist_failed", "workflow_error" — and is empty for the rest.
	Marker  string
	Message string
}

// SegmentInfo describes one segment of a long run.
type SegmentInfo struct {
	TaskID string
	// Index is 0-based, matching LongRunResult.Segments.
	Index int
	// Total is the configured segment budget.
	Total int
	// SessionID is this segment's own session; every segment gets a new one.
	SessionID string
	// Ending is false at the start of a segment and true at its end.
	Ending bool
	// The rest are set only when Ending.
	StopReason StopReason
	Duration   time.Duration
	Productive bool
	CostUSD    float64
	Err        string
}

// CheckpointInfo describes a terminal checkpoint snapshot.
type CheckpointInfo struct {
	TaskID    string
	SessionID string
	AgentName string
	Reason    string
	Round     int
	Messages  int
	FinalText string
}

// BaseObserver implements Observer with no-op methods. Embed it so you only
// override the callbacks you actually need:
//
//	type myObs struct{ agent.BaseObserver }
//	func (o *myObs) OnToolStart(ctx context.Context, info agent.ToolInfo) { ... }
type BaseObserver struct{}

func (BaseObserver) OnModelStart(context.Context, ModelInfo)                    {}
func (BaseObserver) OnModelDelta(context.Context, ModelDelta)                   {}
func (BaseObserver) OnModelEnd(context.Context, ModelInfo, *ModelResult, error) {}
func (BaseObserver) OnToolStart(context.Context, ToolInfo)                      {}
func (BaseObserver) OnToolEnd(context.Context, ToolInfo, any, error)            {}
func (BaseObserver) OnSubAgentStart(context.Context, SubAgentInfo)              {}
func (BaseObserver) OnSubAgentEnd(context.Context, SubAgentInfo, any, error)    {}
func (BaseObserver) OnCheckpoint(context.Context, CheckpointInfo)               {}
func (BaseObserver) OnLint(context.Context, LintInfo)                           {}
func (BaseObserver) OnModelRetry(context.Context, ModelRetryInfo)               {}
func (BaseObserver) OnCompaction(context.Context, CompactionInfo)               {}
func (BaseObserver) OnError(context.Context, ErrorInfo)                         {}
func (BaseObserver) OnSegment(context.Context, SegmentInfo)                     {}

// Ensure BaseObserver satisfies the interface.
var _ Observer = BaseObserver{}

// RegisterObserver adds one or more observers to the service. Safe to call
// concurrently and idempotent-friendly (observers are stored by identity, so
// callers should avoid double-registering the same instance).
func (s *Service) RegisterObserver(obs ...Observer) {
	if s == nil {
		return
	}
	s.observersMu.Lock()
	defer s.observersMu.Unlock()
	for _, o := range obs {
		if o != nil {
			s.observers = append(s.observers, o)
		}
	}
}

// Observers returns a snapshot of the registered observers.
func (s *Service) Observers() []Observer {
	if s == nil {
		return nil
	}
	s.observersMu.RLock()
	defer s.observersMu.RUnlock()
	if len(s.observers) == 0 {
		return nil
	}
	out := make([]Observer, len(s.observers))
	copy(out, s.observers)
	return out
}

// emitObserver fans out an invocation to every registered observer. It
// snapshots under RLock and recovers from panics so a single misbehaving
// observer can't crash a run. Nil-safe and zero-overhead when no observers
// are registered.
func (s *Service) emitObserver(fn func(Observer)) {
	if s == nil || fn == nil {
		return
	}
	s.observersMu.RLock()
	if len(s.observers) == 0 {
		s.observersMu.RUnlock()
		return
	}
	snapshot := make([]Observer, len(s.observers))
	copy(snapshot, s.observers)
	s.observersMu.RUnlock()

	for _, o := range snapshot {
		s.invokeObserver(o, fn)
	}
}

// toolObserverInfo builds a ToolInfo for a tool dispatch. CallID is the tool
// call id so OnToolStart / OnToolEnd pair up.
func (s *Service) toolObserverInfo(currentAgent *Agent, session *Session, tc domain.ToolCall) ToolInfo {
	agentName := ""
	if currentAgent != nil {
		agentName = currentAgent.Name()
	}
	sessionID := ""
	if session != nil {
		sessionID = session.GetID()
	}
	return ToolInfo{
		TaskID:    currentTaskID(session),
		SessionID: sessionID,
		AgentName: agentName,
		Tool:      tc.Function.Name,
		CallID:    tc.ID,
		Args:      tc.Function.Arguments,
	}
}

func (s *Service) invokeObserver(o Observer, fn func(Observer)) {
	defer func() {
		if r := recover(); r != nil {
			if s.logger != nil {
				s.logger.Warn("observer panicked", slog.Any("recover", r))
			}
		}
	}()
	fn(o)
}
