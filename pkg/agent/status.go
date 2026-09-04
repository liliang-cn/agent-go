package agent

import (
	"sort"
	"strings"
	"time"
)

// Asking a Service what it is doing, from outside the run.
//
// Everything below already existed somewhere; what did not exist was a way to
// read it without being the caller of the run. A host that wants to draw
// "what is this agent doing right now" had exactly one source — the Event
// stream — and an Event stream has one consumer: whoever called RunStream. A
// status endpoint, an operator's console, a second window, a health check and
// a metrics scrape are all *not* that consumer, and each of them was left to
// either tee the stream itself or guess.
//
// StatusSnapshot is the read side of the loop. It takes a lock and copies; it
// calls no model, touches no store, and (unless asked for process figures)
// does not stop the world. Call it as often as a screen repaints.
//
// Three things it deliberately is not:
//
//   - It is not health. Doctor probes — it dials providers, opens the store,
//     loads skills — and costs accordingly. Status reports what the process
//     already knows.
//   - It is not history. It describes this moment. Runs that ended are gone
//     from it, because they are gone from the registry; their record is the
//     checkpoint and the task store.
//   - It is not a subscription. A host that wants to be told rather than to
//     ask still wants Observer or the event stream. This answers a question.

// ServiceState is the coarse state of a Service, for a caller that wants one
// word before it wants numbers.
type ServiceState string

const (
	// ServiceIdle means the service is usable and carrying no runs.
	ServiceIdle ServiceState = "idle"
	// ServiceRunning means at least one run is in flight.
	ServiceRunning ServiceState = "running"
	// ServiceClosed means Close has run: every entry point now refuses with
	// ErrServiceClosed, and this service will never run anything again.
	ServiceClosed ServiceState = "closed"
)

// AgentStatus is one snapshot of what a Service is and what it is doing.
//
// Every field is JSON-serialisable so a host can hand it straight to an HTTP
// handler, a gRPC message or a log line. See examples/agent-status for both.
type AgentStatus struct {
	// At is when the snapshot was taken.
	At time.Time `json:"at"`
	// State is the one-word answer.
	State ServiceState `json:"state"`
	// Agent is the identity and capability block Info() returns — name,
	// model, base URL, which subsystems are wired up, and the tool names.
	Agent AgentInfo `json:"agent"`
	// Workspace is the sandbox root, empty when the service has no sandbox.
	// It is where a run's files actually land, which is rarely the process's
	// working directory and is the first thing an operator looks for.
	Workspace string `json:"workspace,omitempty"`
	// Lints are the output lints that will judge this agent's final answers,
	// in the order they run.
	Lints []string `json:"lints,omitempty"`

	// Capacity is what the process is carrying and what its ceilings are.
	Capacity Capacity `json:"capacity"`
	// Runs are the runs in flight, oldest first, each with whatever the loop
	// last published about itself.
	Runs []RunStatus `json:"runs,omitempty"`
	// Background summarises detached work; BackgroundTasks() has the detail
	// (including results, which are deliberately not repeated here).
	Background BackgroundSummary `json:"background"`

	// Process is nil unless the caller asked for it with WithProcessStats.
	// Reading it stops the world briefly, so an unwatched service must not
	// pay for it on every scrape.
	Process *ProcessStats `json:"process,omitempty"`
}

// RunUsage is one run's accounting so far, as the loop has it.
type RunUsage struct {
	InputTokens  int `json:"input_tokens,omitempty"`
	OutputTokens int `json:"output_tokens,omitempty"`
	// EstimatedTokens is the loop's own running tally, which exists even for
	// providers that report no usage at all.
	EstimatedTokens int `json:"estimated_tokens,omitempty"`

	// CachedPromptTokens and CacheWriteTokens are the prompt-cache split, and
	// only ever provider-reported.
	CachedPromptTokens int `json:"cached_prompt_tokens,omitempty"`
	CacheWriteTokens   int `json:"cache_write_tokens,omitempty"`
	// ProviderReported says at least one round came back with real
	// accounting. Without it the token counts are the tokenizer's estimate,
	// and a zero cache split means "not measured", not "no hits".
	ProviderReported bool `json:"provider_reported"`

	CostUSD float64 `json:"cost_usd,omitempty"`
	// CostUnpriced says CostUSD is missing at least one turn's spend because
	// nothing could price the model. A host must show "unpriced", never
	// "$0.00" — a budget ceiling built on that number cannot fire either.
	CostUnpriced bool `json:"cost_unpriced,omitempty"`
}

