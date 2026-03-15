package agent

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/liliang-cn/agent-go/pkg/store"
)

func (s *Store) SaveSquadMembership(membership *SquadMembership) error {
	if membership == nil {
		return fmt.Errorf("membership is required")
	}

	if membership.CreatedAt.IsZero() {
		membership.CreatedAt = time.Now()
	}
	membership.UpdatedAt = time.Now()

	return s.agentGoDB.SaveSquadMembership(&store.SquadMembership{
		AgentID:   membership.AgentID,
		SquadID:   membership.SquadID,
		Role:      string(normalizeMembershipRole(membership.Role)),
		CreatedAt: membership.CreatedAt,
		UpdatedAt: membership.UpdatedAt,
	})
}

func (s *Store) DeleteSquadMembership(agentID, squadID string) error {
	return s.agentGoDB.DeleteSquadMembership(agentID, squadID)
}

func (s *Store) DeleteMembershipsBySquad(squadID string) error {
	return s.agentGoDB.DeleteMembershipsBySquad(squadID)
}

func (s *Store) DeleteMembershipsByAgent(agentID string) error {
	return s.agentGoDB.DeleteMembershipsByAgent(agentID)
}

func (s *Store) ListSquadMemberships() ([]SquadMembership, error) {
	memberships, err := s.agentGoDB.ListSquadMemberships("", "")
	if err != nil {
		return nil, err
	}
	return convertToSquadMemberships(memberships), nil
}

func (s *Store) ListSquadMembershipsByAgent(agentID string) ([]SquadMembership, error) {
	memberships, err := s.agentGoDB.ListSquadMemberships(agentID, "")
	if err != nil {
		return nil, err
	}
	return convertToSquadMemberships(memberships), nil
}

func (s *Store) ListSquadMembershipsBySquad(squadID string) ([]SquadMembership, error) {
	memberships, err := s.agentGoDB.ListSquadMemberships("", squadID)
	if err != nil {
		return nil, err
	}
	return convertToSquadMemberships(memberships), nil
}

func convertToSquadMemberships(memberships []*store.SquadMembership) []SquadMembership {
	result := make([]SquadMembership, len(memberships))
	for i, m := range memberships {
		result[i] = SquadMembership{
			AgentID:   m.AgentID,
			SquadID:   m.SquadID,
			SquadName: m.SquadName,
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
	memberships, err := s.ListSquadMembershipsByAgent(agent.ID)
	if err != nil {
		return err
	}
	agent.Squads = append(agent.Squads[:0], memberships...)
	agent.TeamID = ""
	if len(memberships) > 0 {
		first := memberships[0]
		agent.TeamID = first.SquadID
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

func cloneAgentForMembership(model *AgentModel, membership SquadMembership) *AgentModel {
	if model == nil {
		return nil
	}
	cloned := *model
	cloned.TeamID = membership.SquadID
	cloned.Kind = membership.Role
	cloned.Squads = []SquadMembership{membership}
	return &cloned
}

func hasMembershipRole(memberships []SquadMembership, role AgentKind) bool {
	for _, membership := range memberships {
		if membership.Role == role {
			return true
		}
	}
	return false
}

func leadMemberships(memberships []SquadMembership) []SquadMembership {
	out := make([]SquadMembership, 0, len(memberships))
	for _, membership := range memberships {
		if membership.Role == AgentKindCaptain {
			out = append(out, membership)
		}
	}
	slices.SortFunc(out, func(a, b SquadMembership) int {
		return strings.Compare(a.SquadID, b.SquadID)
	})
	return out
}
