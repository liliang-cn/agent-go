package agent

import (
	"context"
	"strings"
	"sync"

	"github.com/liliang-cn/agent-go/v2/pkg/domain"
)

// StreamingToolExecutor handles concurrent tool execution with progress tracking,
// sibling abort for cascading Bash errors, and synthetic error generation.
type StreamingToolExecutor interface {
	// AddTool adds a tool to the execution queue. Starts execution immediately if conditions allow.
	AddTool(call domain.ToolCall, assistantMsg string) error
	// GetRemainingResults returns a channel that yields tool results as they complete.
	// The channel is closed when all tools have yielded results.
	GetRemainingResults() <-chan *ToolExecutionResult
	// Discard signals that all pending and in-progress tools should be cancelled.
	// Queued tools won't start, and in-progress tools will receive synthetic errors.
	Discard()
}

// TrackedTool represents a tool call being tracked by the streaming executor.
type TrackedTool struct {
	ID                 string
	Call               domain.ToolCall
	AssistantMessage   string
	Status             ToolStatus
	IsConcurrencySafe  bool
	ResultCh           chan *ToolExecutionResult // buffered channel for async result
	Result             *ToolExecutionResult
	PendingProgress    []interface{} // progress messages stored separately
	Err                error
}

// streamingToolExecutor implements StreamingToolExecutor.
type streamingToolExecutor struct {
	svc            *Service
	ctx            context.Context
	session        *Session
	currentAgent   *Agent
	tracked        []*TrackedTool
	mu             sync.Mutex
	hasErrored     bool // set to true when a Bash tool produces an error
	discarded      bool
	siblingCancel   context.CancelFunc // cancels all sibling tools on Bash error
	resultCh       chan *ToolExecutionResult
	progressResolve func() // wakes up GetRemainingResults when progress is available
}

// NewStreamingToolExecutor creates a new streaming tool executor.
func NewStreamingToolExecutor(ctx context.Context, svc *Service, session *Session, currentAgent *Agent) *streamingToolExecutor {
	siblingCtx, siblingCancel := context.WithCancel(ctx)
	executor := &streamingToolExecutor{
		svc:          svc,
		ctx:          siblingCtx,
		session:      session,
		currentAgent: currentAgent,
		tracked:      make([]*TrackedTool, 0),
		discarded:    false,
		hasErrored:   false,
		siblingCancel: siblingCancel,
		resultCh:    make(chan *ToolExecutionResult, 10), // buffered
	}
	return executor
}

// AddTool adds a tool to the execution queue.
func (s *streamingToolExecutor) AddTool(call domain.ToolCall, assistantMsg string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.discarded {
		return nil
	}

	tool := &TrackedTool{
		ID:               call.ID,
		Call:             call,
		AssistantMessage: assistantMsg,
		Status:           ToolStatusQueued,
		ResultCh:         make(chan *ToolExecutionResult, 1),
		PendingProgress:  make([]interface{}, 0),
	}

	// Determine if concurrency safe
	tool.IsConcurrencySafe = s.isConcurrencySafeToolCall(call)

	s.tracked = append(s.tracked, tool)

	// Start execution if possible
	s.processQueueLocked()

	return nil
}

// GetRemainingResults returns a channel that yields tool results as they complete.
func (s *streamingToolExecutor) GetRemainingResults() <-chan *ToolExecutionResult {
	return s.resultCh
}

// Discard signals that all pending and in-progress tools should be cancelled.
func (s *streamingToolExecutor) Discard() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.discarded = true
	s.siblingCancel()
}

// processQueue starts queued tools if conditions allow.
func (s *streamingToolExecutor) processQueue() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.processQueueLocked()
}