// RunStatus is one run in flight: who owns it, what it is doing, and what it
// has spent.
type RunStatus struct {
	// ActiveRun is the registry's own record — RunID (what CancelRun takes),
	// SessionID, TaskID, StartedAt, Tenant.
	ActiveRun
	// Duration is how long it has been running.
	Duration time.Duration `json:"duration_ns"`

	// Reported is false when the loop has not published a reading yet: the
	// run is registered but has not reached its first stage. Every field
	// below is zero in that case, and a host must show "starting", not
	// "round 0 of 0".
	Reported bool `json:"reported"`
	// UpdatedAt is when the loop last published, so a caller can tell a busy
	// run from one wedged inside a tool that never returns.
	UpdatedAt time.Time `json:"updated_at,omitempty"`

	// AgentName is the agent running this turn — not always the service's
	// own agent, since the loop can hand the turn to another one.
	AgentName string `json:"agent_name,omitempty"`
	// Goal is what the run was asked to do. This is the user's own prompt:
	// a host serving more than one person must scope who is allowed to read
	// a status snapshot before exposing it.
	Goal string `json:"goal,omitempty"`

	// Stage is the loop's turn stage — one of the TurnStage* constants, the
	// same value the event stream carries as turn_stage.
	Stage string `json:"stage,omitempty"`
	// StageReason is the loop's own sentence about why it is there.
	StageReason string `json:"stage_reason,omitempty"`
	// Transition is the last loop transition (tool_batch_executed,
	// lint_retry, …), which says how the run got to this stage.
	Transition string `json:"transition,omitempty"`

	Round     int `json:"round,omitempty"`
	MaxRounds int `json:"max_rounds,omitempty"`
	// PendingTools is how many tool calls this round is carrying.
	PendingTools int `json:"pending_tools,omitempty"`
	// ToolCalls is how many the run has made in total.
	ToolCalls int `json:"tool_calls,omitempty"`
	// Interruptible is false while a tool declaring InterruptBehaviorBlock is
	// mid-execution — which is exactly when CancelRun will refuse, so a host
	// should grey the stop button rather than let it fail.
	Interruptible bool `json:"interruptible"`

	// LintRetriesLeft is how many more times a rejected final answer may be
	// re-prompted before the run is blocked.
	LintRetriesLeft int `json:"lint_retries_left,omitempty"`
	// Compactions and Recoveries are how often the run rewrote its own
	// history, and how often a half-streamed turn had to be rebuilt. Both
	// climbing is the signature of a run in trouble.
	Compactions int `json:"compactions,omitempty"`
	Recoveries  int `json:"recoveries,omitempty"`

	Usage RunUsage `json:"usage"`
}

// BackgroundSummary counts detached work by state. Results are not repeated
// here — a status snapshot is polled, and a finished task's answer can be
// arbitrarily long; BackgroundTask has it.
type BackgroundSummary struct {
	Running   int `json:"running"`
	Completed int `json:"completed"`
	Blocked   int `json:"blocked"`
	Cancelled int `json:"cancelled"`
	Failed    int `json:"failed"`
	// Max is the ceiling set by WithBackgroundTasks; 0 means the agent-facing
	// background tools were never registered.
	Max int `json:"max,omitempty"`
	// RunningIDs names the tasks still in flight, so a host can offer to
	// cancel one without a second call.
	RunningIDs []string `json:"running_ids,omitempty"`
}

// StatusOption tunes what a snapshot includes.
type StatusOption func(*statusOptions)

type statusOptions struct {
	process bool
}

// WithProcessStats includes the process figures (heap, goroutines, RSS, CPU)
// in the snapshot. Off by default: reading them stops the world briefly, and
// a status endpoint scraped every second must not make a service pay for
// numbers nobody asked for.
func WithProcessStats() StatusOption {
	return func(o *statusOptions) { o.process = true }
}

