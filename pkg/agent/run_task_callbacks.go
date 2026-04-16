package agent

import (
	"strings"
	"time"
)

func (s *Service) buildRunTaskExecutionCallbacks(session *Session, round int) ToolExecutionCallbacks {
	taskID := currentTaskID(session)
	agentName := currentAgentNameFromSession(session)
	return ToolExecutionCallbacks{
		OnToolCall: func(name string, args map[string]interface{}, interruptBehavior string) {
			s.persistRunTaskEvent(session, taskID, &Event{
				Type:      EventTypeToolCall,
				AgentName: agentName,
				Round:     round,
				ToolName:  name,
				ToolArgs:  args,
				Timestamp: time.Now(),
			})
		},
		OnToolResult: func(name string, result interface{}, err error, interruptBehavior string) {
			content := ""
			if err != nil {
				content = err.Error()
			}
			s.persistRunTaskEvent(session, taskID, &Event{
				Type:       EventTypeToolResult,
				AgentName:  agentName,
				Round:      round,
				ToolName:   name,
				ToolResult: result,
				Content:    content,
				Timestamp:  time.Now(),
			})
		},
	}
}

func currentAgentNameFromSession(session *Session) string {
	if session == nil {
		return ""
	}
	if session.Metadata != nil {
		if name, ok := session.Metadata["agent_name"].(string); ok && strings.TrimSpace(name) != "" {
			return strings.TrimSpace(name)
		}
	}
	return session.AgentID
}
