package agent

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/liliang-cn/agent-go/v2/pkg/domain"
	"github.com/liliang-cn/agent-go/v2/pkg/store"
)

// Store provides persistent storage for agent plans and sessions by wrapping AgentGoDB
type Store struct {
	agentGoDB *store.AgentGoDB
}

// NewStore creates a new storage backend for agent data using the unified database
func NewStore(dbPath string) (*Store, error) {
	agentGoDB, err := store.NewAgentGoDB(dbPath)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize unified AgentGoDB: %w", err)
	}
	return &Store{agentGoDB: agentGoDB}, nil
}

// NewStoreWithAgentGoDB creates a store with an existing AgentGoDB instance
func NewStoreWithAgentGoDB(agentGoDB *store.AgentGoDB) *Store {
	return &Store{agentGoDB: agentGoDB}
}

// GetAgentGoDB returns the underlying unified AgentGoDB
func (s *Store) GetAgentGoDB() *store.AgentGoDB {
	return s.agentGoDB
}

// SavePlan saves or updates an agent plan
func (s *Store) SavePlan(plan *Plan) error {
	stepsJSON, _ := json.Marshal(plan.Steps)
	return s.agentGoDB.SavePlan(&store.Plan{
		ID:        plan.ID,
		Goal:      plan.Goal,
		SessionID: plan.SessionID,
		Steps:     stepsJSON,
		Status:    plan.Status,
		Reasoning: plan.Reasoning,
		Error:     plan.Error,
		CreatedAt: plan.CreatedAt,
		UpdatedAt: plan.UpdatedAt,
	})
}

// GetPlan retrieves a plan by ID
func (s *Store) GetPlan(id string) (*Plan, error) {
	p, err := s.agentGoDB.GetPlan(id)
	if err != nil {
		return nil, err
	}

	plan := &Plan{
		ID:        p.ID,
		Goal:      p.Goal,
		SessionID: p.SessionID,
		Status:    p.Status,
		Reasoning: p.Reasoning,
		Error:     p.Error,
		CreatedAt: p.CreatedAt,
		UpdatedAt: p.UpdatedAt,
	}
	_ = json.Unmarshal(p.Steps, &plan.Steps)
	return plan, nil
}

// ListPlans retrieves plans with optional limit and session filtering
func (s *Store) ListPlans(sessionID string, limit int) ([]*Plan, error) {
	plans, err := s.agentGoDB.ListPlans(sessionID, limit)
	if err != nil {
		return nil, err
	}

	result := make([]*Plan, len(plans))
	for i, p := range plans {
		plan := &Plan{
			ID:        p.ID,
			Goal:      p.Goal,
			SessionID: p.SessionID,
			Status:    p.Status,
			Reasoning: p.Reasoning,
			Error:     p.Error,
			CreatedAt: p.CreatedAt,
			UpdatedAt: p.UpdatedAt,
		}
		_ = json.Unmarshal(p.Steps, &plan.Steps)
		result[i] = plan
	}
	return result, nil
}

// SaveSession saves or updates an agent session
func (s *Store) SaveSession(session *Session) error {
	messages := make([]store.ChatMessage, len(session.Messages))
	for i, m := range session.Messages {
		messages[i] = store.ChatMessage{
			Role:             m.Role,
			Content:          m.Content,
			ReasoningContent: m.ReasoningContent,
			ToolCalls:        m.ToolCalls,
			ToolCallID:       m.ToolCallID,
			TaskID:           m.TaskID,
			ResponseID:       m.ResponseID,
		}
	}

	return s.agentGoDB.SaveSession(&store.ChatSession{
		ID:        session.ID,
		Type:      store.ChatTypeAgent,
		Title:     "", // AgentGoDB handles title generation
		Messages:  messages,
		Summary:   session.Summary,
		Context:   session.Context,
		Metadata:  session.Metadata,
		CreatedAt: session.CreatedAt,
		UpdatedAt: session.UpdatedAt,
	})
}

