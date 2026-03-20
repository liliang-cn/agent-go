package agent

import (
	"context"
	"fmt"
	"strings"

	"github.com/liliang-cn/agent-go/v2/pkg/domain"
)

const (
	sessionContextMemoryAgentScope = "memory.agent_scope_id"
	sessionContextMemorySquadScope = "memory.squad_scope_id"
	sessionContextMemoryUserScope  = "memory.user_scope_id"
)

// SetMemoryScope configures the higher-level memory scopes used during retrieval.
func (s *Service) SetMemoryScope(agentID, squadID, userID string) {
	if s == nil {
		return
	}
	s.memoryScopeAgentID = strings.TrimSpace(agentID)
	s.memoryScopeSquadID = strings.TrimSpace(squadID)
	s.memoryScopeUserID = strings.TrimSpace(userID)
}

func (s *Service) resolveMemoryQueryContext(session *Session) domain.MemoryQueryContext {
	queryContext := domain.MemoryQueryContext{
		AgentID: strings.TrimSpace(s.memoryScopeAgentID),
		SquadID: strings.TrimSpace(s.memoryScopeSquadID),
		UserID:  strings.TrimSpace(s.memoryScopeUserID),
	}

	if session != nil {
		queryContext.SessionID = strings.TrimSpace(session.GetID())

		if value, ok := session.GetContext(sessionContextMemoryAgentScope); ok {
			queryContext.AgentID = firstNonEmpty(memoryContextString(value), queryContext.AgentID)
		}
		if value, ok := session.GetContext(sessionContextMemorySquadScope); ok {
			queryContext.SquadID = firstNonEmpty(memoryContextString(value), queryContext.SquadID)
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
		queryContext.AgentID = firstNonEmpty(strings.TrimSpace(agent.Name()), queryContext.AgentID)
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
	if queryContext.SquadID != "" {
		session.SetContext(sessionContextMemorySquadScope, queryContext.SquadID)
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
