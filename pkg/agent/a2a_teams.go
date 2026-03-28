package agent

import (
	"context"
	"strings"
	"time"
)

// ListA2ATeams returns teams explicitly opted in for A2A exposure.
func (m *TeamManager) ListA2ATeams() ([]*Team, error) {
	teams, err := m.ListTeams()
	if err != nil {
		return nil, err
	}
	out := make([]*Team, 0, len(teams))
	for _, team := range teams {
		if team != nil && team.EnableA2A {
			out = append(out, team)
		}
	}
	return out, nil
}

// SetTeamA2AEnabled explicitly toggles A2A exposure for one team.
func (m *TeamManager) SetTeamA2AEnabled(_ context.Context, name string, enabled bool) (*Team, error) {
	team, err := m.GetTeamByName(strings.TrimSpace(name))
	if err != nil {
		return nil, err
	}
	team.EnableA2A = enabled
	team.UpdatedAt = time.Now()
	if err := m.store.SaveTeam(team); err != nil {
		return nil, err
	}
	return m.GetTeamByName(team.Name)
}
