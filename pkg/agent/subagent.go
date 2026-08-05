package agent

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/liliang-cn/agent-go/v3/pkg/domain"
)

// SubAgentMode defines how the sub-agent runs
type SubAgentMode string

const (
	// SubAgentModeForeground runs blocking, returns result
	SubAgentModeForeground SubAgentMode = "foreground"
	// SubAgentModeBackground runs non-blocking, can be resumed
	SubAgentModeBackground SubAgentMode = "background"
)

// SubAgentState defines the current state of a sub-agent
type SubAgentState string

const (
	SubAgentStatePending   SubAgentState = "pending"
	SubAgentStateRunning   SubAgentState = "running"
	SubAgentStateCompleted SubAgentState = "completed"
	SubAgentStateFailed    SubAgentState = "failed"
	SubAgentStatePaused    SubAgentState = "paused"
	SubAgentStateCancelled SubAgentState = "cancelled"
	SubAgentStateTimeout   SubAgentState = "timeout"
)

// SubAgentProgress represents progress information
type SubAgentProgress struct {
	SubagentID   string        `json:"subagent_id"`
	SubagentName string        `json:"subagent_name"`
	CurrentTurn  int           `json:"current_turn"`
	MaxTurns     int           `json:"max_turns"`
	State        SubAgentState `json:"state"`
	Goal         string        `json:"goal"`
	ElapsedTime  time.Duration `json:"elapsed_time"`
	Message      string        `json:"message,omitempty"`
}

// SubAgentProgressCallback is called when progress updates
type SubAgentProgressCallback func(progress SubAgentProgress)

// SubAgentConfig configures a sub-agent execution
type SubAgentConfig struct {
	Agent           *Agent                   // Target agent to run
	Mode            SubAgentMode             // Foreground or background
	MaxTurns        int                      // Maximum turns (default: 10)
	Isolated        bool                     // Isolate context from parent (default: true)
	ToolAllowlist   []string                 // Only allow these tools (nil = all)
	ToolDenylist    []string                 // Deny these tools
	ParentSession   *Session                 // Parent session (for context inheritance)
	Goal            string                   // Task goal
	Context         map[string]interface{}   // Additional context
	Service         *Service                 // Parent service for tool access
	Timeout         time.Duration            // Execution timeout (0 = no timeout)
	ProgressCb      SubAgentProgressCallback // Progress callback
	RetryOnFailure  int                      // Number of retries on failure (default: 0)
	CancelOnTimeout bool                     // Cancel execution on timeout (default: true)
	ToolCall        *domain.ToolCall         // (Optional) Specific tool call to execute
	Debug           bool                     // Emit debug prompt/response events

	// Worktree, when non-nil, runs this sub-agent inside an isolated git
	// worktree. Its fs_* / bash / shell_* tools are rooted at the worktree
	// checkout so writes land there instead of the parent repo. See
	// WithSubAgentWorktree.
	Worktree *WorktreeSpec
}

// SubAgent represents a wrapped agent execution with independent context
type SubAgent struct {
	id      string
	config  SubAgentConfig
	state   SubAgentState
	session *Session // Isolated session

	// State management
	mu          sync.RWMutex
	currentTurn int
	result      interface{}
	err         error
	startTime   time.Time
	endTime     *time.Time

	// Context management
	ctx           context.Context
	cancel        context.CancelFunc
	timeoutCtx    context.Context
	timeoutCancel context.CancelFunc

	// Hooks reference (from parent service)
	hooks *HookRegistry

	// Progress tracking
	progressChan chan SubAgentProgress
	events       chan *Event

	// Worktree isolation (set up in Run when config.Worktree != nil).
	activeWorktree *worktreeRuntime
	worktreePath   string
}

// SubAgentOption configures a SubAgent
type SubAgentOption func(*SubAgentConfig)