// GetSession retrieves a session by ID
func (s *Store) GetSession(id string) (*Session, error) {
	sess, err := s.agentGoDB.GetSession(id)
	if err != nil {
		return nil, err
	}

	session := &Session{
		ID:        sess.ID,
		AgentID:   "", // Will be populated from metadata if available
		Summary:   sess.Summary,
		Context:   sess.Context,
		Metadata:  sess.Metadata,
		CreatedAt: sess.CreatedAt,
		UpdatedAt: sess.UpdatedAt,
	}

	session.Messages = make([]domain.Message, len(sess.Messages))
	for i, m := range sess.Messages {
		session.Messages[i] = domain.Message{
			Role:             m.Role,
			Content:          m.Content,
			ReasoningContent: m.ReasoningContent,
			ToolCalls:        m.ToolCalls,
			ToolCallID:       m.ToolCallID,
			TaskID:           m.TaskID,
			ResponseID:       m.ResponseID,
		}
	}

	return session, nil
}

// ListSessions retrieves all sessions
func (s *Store) ListSessions(limit int) ([]*Session, error) {
	sessions, err := s.agentGoDB.ListSessions(store.ChatTypeAgent, limit)
	if err != nil {
		return nil, err
	}

	result := make([]*Session, len(sessions))
	for i, sess := range sessions {
		session := &Session{
			ID:        sess.ID,
			AgentID:   "",
			Summary:   sess.Summary,
			Context:   sess.Context,
			Metadata:  sess.Metadata,
			CreatedAt: sess.CreatedAt,
			UpdatedAt: sess.UpdatedAt,
		}

		session.Messages = make([]domain.Message, len(sess.Messages))
		for j, m := range sess.Messages {
			session.Messages[j] = domain.Message{
				Role:             m.Role,
				Content:          m.Content,
				ReasoningContent: m.ReasoningContent,
				ToolCalls:        m.ToolCalls,
				ToolCallID:       m.ToolCallID,
				TaskID:           m.TaskID,
				ResponseID:       m.ResponseID,
			}
		}
		result[i] = session
	}
	return result, nil
}

// DeleteSession deletes a session
func (s *Store) DeleteSession(id string) error {
	return s.agentGoDB.DeleteSession(id)
}

// SaveAgentModel saves or updates an agent model configuration
func (s *Store) SaveAgentModel(agent *AgentModel) error {
	return s.agentGoDB.SaveAgentModel(&store.AgentModel{
		ID:                    agent.ID,
		A2AID:                 agent.A2AID,
		TeamID:                agent.TeamID,
		Name:                  agent.Name,
		Kind:                  string(agent.Kind),
		Description:           agent.Description,
		Instructions:          agent.Instructions,
		Model:                 agent.Model,
		PreferredProvider:     agent.PreferredProvider,
		PreferredModel:        agent.PreferredModel,
		RequiredLLMCapability: agent.RequiredLLMCapability,
		MCPTools:              agent.MCPTools,
		Skills:                agent.Skills,
		EnableRAG:             agent.EnableRAG,
		EnableMemory:          agent.EnableMemory,
		EnablePTC:             agent.EnablePTC,
		EnableMCP:             agent.EnableMCP,
		EnableA2A:             agent.EnableA2A,
		CreatedAt:             agent.CreatedAt,
		UpdatedAt:             agent.UpdatedAt,
	})
}

func (s *Store) GetAgentModelByA2AID(a2aID string) (*AgentModel, error) {
	a, err := s.agentGoDB.GetAgentModelByA2AID(a2aID)
	if err != nil {
		return nil, err
	}
	model := convertToAgentModel(a)
	if err := s.hydrateAgentMemberships(model); err != nil {
		return nil, err
	}
	return model, nil
}

// GetAgentModel retrieves an agent model by ID
func (s *Store) GetAgentModel(id string) (*AgentModel, error) {
	a, err := s.agentGoDB.GetAgentModel(id)
	if err != nil {
		return nil, err
	}
	model := convertToAgentModel(a)
	if err := s.hydrateAgentMemberships(model); err != nil {
		return nil, err
	}
	return model, nil
}

// GetAgentModelByName retrieves an agent model by Name
func (s *Store) GetAgentModelByName(name string) (*AgentModel, error) {
	a, err := s.agentGoDB.GetAgentModelByName(name)
	if err != nil {
		return nil, err
	}
	model := convertToAgentModel(a)
	if err := s.hydrateAgentMemberships(model); err != nil {
		return nil, err
	}
	return model, nil
}

