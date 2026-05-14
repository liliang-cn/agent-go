package agent

import (
	"context"

	"github.com/liliang-cn/agent-go/v2/pkg/domain"
)

// executeStopHooks emits HookEventStop through the unified HookRegistry and
// returns a legacy *StopHookResult view of the aggregated outcome. The result
// is nil when no Stop hooks ran or none asked to interrupt the loop.
//
// Callers should treat a non-nil result with PreventContinuation=true as a
// signal to terminate the current task with StopReason as the final text.
func (s *Service) executeStopHooks(ctx context.Context, session *Session, currentAgent *Agent, goal string, messages []domain.Message, toolResults []ToolExecutionResult) *StopHookResult {
	if s == nil || s.hooks == nil {
		return nil
	}
	if len(s.hooks.List(HookEventStop)) == 0 {
		// No-op fast path: avoids building metadata when nobody is listening.
		return nil
	}

	sessionID := ""
	if session != nil {
		sessionID = session.ID
	}
	if sessionID == "" {
		sessionID = s.currentSessionID
	}

	agentID := ""
	if currentAgent != nil {
		agentID = currentAgent.ID()
	}

	data := HookData{
		SessionID: sessionID,
		AgentID:   agentID,
		Goal:      goal,
		Metadata: map[string]interface{}{
			"message_count":     len(messages),
			"tool_result_count": len(toolResults),
		},
	}
	if len(messages) > 0 {
		last := messages[len(messages)-1]
		data.Metadata["last_message_role"] = last.Role
		if last.Role == "assistant" {
			data.Metadata["last_assistant_content"] = truncateForHook(last.Content)
		}
	}
	if len(toolResults) > 0 {
		data.Metadata["tool_results"] = FormatToolResultsForHook(toolResults)
	}

	out, err := s.hooks.EmitWithResult(ctx, HookEventStop, data)
	if err != nil {
		// EmitWithResult returns the error from the first handler that
		// returned one; treat that as a blocking failure.
		out.PreventContinuation = true
		if out.BlockingError == "" {
			out.BlockingError = err.Error()
		}
		if out.StopReason == "" {
			out.StopReason = err.Error()
		}
	}
	return stopHookResultFromData(out)
}
