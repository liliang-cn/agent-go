package agent

import (
	"fmt"
	"strings"
	"time"

	"github.com/liliang-cn/agent-go/v3/pkg/domain"
)

// Step status constants
const (
	StepStatusPending   = "pending"
	StepStatusRunning   = "running"
	StepStatusCompleted = "completed"
	StepStatusFailed    = "failed"
	StepStatusSkipped   = "skipped"

	// Convenience aliases for UI compatibility
	StepPending   = StepStatusPending
	StepRunning   = StepStatusRunning
	StepCompleted = StepStatusCompleted
	StepFailed    = StepStatusFailed
	StepSkipped   = StepStatusSkipped
)

// Plan status constants
const (
	PlanStatusPending   = "pending"
	PlanStatusRunning   = "running"
	PlanStatusCompleted = "completed"
	PlanStatusFailed    = "failed"

	// Convenience aliases for UI compatibility
	StatusPending   = PlanStatusPending
	StatusRunning   = PlanStatusRunning
	StatusCompleted = PlanStatusCompleted
	StatusFailed    = PlanStatusFailed
)

// Step represents a single step in an agent's execution plan
type Step struct {
	ID          string                 `json:"id"`
	Description string                 `json:"description"`
	Tool        string                 `json:"tool"`
	Arguments   map[string]interface{} `json:"arguments,omitempty"`
	Status      string                 `json:"status"`
	Result      interface{}            `json:"result,omitempty"`
	Error       string                 `json:"error,omitempty"`
	DependsOn   []string               `json:"depends_on,omitempty"`  // IDs of steps this step depends on
	OutputFile  string                 `json:"output_file,omitempty"` // Write result to this file
	StartedAt   *time.Time             `json:"started_at,omitempty"`
	CompletedAt *time.Time             `json:"completed_at,omitempty"`
}