// NewSubAgent creates a new sub-agent wrapper
func NewSubAgent(cfg SubAgentConfig, opts ...SubAgentOption) *SubAgent {
	// Apply options
	for _, opt := range opts {
		opt(&cfg)
	}

	// Set defaults
	if cfg.MaxTurns == 0 {
		cfg.MaxTurns = 10
	}
	if cfg.Mode == "" {
		cfg.Mode = SubAgentModeForeground
	}

	// Create isolated session
	session := NewSession(cfg.Agent.ID())
	if cfg.Isolated && cfg.ParentSession != nil {
		// Copy only context, not full history
		session.Context = copyMap(cfg.ParentSession.Context)
	}

	// Get hooks from service
	var hooks *HookRegistry
	if cfg.Service != nil {
		hooks = cfg.Service.GetHooks()
	} else {
		hooks = NewHookRegistry()
	}

	return &SubAgent{
		id:           uuid.New().String(),
		config:       cfg,
		state:        SubAgentStatePending,
		session:      session,
		hooks:        hooks,
		progressChan: make(chan SubAgentProgress, 10),
		events:       make(chan *Event, 64),
	}
}

// ID returns the sub-agent ID
func (sa *SubAgent) ID() string {
	return sa.id
}

// observerInfo builds the SubAgentInfo passed to OnSubAgentStart/End.
func (sa *SubAgent) observerInfo() SubAgentInfo {
	sessionID := ""
	if sa.session != nil {
		sessionID = sa.session.GetID()
	}
	return SubAgentInfo{
		ParentTaskID: currentTaskID(sa.config.ParentSession),
		SubAgentID:   sa.id,
		Name:         sa.config.Agent.Name(),
		Goal:         sa.config.Goal,
		SessionID:    sessionID,
	}
}

// Name returns the agent name
func (sa *SubAgent) Name() string {
	return sa.config.Agent.Name()
}

// ProgressChan returns a channel for progress updates
func (sa *SubAgent) ProgressChan() <-chan SubAgentProgress {
	return sa.progressChan
}

// emitProgress emits progress update
func (sa *SubAgent) emitProgress(message string) {
	sa.mu.RLock()
	elapsed := time.Since(sa.startTime)
	progress := SubAgentProgress{
		SubagentID:   sa.id,
		SubagentName: sa.config.Agent.Name(),
		CurrentTurn:  sa.currentTurn,
		MaxTurns:     sa.config.MaxTurns,
		State:        sa.state,
		Goal:         sa.config.Goal,
		ElapsedTime:  elapsed,
		Message:      message,
	}
	sa.mu.RUnlock()

	// Send to channel (non-blocking)
	select {
	case sa.progressChan <- progress:
	default:
	}

	// Call callback if set
	if sa.config.ProgressCb != nil {
		sa.config.ProgressCb(progress)
	}

	sa.emitEvent(&Event{
		ID:        uuid.New().String(),
		Type:      EventTypeStateUpdate,
		AgentID:   sa.config.Agent.ID(),
		AgentName: sa.config.Agent.Name(),
		Content:   fmt.Sprintf("Turn %d/%d: %s", progress.CurrentTurn, progress.MaxTurns, progress.Message),
		Timestamp: time.Now(),
	})

	// Emit progress hook
	if sa.hooks != nil {
		sa.hooks.Emit(HookEventSubagentProgress, HookData{
			SubagentID:   sa.id,
			SubagentName: sa.config.Agent.Name(),
			Goal:         sa.config.Goal,
			Metadata: map[string]interface{}{
				"current_turn": progress.CurrentTurn,
				"max_turns":    progress.MaxTurns,
				"elapsed_time": progress.ElapsedTime.String(),
				"message":      message,
			},
		})
	}
}

