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