// processQueueLocked must be called with mutex held.
func (s *streamingToolExecutor) processQueueLocked() {
	if s.discarded {
		return
	}

	for _, tool := range s.tracked {
		if tool.Status != ToolStatusQueued {
			continue
		}

		if s.canExecuteToolLocked(tool.IsConcurrencySafe) {
			go s.executeTool(tool)
		} else if !tool.IsConcurrencySafe {
			// Non-concurrent tool can't start yet and we're enforcing serial execution
			// Stop processing - we'll be called again when a tool completes
			break
		}
	}
}

// canExecuteToolLocked checks if a tool can execute based on current concurrency state.
// Must be called with mutex held.
func (s *streamingToolExecutor) canExecuteToolLocked(isConcurrencySafe bool) bool {
	executing := s.countByStatusLocked(ToolStatusExecuting)
	if executing == 0 {
		return true
	}
	if isConcurrencySafe {
		// Can start if all currently executing tools are also concurrency-safe
		for _, tool := range s.tracked {
			if tool.Status == ToolStatusExecuting && !tool.IsConcurrencySafe {
				return false
			}
		}
		return true
	}
	return false
}

// countByStatusLocked counts tools with the given status. Must be called with mutex held.
func (s *streamingToolExecutor) countByStatusLocked(status ToolStatus) int {
	count := 0
	for _, tool := range s.tracked {
		if tool.Status == status {
			count++
		}
	}
	return count
}

// executeTool executes a single tool.
func (s *streamingToolExecutor) executeTool(tool *TrackedTool) {
	s.mu.Lock()
	if tool.Status != ToolStatusQueued {
		s.mu.Unlock()
		return
	}
	tool.Status = ToolStatusExecuting
	s.mu.Unlock()

	// Create per-tool context that inherits from sibling context
	// When siblingCancel is called (Bash error), this context gets cancelled
	toolCtx, toolCancel := context.WithCancel(s.ctx)
	defer toolCancel()

	// Check for abort reason before starting
	abortReason := s.getAbortReason(tool)
	if abortReason != "" {
		s.handleAbortedTool(tool, abortReason)
		return
	}

	// Execute the tool
	s.executeSingleTool(toolCtx, tool)

	// Process queue to start any newly-available tools
	s.processQueue()
}

// getAbortReason determines why a tool should be aborted.
func (s *streamingToolExecutor) getAbortReason(tool *TrackedTool) string {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.discarded {
		return "streaming_fallback"
	}
	if s.hasErrored {
		return "sibling_error"
	}
	return ""
}

// handleAbortedTool handles a tool that should be cancelled/aborted.
func (s *streamingToolExecutor) handleAbortedTool(tool *TrackedTool, reason string) {
	s.mu.Lock()
	tool.Status = ToolStatusCompleted
	result := s.createSyntheticResult(tool, reason)
	s.mu.Unlock()

	// Send result
	select {
	case tool.ResultCh <- result:
	default:
	}
	s.sendResult(result)
	s.processQueue()
}

// createSyntheticResult creates a synthetic error result for an aborted tool.
func (s *streamingToolExecutor) createSyntheticResult(tool *TrackedTool, reason string) *ToolExecutionResult {
	var content string
	switch reason {
	case "streaming_fallback":
		content = "<tool_use_error>Error: Streaming fallback - tool execution discarded</tool_use_error>"
	case "sibling_error":
		content = "<tool_use_error>Error: Cancelled: parallel tool call errored</tool_use_error>"
	case "user_interrupted":
		content = "<tool_use_error>Error: User rejected tool use</tool_use_error>"
	default:
		content = "<tool_use_error>Error: Tool execution cancelled</tool_use_error>"
	}

	return &ToolExecutionResult{
		ToolCallID: tool.Call.ID,
		ToolName:   tool.Call.Function.Name,
		ToolType:   "synthetic_error",
		Result:     content,
	}
}