// Plan represents an agent's execution plan for a goal
type Plan struct {
	ID        string    `json:"id"`
	Goal      string    `json:"goal"`
	SessionID string    `json:"session_id"`
	Steps     []Step    `json:"steps"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	Status    string    `json:"status"`
	Error     string    `json:"error,omitempty"`
	Reasoning string    `json:"reasoning,omitempty"` // LLM's reasoning for the plan
}

// ExecutionResult represents the result of an agent execution
type ExecutionResult struct {
	PlanID    string `json:"plan_id"`
	SessionID string `json:"session_id"`
	TaskID    string `json:"task_id,omitempty"`
	Success   bool   `json:"success"`
	// Blocked reports that the agent ran fine and concluded it could not
	// proceed, explaining why in Text(). That is an outcome, not a failure:
	// Err() stays nil so a caller doing `if err != nil { return }` cannot
	// silently discard the explanation. Check Blocked (or Success) to branch.
	Blocked bool `json:"blocked,omitempty"`
	// Cancelled reports that the run's context was cancelled before it
	// finished. Like Blocked this is an outcome, not a failure — Err() stays
	// nil and Text() holds whatever had been produced — so a caller that
	// branches on err cannot mistake its own stop button for a crash.
	Cancelled       bool       `json:"cancelled,omitempty"`
	StepsTotal      int        `json:"steps_total"`
	StepsDone       int        `json:"steps_done"`
	StepsFailed     int        `json:"steps_failed"`
	StartedAt       *time.Time `json:"started_at,omitempty"`
	CompletedAt     *time.Time `json:"completed_at,omitempty"`
	ToolCalls       int        `json:"tool_calls"`
	ToolsUsed       []string   `json:"tools_used,omitempty"`
	EstimatedTokens int        `json:"estimated_tokens"`
	// Usage is what the provider actually billed, summed over the run's
	// rounds — including the prompt-cache split, which is the only way to
	// answer "is this long run re-reading its whole prompt every round".
	// Nil when no provider on the run reported usage; EstimatedTokens is
	// the tokenizer's guess and is always populated.
	// StopReason is why the run ended, copied from its terminal event. It is
	// the difference between "the model finished" and "the round budget ran
	// out and the runtime synthesised an answer from what it had" — both of
	// which report Success, and only one of which is an answer to trust.
	StopReason StopReason `json:"stop_reason,omitempty"`
	// EstimatedCostUSD is the run's cost estimate, copied from its terminal
	// event. RunConfig.MaxBudgetUSD enforces a cap on it; a caller that wants
	// to know what a run actually cost, or to budget across many runs, needs
	// to be able to read it back.
	EstimatedCostUSD float64                   `json:"estimated_cost_usd,omitempty"`
	Usage            *domain.TokenUsage        `json:"usage,omitempty"`
	FinalResult      interface{}               `json:"final_result,omitempty"`
	Sources          []domain.Chunk            `json:"sources,omitempty"`      // RAG sources when EnableRAG is true
	Memories         []*domain.MemoryWithScore `json:"memories,omitempty"`     // Retrieved long-term memories
	MemoryLogic      string                    `json:"memory_logic,omitempty"` // IndexNavigator's reasoning for memory selection
	Error            string                    `json:"error,omitempty"`
	Duration         string                    `json:"duration"`
	Metadata         map[string]interface{}    `json:"metadata,omitempty"`
}

// AgentInfo contains information about an agent's status and configuration
type AgentInfo struct {
	ID            string   `json:"id"`
	Name          string   `json:"name"`
	Status        string   `json:"status"` // "running", "idle"
	Model         string   `json:"model,omitempty"`
	BaseURL       string   `json:"base_url,omitempty"`
	FastModel     bool     `json:"fast_model,omitempty"`
	Debug         bool     `json:"debug"`
	RAGEnabled    bool     `json:"rag_enabled"`
	MemoryEnabled bool     `json:"memory_enabled"`
	MCPEnabled    bool     `json:"mcp_enabled"`
	SkillsEnabled bool     `json:"skills_enabled"`
	Tools         []string `json:"tools,omitempty"`
}

// Text returns the agent's text response as a plain string.
// This is the idiomatic accessor for library integrations — use this
// instead of type-asserting or fmt.Sprintf'ing FinalResult.
//
// formatted result of the code execution (e.g. return values and logs).
//
//	result, err := svc.Chat(ctx, "Hello")
//	fmt.Println(result.Text())
func (r *ExecutionResult) Text() string {
	if r == nil {
		return ""
	}

	if s, ok := r.FinalResult.(string); ok {
		return s
	}
	if r.FinalResult != nil {
		return fmt.Sprintf("%v", r.FinalResult)
	}
	return ""
}

// Err returns the execution error as a Go error, or nil on success.
// Useful for pipeline-style integrations where the caller only checks errors.
//
//	result, err := svc.Run(ctx, goal)
//	if err := result.Err(); err != nil {
//	    return err
//	}
func (r *ExecutionResult) Err() error {
	if r == nil || r.Error == "" {
		return nil
	}
	// A blocked run is a considered answer ("I can't, because X"), not a
	// failure to execute. Reporting it as an error made every caller that
	// checks err first throw the explanation away — which is how 6 of 50
	// benchmark tasks came back with nothing at all for the user to read.
	if r.Blocked {
		return nil
	}
	// Same reasoning for a stop: the caller asked for it, so reporting it as
	// an error would turn "you pressed cancel" into a red failure.
	if r.Cancelled {
		return nil
	}
	return fmt.Errorf("%s", r.Error)
}

// HasSources reports whether the result contains RAG source documents.
func (r *ExecutionResult) HasSources() bool {
	return r != nil && len(r.Sources) > 0
}

// ============================================================
// RunConfig - Configuration for agent runs
// ============================================================

// RunConfig holds configuration for a single agent run
type RunConfig struct {
	// MaxTurns limits the number of agent loop iterations. Zero means the
	// run has no opinion: the service's WithAutonomy budget applies, and
	// failing that DefaultMaxRounds. Set it with WithMaxTurns.
	MaxTurns int

	// PriorToolCalls names tools that earlier segments of the same task already
	// called. Contract lints ask "the user asked for this and you never did
	// it", and a segment is not the task — an action carried out in segment
	// zero is still carried out when segment five is checked. RunSegments
	// fills this in; a single run leaves it empty. Set it with
	// WithPriorToolCalls.
	PriorToolCalls []string

	// MaxLLMRetries is how many extra attempts a transient provider failure
	// (a 502, a rate limit, a dropped stream) gets before the run gives up.
	// Zero means the run has no opinion and the framework default applies;
	// a negative value means none at all. Set it with WithLLMRetries.
	MaxLLMRetries int

	// ErrorHandlers allows custom handling of specific error conditions
	// Key: error kind (e.g., "max_turns")
	// Value: function that returns a fallback result
	ErrorHandlers map[string]ErrorHandlerFunc

	// Temperature for LLM generation
	Temperature float64

	// MaxTokens for LLM generation
	MaxTokens int

	// Debug enables verbose logging
	Debug bool

	// ToolAllowlist, when non-empty, restricts this run to the named tools.
	// ToolDenylist removes named tools. Both are applied by the single loop's
	// turn preparation, which is how sub-agents get a narrower tool surface
	// without needing an execution path of their own.
	ToolAllowlist []string
	ToolDenylist  []string

	// recalledContext is filled by the runtime at run start when a RunMemory
	// is attached: it is appended to the system prompt under a
	// "Recalled context" heading on every turn of this run. Kept on the run
	// config (not the service) so concurrent runs cannot see each other's
	// recall.
	recalledContext string

	// resumedPlan is filled by the runtime at run start with the summary of
	// whatever plan the PlanStore already holds for this task — what an
	// earlier, interrupted run got through and what each finished step turned
	// out to be. Empty when there is no plan or nothing has been attempted.
	// Injected alongside recalledContext at the end of the system prompt.
	resumedPlan string

	// resumedTask is filled by the runtime at run start with what the
	// TaskStore remembers about this task — the resume brief, how earlier
	// runs ended, the lessons they left. Empty for a task the store has
	// never seen. Injected alongside resumedPlan.
	resumedTask string

	// resumedWorkspace is filled at run start with an inventory of the sandbox
	// workspace — the files an earlier segment left behind. On a coding task
	// those files are most of the state, and a segment that starts a fresh
	// session has no other way to know they exist.
	resumedWorkspace string

	// resumedNotes is filled at run start with the contents of the workspace
	// notes file — the one file whose text, not just its name, is carried
	// across segments. See notes_handoff.go.
	resumedNotes string

	// ToolsDisabled attaches no tools at all to this run. Set it directly with
	// WithToolsDisabled() when the caller already knows tools are off limits;
	// the runtime also sets it from extracted constraints when the user's own
	// request refused tool use.
	ToolsDisabled bool

	// RequiredDeliverables are side effects this run must actually perform
	// before it may complete. Declaring them with WithRequiredDeliverables()
	// skips constraint extraction entirely.
	RequiredDeliverables []DeliverableRequirement

	// RequiredActions are tool actions the user asked this run to carry out
	// (set a reminder, add a schedule, record a note). Declaring them with
	// WithRequestedActions() skips constraint extraction entirely.
	RequiredActions []RequestedAction

	// DisableConstraintExtraction turns off the per-run structured pass that
	// derives constraints from the goal. With it off, only constraints the
	// caller declared outright are enforced.
	DisableConstraintExtraction bool

	// resolvedConstraints caches the outcome for the whole run so the loop
	// pays for the extraction once, not once per round.
	resolvedConstraints *RunConstraints

	// SystemPromptOverride replaces the agent's instructions for this run.
	SystemPromptOverride string

	// RunID names this run in the service's in-flight registry so
	// Service.CancelRun can stop exactly this one. Empty means the runtime
	// generates a UUID, which Service.ActiveRuns then reports. Set it with
	// WithRunID when the host wants to hold the handle itself (a UI that
	// tags a request before the run has started, say).
	RunID string

	// SessionID specifies a session ID for multi-turn conversations
	SessionID string

	// TaskID identifies the execution task boundary inside a session.
	TaskID string
	// ParentTaskID links nested/child task invocations back to their caller task.
	ParentTaskID string

	// Inherited memory scope lets a delegated run keep the caller's durable memory namespace
	// without reusing the same session ID.
	InheritedMemoryAgentID string
	InheritedMemoryTeamID  string
	InheritedMemoryUserID  string

	// Stream enables streaming mode for real-time events

	// ResumeMessages, when non-empty, makes the runtime skip its normal
	// initial-history assembly and instead use these messages as the
	// task's starting point. Used by Tasks().Resume to rebuild a run
	// from a TaskCheckpoint snapshot. The runtime treats the last
	// message as already-delivered context and prompts the model for
	// the next round.
	ResumeMessages []domain.Message

	// InputParts, when non-empty, are appended to the fresh run's initial
	// user message as structured multimodal content blocks (e.g. images) so
	// vision-capable providers see them directly. The goal text is fronted as
	// a text part. Ignored on resume runs.
	InputParts []domain.MessagePart

	// Thinking, when non-nil, is forwarded to the provider as the
	// `thinking` request field (DeepSeek v4 + reasoner-compatible
	// providers). Use WithThinking(false) to disable chain-of-thought
	// on tool-heavy / latency-sensitive runs. Nil = leave provider
	// default in place.
	Thinking *domain.ThinkingOptions

	// StructuredOutput, when non-nil, constrains the model's final answer
	// to a JSON schema. Tier B (native response_format) is sent on every
	// LLM call so the provider can fast-path compliant providers; Tier A
	// (post-validation lint) runs on the final answer and re-prompts on
	// invalid output, bounded by lintRetryBudget. Use WithStructuredOutput
	// or WithStructuredOutputType to set this.
	StructuredOutput *StructuredOutputSpec

	// Compaction controls auto-compaction. When the message history's
	// estimated token count exceeds CompactionThresholdTokens (or the
	// per-turn diminishing-returns signal fires), the runtime collapses
	// older history into a summary and continues. CompactionKeepRecent
	// is the number of most-recent rounds preserved verbatim (default 6).
	// Zero values fall back to defaults; use WithAutoCompaction to set.
	CompactionThresholdTokens int
	CompactionKeepRecent      int
	DisableAutoCompaction     bool
	// PlanKey is the scratchpad list this run's plan lives under. Empty means
	// the scratchpad's own default. RunSegments sets it so every segment of a
	// task reads and writes one list, scoped to the task.
	PlanKey string

	// MaxBudgetUSD caps the estimated cumulative cost of the run in
	// USD (input + output tokens × model pricing). When exceeded the
	// runtime stops with StopReasonMaxBudgetUSD. Zero = unlimited. Use
	// WithMaxBudgetUSD to set.
	MaxBudgetUSD float64
}

// ErrorHandlerFunc handles errors during agent execution
type ErrorHandlerFunc func(ErrorHandlerInput) ErrorHandlerResult

// ErrorHandlerInput provides context for error handling
type ErrorHandlerInput struct {
	// Kind of error (e.g., "max_turns")
	Kind string
	// Current round number
	Round int
	// MaxTurns limit
	MaxTurns int
	// Messages in conversation so far
	MessageCount int
	// Original goal
	Goal string
}

// ErrorHandlerResult specifies how to handle the error
type ErrorHandlerResult struct {
	// FinalOutput to return instead of error
	FinalOutput interface{}
	// IncludeInHistory determines if the fallback output is added to conversation
	IncludeInHistory bool
	// Error to return (if FinalOutput is nil)
	Error error
}

// DefaultRunConfig returns the default run configuration
func DefaultRunConfig() *RunConfig {
	return &RunConfig{
		// Left unset on purpose: the budget is resolved per run, so that a
		// service configured for long-horizon work (WithAutonomy) is not
		// silently capped at the interactive default by a config the caller
		// never touched. See resolveMaxRounds.
		MaxTurns: 0,
		// Unset for the same reason, and it was not: a hardcoded 2000 here
		// shadowed defaultRunMaxTokens entirely, so raising that constant
		// changed nothing a run could see. Two defaults for one knob, the
		// nearer one silently winning — the same shape as MaxTurns above.
		MaxTokens:   0,
		Temperature: 0.3,
		Debug:       false,
	}
}

// RunOption modifies RunConfig
type RunOption func(*RunConfig)

// WithMaxTurns sets this run's tool-round budget, overriding both the
// service's WithAutonomy setting and DefaultMaxRounds. A long-horizon run
// wants hundreds; n <= 0 leaves the budget unset.
func WithMaxTurns(n int) RunOption {
	return func(c *RunConfig) { c.MaxTurns = n }
}

// WithPriorToolCalls tells this run which tools earlier stretches of the same
// task already called, so a contract lint judges the task rather than the
// segment. RunSegments sets it; callers driving their own segments should too.
func WithPriorToolCalls(names []string) RunOption {
	return func(c *RunConfig) { c.PriorToolCalls = append([]string(nil), names...) }
}

// WithLLMRetries sets how many extra attempts a transient provider failure
// gets before the run gives up, overriding the framework default. Backoff is
// exponential and jittered between attempts. Pass a negative number to
// disable retrying entirely; zero leaves the default in place.
func WithLLMRetries(n int) RunOption {
	return func(c *RunConfig) { c.MaxLLMRetries = n }
}

// WithTemperature sets the LLM temperature
func WithTemperature(t float64) RunOption {
	return func(c *RunConfig) { c.Temperature = t }
}

// WithMaxTokens sets the maximum tokens for LLM generation
func WithMaxTokens(n int) RunOption {
	return func(c *RunConfig) { c.MaxTokens = n }
}

// WithDebug enables debug mode for this run
func WithDebug(debug bool) RunOption {
	return func(c *RunConfig) { c.Debug = debug }
}

// WithSessionID sets a specific session ID for the run
func WithSessionID(sessionID string) RunOption {
	return func(c *RunConfig) { c.SessionID = sessionID }
}

// WithTaskID sets a specific task ID for the run.
func WithTaskID(taskID string) RunOption {
	return func(c *RunConfig) { c.TaskID = taskID }
}

// WithRunID names the run so Service.CancelRun(id) can stop exactly this one.
// Without it the runtime generates a UUID and the caller has to read it back
// from Service.ActiveRuns — fine for a supervisor, useless for a UI that needs
// to be able to press stop before the first event arrives.
func WithRunID(runID string) RunOption {
	return func(c *RunConfig) { c.RunID = runID }
}

// WithResumeMessages seeds the runtime with a pre-assembled message
// history (typically restored from a TaskCheckpoint). The runtime skips
// its normal context-prep step and starts the loop with these messages.
func WithResumeMessages(msgs []domain.Message) RunOption {
	return func(c *RunConfig) {
		if len(msgs) == 0 {
			return
		}
		c.ResumeMessages = make([]domain.Message, len(msgs))
		copy(c.ResumeMessages, msgs)
	}
}

// WithInputParts attaches structured multimodal content blocks (e.g. images
// via domain.ImageLocalPathPart / ImageBase64Part) to the fresh run's initial
// user message, so vision-capable providers receive them alongside the goal
// text. Honored on the streaming runtime path (RunStream / RunStreamWithOptions);
// no effect on resume runs.
func WithInputParts(parts ...domain.MessagePart) RunOption {
	return func(c *RunConfig) {
		if len(parts) == 0 {
			return
		}
		c.InputParts = append(c.InputParts, parts...)
	}
}

// WithInputImages is a convenience over WithInputParts that attaches local
// image files (by path) to the initial user message for vision models.
func WithInputImages(paths ...string) RunOption {
	return func(c *RunConfig) {
		for _, p := range paths {
			if strings.TrimSpace(p) == "" {
				continue
			}
			c.InputParts = append(c.InputParts, domain.ImageLocalPathPart(p))
		}
	}
}

// WithMaxBudgetUSD caps the run's estimated cumulative cost in USD.
// When the running spend (input + output tokens × model pricing) crosses
// the limit, the runtime stops with StopReasonMaxBudgetUSD as the final
// outcome. Pass 0 (or omit) to leave the run unbounded.
//
// Cost is estimated using pkg/usage's per-model pricing table. Providers
// that don't have a row in the table report cost as 0 — the cap effectively
// has no force for those models. Add pricing in pkg/usage/token_counter.go
// to enable caps on new providers.
func WithMaxBudgetUSD(amount float64) RunOption {
	return func(c *RunConfig) {
		if amount < 0 {
			amount = 0
		}
		c.MaxBudgetUSD = amount
	}
}

// WithAutoCompaction enables in-loop history compaction. When the
// estimated context tokens exceed thresholdTokens (or the runtime's
// diminishing-returns signal fires), the runtime summarizes older
// history into a single system message and continues. keepRecent is the
// number of most-recent rounds preserved verbatim — 6 is a sensible
// default that retains the model's working state.
//
// Pass 0 for either argument to keep the framework default
// (CompactionDefaultThresholdTokens / CompactionDefaultKeepRecent).
func WithAutoCompaction(thresholdTokens, keepRecent int) RunOption {
	return func(c *RunConfig) {
		c.DisableAutoCompaction = false
		if thresholdTokens > 0 {
			c.CompactionThresholdTokens = thresholdTokens
		}
		if keepRecent > 0 {
			c.CompactionKeepRecent = keepRecent
		}
	}
}

// WithoutAutoCompaction disables in-loop compaction entirely so the runtime
// keeps the full history until a hard stop. Useful when an external archive
// process owns the history. This is the only way to turn compaction off —
// WithAutoCompaction only ever enables it.
func WithoutAutoCompaction() RunOption {
	return func(c *RunConfig) { c.DisableAutoCompaction = true }
}

// WithThinking turns provider-side chain-of-thought on or off for this
// run. Currently honored by DeepSeek v4 reasoner models (and providers
// that mirror the same `thinking.type` field shape). Calling
// `WithThinking(false)` on a tool-heavy or latency-sensitive run drops
// per-call latency significantly because the model emits no
// reasoning_content. Defaults to provider behaviour when unset.
func WithThinking(enabled bool) RunOption {
	return func(c *RunConfig) {
		typeStr := "enabled"
		if !enabled {
			typeStr = "disabled"
		}
		c.Thinking = &domain.ThinkingOptions{Type: typeStr}
	}
}

func WithParentTaskID(parentTaskID string) RunOption {
	return func(c *RunConfig) { c.ParentTaskID = parentTaskID }
}

// WithToolAllowlist restricts a run to the named tools.
func WithToolAllowlist(names []string) RunOption {
	return func(c *RunConfig) { c.ToolAllowlist = names }
}

// WithToolDenylist removes the named tools from a run.
func WithToolDenylist(names []string) RunOption {
	return func(c *RunConfig) { c.ToolDenylist = names }
}

// WithToolsDisabled runs with no tools attached at all — not the terminal
// signals, not search_available_tools. Any tool
// call the model emits anyway is refused with structured feedback.
//
// Declaring this skips constraint extraction: the caller has already answered
// the question the extraction would have asked.
func WithToolsDisabled() RunOption {
	return func(c *RunConfig) { c.ToolsDisabled = true }
}

// WithRequiredDeliverables declares the side effects this run must perform
// before it may complete. The delivery-contract lint refuses to let the run
// finish until each has a matching successful tool call.
//
// Declaring these skips constraint extraction.
func WithRequiredDeliverables(deliverables ...DeliverableRequirement) RunOption {
	return func(c *RunConfig) {
		c.RequiredDeliverables = append(c.RequiredDeliverables, deliverables...)
	}
}

// WithRequestedActions declares tool actions this run was asked to carry out
// (a reminder, a calendar entry, a note). The requested-action contract lint
// refuses to let the run complete while a matching tool was available and never
// called — which is what stops the model from writing "I've set the reminder"
// without setting anything.
//
// Declaring these skips constraint extraction.
func WithRequestedActions(actions ...RequestedAction) RunOption {
	return func(c *RunConfig) {
		c.RequiredActions = append(c.RequiredActions, actions...)
	}
}

// WithConstraintExtraction turns the per-run constraint extraction on or off.
// It is on by default. Turning it off leaves only what the caller declared
// through WithToolsDisabled / WithRequiredDeliverables in force, and saves one
// small structured model call per run.
func WithConstraintExtraction(enabled bool) RunOption {
	return func(c *RunConfig) { c.DisableConstraintExtraction = !enabled }
}

// WithPlanKey names the scratchpad list this run's plan lives under.
//
// The scratchpad tools key their lists by an argument the model supplies,
// defaulting to "default". A supervisor that scopes a task's plan to the task
// id has to tell the run so, or the tools keep writing to "default" while the
// supervisor reads the scoped key — an empty list, which reads as "no
// unfinished steps", which reads as finished. That was the case: the gate
// RunSegments relies on to say a task is done was disabled by the same commit
// that scoped the key, and no test noticed because the scripted plans were
// seeded under the key being read.
func WithPlanKey(key string) RunOption {
	return func(c *RunConfig) { c.PlanKey = strings.TrimSpace(key) }
}
