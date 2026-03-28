package agent

import (
	"fmt"
	"slices"
	"strings"
)

type TeamRuntimeStatus struct {
	TeamID          string   `json:"team_id"`
	Name            string   `json:"name"`
	Description     string   `json:"description"`
	EnableA2A       bool     `json:"enable_a2a"`
	Status          string   `json:"status"`
	AgentCount      int      `json:"agent_count"`
	CaptainCount    int      `json:"captain_count"`
	SpecialistCount int      `json:"specialist_count"`
	CaptainNames    []string `json:"captain_names,omitempty"`
	RunningCaptains []string `json:"running_captains,omitempty"`
	ActiveTaskIDs   []string `json:"active_task_ids,omitempty"`
	RunningTasks    int      `json:"running_tasks"`
	QueuedTasks     int      `json:"queued_tasks"`
}

func (m *TeamManager) GetTeamStatus(teamID string) (*TeamRuntimeStatus, error) {
	teamID = strings.TrimSpace(teamID)
	if teamID == "" {
		return nil, fmt.Errorf("team id is required")
	}

	team, err := m.store.GetTeam(teamID)
	if err != nil {
		return nil, err
	}

	members, err := m.ListTeamAgentsByTeam(teamID)
	if err != nil {
		return nil, err
	}

	status := &TeamRuntimeStatus{
		TeamID:      team.ID,
		Name:        team.Name,
		Description: team.Description,
		EnableA2A:   team.EnableA2A,
		Status:      "idle",
	}

	leadSet := make(map[string]struct{})
	for _, member := range members {
		status.AgentCount++
		switch member.Kind {
		case AgentKindCaptain:
			status.CaptainCount++
			status.CaptainNames = append(status.CaptainNames, member.Name)
			leadSet[member.Name] = struct{}{}
		case AgentKindSpecialist:
			status.SpecialistCount++
		}
	}

	m.queueMu.Lock()
	for _, task := range m.sharedTasks {
		if task.TeamID != teamID {
			continue
		}
		if _, ok := leadSet[task.CaptainName]; !ok {
			continue
		}
		switch task.Status {
		case SharedTaskStatusRunning:
			status.RunningTasks++
			status.ActiveTaskIDs = append(status.ActiveTaskIDs, task.ID)
			status.RunningCaptains = append(status.RunningCaptains, task.CaptainName)
		case SharedTaskStatusQueued:
			status.QueuedTasks++
		}
	}
	m.queueMu.Unlock()

	switch {
	case status.RunningTasks > 0:
		status.Status = "running"
	case status.QueuedTasks > 0:
		status.Status = "queued"
	case status.CaptainCount == 0:
		status.Status = "empty"
	}

	slices.Sort(status.CaptainNames)
	slices.Sort(status.RunningCaptains)
	slices.Sort(status.ActiveTaskIDs)
	return status, nil
}

func (m *TeamManager) ListTeamStatuses() ([]*TeamRuntimeStatus, error) {
	teams, err := m.ListTeams()
	if err != nil {
		return nil, err
	}

	statuses := make([]*TeamRuntimeStatus, 0, len(teams))
	for _, team := range teams {
		status, err := m.GetTeamStatus(team.ID)
		if err != nil {
			return nil, err
		}
		statuses = append(statuses, status)
	}

	slices.SortFunc(statuses, func(a, b *TeamRuntimeStatus) int {
		switch {
		case a.Name < b.Name:
			return -1
		case a.Name > b.Name:
			return 1
		default:
			return 0
		}
	})
	return statuses, nil
}
