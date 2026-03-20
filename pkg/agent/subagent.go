package agent

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/liliang-cn/agent-go/v2/pkg/domain"
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
}

// SubAgentOption configures a SubAgent
type SubAgentOption func(*SubAgentConfig)

// WithSubAgentToolCall sets a specific tool call to execute
func WithSubAgentToolCall(tc *domain.ToolCall) SubAgentOption {
	return func(c *SubAgentConfig) { c.ToolCall = tc }
}

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
	sa.emitStart(fmt.Sprintf("Starting sub-agent goal: %s", sa.config.Goal))

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

		// Close progress channel
		close(sa.progressChan)
		close(sa.events)

		// Cleanup timeout context
		if sa.timeoutCancel != nil {
			sa.timeoutCancel()
		}

		// Emit SubagentStop hook
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
	}()

	// Execute with retry support
	var result interface{}
	var err error
	for attempt := 0; attempt <= sa.config.RetryOnFailure; attempt++ {
		if sa.config.ToolCall != nil {
			// Specific tool execution mode
			sa.emitProgress(fmt.Sprintf("Executing specific tool: %s", sa.config.ToolCall.Function.Name))
			res, terr, _ := sa.executeTool(sa.ctx, *sa.config.ToolCall)
			result, err = res, terr
		} else {
			// Normal agent execution mode
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
	sa.hooks.Emit(HookEventSubagentCancel, HookData{
		SubagentID:   sa.id,
		SubagentName: sa.config.Agent.Name(),
		Goal:         sa.config.Goal,
		Metadata: map[string]interface{}{
			"current_turn": sa.currentTurn,
		},
	})

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
func (sa *SubAgent) execute(ctx context.Context) (interface{}, error) {
	if sa.config.Service == nil {
		return nil, fmt.Errorf("service not configured for sub-agent")
	}

	// Collect tools with filtering
	tools := sa.collectFilteredTools(ctx, sa.config.Agent)

	// Build messages with SubAgent-specific goal framing
	messages := sa.buildInitialMessages(tools)

	sa.emitProgress("Starting execution")

	// Track whether any tool has actually been invoked.
	// This is used to decide whether to nudge the LLM when it responds
	// with text only — a common issue where the model describes what it
	// *would* do instead of actually calling the tools.
	toolUsed := false
	// Only nudge once per execution to avoid infinite nudge loops.
	nudged := false

	// Execute with turn limit
	for round := 0; round < sa.config.MaxTurns; round++ {
		// Check cancellation
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}

		sa.mu.Lock()
		sa.currentTurn = round + 1
		sa.mu.Unlock()

		sa.emitProgress(fmt.Sprintf("Turn %d/%d", sa.currentTurn, sa.config.MaxTurns))

		// Build system prompt with SubAgent-specific tool instructions
		systemMsg := sa.buildSystemPrompt(ctx, tools)
		genMessages := append([]domain.Message{{Role: "system", Content: systemMsg}}, messages...)

		// On the first turn with tools available and before any tool has been used
		// or nudge has been attempted, set tool_choice=required to force the API
		// to emit a function call rather than plain text.
		// After a nudge or once tools have been used, let the model choose freely
		// ("auto") — forcing "required" beyond Turn 1 can cause some proxies to
		// return non-standard binary responses.
		genOpts := sa.config.Service.toolGenerationOptions(0.3, 2000, "")
		if len(tools) > 0 && !toolUsed && !nudged {
			genOpts.ToolChoice = "required"
		}

		if sa.config.Debug {
			sa.emitDebug(round+1, "prompt", buildDebugPrompt(sa.config.Service.Info(), tools, genMessages))
		}

		var (
			fullContent strings.Builder
			toolCalls   []domain.ToolCall
		)

		err := sa.config.Service.llmService.StreamWithTools(ctx, genMessages, tools, genOpts, func(delta *domain.GenerationResult) error {
			if delta.ReasoningContent != "" {
				sa.emitThinking(delta.ReasoningContent)
			}
			if delta.Content != "" {
				fullContent.WriteString(delta.Content)
				sa.emitPartial(delta.Content)
			}
			if len(delta.ToolCalls) > 0 {
				toolCalls = delta.ToolCalls
			}
			return nil
		})
		if err != nil {
			return nil, fmt.Errorf("LLM error: %w", err)
		}
		result := &domain.GenerationResult{
			Content:   fullContent.String(),
			ToolCalls: toolCalls,
		}

		if sa.config.Debug {
			sa.emitDebug(round+1, "response", buildDebugResponse(result.Content, result.ToolCalls, nil))
		}

		// Handle tool calls
		if len(result.ToolCalls) > 0 {
			toolUsed = true

			// Add assistant message with tool calls
			messages = append(messages, domain.Message{
				Role:      "assistant",
				Content:   result.Content,
				ToolCalls: result.ToolCalls,
			})

			// Execute tools
			for _, tc := range result.ToolCalls {
				sa.emitProgress(fmt.Sprintf("Executing tool: %s", tc.Function.Name))
				sa.emitToolCall(tc.Function.Name, tc.Function.Arguments)

				toolResult, toolErr, _ := sa.executeTool(ctx, tc)
				sa.emitToolResult(tc.Function.Name, toolResult, toolErr)

				var content string
				if toolErr != nil {
					content = fmt.Sprintf("Error: %v", toolErr)
				} else {
					content = toolResultToString(toolResult)
				}

				messages = append(messages, domain.Message{
					Role:       "tool",
					ToolCallID: tc.ID,
					Content:    content,
				})
			}
			continue
		}

		// No tool calls in response.
		// If tools are available but the LLM hasn't used any yet,
		// nudge it to actually invoke the tools rather than just
		// describing what it would do.
		if len(tools) > 0 && !toolUsed && !nudged {
			nudged = true
			sa.emitProgress("Nudging LLM to use available tools")

			// Add the LLM's text as an assistant message
			messages = append(messages, domain.Message{
				Role:    "assistant",
				Content: result.Content,
			})

			// Add a nudge message to encourage actual tool invocation
			messages = append(messages, domain.Message{
				Role: "user",
				Content: "Do not describe what you would do. " +
					"You have tools available — call them now to accomplish the goal. " +
					"Use the tool functions provided to you.",
			})
			continue
		}

		// LLM returned text without tool calls and either:
		// - No tools are available (pure text task)
		// - Tools were already used (LLM is summarizing results)
		// - Nudge was already attempted (LLM genuinely can't/won't use tools)
		// In all cases, treat as completed.
		sa.emitProgress("Execution completed")
		return result.Content, nil
	}

	return nil, fmt.Errorf("exceeded maximum turns (%d)", sa.config.MaxTurns)
}

// buildInitialMessages constructs the initial message list for the SubAgent.
// When tools are available, the goal is framed to encourage tool usage.
func (sa *SubAgent) buildInitialMessages(tools []domain.ToolDefinition) []domain.Message {
	content := sa.config.Goal

	// Add context if provided
	if len(sa.config.Context) > 0 {
		content += "\n\n--- Context ---\n" + formatContext(sa.config.Context)
	}

	// When tools are available, append explicit instruction
	if len(tools) > 0 {
		toolNames := make([]string, len(tools))
		for i, t := range tools {
			toolNames[i] = t.Function.Name
		}
		content += "\n\nYou MUST use the available tools (" +
			strings.Join(toolNames, ", ") +
			") to accomplish this goal. Do not just describe what you would do — actually call the tools."
	}

	return []domain.Message{
		{Role: "user", Content: content},
	}
}

// buildSystemPrompt constructs the system prompt with SubAgent-specific
// instructions layered on top of the base agent system prompt.
func (sa *SubAgent) buildSystemPrompt(ctx context.Context, tools []domain.ToolDefinition) string {
	base := sa.config.Service.buildSystemPrompt(ctx, sa.config.Agent)

	if len(tools) == 0 {
		return base
	}

	// Append SubAgent-specific tool-use instructions
	return base + subAgentToolPrompt
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

// executeTool executes a single tool call with filtering
func (sa *SubAgent) executeTool(ctx context.Context, tc domain.ToolCall) (interface{}, error, bool) {
	// Check tool allowlist
	if len(sa.config.ToolAllowlist) > 0 && !containsStr(sa.config.ToolAllowlist, tc.Function.Name) {
		return nil, fmt.Errorf("tool %s not in allowlist", tc.Function.Name), false
	}

	// Check tool denylist
	if containsStr(sa.config.ToolDenylist, tc.Function.Name) {
		return nil, fmt.Errorf("tool %s is denied", tc.Function.Name), false
	}

	// Execute via service's tool execution logic
	rt := &Runtime{
		svc:          sa.config.Service,
		currentAgent: sa.config.Agent,
		session:      sa.session,
	}
	return rt.executeToolOrHandoff(ctx, tc)
}

// collectFilteredTools collects and filters tools for this sub-agent
func (sa *SubAgent) collectFilteredTools(ctx context.Context, agent *Agent) []domain.ToolDefinition {
	if sa.config.Service == nil {
		return nil
	}

	allTools := sa.config.Service.collectAllAvailableTools(ctx, agent)
	return filterTools(allTools, sa.config.ToolAllowlist, sa.config.ToolDenylist)
}

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

// SubAgentResult represents the result of a SubAgent execution
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

// RunAllAsync starts all SubAgents concurrently in separate goroutines
func (c *SubAgentCoordinator) RunAllAsync(ctx context.Context) <-chan *SubAgentResult {
	resultChan := make(chan *SubAgentResult, len(c.subagents))

	c.mu.RLock()
	count := len(c.subagents)
	c.mu.RUnlock()

	if count == 0 {
		close(resultChan)
		return resultChan
	}

	go func() {
		var wg sync.WaitGroup

		c.mu.RLock()
		for _, sa := range c.subagents {
			wg.Add(1)
			go func(subagent *SubAgent) {
				defer wg.Done()

				runCtx, cancel := context.WithCancel(ctx)
				c.mu.Lock()
				c.running[subagent.id] = cancel
				c.mu.Unlock()

				defer func() {
					c.mu.Lock()
					delete(c.running, subagent.id)
					c.mu.Unlock()
				}()

				result, err := subagent.Run(runCtx)

				r := &SubAgentResult{
					ID:     subagent.id,
					Name:   subagent.config.Agent.Name(),
					Result: result,
					Error:  err,
					State:  subagent.GetState(),
				}

				c.mu.Lock()
				c.results[subagent.id] = r
				c.mu.Unlock()

				resultChan <- r
			}(sa)
		}
		c.mu.RUnlock()

		wg.Wait()
		close(resultChan)
	}()

	return resultChan
}

// WaitAll waits for all SubAgents to complete
func (c *SubAgentCoordinator) WaitAll(ctx context.Context) map[string]*SubAgentResult {
	results := make(map[string]*SubAgentResult)

	for result := range c.RunAllAsync(ctx) {
		results[result.ID] = result
	}

	return results
}

// WaitAny waits for any SubAgent to complete and returns its result
func (c *SubAgentCoordinator) WaitAny(ctx context.Context) *SubAgentResult {
	resultChan := c.RunAllAsync(ctx)

	select {
	case result, ok := <-resultChan:
		if ok {
			// Cancel remaining SubAgents
			c.CancelAll()
			return result
		}
	case <-ctx.Done():
		c.CancelAll()
		return &SubAgentResult{
			Error: ctx.Err(),
			State: SubAgentStateCancelled,
		}
	}

	return nil
}

// CancelAll cancels all running SubAgents
func (c *SubAgentCoordinator) CancelAll() {
	c.mu.RLock()
	cancels := make([]context.CancelFunc, 0, len(c.running))
	for _, cancel := range c.running {
		cancels = append(cancels, cancel)
	}
	c.mu.RUnlock()

	for _, cancel := range cancels {
		cancel()
	}
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

// GetAllResults returns all results
func (c *SubAgentCoordinator) GetAllResults() map[string]*SubAgentResult {
	c.mu.RLock()
	defer c.mu.RUnlock()

	results := make(map[string]*SubAgentResult, len(c.results))
	for k, v := range c.results {
		results[k] = v
	}
	return results
}

// ListRunning returns IDs of all running SubAgents
func (c *SubAgentCoordinator) ListRunning() []string {
	c.mu.RLock()
	defer c.mu.RUnlock()

	ids := make([]string, 0, len(c.running))
	for id := range c.running {
		ids = append(ids, id)
	}
	return ids
}

// Count returns the number of managed SubAgents
func (c *SubAgentCoordinator) Count() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.subagents)
}

// CountRunning returns the number of currently running SubAgents
func (c *SubAgentCoordinator) CountRunning() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.running)
}

func formatContext(ctx map[string]interface{}) string {
	var result string
	for k, v := range ctx {
		result += fmt.Sprintf("- %s: %v\n", k, v)
	}
	return result
}
