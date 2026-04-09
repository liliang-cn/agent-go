package agent

import (
	"context"
	"fmt"
	"strings"

	"github.com/liliang-cn/agent-go/v2/pkg/domain"
)

const (
	sessionContextMemoryAgentScope = "memory.agent_scope_id"
	sessionContextMemoryTeamScope  = "memory.team_scope_id"
	sessionContextMemoryUserScope  = "memory.user_scope_id"
	sessionContextTaskID           = "runtime.task_id"
	sessionContextTaskSummaries    = "runtime.task_summaries"
)

// SetMemoryScope configures the higher-level memory scopes used during retrieval.
func (s *Service) SetMemoryScope(agentID, teamID, userID string) {
	if s == nil {
		return
	}
	s.memoryScopeAgentID = strings.TrimSpace(agentID)
	s.memoryScopeTeamID = strings.TrimSpace(teamID)
	s.memoryScopeUserID = strings.TrimSpace(userID)
}

func (s *Service) resolveMemoryQueryContext(session *Session) domain.MemoryQueryContext {
	queryContext := domain.MemoryQueryContext{
		AgentID: strings.TrimSpace(s.memoryScopeAgentID),
		TeamID:  strings.TrimSpace(s.memoryScopeTeamID),
		UserID:  strings.TrimSpace(s.memoryScopeUserID),
	}

	if session != nil {
		queryContext.SessionID = strings.TrimSpace(session.GetID())

		if value, ok := session.GetContext(sessionContextMemoryAgentScope); ok {
			queryContext.AgentID = firstNonEmpty(memoryContextString(value), queryContext.AgentID)
		}
		if value, ok := session.GetContext(sessionContextMemoryTeamScope); ok {
			queryContext.TeamID = firstNonEmpty(memoryContextString(value), queryContext.TeamID)
		}
		if value, ok := session.GetContext(sessionContextMemoryUserScope); ok {
			queryContext.UserID = firstNonEmpty(memoryContextString(value), queryContext.UserID)
		}
	}

	if queryContext.AgentID == "" && s.agent != nil {
		queryContext.AgentID = strings.TrimSpace(s.agent.Name())
	}

	return queryContext
}

func (s *Service) resolveMemoryQueryContextFromContext(ctx context.Context) domain.MemoryQueryContext {
	session := getCurrentSession(ctx)
	queryContext := s.resolveMemoryQueryContext(session)
	if agent := getCurrentAgent(ctx); agent != nil {
		currentAgentID := strings.TrimSpace(agent.Name())
		switch {
		case currentAgentID == "":
			// Keep inherited query context as-is.
		case queryContext.AgentID == "":
			queryContext.AgentID = currentAgentID
		case !isBuiltInStandaloneAgentName(currentAgentID):
			// Custom or primary agents should own their memory scope.
			queryContext.AgentID = currentAgentID
		}
	}
	return queryContext
}

func (s *Service) rememberMemoryQueryContext(session *Session, queryContext domain.MemoryQueryContext) {
	if s == nil || session == nil {
		return
	}

	if queryContext.AgentID != "" {
		session.SetContext(sessionContextMemoryAgentScope, queryContext.AgentID)
	}
	if queryContext.TeamID != "" {
		session.SetContext(sessionContextMemoryTeamScope, queryContext.TeamID)
	}
	if queryContext.UserID != "" {
		session.SetContext(sessionContextMemoryUserScope, queryContext.UserID)
	}
}

func memoryContextString(value interface{}) string {
	switch v := value.(type) {
	case nil:
		return ""
	case string:
		return strings.TrimSpace(v)
	case fmt.Stringer:
		return strings.TrimSpace(v.String())
	default:
		return strings.TrimSpace(fmt.Sprintf("%v", value))
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func taskSummaries(session *Session) map[string]string {
	if session == nil {
		return nil
	}
	raw, ok := session.GetContext(sessionContextTaskSummaries)
	if !ok || raw == nil {
		return nil
	}
	switch v := raw.(type) {
	case map[string]string:
		out := make(map[string]string, len(v))
		for key, value := range v {
			key = strings.TrimSpace(key)
			value = strings.TrimSpace(value)
			if key != "" && value != "" {
				out[key] = value
			}
		}
		return out
	case map[string]interface{}:
		out := make(map[string]string, len(v))
		for key, value := range v {
			key = strings.TrimSpace(key)
			text := memoryContextString(value)
			if key != "" && text != "" {
				out[key] = text
			}
		}
		return out
	default:
		return nil
	}
}

func taskSummary(session *Session, taskID string) string {
	taskID = strings.TrimSpace(taskID)
	if taskID == "" || session == nil {
		return ""
	}
	return strings.TrimSpace(taskSummaries(session)[taskID])
}

func setTaskSummary(session *Session, taskID, summary string) {
	taskID = strings.TrimSpace(taskID)
	summary = strings.TrimSpace(summary)
	if session == nil || taskID == "" {
		return
	}
	summaries := taskSummaries(session)
	if summaries == nil {
		summaries = make(map[string]string)
	}
	if summary == "" {
		delete(summaries, taskID)
	} else {
		summaries[taskID] = summary
	}
	session.SetContext(sessionContextTaskSummaries, summaries)
}

func resolveConversationSummary(session *Session) string {
	if session == nil {
		return ""
	}
	if summary := taskSummary(session, currentTaskID(session)); summary != "" {
		return summary
	}
	return strings.TrimSpace(session.GetSummary())
}
