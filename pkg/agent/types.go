package agent

import (
	"fmt"
	"time"

	"github.com/liliang-cn/agent-go/v2/pkg/domain"
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
	PlanID          string                    `json:"plan_id"`
	SessionID       string                    `json:"session_id"`
	TaskID          string                    `json:"task_id,omitempty"`
	Success         bool                      `json:"success"`
	StepsTotal      int                       `json:"steps_total"`
	StepsDone       int                       `json:"steps_done"`
	StepsFailed     int                       `json:"steps_failed"`
	StartedAt       *time.Time                `json:"started_at,omitempty"`
	CompletedAt     *time.Time                `json:"completed_at,omitempty"`
	ToolCalls       int                       `json:"tool_calls"`
	ToolsUsed       []string                  `json:"tools_used,omitempty"`
	EstimatedTokens int                       `json:"estimated_tokens"`
	FinalResult     interface{}               `json:"final_result,omitempty"`
	Sources         []domain.Chunk            `json:"sources,omitempty"`      // RAG sources when EnableRAG is true
	Memories        []*domain.MemoryWithScore `json:"memories,omitempty"`     // Retrieved long-term memories
	MemoryLogic     string                    `json:"memory_logic,omitempty"` // IndexNavigator's reasoning for memory selection
	Error           string                    `json:"error,omitempty"`
	Duration        string                    `json:"duration"`
	Metadata        map[string]interface{}    `json:"metadata,omitempty"`
	// PTCResult contains PTC execution details when PTC mode is active.
	PTCResult *PTCResult `json:"ptc_result,omitempty"`
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
	PTCEnabled    bool     `json:"ptc_enabled"`
	MemoryEnabled bool     `json:"memory_enabled"`
	MCPEnabled    bool     `json:"mcp_enabled"`
	SkillsEnabled bool     `json:"skills_enabled"`
	Tools         []string `json:"tools,omitempty"`
}

// Text returns the agent's text response as a plain string.
// This is the idiomatic accessor for library integrations — use this
// instead of type-asserting or fmt.Sprintf'ing FinalResult.
//
// If PTC (Programmatic Tool Calling) was used, Text() returns the
// formatted result of the code execution (e.g. return values and logs).
//
//	result, err := svc.Chat(ctx, "Hello")
//	fmt.Println(result.Text())
func (r *ExecutionResult) Text() string {
	if r == nil {
		return ""
	}

	// If PTC was used and we have an executed result, return the formatted output
	// (includes return values, logs, and tool call status).
	if r.PTCResult != nil && r.PTCResult.Type != PTCResultTypeText {
		return r.PTCResult.FormatForLLM()
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
	// MaxTurns limits the number of agent loop iterations (default: 20)
	MaxTurns int

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

	// StoreHistory enables storing execution history to database
	StoreHistory bool

	// HistoryDBPath specifies the database path for history storage
	HistoryDBPath string

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
	Stream bool

	// DisablePTC forces this run through direct function calling even if the
	// service has PTC enabled.
	DisablePTC bool

	// DisableMemoryRecallShortcut, when true, prevents the runtime from
	// answering a turn directly out of recalled long-term memory (the
	// "explicit memory recall" short-circuit). With memory enabled, that
	// shortcut otherwise fires whenever relevant memories exist — which
	// hijacks action turns ("记下这件事") on an assistant that also has
	// tools to call. Set true for action-taking agents that use memory only
	// as context; recall still works through the normal tool/LLM path.
	DisableMemoryRecallShortcut bool

	// ResumeMessages, when non-empty, makes the runtime skip its normal
	// initial-history assembly and instead use these messages as the
	// task's starting point. Used by Tasks().Resume to rebuild a run
	// from a TaskCheckpoint snapshot. The runtime treats the last
	// message as already-delivered context and prompts the model for
	// the next round.
	ResumeMessages []domain.Message

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
		MaxTurns:     20,
		Temperature:  0.3,
		MaxTokens:    2000,
		Debug:        false,
		StoreHistory: false,
	}
}

// RunOption modifies RunConfig
type RunOption func(*RunConfig)

// WithMaxTurns sets the maximum number of turns (default: 20)
func WithMaxTurns(n int) RunOption {
	return func(c *RunConfig) { c.MaxTurns = n }
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

// WithErrorHandler adds an error handler for a specific error kind
func WithErrorHandler(kind string, handler ErrorHandlerFunc) RunOption {
	return func(c *RunConfig) {
		if c.ErrorHandlers == nil {
			c.ErrorHandlers = make(map[string]ErrorHandlerFunc)
		}
		c.ErrorHandlers[kind] = handler
	}
}

// WithStoreHistory enables storing execution history to database
func WithStoreHistory(store bool) RunOption {
	return func(c *RunConfig) { c.StoreHistory = store }
}

// WithHistoryDBPath sets the database path for history storage
func WithHistoryDBPath(path string) RunOption {
	return func(c *RunConfig) { c.HistoryDBPath = path }
}

// WithSessionID sets a specific session ID for the run
func WithSessionID(sessionID string) RunOption {
	return func(c *RunConfig) { c.SessionID = sessionID }
}

// WithTaskID sets a specific task ID for the run.
func WithTaskID(taskID string) RunOption {
	return func(c *RunConfig) { c.TaskID = taskID }
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

// WithoutAutoCompaction disables in-loop compaction entirely so the
// runtime keeps the full history until a hard stop. Useful when an
// external archive process owns the history.
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

// WithInheritedMemoryScope carries the caller's memory scope into a delegated run.
func WithInheritedMemoryScope(agentID, teamID, userID string) RunOption {
	return func(c *RunConfig) {
		c.InheritedMemoryAgentID = agentID
		c.InheritedMemoryTeamID = teamID
		c.InheritedMemoryUserID = userID
	}
}

// WithStream enables streaming mode, returns events via the returned channel
func WithStream() RunOption {
	return func(c *RunConfig) { c.Stream = true }
}

func WithPTCEnabled(enabled bool) RunOption {
	return func(c *RunConfig) { c.DisablePTC = !enabled }
}

// WithMemoryRecallShortcut toggles the "answer directly from recalled memory"
// short-circuit for this run. It is enabled by default. Pass false on
// action-taking agents (ones with tools that must fire even when relevant
// memories exist) so statement/command turns aren't hijacked into a memory
// answer; recall questions still work via the normal LLM path with memory
// injected as context.
func WithMemoryRecallShortcut(enabled bool) RunOption {
	return func(c *RunConfig) { c.DisableMemoryRecallShortcut = !enabled }
}
