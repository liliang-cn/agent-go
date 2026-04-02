package agent

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"

	"github.com/liliang-cn/agent-go/v2/pkg/domain"
	"golang.org/x/sync/errgroup"
)

const (
	toolExecutionStateQueued    = "queued"
	toolExecutionStateExecuting = "executing"
	toolExecutionStateCompleted = "completed"
	toolExecutionStateYielded   = "yielded"
)

type toolExecutionEntry struct {
	index    int
	call     domain.ToolCall
	behavior string
	state    string
	result   ToolExecutionResult
	err      error
}

type toolBatchExecutor struct {
	svc             *Service
	ctx             context.Context
	currentAgent    *Agent
	session         *Session
	callbacks       ToolExecutionCallbacks
	continueOnError bool

	mu      sync.Mutex
	entries []*toolExecutionEntry
	results []ToolExecutionResult
}

func newToolBatchExecutor(ctx context.Context, svc *Service, currentAgent *Agent, session *Session, toolCalls []domain.ToolCall, callbacks ToolExecutionCallbacks, continueOnError bool) *toolBatchExecutor {
	executor := &toolBatchExecutor{
		svc:             svc,
		ctx:             ctx,
		currentAgent:    currentAgent,
		session:         session,
		callbacks:       callbacks,
		continueOnError: continueOnError,
		entries:         make([]*toolExecutionEntry, 0, len(toolCalls)),
		results:         make([]ToolExecutionResult, len(toolCalls)),
	}
	for idx, tc := range toolCalls {
		executor.entries = append(executor.entries, &toolExecutionEntry{
			index:    idx,
			call:     tc,
			behavior: svc.toolInterruptBehavior(tc.Function.Name, currentAgent),
			state:    toolExecutionStateQueued,
		})
	}
	return executor
}

func (e *toolBatchExecutor) run() ([]ToolExecutionResult, error) {
	if len(e.entries) == 0 {
		return nil, nil
	}

	for _, entry := range e.entries {
		e.emitState(entry, toolExecutionStateQueued)
	}

	for _, batch := range e.svc.partitionToolCalls(e.toolCalls(), e.session, e.currentAgent) {
		if batch.isConcurrencySafe {
			g, groupCtx := errgroup.WithContext(e.ctx)
			for _, tc := range batch.toolCalls {
				entry := e.entryForCall(tc)
				if entry == nil {
					continue
				}
				g.Go(func() error {
					return e.executeEntry(groupCtx, entry)
				})
			}
			if err := g.Wait(); err != nil {
				return nil, err
			}
			continue
		}

		for _, tc := range batch.toolCalls {
			entry := e.entryForCall(tc)
			if entry == nil {
				continue
			}
			if err := e.executeEntry(e.ctx, entry); err != nil {
				return nil, err
			}
		}
	}

	return append([]ToolExecutionResult(nil), e.results...), nil
}

func (e *toolBatchExecutor) toolCalls() []domain.ToolCall {
	toolCalls := make([]domain.ToolCall, 0, len(e.entries))
	for _, entry := range e.entries {
		toolCalls = append(toolCalls, entry.call)
	}
	return toolCalls
}

func (e *toolBatchExecutor) entryForCall(target domain.ToolCall) *toolExecutionEntry {
	for _, entry := range e.entries {
		if entry.call.ID == target.ID && entry.call.Function.Name == target.Function.Name {
			return entry
		}
	}
	return nil
}

func (e *toolBatchExecutor) executeEntry(ctx context.Context, entry *toolExecutionEntry) error {
	e.emitState(entry, toolExecutionStateExecuting)

	result, err := e.svc.executeSingleToolCall(ctx, e.currentAgent, e.session, entry.call, e.callbacks, e.continueOnError)

	finalState := toolExecutionStateCompleted
	if e.continueOnError && isRecoverableToolExecutionResult(result) {
		finalState = toolExecutionStateYielded
	}
	if err != nil && e.continueOnError {
		finalState = toolExecutionStateYielded
	}

	e.mu.Lock()
	entry.result = result
	entry.err = err
	entry.state = finalState
	e.results[entry.index] = result
	e.mu.Unlock()

	e.emitState(entry, finalState)

	if err != nil {
		return err
	}
	return nil
}

