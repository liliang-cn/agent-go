package agent

import (
	"context"
	"strings"
	"time"
)

// ListA2AAgents returns standalone agents explicitly opted in for A2A exposure.
func (m *TeamManager) ListA2AAgents() ([]*AgentModel, error) {
	agents, err := m.ListStandaloneAgents()
	if err != nil {
		return nil, err
	}
	out := make([]*AgentModel, 0, len(agents))
	for _, model := range agents {
		if model != nil && model.EnableA2A {
			out = append(out, model)
		}
	}
	return out, nil
}

// SetAgentA2AEnabled explicitly toggles A2A exposure for one agent.
func (m *TeamManager) SetAgentA2AEnabled(ctx context.Context, name string, enabled bool) (*AgentModel, error) {
	model, err := m.GetAgentByName(strings.TrimSpace(name))
	if err != nil {
		return nil, err
	}
	model.EnableA2A = enabled
	model.UpdatedAt = time.Now()
	return m.UpdateAgentA2A(ctx, model)
}

// UpdateAgentA2A persists an AgentModel while preserving explicit EnableA2A state.
func (m *TeamManager) UpdateAgentA2A(_ context.Context, model *AgentModel) (*AgentModel, error) {
	if model == nil {
		return nil, nil
	}
	current, err := m.store.GetAgentModel(model.ID)
	if err != nil {
		if strings.TrimSpace(model.ID) == "" {
			current, err = m.store.GetAgentModelByName(strings.TrimSpace(model.Name))
		}
		if err != nil {
			return nil, err
		}
	}
	current.EnableA2A = model.EnableA2A
	current.UpdatedAt = time.Now()
	if err := m.store.SaveAgentModel(current); err != nil {
		return nil, err
	}
	m.clearCachedAgent(current.Name)
	return m.store.GetAgentModel(current.ID)
}