// Run starts the sub-agent execution (blocking)
func (sa *SubAgent) Run(parentCtx context.Context) (interface{}, error) {
	sa.mu.Lock()
	sa.state = SubAgentStateRunning
	sa.startTime = time.Now()
	sa.ctx, sa.cancel = context.WithCancel(parentCtx)
	sa.mu.Unlock()

	// Setup timeout if configured
	if sa.config.Timeout > 0 {
		sa.timeoutCtx, sa.timeoutCancel = context.WithTimeout(sa.ctx, sa.config.Timeout)
		sa.ctx = sa.timeoutCtx
	}

	// Emit SubagentStart hook
	if sa.hooks != nil {
		sa.hooks.Emit(HookEventSubagentStart, HookData{
			SubagentID:   sa.id,
			SubagentName: sa.config.Agent.Name(),
			Goal:         sa.config.Goal,
			SessionID:    sa.session.GetID(),
			AgentID:      sa.config.Agent.ID(),
			Metadata: map[string]interface{}{
				"max_turns": sa.config.MaxTurns,
				"timeout":   sa.config.Timeout.String(),
			},
		})
	}
	sa.emitStart(fmt.Sprintf("Starting sub-agent goal: %s", sa.config.Goal))

	// Observer seam: only for goal-driven sub-agents, not the per-tool-call
	// wrapper sub-agents created by executeToolViaSubAgentWithEvents (those set
	// ToolCall). Tool dispatch is already bracketed by OnToolStart/OnToolEnd.
	subInfo := sa.observerInfo()
	if sa.config.ToolCall == nil && sa.config.Service != nil {
		sa.config.Service.emitObserver(func(o Observer) { o.OnSubAgentStart(sa.ctx, subInfo) })
	}

	// Set up git-worktree isolation if requested: create the worktree, root the
	// sub-agent's fs/bash tools there, and arrange teardown.
	if sa.config.Worktree != nil {
		rt, err := sa.setupWorktree(sa.ctx)
		if err != nil {
			sa.mu.Lock()
			sa.err = fmt.Errorf("worktree setup: %w", err)
			sa.state = SubAgentStateFailed
			sa.mu.Unlock()
			return nil, sa.err
		}
		sa.activeWorktree = rt
		defer sa.teardownWorktree(context.WithoutCancel(sa.ctx))
	}

	defer func() {
		sa.mu.Lock()
		now := time.Now()
		sa.endTime = &now

		// Determine final state
		if sa.ctx != nil {
			if sa.ctx.Err() == context.DeadlineExceeded {
				sa.state = SubAgentStateTimeout
				sa.err = fmt.Errorf("execution timed out after %s", sa.config.Timeout)
			} else if sa.ctx.Err() == context.Canceled {
				if sa.state != SubAgentStatePaused {
					sa.state = SubAgentStateCancelled
					sa.err = fmt.Errorf("execution cancelled")
				}
			} else if sa.err != nil {
				sa.state = SubAgentStateFailed
			} else {
				sa.state = SubAgentStateCompleted
			}
		}
		finalErr := sa.err
		finalResult := sa.result
		sa.mu.Unlock()

		if finalErr != nil {
			sa.emitError(finalErr.Error())
		} else {
			sa.emitComplete(toolResultToString(finalResult))
		}

		// Observer seam: pair with OnSubAgentStart (goal-driven sub-agents only).
		if sa.config.ToolCall == nil && sa.config.Service != nil {
			sa.config.Service.emitObserver(func(o Observer) {
				o.OnSubAgentEnd(context.Background(), subInfo, finalResult, finalErr)
			})
		}

		// Close progress channel
		close(sa.progressChan)
		close(sa.events)

		// Cleanup timeout context
		if sa.timeoutCancel != nil {
			sa.timeoutCancel()
		}

		// Emit SubagentStop hook
		if sa.hooks != nil {
			sa.hooks.Emit(HookEventSubagentStop, HookData{
				SubagentID:   sa.id,
				SubagentName: sa.config.Agent.Name(),
				Result:       sa.result,
				Error:        sa.err,
				Duration:     now.Sub(sa.startTime),
				SessionID:    sa.session.GetID(),
				Metadata: map[string]interface{}{
					"final_state": string(sa.state),
					"turns_used":  sa.currentTurn,
				},
			})
		}
	}()

	// Execute with retry support
	var result interface{}
	var err error
	for attempt := 0; attempt <= sa.config.RetryOnFailure; attempt++ {
		if sa.config.ToolCall != nil {
			// Single-tool mode: the sub-agent wraps ONE tool call so it gets
			// its own isolation, event stream and (optionally) worktree. No
			// model turn is involved, so it must not enter the agent loop.
			sa.emitProgress(fmt.Sprintf("Executing specific tool: %s", sa.config.ToolCall.Function.Name))
			res, terr, _ := sa.executeTool(sa.ctx, *sa.config.ToolCall)
			result, err = res, terr
		} else {
			result, err = sa.execute(sa.ctx)
		}

		if err == nil {
			sa.result = result
			sa.err = nil
			return result, nil
		}

		// Don't retry on cancellation or timeout
		if sa.ctx.Err() != nil {
			break
		}

		// Retry
		if attempt < sa.config.RetryOnFailure {
			sa.emitProgress(fmt.Sprintf("Retrying (attempt %d/%d)", attempt+1, sa.config.RetryOnFailure))
		}
	}

	sa.result = result
	sa.err = err
	return result, err
}