func isRecoverableToolExecutionResult(result ToolExecutionResult) bool {
	value, ok := result.Result.(string)
	if !ok {
		return false
	}
	return strings.HasPrefix(value, "Error:")
}

func (e *toolBatchExecutor) emitState(entry *toolExecutionEntry, state string) {
	e.mu.Lock()
	entry.state = state
	e.mu.Unlock()

	if e.callbacks.OnToolState != nil {
		e.callbacks.OnToolState(entry.call.Function.Name, state, entry.behavior)
	}
}

func (s *Service) executeToolCallsWithOptions(ctx context.Context, currentAgent *Agent, session *Session, toolCalls []domain.ToolCall, callbacks ToolExecutionCallbacks, continueOnError bool) ([]ToolExecutionResult, error) {
	executor := newToolBatchExecutor(ctx, s, currentAgent, session, toolCalls, callbacks, continueOnError)
	results, err := executor.run()
	if err != nil {
		return nil, err
	}
	return results, nil
}

func (s *Service) executeSingleToolCall(ctx context.Context, currentAgent *Agent, session *Session, toolCall domain.ToolCall, callbacks ToolExecutionCallbacks, continueOnError bool) (ToolExecutionResult, error) {
	toolCtx := withCurrentAgent(ctx, currentAgent)
	behavior, endExecution := s.beginToolExecution(toolCall.Function.Name, currentAgent)
	defer endExecution()

	toolName := toolCall.Function.Name
	toolDesc := toolName
	if strings.HasPrefix(toolName, "mcp_") {
		toolDesc = strings.TrimPrefix(toolName, "mcp_")
	}
	result := ToolExecutionResult{
		ToolCallID: toolCall.ID,
		ToolName:   toolCall.Function.Name,
	}

	if callbacks.OnToolCall != nil {
		callbacks.OnToolCall(toolCall.Function.Name, toolCall.Function.Arguments, behavior)
	}

	s.emitProgress("tool_call", fmt.Sprintf("→ %s", toolDesc), 0, toolName)

	if s.debug {
		s.EmitDebugPrint(0, "tool_call", fmt.Sprintf("TOOL: %s\nARGS: %v", toolName, toolCall.Function.Arguments))
	}

	s.logger.Debug("Executing Tool",
		slog.String("tool", toolName),
		slog.Any("arguments", toolCall.Function.Arguments))

	execResult, err, _ := s.executeToolViaSubAgentWithEvents(toolCtx, currentAgent, session, toolCall, callbacks.EventSink, callbacks.Debug)
	if callbacks.OnToolResult != nil {
		callbacks.OnToolResult(toolCall.Function.Name, execResult, err, behavior)
	}
	if err != nil {
		s.logger.Error("Tool execution failed",
			slog.String("tool", toolName),
			slog.Any("error", err))

		if s.debug {
			s.EmitDebugPrint(0, "tool_result", fmt.Sprintf("TOOL: %s\nERROR: %v", toolName, err))
		}
		result.Result = fmt.Sprintf("Error: %v", err)
		if continueOnError {
			return result, nil
		}
		return result, fmt.Errorf("Tool %s failed: %w", toolCall.Function.Name, err)
	}

	s.logger.Debug("Tool Result",
		slog.String("tool", toolName),
		slog.Any("result", execResult))

	if s.debug {
		s.EmitDebugPrint(0, "tool_result", fmt.Sprintf("TOOL: %s\nRESULT: %v", toolName, execResult))
	}

	s.emitProgress("tool_result", fmt.Sprintf("✓ %s Done", toolDesc), 0, toolName)
	result.Result = execResult
	return result, nil
}
