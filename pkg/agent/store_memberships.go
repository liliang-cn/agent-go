package agent

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/liliang-cn/agent-go/v2/pkg/store"
)

func (s *Store) SaveTeamMembership(membership *TeamMembership) error {
	if membership == nil {
		return fmt.Errorf("membership is required")
	}

	if membership.CreatedAt.IsZero() {
		membership.CreatedAt = time.Now()
	}
	membership.UpdatedAt = time.Now()

	return s.agentGoDB.SaveTeamMembership(&store.TeamMembership{
		AgentID:   membership.AgentID,
		TeamID:    membership.TeamID,
		Role:      string(normalizeMembershipRole(membership.Role)),
		CreatedAt: membership.CreatedAt,
		UpdatedAt: membership.UpdatedAt,
	})
}

func (s *Store) DeleteTeamMembership(agentID, teamID string) error {
	return s.agentGoDB.DeleteTeamMembership(agentID, teamID)
}

func (s *Store) DeleteMembershipsByTeam(teamID string) error {
	return s.agentGoDB.DeleteMembershipsByTeam(teamID)
}

func (s *Store) DeleteMembershipsByAgent(agentID string) error {
	return s.agentGoDB.DeleteMembershipsByAgent(agentID)
}

func (s *Store) ListTeamMemberships() ([]TeamMembership, error) {
	memberships, err := s.agentGoDB.ListTeamMemberships("", "")
	if err != nil {
		return nil, err
	}
	return convertToTeamMemberships(memberships), nil
}

func (s *Store) ListTeamMembershipsByAgent(agentID string) ([]TeamMembership, error) {
	memberships, err := s.agentGoDB.ListTeamMemberships(agentID, "")
	if err != nil {
		return nil, err
	}
	return convertToTeamMemberships(memberships), nil
}

func (s *Store) ListTeamMembershipsByTeam(teamID string) ([]TeamMembership, error) {
	memberships, err := s.agentGoDB.ListTeamMemberships("", teamID)
	if err != nil {
		return nil, err
	}
	return convertToTeamMemberships(memberships), nil
}

func convertToTeamMemberships(memberships []*store.TeamMembership) []TeamMembership {
	result := make([]TeamMembership, len(memberships))
	for i, m := range memberships {
		result[i] = TeamMembership{
			AgentID:   m.AgentID,
			TeamID:    m.TeamID,
			TeamName:  m.TeamName,
			Role:      normalizeMembershipRole(AgentKind(m.Role)),
			CreatedAt: m.CreatedAt,
			UpdatedAt: m.UpdatedAt,
		}
	}
	return result
}

func (s *Store) hydrateAgentMemberships(agent *AgentModel) error {
	if agent == nil {
		return nil
	}
	memberships, err := s.ListTeamMembershipsByAgent(agent.ID)
	if err != nil {
		return err
	}
	agent.Teams = append(agent.Teams[:0], memberships...)
	agent.TeamID = ""
	if len(memberships) > 0 {
		first := memberships[0]
		agent.TeamID = first.TeamID
		agent.Kind = AgentKindAgent
	}
	if strings.TrimSpace(agent.TeamID) == "" && agent.Kind != AgentKindAgent {
		agent.Kind = AgentKindAgent
	}
	return nil
}

func normalizeMembershipRole(role AgentKind) AgentKind {
	switch role {
	case AgentKindCaptain:
		return AgentKindCaptain
	case AgentKindSpecialist:
		return AgentKindSpecialist
	default:
		return AgentKindSpecialist
	}
}

func cloneAgentForMembership(model *AgentModel, membership TeamMembership) *AgentModel {
	if model == nil {
		return nil
	}
	cloned := *model
	cloned.TeamID = membership.TeamID
	cloned.Kind = membership.Role
	cloned.Teams = []TeamMembership{membership}
	return &cloned
}

func hasMembershipRole(memberships []TeamMembership, role AgentKind) bool {
	for _, membership := range memberships {
		if membership.Role == role {
			return true
		}
	}
	return false
}

func leadMemberships(memberships []TeamMembership) []TeamMembership {
	out := make([]TeamMembership, 0, len(memberships))
	for _, membership := range memberships {
		if membership.Role == AgentKindCaptain {
			out = append(out, membership)
		}
	}
	slices.SortFunc(out, func(a, b TeamMembership) int {
		return strings.Compare(a.TeamID, b.TeamID)
	})
	return out
}