// RunAsync starts the sub-agent in background
func (sa *SubAgent) RunAsync(parentCtx context.Context) <-chan *Event {
	go func() {
		_, _ = sa.Run(parentCtx)
	}()

	return sa.events
}

// Cancel forcefully cancels the sub-agent execution
func (sa *SubAgent) Cancel() error {
	sa.mu.Lock()
	defer sa.mu.Unlock()

	if sa.state != SubAgentStateRunning {
		return fmt.Errorf("subagent not running (current: %s)", sa.state)
	}

	sa.state = SubAgentStateCancelled

	// Cancel context
	if sa.cancel != nil {
		sa.cancel()
	}

	// Emit cancel hook
	if sa.hooks != nil {
		sa.hooks.Emit(HookEventSubagentCancel, HookData{
			SubagentID:   sa.id,
			SubagentName: sa.config.Agent.Name(),
			Goal:         sa.config.Goal,
			Metadata: map[string]interface{}{
				"current_turn": sa.currentTurn,
			},
		})
	}

	return nil
}

// Stop gracefully stops the sub-agent (alias for Cancel for clarity)
func (sa *SubAgent) Stop() error {
	return sa.Cancel()
}

// Wait waits for the sub-agent to complete and returns the result
func (sa *SubAgent) Wait(ctx context.Context) (interface{}, error) {
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-ticker.C:
			sa.mu.RLock()
			state := sa.state
			result := sa.result
			err := sa.err
			sa.mu.RUnlock()

			if state == SubAgentStateCompleted ||
				state == SubAgentStateFailed ||
				state == SubAgentStateCancelled ||
				state == SubAgentStateTimeout {
				return result, err
			}
		}
	}
}

// IsTerminal returns true if the sub-agent is in a terminal state
func (sa *SubAgent) IsTerminal() bool {
	sa.mu.RLock()
	defer sa.mu.RUnlock()
	return sa.state == SubAgentStateCompleted ||
		sa.state == SubAgentStateFailed ||
		sa.state == SubAgentStateCancelled ||
		sa.state == SubAgentStateTimeout
}

// execute runs the agent with tool filtering
// execute runs the sub-agent through the SAME loop the top-level run uses.
// A sub-agent is not a second execution engine — it is one more Runtime over
// the same Service, with its own session, a narrower tool surface and a
// different event sink. Everything the loop provides (lints, compaction,
// checkpoints, tool lifecycle, observers) therefore applies unchanged.
func (sa *SubAgent) execute(ctx context.Context) (interface{}, error) {
	if sa.config.Service == nil {
		return nil, fmt.Errorf("service not configured for sub-agent")
	}

	sa.emitProgress("Starting execution")

	svc := sa.config.Service
	agentForRun := sa.config.Agent
	if agentForRun != nil && svc.registry != nil {
		svc.registry.Register(agentForRun)
		sa.session.AgentID = agentForRun.ID()
	}

	cfg := DefaultRunConfig()
	cfg.MaxTurns = sa.config.MaxTurns
	cfg.SessionID = sa.session.GetID()
	cfg.TaskID = currentTaskID(sa.session)
	cfg.ToolAllowlist = sa.config.ToolAllowlist
	cfg.ToolDenylist = sa.config.ToolDenylist
	cfg.SystemPromptOverride = svc.buildSystemPrompt(ctx, agentForRun) + subAgentToolPrompt

	runtime := NewRuntime(svc, sa.session, cfg)
	runtime.currentAgent = agentForRun

	var (
		final   string
		blocked string
		runErr  error
	)
	for evt := range runtime.RunStream(ctx, sa.subAgentGoal()) {
		if evt == nil {
			continue
		}
		switch evt.Type {
		case EventTypeComplete:
			final = evt.Content
		case EventTypeBlocked:
			blocked = evt.Content
		case EventTypeError:
			if strings.TrimSpace(evt.Content) != "" {
				runErr = errors.New(evt.Content)
			}
		}
		sa.emitEvent(evt)
	}

	sa.emitProgress("Execution completed")
	switch {
	case blocked != "":
		return terminalRunResult{Text: blocked, Blocked: true}, nil
	case final != "":
		return final, nil
	case runErr != nil:
		return nil, runErr
	default:
		return "", nil
	}
}

