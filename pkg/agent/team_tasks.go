package agent

import (
	"fmt"
	"strings"
	"time"
)

func (m *TeamManager) resolveSharedTaskContext(teamID, captainName string) (*Team, *AgentModel, error) {
	if teamID != "" {
		team, err := m.store.GetTeam(teamID)
		if err != nil {
			return nil, nil, err
		}
		leadAgent, err := m.GetLeadAgentForTeam(teamID)
		if err != nil {
			return nil, nil, err
		}
		if captainName != "" && !strings.EqualFold(captainName, leadAgent.Name) {
			return nil, nil, fmt.Errorf("%s is not the lead agent for team %s", captainName, team.Name)
		}
		return team, leadAgent, nil
	}

	if captainName == "" {
		captainName = defaultCaptainAgentName
	}

	model, err := m.store.GetAgentModelByName(captainName)
	if err != nil {
		return nil, nil, fmt.Errorf("cannot load team lead agent %s: %w", captainName, err)
	}

	leadMemberships := leadMemberships(model.Teams)
	if len(leadMemberships) == 0 {
		return nil, nil, fmt.Errorf("%s is not a team lead agent", captainName)
	}

	var membership TeamMembership
	switch {
	case len(leadMemberships) == 1:
		membership = leadMemberships[0]
	default:
		for _, candidate := range leadMemberships {
			if candidate.TeamID == defaultTeamID {
				membership = candidate
				break
			}
		}
		if strings.TrimSpace(membership.TeamID) == "" {
			return nil, nil, fmt.Errorf("%s leads multiple teams; specify team id", captainName)
		}
	}

	team, err := m.store.GetTeam(membership.TeamID)
	if err != nil {
		return nil, nil, err
	}
	return team, cloneAgentForMembership(model, membership), nil
}

func (m *TeamManager) isTeamTaskActive(teamID string) bool {
	m.queueMu.Lock()
	defer m.queueMu.Unlock()

	for _, task := range m.sharedTasks {
		if task.TeamID != teamID {
			continue
		}
		if task.Status == SharedTaskStatusQueued || task.Status == SharedTaskStatusRunning {
			return true
		}
	}
	return false
}

func pruneSharedTaskResults(tasks []*SharedTask, since time.Time) []*SharedTask {
	if since.IsZero() {
		return tasks
	}
	out := make([]*SharedTask, 0, len(tasks))
	for _, task := range tasks {
		if task == nil {
			continue
		}
		if task.CreatedAt.After(since) || (task.FinishedAt != nil && task.FinishedAt.After(since)) {
			out = append(out, task)
		}
	}
	return out
}
