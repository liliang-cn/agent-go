package agent

import (
	"context"
	"strings"
	"time"
)

// ListA2ASquads returns squads explicitly opted in for A2A exposure.
func (m *SquadManager) ListA2ASquads() ([]*Squad, error) {
	squads, err := m.ListSquads()
	if err != nil {
		return nil, err
	}
	out := make([]*Squad, 0, len(squads))
	for _, squad := range squads {
		if squad != nil && squad.EnableA2A {
			out = append(out, squad)
		}
	}
	return out, nil
}

// SetSquadA2AEnabled explicitly toggles A2A exposure for one squad.
func (m *SquadManager) SetSquadA2AEnabled(_ context.Context, name string, enabled bool) (*Squad, error) {
	squad, err := m.GetSquadByName(strings.TrimSpace(name))
	if err != nil {
		return nil, err
	}
	squad.EnableA2A = enabled
	squad.UpdatedAt = time.Now()
	if err := m.store.SaveTeam(squad); err != nil {
		return nil, err
	}
	return m.GetSquadByName(squad.Name)
}