// executeSingleTool executes a single tool call.
func (s *streamingToolExecutor) executeSingleTool(ctx context.Context, tool *TrackedTool) {
	callbacks := ToolExecutionCallbacks{
		OnToolCall: func(name string, args map[string]interface{}, interruptBehavior string) {
			// Tool started
		},
		OnToolResult: func(name string, result interface{}, err error, interruptBehavior string) {
			// Tool completed
		},
		OnToolState: func(name string, state string, interruptBehavior string) {
			// State changed
		},
	}

	result, err := s.svc.executeSingleToolCall(ctx, s.currentAgent, s.session, tool.Call, callbacks, false)

	// Check if this was a Bash error that should cascade
	if err != nil && s.isBashTool(tool.Call.Function.Name) {
		s.mu.Lock()
		s.hasErrored = true
		s.siblingCancel() // Cancel all siblings
		s.mu.Unlock()
	}

	s.mu.Lock()
	tool.Status = ToolStatusCompleted
	if err != nil {
		result = ToolExecutionResult{
			ToolCallID: tool.Call.ID,
			ToolName:   tool.Call.Function.Name,
			ToolType:   "error",
			Result:     "Error: " + err.Error(),
		}
	}
	tool.Err = err
	s.mu.Unlock()

	// Send result
	s.sendResult(&result)
}

// sendResult sends a result to the main channel and yields any pending progress.
func (s *streamingToolExecutor) sendResult(result *ToolExecutionResult) {
	s.mu.Lock()
	// Find the tool and yield pending progress first
	for _, tool := range s.tracked {
		if tool.Call.ID == result.ToolCallID {
			// Yield pending progress
			for _, prog := range tool.PendingProgress {
				progressResult := &ToolExecutionResult{
					ToolCallID: result.ToolCallID,
					ToolName:   result.ToolName,
					ToolType:   "progress",
					Result:     prog,
				}
				select {
				case s.resultCh <- progressResult:
				default:
				}
			}
			tool.PendingProgress = nil
			break
		}
	}
	s.mu.Unlock()

	// Mark as yielded and send final result
	result.ToolType = "result"
	select {
	case s.resultCh <- result:
	default:
	}

	// Close the tool's result channel
	s.mu.Lock()
	for _, tool := range s.tracked {
		if tool.Call.ID == result.ToolCallID {
			tool.Status = ToolStatusYielded
			close(tool.ResultCh)
			break
		}
	}
	s.mu.Unlock()
}

// isConcurrencySafeToolCall determines if a tool call is concurrency-safe.
func (s *streamingToolExecutor) isConcurrencySafeToolCall(call domain.ToolCall) bool {
	name := strings.ToLower(strings.TrimSpace(call.Function.Name))
	if name == "" {
		return false
	}
	if name == "task_complete" || strings.HasPrefix(name, "transfer_to_") {
		return false
	}

	metadata := s.svc.lookupToolMetadataForAgent(call.Function.Name, s.currentAgent)
	if metadata.ReadOnly || metadata.ConcurrencySafe {
		return true
	}
	if metadata.Destructive {
		return false
	}

	// Fallback: infer from name
	return s.inferConcurrencySafe(name)
}

// inferConcurrencySafe infers concurrency safety from tool name.
func (s *streamingToolExecutor) inferConcurrencySafe(name string) bool {
	lower := strings.ToLower(name)
	if strings.Contains(lower, "read") || strings.Contains(lower, "list") ||
		strings.Contains(lower, "get") || strings.Contains(lower, "search") ||
		strings.Contains(lower, "fetch") || strings.Contains(lower, "query") ||
		strings.Contains(lower, "glob") || strings.Contains(lower, "grep") {
		return true
	}
	return false
}

// isBashTool checks if a tool is a Bash-type tool that should cascade errors.
func (s *streamingToolExecutor) isBashTool(name string) bool {
	lower := strings.ToLower(name)
	return strings.Contains(lower, "bash") || strings.Contains(lower, "shell") ||
		strings.Contains(lower, "terminal") || strings.Contains(lower, "execute")
}

// ToolProgressCallback is called when a tool reports progress.
type ToolProgressCallback func(toolName string, progress interface{})