// executeTool runs one tool call directly (single-tool sub-agent mode).
func (sa *SubAgent) executeTool(ctx context.Context, tc domain.ToolCall) (interface{}, error, bool) {
	if len(sa.config.ToolAllowlist) > 0 && !containsStr(sa.config.ToolAllowlist, tc.Function.Name) {
		return nil, fmt.Errorf("tool %s not in allowlist", tc.Function.Name), false
	}
	if containsStr(sa.config.ToolDenylist, tc.Function.Name) {
		return nil, fmt.Errorf("tool %s is denied", tc.Function.Name), false
	}

	ctx = withEventSink(ctx, sa.emitEvent)
	ctx = withRunDebug(ctx, sa.config.Debug)
	ctx = withCurrentSession(ctx, sa.session)

	return sa.config.Service.executeDirectToolCall(ctx, sa.config.Agent, sa.session, tc, DirectToolExecutionOptions{})
}

// subAgentGoal renders the goal plus any caller-supplied context. The sub-agent
// cannot see the parent conversation, so everything it needs has to be here.
func (sa *SubAgent) subAgentGoal() string {
	content := sa.config.Goal
	if len(sa.config.Context) > 0 {
		content += "\n\n--- Context ---\n" + formatContext(sa.config.Context)
	}
	return content
}

// subAgentToolPrompt is appended to the system prompt when tools are available.
// It overrides the default "summarize and stop" behavior to encourage actual
// tool invocation — the most common failure mode for SubAgent execution.
const subAgentToolPrompt = `

## Sub-Agent Execution Rules
You are executing as a sub-agent with a specific goal. Follow these rules strictly:
1. You MUST call the provided tool functions to accomplish the goal. Do NOT respond with text describing what you would do.
2. After receiving tool results, synthesize them into a final answer.
3. Only respond with a text-only message (no tool calls) when you have gathered all necessary information and are ready to provide the final answer.`

// filterTools filters tools based on allowlist and denylist
func filterTools(tools []domain.ToolDefinition, allowlist, denylist []string) []domain.ToolDefinition {
	if len(allowlist) == 0 && len(denylist) == 0 {
		return tools
	}

	denySet := make(map[string]bool)
	for _, name := range denylist {
		denySet[name] = true
	}

	var result []domain.ToolDefinition

	if len(allowlist) > 0 {
		allowSet := make(map[string]bool)
		for _, name := range allowlist {
			allowSet[name] = true
		}

		for _, tool := range tools {
			name := tool.Function.Name
			if allowSet[name] && !denySet[name] {
				result = append(result, tool)
			}
		}
	} else {
		for _, tool := range tools {
			name := tool.Function.Name
			if !denySet[name] {
				result = append(result, tool)
			}
		}
	}

	return result
}

// Resume resumes a paused/background sub-agent
func (sa *SubAgent) Resume(ctx context.Context, newGoal string) (interface{}, error) {
	sa.mu.Lock()
	if sa.state != SubAgentStatePaused {
		sa.mu.Unlock()
		return nil, fmt.Errorf("subagent not in paused state (current: %s)", sa.state)
	}
	sa.state = SubAgentStateRunning
	sa.config.Goal = newGoal
	sa.progressChan = make(chan SubAgentProgress, 10)
	sa.events = make(chan *Event, 64)
	sa.mu.Unlock()

	return sa.Run(ctx)
}

// Pause pauses a running sub-agent
func (sa *SubAgent) Pause() error {
	sa.mu.Lock()
	defer sa.mu.Unlock()

	if sa.state != SubAgentStateRunning {
		return fmt.Errorf("subagent not running (current: %s)", sa.state)
	}

	if sa.cancel != nil {
		sa.cancel()
	}

	sa.state = SubAgentStatePaused
	return nil
}

// GetState returns current state
func (sa *SubAgent) GetState() SubAgentState {
	sa.mu.RLock()
	defer sa.mu.RUnlock()
	return sa.state
}

// GetResult returns the result (if completed)
func (sa *SubAgent) GetResult() (interface{}, error) {
	sa.mu.RLock()
	defer sa.mu.RUnlock()
	return sa.result, sa.err
}

