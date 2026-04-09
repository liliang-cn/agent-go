package agent

import (
	"context"

	"github.com/liliang-cn/agent-go/v2/pkg/domain"
)

func (s *Service) executeStopHooks(ctx context.Context, session *Session, currentAgent *Agent, goal string, messages []domain.Message, toolResults []ToolExecutionResult) *StopHookResult {
	if s == nil || s.stopHookService == nil {
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

	return s.stopHookService.ExecuteStopHooks(ctx, sessionID, agentID, goal, messages, toolResults)
}