// StatusSnapshot returns what this service is and is doing right now.
//
// Safe on a nil Service (returns a closed-looking zero snapshot) and safe to
// call concurrently with runs, from any goroutine.
func (s *Service) StatusSnapshot(opts ...StatusOption) AgentStatus {
	status := AgentStatus{At: time.Now(), State: ServiceClosed}
	if s == nil {
		return status
	}
	var o statusOptions
	for _, opt := range opts {
		if opt != nil {
			opt(&o)
		}
	}

	status.Agent = s.Info()
	status.Workspace = s.workspaceRoot()
	if lints := s.OutputLints(); lints != nil {
		status.Lints = lints.Names(status.Agent.Name)
	}
	status.Capacity = s.Capacity()
	status.Runs = s.RunStatuses()
	status.Background = s.backgroundSummary()

	switch {
	case s.Closed():
		status.State = ServiceClosed
	case len(status.Runs) > 0:
		status.State = ServiceRunning
	default:
		status.State = ServiceIdle
	}

	if o.process {
		p := SampleProcess()
		status.Process = &p
	}
	return status
}

// RunStatuses returns one RunStatus per run in flight, oldest first. It is the
// same ordering ActiveRuns uses, with the loop's own reading attached.
func (s *Service) RunStatuses() []RunStatus {
	if s == nil {
		return nil
	}
	now := time.Now()

	s.cancelMu.RLock()
	out := make([]RunStatus, 0, len(s.runs))
	seqs := make(map[string]uint64, len(s.runs))
	for id, h := range s.runs {
		out = append(out, h.status(now))
		seqs[id] = h.seq
	}
	s.cancelMu.RUnlock()

	sort.Slice(out, func(i, j int) bool { return seqs[out[i].RunID] < seqs[out[j].RunID] })
	return out
}

// RunStatus returns one run's status by id, and whether it was found. A run
// that has ended is not found: it left the registry when its event stream
// closed, which is the same rule CancelRun follows.
func (s *Service) RunStatus(runID string) (RunStatus, bool) {
	if s == nil {
		return RunStatus{}, false
	}
	runID = strings.TrimSpace(runID)
	if runID == "" {
		return RunStatus{}, false
	}
	s.cancelMu.RLock()
	h := s.runs[runID]
	s.cancelMu.RUnlock()
	if h == nil {
		return RunStatus{}, false
	}
	return h.status(time.Now()), true
}

// backgroundSummary counts the detached work this service remembers.
func (s *Service) backgroundSummary() BackgroundSummary {
	summary := BackgroundSummary{Max: s.maxBackgroundTasks}
	reg := s.background()
	if reg == nil {
		return summary
	}
	reg.mu.RLock()
	defer reg.mu.RUnlock()
	for id, t := range reg.tasks {
		switch t.Status {
		case BackgroundRunning:
			summary.Running++
			summary.RunningIDs = append(summary.RunningIDs, id)
		case BackgroundCompleted:
			summary.Completed++
		case BackgroundBlocked:
			summary.Blocked++
		case BackgroundCancelled:
			summary.Cancelled++
		case BackgroundFailed:
			summary.Failed++
		}
	}
	sort.Strings(summary.RunningIDs)
	return summary
}

// --- what the loop publishes ------------------------------------------------

// runProgress is the mutable half of a run's status: everything the loop knows
// and the registry does not. It is stored behind an atomic pointer and only
// ever replaced whole, so a reader can never observe half an update and a
// writer never contends with a reader.
type runProgress struct {
	updatedAt       time.Time
	agentName       string
	goal            string
	stage           string
	stageReason     string
	transition      string
	round           int
	maxRounds       int
	pendingTools    int
	toolCalls       int
	interruptible   bool
	lintRetriesLeft int
	compactions     int
	recoveries      int
	usage           RunUsage
}

// status assembles the run's public snapshot from the registry record plus
// whatever the loop last published.
func (h *runHandle) status(now time.Time) RunStatus {
	out := RunStatus{ActiveRun: h.ActiveRun, Duration: now.Sub(h.ActiveRun.StartedAt)}
	p := h.progress.Load()
	if p == nil {
		return out
	}
	out.Reported = true
	out.UpdatedAt = p.updatedAt
	out.AgentName = p.agentName
	out.Goal = p.goal
	out.Stage = p.stage
	out.StageReason = p.stageReason
	out.Transition = p.transition
	out.Round = p.round
	out.MaxRounds = p.maxRounds
	out.PendingTools = p.pendingTools
	out.ToolCalls = p.toolCalls
	out.Interruptible = p.interruptible
	out.LintRetriesLeft = p.lintRetriesLeft
	out.Compactions = p.compactions
	out.Recoveries = p.recoveries
	out.Usage = p.usage
	return out
}

