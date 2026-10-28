package agent

import (
	"context"
	"log/slog"
	"time"

	"github.com/liliang-cn/agent-go/v3/pkg/domain"
	"github.com/liliang-cn/agent-go/v3/pkg/trace"
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
//
// SpanID is a W3C span id (16 hex digits) in the run's flow: the ctx handed
// to OnModelStart carries the run's span (trace.SpanFromContext), which is
// this call's parent.
//
// Round is the round in progress when the call STARTED, on both callbacks —
// 0 for a call made before the first round (constraint extraction, a memory
// pick). A call on the run's behalf can outlive the round it began in; its
// end used to report the round it finished in, so its start and end lines
// disagreed.
type ModelInfo struct {
	TaskID string
	// RunID is the id the run is registered under, the one CancelRun takes.
	// It is what separates two runs sharing a session.
	RunID     string
	SessionID string
	AgentName string
	Round     int
	SpanID    string
	Messages  int // number of messages sent to the model
	Tools     int // number of tools offered to the model
	// Model is the model name the turn was sent to, so an observer can
	// attribute its tokens without asking the service.
	Model string
	// Purpose is what the call was for: "turn" for the model answering, or
	// one made on the run's behalf — "constraints", "memory-pick",
	// "compaction", "summary", "final-synthesis" and so on. Those used to be
	// invisible to observers, which is where a slow start hid.
	Purpose string
	// Request is what the model was sent, when the service was built with
	// WithObserverPayloads(); nil otherwise. Messages and Tools above are
	// counts and are always filled. For the answering turn and the final
	// synthesis it is set on OnModelStart; for a call made on the run's
	// behalf (constraints, memory picks, compaction) it is only known by
	// OnModelEnd, so an observer that wants every request reads it there.
	// It is shared with the runtime, not copied: read it, do not change it.
	Request *ModelRequest
}

// ModelRequest is the content of one model call: the message list of a
// chat turn with the tools offered beside it, or the prompt of a structured
// call. Exactly one of Messages and Prompt is set.
//
// Observers see counts and output by default and never the input, which was
// the one thing missing when a run went wrong: a Phoenix trace showed every
// answer and no prompt, so "what did the model see in round 7" meant
// reproducing round 7. This carries it. It is off by default because a
// 60k-token context repeated on every round is most of a trace's bytes, and
// because a request holds whatever the user typed and whatever a tool read:
// a host that exports it owns where it goes.
type ModelRequest struct {
	Messages []domain.Message
	Tools    []domain.ToolDefinition
	Prompt   string
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
	// provider reported one (0 otherwise).
	CachedTokens int
	// CacheWriteTokens is the prompt tokens written to the provider's cache
	// on this call (Anthropic's cache_creation_input_tokens), 0 otherwise.
	// A cache that is written every round and never read is the expensive
	// failure, and it shows only with both numbers.
	CacheWriteTokens int
	// PromptTokens and CompletionTokens are the two halves of TokensUsed;
	// a host that bills by token needs them apart.
	PromptTokens     int
	CompletionTokens int
	// TTFTMs is how long before the first output arrived, for a streamed
	// call; 0 when unknown or when the call did not stream.
	TTFTMs int64
	// Streamed is true when the answer arrived as a stream. A call that did
	// not stream has no time to first output worth the name.
	Streamed bool
	// ServerTools is what the provider did on its side of the call — web
	// searches its server tool ran, resumes of a paused turn, per-request
	// durations, citations — when it reported any; nil otherwise.
	ServerTools *domain.ServerToolReport
	// Model and Provider are what actually answered, when the provider said
	// (a routed run or a pool fallback can differ from ModelInfo.Model, which
	// is what was asked for). Empty when unknown.
	Model    string
	Provider string
	// Response is the rest of what the model answered, when the service was
	// built with WithObserverPayloads; nil otherwise. Content above is the
	// text, always filled; ToolCalls above is only a count.
	Response *ModelResponse
}

// ModelResponse is the model's side of one call beyond its text: the tool
// calls it asked for with their arguments, what it reasoned, and why it
// stopped. With ModelRequest it is the whole exchange, so a trace can say not
// only what the model saw in round 7 but what it decided there.
type ModelResponse struct {
	Reasoning    string
	ToolCalls    []domain.ToolCall
	FinishReason string
}

// StepInfo is a timed step that is not a model or tool call: Kind is
// "memory" (recall, rag, skills), Name says which.
type StepInfo struct {
	TaskID     string
	RunID      string
	SessionID  string
	Kind       string
	Name       string
	DurationMs int64
	Err        string
}

// StepObserver is an optional extension: an Observer that also implements it
// hears the run's memory lookups and other timed steps.
type StepObserver interface {
	OnStep(ctx context.Context, info StepInfo)
}

// ToolInfo identifies a single tool call. CallID is a stable per-call id; the
// SAME CallID is passed to the matching OnToolEnd.
type ToolInfo struct {
	TaskID    string
	RunID     string
	SessionID string
	AgentName string
	Tool      string
	CallID    string
	Args      map[string]any
	// Inner is true when the tool executes inside a sub-agent rather than at
	// the top-level runtime loop.
	Inner bool
	// SpanID is the call's W3C span id, a child of the span on the ctx the
	// callback receives. The tool runs under it, so a sub-agent it starts,
	// an MCP request it makes, or a model call inside it is its child.
	SpanID string
	// Remote is set on OnToolEnd: the traceparents other processes answered
	// with — an MCP server's or an HTTP service's own span for the work —
	// so the flow's tree continues into them.
	Remote []string
}

// SubAgentInfo identifies a goal-driven sub-agent run.
type SubAgentInfo struct {
	ParentTaskID string
	// RunID is the PARENT run's id: a sub-agent runs under its parent's
	// context and is deliberately not registered separately.
	RunID      string
	SubAgentID string
	Name       string
	Goal       string
	SessionID  string
	// Kind says what sort of sub-agent this is. Empty — the zero value every
	// existing caller produces — means the in-process kind: another Runtime on
	// this Service, whose tokens already land in the parent's own accounting.
	// "cli" means the work was handed to an agent CLI installed on the
	// machine, which is a separate process on a separate account, and an
	// observer adding up what a run used must not fold the two together.
	Kind string
	// Provider names the thing that actually ran the work when Kind is not the
	// in-process one — "claude", "codex", "gemini", "cursor-agent". It is
	// empty for an ordinary sub-agent, whose provider is the service's own.
	Provider string
}

// LintInfo describes one output-lint rejection.
type LintInfo struct {
	TaskID    string
	RunID     string
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
	RunID     string
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
	RunID     string
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

	// ContextTokensAfter is the same measure as ContextTokens, after this step.
	ContextTokensAfter int
	// Threshold is the context size the run compacts at, and ThresholdSource
	// where it came from: CompactionThresholdConfigured,
	// CompactionThresholdModelWindow, or CompactionThresholdDefault — the
	// last meaning nothing knew the model's window and the fixed default was
	// used.
	Threshold       int
	ThresholdSource string
	// Mode is how the history was shrunk: CompactionModeClip (old tool
	// results replaced by stubs, no model call), CompactionModeSummary (a
	// model-written summary), or CompactionModeFallback (a mechanical summary,
	// because the summariser kept failing).
	Mode string
	// ClippedResults is how many tool results were replaced by stubs.
	ClippedResults int
	// TrimmedResults is how many oversized recent tool results were cut to
	// their head and tail.
	TrimmedResults int
	// NoProgress is true when the step freed less than a tenth of the
	// context; compaction then holds off until the context grows again.
	NoProgress bool
	// SummaryCalls is how many summariser calls this step made (0 or 1).
	SummaryCalls int
	// SummaryError is the summariser's error when it was asked and failed.
	SummaryError string
	// Degraded is true when the history was folded without a model summary.
	Degraded bool
}

// ErrorInfo describes one error the runtime reported.
type ErrorInfo struct {
	TaskID    string
	RunID     string
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
	Err        string
}

// CheckpointInfo describes a terminal checkpoint snapshot.
type CheckpointInfo struct {
	TaskID    string
	RunID     string
	SessionID string
	AgentName string
	Reason    string
	Round     int
	Messages  int
	FinalText string
}

// RunStartInfo identifies one run — one pass of the loop, a top-level Run or a
// sub-agent's — and its place in the flow.
type RunStartInfo struct {
	TaskID    string
	RunID     string
	SessionID string
	AgentName string
	Model     string
	// TraceID, SpanID and ParentSpanID are the run's W3C span: TraceID is
	// the flow, shared by every run it fanned out into; ParentSpanID is the
	// span that started this run (a tool call for a sub-agent, a remote
	// caller's span for a flow that arrived over a traceparent), empty for
	// the root.
	TraceID      string
	SpanID       string
	ParentSpanID string
	// Goal is what the run was asked; observers clip it.
	Goal string
}

// RunEndInfo is how a run ended.
type RunEndInfo struct {
	RunStartInfo
	// Outcome is "completed", "blocked", "cancelled" or "failed".
	Outcome    string
	StopReason StopReason
	Duration   time.Duration
	Rounds     int
	Err        string
}

// RunObserver is an optional extension: an Observer that implements it hears
// each run begin and end, on every terminal path. It is optional rather than
// two more Observer methods for the reason ResourceObserver is: hosts
// implement Observer in full, and a new method would break each one that
// does not embed BaseObserver.
//
// The methods are not named OnRunStart/OnRunEnd because RunLifecycle, which
// an extension implements, already has those with other signatures.
type RunObserver interface {
	OnRunBegin(ctx context.Context, info RunStartInfo)
	OnRunFinish(ctx context.Context, info RunEndInfo)
}

// RuntimeDecisionInfo is a choice the runtime made that changes what the model sees
// or what is sent for it — MCP search tools hidden because native search has
// proved itself, a server tool dropped for a function tool of its name, a
// round sent again without native search, tools deferred behind the search
// or kept eager. Each is reported once per run for as long as it holds, and
// again when it changes.
//
// It is not DecisionInfo, which is a decision gate's consultation.
type RuntimeDecisionInfo struct {
	TaskID    string
	RunID     string
	SessionID string
	AgentName string
	Round     int
	What      string
	Reason    string
	Detail    map[string]any
}

// RuntimeDecisionObserver is an optional extension: an Observer that
// implements it hears runtime decisions.
type RuntimeDecisionObserver interface {
	OnRuntimeDecision(ctx context.Context, info RuntimeDecisionInfo)
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
	// The run narration into the service's logger hears everything first.
	// It is not in the registered list (Observers() is the host's), and it
	// asks its logger before doing any work.
	if s.runLog != nil {
		s.invokeObserver(s.runLog, fn)
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
func (s *Service) toolObserverInfo(ctx context.Context, currentAgent *Agent, session *Session, tc domain.ToolCall) ToolInfo {
	agentName := ""
	if currentAgent != nil {
		agentName = currentAgent.Name()
	}
	sessionID := ""
	if session != nil {
		sessionID = session.GetID()
	}
	info := ToolInfo{
		TaskID:    currentTaskID(session),
		RunID:     currentRunID(ctx),
		SessionID: sessionID,
		AgentName: agentName,
		Tool:      tc.Function.Name,
		CallID:    tc.ID,
		Args:      tc.Function.Arguments,
	}
	if parent, ok := trace.SpanFromContext(ctx); ok {
		info.SpanID = trace.ChildSpanID(parent, tc.ID)
	}
	return info
}

// toolSpanContext is the context a tool call runs under: its own span, a
// child of the one on ctx, and a collector for the spans other processes
// answer with. done returns those spans once the call is over.
func toolSpanContext(ctx context.Context, info ToolInfo) (context.Context, func() []string) {
	parent, ok := trace.SpanFromContext(ctx)
	if !ok || info.SpanID == "" {
		return ctx, func() []string { return nil }
	}
	ctx = trace.ContextWithSpan(ctx, trace.SpanContext{TraceID: parent.TraceID, SpanID: info.SpanID,
		ParentSpanID: parent.SpanID, TraceState: parent.TraceState})
	ctx, links := trace.WithLinks(ctx)
	return ctx, links.List
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
