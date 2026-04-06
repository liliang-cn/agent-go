package agent

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/liliang-cn/agent-go/v2/pkg/domain"
)

// StopHookConfig configures a stop hook
type StopHookConfig struct {
	// Command is the shell command to execute
	Command string
	// Description explains what this hook does
	Description string
	// Blocking if true, the hook blocks continuation (returns error to user)
	Blocking bool
	// Timeout is the max execution time (0 = no limit)
	Timeout time.Duration
	// EnvVars additional environment variables for the hook
	EnvVars map[string]string
}

// StopHookExecutor executes stop hooks
type StopHookExecutor struct {
	hooks   []*StopHookConfig
	abortCh chan struct{}
}

// NewStopHookExecutor creates a new stop hook executor
func NewStopHookExecutor() *StopHookExecutor {
	return &StopHookExecutor{
		hooks:   make([]*StopHookConfig, 0),
		abortCh: make(chan struct{}),
	}
}

// Register adds a stop hook configuration
func (e *StopHookExecutor) Register(cfg StopHookConfig) {
	e.hooks = append(e.hooks, &cfg)
}

// Unregister removes all stop hooks
func (e *StopHookExecutor) Unregister() {
	e.hooks = nil
}

// Abort signals all running hooks to stop
func (e *StopHookExecutor) Abort() {
	close(e.abortCh)
}

// ExecuteStopHooks runs all registered stop hooks and returns aggregated results
// It runs hooks in sequence, stopping if a blocking hook fails
func (e *StopHookExecutor) ExecuteStopHooks(ctx context.Context, hookData HookData) *StopHookResult {
	if len(e.hooks) == 0 {
		return nil
	}

	aggregated := &StopHookResult{}
	allOutput := &strings.Builder{}

	for _, hook := range e.hooks {
		result := e.executeSingleHook(ctx, hook, hookData)
		if result == nil {
			continue
		}

		// Accumulate output
		if result.HookOutput != "" {
			allOutput.WriteString(result.HookOutput)
		}

		// Track if any hook wants to prevent continuation
		if result.PreventContinuation {
			aggregated.PreventContinuation = true
			if result.StopReason != "" {
				aggregated.StopReason = result.StopReason
			}
		}

		// If blocking hook failed, record the blocking error
		if result.BlockingError != "" {
			aggregated.BlockingError = result.BlockingError
		}
	}

	if allOutput.Len() > 0 {
		aggregated.HookOutput = allOutput.String()
	}

	// Return nil if nothing happened
	if !aggregated.PreventContinuation && aggregated.BlockingError == "" && aggregated.HookOutput == "" {
		return nil
	}

	return aggregated
}

// executeSingleHook runs a single stop hook
func (e *StopHookExecutor) executeSingleHook(ctx context.Context, cfg *StopHookConfig, data HookData) *StopHookResult {
	start := time.Now()

	// Build command with context
	cmdCtx, cancel := context.WithTimeout(ctx, cfg.Timeout)
	defer cancel()

	// Create the command
	shell := "/bin/sh"
	args := []string{"-c", cfg.Command}
	cmd := exec.CommandContext(cmdCtx, shell, args...)

	// Set up environment
	env := []string{
		fmt.Sprintf("AGENTGO_SESSION_ID=%s", data.SessionID),
		fmt.Sprintf("AGENTGO_AGENT_ID=%s", data.AgentID),
		fmt.Sprintf("AGENTGO_GOAL=%s", data.Goal),
	}
	for k, v := range cfg.EnvVars {
		env = append(env, fmt.Sprintf("%s=%s", k, v))
	}
	cmd.Env = env

	// Capture output
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	// Execute
	err := cmd.Run()
	duration := time.Since(start)

	result := &StopHookResult{
		DurationMs: duration.Milliseconds(),
		HookOutput: stdout.String(),
		HookError:  stderr.String(),
	}

	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			result.BlockingError = fmt.Sprintf("Stop hook timed out after %v: %s", cfg.Timeout, cfg.Command)
		} else {
			result.ExitCode = -1
			if exitErr, ok := err.(*exec.ExitError); ok {
				result.ExitCode = exitErr.ExitCode()
			}
			if cfg.Blocking {
				result.BlockingError = fmt.Sprintf("Stop hook failed: %s (exit %d)", stderr.String(), result.ExitCode)
			}
		}
	}

	return result
}

// ExecuteToolResultsHooks formats tool results for stop hook context
func FormatToolResultsForHook(results []ToolExecutionResult) string {
	if len(results) == 0 {
		return ""
	}
	var lines []string
	for _, r := range results {
		resultStr := fmt.Sprintf("%v", r.Result)
		if r.ToolType == "error" {
			lines = append(lines, fmt.Sprintf("[%s] ERROR: %s", r.ToolName, resultStr))
		} else {
			lines = append(lines, fmt.Sprintf("[%s] OK: %s", r.ToolName, truncateForHook(resultStr)))
		}
	}
	return strings.Join(lines, "\n")
}

// truncateForHook truncates tool output for hook context
func truncateForHook(s string) string {
	const maxLen = 500
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "... (truncated)"
}

// StopHookService provides stop hooks functionality for the agent service
type StopHookService struct {
	executor *StopHookExecutor
}

// NewStopHookService creates a new stop hook service
func NewStopHookService() *StopHookService {
	return &StopHookService{
		executor: NewStopHookExecutor(),
	}
}

// RegisterStopHook registers a new stop hook
func (s *StopHookService) RegisterStopHook(cfg StopHookConfig) {
	s.executor.Register(cfg)
}

// UnregisterStopHooks removes all stop hooks
func (s *StopHookService) UnregisterStopHooks() {
	s.executor.Unregister()
}

// ExecuteStopHooks runs all registered stop hooks
func (s *StopHookService) ExecuteStopHooks(ctx context.Context, sessionID, agentID, goal string, messages []domain.Message, lastToolResults []ToolExecutionResult) *StopHookResult {
	hookData := HookData{
		SessionID: sessionID,
		AgentID:   agentID,
		Goal:      goal,
		Metadata: map[string]interface{}{
			"message_count":    len(messages),
			"tool_result_count": len(lastToolResults),
		},
	}

	// Format recent messages summary
	if len(messages) > 0 {
		lastMsg := messages[len(messages)-1]
		hookData.Metadata["last_message_role"] = lastMsg.Role
		if lastMsg.Role == "assistant" {
			hookData.Metadata["last_assistant_content"] = truncateForHook(lastMsg.Content)
		}
	}

	// Format tool results
	if len(lastToolResults) > 0 {
		hookData.Metadata["tool_results"] = FormatToolResultsForHook(lastToolResults)
	}

	return s.executor.ExecuteStopHooks(ctx, hookData)
}