// GetCurrentTurn returns the current turn number
func (sa *SubAgent) GetCurrentTurn() int {
	sa.mu.RLock()
	defer sa.mu.RUnlock()
	return sa.currentTurn
}

// GetSession returns the isolated session
func (sa *SubAgent) GetSession() *Session {
	return sa.session
}

// GetDuration returns the execution duration
func (sa *SubAgent) GetDuration() time.Duration {
	sa.mu.RLock()
	defer sa.mu.RUnlock()

	if sa.endTime != nil {
		return sa.endTime.Sub(sa.startTime)
	}
	if !sa.startTime.IsZero() {
		return time.Since(sa.startTime)
	}
	return 0
}

// Helper functions

func copyMap(m map[string]interface{}) map[string]interface{} {
	if m == nil {
		return nil
	}
	result := make(map[string]interface{})
	for k, v := range m {
		result[k] = v
	}
	return result
}

// ============================================================
// SubAgentCoordinator - Manages concurrent SubAgent execution
// ============================================================

// SubAgentResult is the outcome of one coordinated sub-agent run.
type SubAgentResult struct {
	ID     string
	Name   string
	Result interface{}
	Error  error
	State  SubAgentState
}

// SubAgentCoordinator manages multiple SubAgents running concurrently
type SubAgentCoordinator struct {
	mu        sync.RWMutex
	subagents map[string]*SubAgent
	results   map[string]*SubAgentResult
	running   map[string]context.CancelFunc

	logger *slog.Logger
}

// NewSubAgentCoordinator creates a new coordinator
func NewSubAgentCoordinator() *SubAgentCoordinator {
	return &SubAgentCoordinator{
		subagents: make(map[string]*SubAgent),
		results:   make(map[string]*SubAgentResult),
		running:   make(map[string]context.CancelFunc),
		logger:    slog.Default().With("module", "subagent.coordinator"),
	}
}

// Add adds a SubAgent to the coordinator
func (c *SubAgentCoordinator) Add(sa *SubAgent) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.subagents[sa.id] = sa
}

// Remove removes a SubAgent from the coordinator
func (c *SubAgentCoordinator) Remove(id string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.subagents, id)
	delete(c.results, id)
	if cancel, ok := c.running[id]; ok {
		cancel()
		delete(c.running, id)
	}
}

// RunAsync starts a SubAgent in a separate goroutine
func (c *SubAgentCoordinator) RunAsync(ctx context.Context, sa *SubAgent) <-chan *SubAgentResult {
	resultChan := make(chan *SubAgentResult, 1)

	c.mu.Lock()
	c.subagents[sa.id] = sa
	c.mu.Unlock()

	go func() {
		defer close(resultChan)

		// Create cancellable context
		runCtx, cancel := context.WithCancel(ctx)
		c.mu.Lock()
		c.running[sa.id] = cancel
		c.mu.Unlock()

		// Cleanup on exit
		defer func() {
			c.mu.Lock()
			delete(c.running, sa.id)
			c.mu.Unlock()
		}()

		// Execute SubAgent
		result, err := sa.Run(runCtx)

		// Store result
		r := &SubAgentResult{
			ID:     sa.id,
			Name:   sa.config.Agent.Name(),
			Result: result,
			Error:  err,
			State:  sa.GetState(),
		}

		c.mu.Lock()
		c.results[sa.id] = r
		c.mu.Unlock()

		resultChan <- r
	}()

	return resultChan
}

// Cancel cancels a specific SubAgent
func (c *SubAgentCoordinator) Cancel(id string) bool {
	c.mu.RLock()
	cancel, ok := c.running[id]
	c.mu.RUnlock()

	if ok {
		cancel()
		return true
	}
	return false
}

// GetResult returns the result of a specific SubAgent
func (c *SubAgentCoordinator) GetResult(id string) (*SubAgentResult, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	r, ok := c.results[id]
	return r, ok
}

// Count returns the number of managed SubAgents
func (c *SubAgentCoordinator) Count() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.subagents)
}

func formatContext(ctx map[string]interface{}) string {
	var result string
	for k, v := range ctx {
		result += fmt.Sprintf("- %s: %v\n", k, v)
	}
	return result
}
