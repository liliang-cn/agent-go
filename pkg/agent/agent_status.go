package agent

import (
	"fmt"
	"strings"
)

type AgentRuntimeStatus struct {
	AgentID           string            `json:"agent_id"`
	Name              string            `json:"name"`
	Kind              AgentKind         `json:"kind"`
	Status            string            `json:"status"`
	Description       string            `json:"description"`
	Squads            []SquadMembership `json:"squads,omitempty"`
	RunningTaskCount  int               `json:"running_task_count"`
	QueuedTaskCount   int               `json:"queued_task_count"`
	PreferredProvider string            `json:"preferred_provider,omitempty"`
	PreferredModel    string            `json:"preferred_model,omitempty"`
	ConfiguredModel   string            `json:"configured_model,omitempty"`
	BuiltIn           bool              `json:"built_in"`
}

func (m *SquadManager) GetAgentStatus(name string) (*AgentRuntimeStatus, error) {
	model, err := m.store.GetAgentModelByName(strings.TrimSpace(name))
	if err != nil {
		return nil, err
	}

	status := &AgentRuntimeStatus{
		AgentID:           model.ID,
		Name:              model.Name,
		Kind:              model.Kind,
		Status:            "idle",
		Description:       model.Description,
		Squads:            append([]SquadMembership(nil), model.Squads...),
		PreferredProvider: strings.TrimSpace(model.PreferredProvider),
		PreferredModel:    strings.TrimSpace(model.PreferredModel),
		ConfiguredModel:   strings.TrimSpace(model.Model),
		BuiltIn:           isBuiltInAgentModel(model),
	}

	m.mu.RLock()
	svc := m.services[model.Name]
	m.mu.RUnlock()
	if svc != nil && svc.IsRunning() {
		status.Status = "running"
	}

	m.queueMu.Lock()
	for _, task := range m.sharedTasks {
		if task == nil {
			continue
		}
		involvesAgent := strings.EqualFold(task.CaptainName, model.Name)
		if !involvesAgent {
			for _, agentName := range task.AgentNames {
				if strings.EqualFold(agentName, model.Name) {
					involvesAgent = true
					break
				}
			}
		}
		if !involvesAgent {
			continue
		}
		switch task.Status {
		case SharedTaskStatusRunning:
			status.RunningTaskCount++
		case SharedTaskStatusQueued:
			status.QueuedTaskCount++
		}
	}
	m.queueMu.Unlock()

	switch {
	case status.RunningTaskCount > 0:
		status.Status = "running"
	case status.Status != "running" && status.QueuedTaskCount > 0:
		status.Status = "queued"
	}

	return status, nil
}

func (m *SquadManager) ListAgentStatuses() ([]*AgentRuntimeStatus, error) {
	agents, err := m.ListAgents()
	if err != nil {
		return nil, err
	}

	statuses := make([]*AgentRuntimeStatus, 0, len(agents))
	for _, model := range agents {
		status, err := m.GetAgentStatus(model.Name)
		if err != nil {
			return nil, err
		}
		statuses = append(statuses, status)
	}
	return statuses, nil
}

func isBuiltInAgentModel(model *AgentModel) bool {
	if model == nil {
		return false
	}
	switch strings.TrimSpace(model.ID) {
	case defaultConciergeAgentID, defaultIntentRouterAgentID, defaultPromptOptimizerAgentID, defaultAssistantAgentID, defaultOperatorAgentID, defaultCaptainAgentID, defaultStakeholderAgentID, defaultArchivistAgentID, defaultVerifierAgentID:
		return true
	}
	switch strings.TrimSpace(model.Name) {
	case defaultConciergeAgentName, defaultIntentRouterAgentName, defaultPromptOptimizerAgentName, defaultAssistantAgentName, defaultOperatorAgentName, defaultCaptainAgentName, defaultStakeholderAgentName, defaultArchivistAgentName, defaultVerifierAgentName:
		return true
	default:
		return false
	}
}

func (s *AgentRuntimeStatus) Summary() string {
	if s == nil {
		return ""
	}
	base := fmt.Sprintf("%s (%s) is %s", s.Name, s.Kind, s.Status)
	if s.RunningTaskCount > 0 || s.QueuedTaskCount > 0 {
		base += fmt.Sprintf(" [running=%d queued=%d]", s.RunningTaskCount, s.QueuedTaskCount)
	}
	return base
}