// ListAgentModels retrieves all agent models
func (s *Store) ListAgentModels() ([]*AgentModel, error) {
	agents, err := s.agentGoDB.ListAgentModels()
	if err != nil {
		return nil, err
	}

	result := make([]*AgentModel, len(agents))
	for i, a := range agents {
		model := convertToAgentModel(a)
		if err := s.hydrateAgentMemberships(model); err != nil {
			return nil, err
		}
		result[i] = model
	}
	return result, nil
}

// SaveTeam saves or updates a team
func (s *Store) SaveTeam(team *Team) error {
	return s.agentGoDB.SaveTeam(&store.Team{
		ID:          team.ID,
		A2AID:       team.A2AID,
		Name:        team.Name,
		Description: team.Description,
		EnableA2A:   team.EnableA2A,
		CreatedAt:   team.CreatedAt,
		UpdatedAt:   team.UpdatedAt,
	})
}

func (s *Store) GetTeamByA2AID(a2aID string) (*Team, error) {
	sq, err := s.agentGoDB.GetTeamByA2AID(a2aID)
	if err != nil {
		return nil, err
	}
	return convertToTeam(sq), nil
}

// GetTeam retrieves a team by ID
func (s *Store) GetTeam(id string) (*Team, error) {
	sq, err := s.agentGoDB.GetTeam(id)
	if err != nil {
		return nil, err
	}
	return convertToTeam(sq), nil
}

// GetTeamByName retrieves a team by name
func (s *Store) GetTeamByName(name string) (*Team, error) {
	sq, err := s.agentGoDB.GetTeamByName(name)
	if err != nil {
		return nil, err
	}
	return convertToTeam(sq), nil
}

// ListTeams retrieves all teams
func (s *Store) ListTeams() ([]*Team, error) {
	teams, err := s.agentGoDB.ListTeams()
	if err != nil {
		return nil, err
	}

	result := make([]*Team, len(teams))
	for i, sq := range teams {
		result[i] = convertToTeam(sq)
	}
	return result, nil
}

// DeleteTeam deletes a team
func (s *Store) DeleteTeam(id string) error {
	return s.agentGoDB.DeleteTeam(id)
}

// DeleteAgentModel
func (s *Store) DeleteAgentModel(id string) error {
	return s.agentGoDB.DeleteAgentModel(id)
}

// Close closes the database connection
func (s *Store) Close() error {
	return s.agentGoDB.Close()
}

// Helper converters

func convertToAgentModel(a *store.AgentModel) *AgentModel {
	return &AgentModel{
		ID:                    a.ID,
		A2AID:                 a.A2AID,
		TeamID:                a.TeamID,
		Name:                  a.Name,
		Kind:                  AgentKind(a.Kind),
		Description:           a.Description,
		Instructions:          a.Instructions,
		Model:                 a.Model,
		PreferredProvider:     a.PreferredProvider,
		PreferredModel:        a.PreferredModel,
		RequiredLLMCapability: a.RequiredLLMCapability,
		MCPTools:              a.MCPTools,
		Skills:                a.Skills,
		EnableRAG:             a.EnableRAG,
		EnableMemory:          a.EnableMemory,
		EnablePTC:             a.EnablePTC,
		EnableMCP:             a.EnableMCP,
		EnableA2A:             a.EnableA2A,
		CreatedAt:             a.CreatedAt,
		UpdatedAt:             a.UpdatedAt,
	}
}

func convertToTeam(sq *store.Team) *Team {
	return &Team{
		ID:          sq.ID,
		A2AID:       sq.A2AID,
		Name:        sq.Name,
		Description: sq.Description,
		EnableA2A:   sq.EnableA2A,
		CreatedAt:   sq.CreatedAt,
		UpdatedAt:   sq.UpdatedAt,
	}
}

func normalizeAgentKind(agent *AgentModel) AgentKind {
	if agent == nil {
		return AgentKindCaptain
	}
	if agent.Kind == AgentKindCaptain || agent.Kind == AgentKindSpecialist || agent.Kind == AgentKindAgent {
		return agent.Kind
	}
	if strings.TrimSpace(agent.TeamID) == "" {
		return AgentKindAgent
	}
	return AgentKindCaptain
}
