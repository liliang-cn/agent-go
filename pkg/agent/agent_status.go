package agent

import (
	"fmt"
	"strings"
	"time"
)

type AgentRuntimeStatus struct {
	AgentID           string           `json:"agent_id"`
	Name              string           `json:"name"`
	Kind              AgentKind        `json:"kind"`
	Status            string           `json:"status"`
	Description       string           `json:"description"`
	Teams             []TeamMembership `json:"teams,omitempty"`
	RunningTaskCount  int              `json:"running_task_count"`
	QueuedTaskCount   int              `json:"queued_task_count"`
	PreferredProvider string           `json:"preferred_provider,omitempty"`
	PreferredModel    string           `json:"preferred_model,omitempty"`
	ConfiguredModel   string           `json:"configured_model,omitempty"`
	BuiltIn           bool             `json:"built_in"`
	RuntimeMode       string           `json:"runtime_mode,omitempty"`
	WorkerCount       int              `json:"worker_count,omitempty"`
	ActiveWorkers     int              `json:"active_workers,omitempty"`
	QueueDepth        int              `json:"queue_depth,omitempty"`
	ProcessedMessages int              `json:"processed_messages,omitempty"`
	LastMessageType   AgentMessageType `json:"last_message_type,omitempty"`
	LastCorrelationID string           `json:"last_correlation_id,omitempty"`
	LastError         string           `json:"last_error,omitempty"`
	LastActiveAt      *time.Time       `json:"last_active_at,omitempty"`
}

func (m *TeamManager) GetAgentStatus(name string) (*AgentRuntimeStatus, error) {
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
		Teams:             append([]TeamMembership(nil), model.Teams...),
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
	if status.BuiltIn {
		status.RuntimeMode = "worker"
		if isBuiltInDispatchOnlyAgentName(model.Name) {
			status.RuntimeMode = "worker_pool"
		}
	}
	if snapshot := m.builtInAgentRuntimeSnapshot(model.Name); snapshot != nil {
		status.WorkerCount = snapshot.WorkerCount
		status.ActiveWorkers = snapshot.ActiveWorkers
		status.QueueDepth = snapshot.QueueDepth
		status.ProcessedMessages = snapshot.ProcessedCount
		status.LastMessageType = snapshot.LastMessageType
		status.LastCorrelationID = snapshot.LastCorrelationID
		status.LastError = snapshot.LastError
		status.LastActiveAt = snapshot.LastSeenAt
		if snapshot.Active {
			status.Status = "running"
		}
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

func (m *TeamManager) ListAgentStatuses() ([]*AgentRuntimeStatus, error) {
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