// publishRunProgress records what a run is doing, for readers who are not the
// caller of that run. A run id the registry does not know — a sub-agent, which
// runs under its parent and is deliberately never registered — is a no-op.
func (s *Service) publishRunProgress(runID string, p runProgress) {
	if s == nil {
		return
	}
	runID = strings.TrimSpace(runID)
	if runID == "" {
		return
	}
	s.cancelMu.RLock()
	h := s.runs[runID]
	s.cancelMu.RUnlock()
	if h == nil {
		return
	}
	h.progress.Store(&p)
}

// amendRunProgress replaces a run's reading with a modified copy of the one
// before it. Only the run's own loop goroutine publishes for a given run id,
// so read-modify-write here has a single writer by construction; readers see
// either the old reading or the new one and never half of either.
func (s *Service) amendRunProgress(runID string, fn func(*runProgress)) {
	if s == nil || fn == nil {
		return
	}
	runID = strings.TrimSpace(runID)
	if runID == "" {
		return
	}
	s.cancelMu.RLock()
	h := s.runs[runID]
	s.cancelMu.RUnlock()
	if h == nil {
		return
	}
	next := runProgress{}
	if prev := h.progress.Load(); prev != nil {
		next = *prev
	}
	fn(&next)
	next.updatedAt = time.Now()
	h.progress.Store(&next)
}

// publishStage records a stage announcement that carries no loop state — the
// two phases before the first round, and the completed marker after the last.
// It amends rather than replaces: a bare announcement must not blank out the
// round count and the spend the previous full reading carried.
func (r *Runtime) publishStage(stage, reason string, round, toolCount int) {
	if r == nil || r.svc == nil {
		return
	}
	r.svc.amendRunProgress(r.runID(), func(p *runProgress) {
		p.stage = stage
		p.stageReason = reason
		p.interruptible = !r.svc.hasBlockingToolInProgress()
		if p.agentName == "" {
			p.agentName = r.currentAgentName()
		}
		if p.goal == "" {
			p.goal = r.goal
		}
		if round > 0 {
			p.round = round
		}
		p.pendingTools = toolCount
	})
}

// publishProgress is the loop's side of that: one reading assembled from the
// state it already keeps.
//
// It is called wherever the loop announces a stage change to the event stream,
// and once more when a round closes — the point at which the round's tokens
// and cost have landed. Publishing happens BEFORE the event send, so a status
// reader stays current even when the event consumer has stopped reading.
func (r *Runtime) publishProgress(state *queryLoopState) {
	if r == nil || r.svc == nil || state == nil {
		return
	}
	r.svc.publishRunProgress(r.runID(), runProgress{
		updatedAt:       time.Now(),
		agentName:       r.currentAgentName(),
		goal:            state.Goal,
		stage:           state.Stage,
		stageReason:     state.TransitionReason,
		transition:      state.LoopTransition,
		round:           state.CurrentRound,
		maxRounds:       state.Budget.MaxRounds,
		pendingTools:    state.PendingToolCount,
		toolCalls:       state.TotalToolCalls,
		interruptible:   !r.svc.hasBlockingToolInProgress(),
		lintRetriesLeft: r.lintRetryBudget,
		compactions:     state.Budget.CompactionCount,
		recoveries:      state.Budget.RecoveryCount,
		usage: RunUsage{
			InputTokens:        state.Budget.InputTokens,
			OutputTokens:       state.Budget.OutputTokens,
			EstimatedTokens:    state.Budget.EstimatedTokens,
			CachedPromptTokens: state.Budget.CachedPromptTokens,
			CacheWriteTokens:   state.Budget.CacheWriteTokens,
			ProviderReported:   state.Budget.ProviderReportedUsage,
			CostUSD:            state.Budget.EstimatedCostUSD,
			CostUnpriced:       r.warnedUnpriced,
		},
	})
}
