package agent

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

func (m *TeamManager) ListAgents() ([]*AgentModel, error) {
	return m.store.ListAgentModels()
}

func (m *TeamManager) ListStandaloneAgents() ([]*AgentModel, error) {
	agents, err := m.store.ListAgentModels()
	if err != nil {
		return nil, err
	}
	standalone := make([]*AgentModel, 0, len(agents))
	for _, model := range agents {
		if len(model.Teams) == 0 {
			standalone = append(standalone, model)
		}
	}
	return standalone, nil
}

func (m *TeamManager) ListTeamAgentsByTeam(teamID string) ([]*AgentModel, error) {
	teamID = strings.TrimSpace(teamID)
	if teamID == "" {
		return nil, fmt.Errorf("team id is required")
	}
	memberships, err := m.store.ListTeamMembershipsByTeam(teamID)
	if err != nil {
		return nil, err
	}
	agents := make([]*AgentModel, 0, len(memberships))
	for _, membership := range memberships {
		model, getErr := m.store.GetAgentModel(membership.AgentID)
		if getErr != nil {
			return nil, getErr
		}
		agents = append(agents, cloneAgentForMembership(model, membership))
	}
	return agents, nil
}

func (m *TeamManager) GetLeadAgentForTeam(teamID string) (*AgentModel, error) {
	agents, err := m.ListTeamAgentsByTeam(teamID)
	if err != nil {
		return nil, err
	}
	for _, model := range agents {
		if model.Kind == AgentKindCaptain {
			return model, nil
		}
	}
	return nil, fmt.Errorf("team %s has no lead agent", teamID)
}

func (m *TeamManager) GetAgentByName(name string) (*AgentModel, error) {
	return m.store.GetAgentModelByName(strings.TrimSpace(name))
}

func (m *TeamManager) GetAgentByA2AID(a2aID string) (*AgentModel, error) {
	return m.store.GetAgentModelByA2AID(strings.TrimSpace(a2aID))
}

func (m *TeamManager) GetAgentService(name string) (*Service, error) {
	return m.getOrBuildService(strings.TrimSpace(name))
}

func (m *TeamManager) CreateAgent(ctx context.Context, model *AgentModel) (*AgentModel, error) {
	if model == nil {
		return nil, fmt.Errorf("agent model is required")
	}

	now := time.Now()
	requestedTeamID := strings.TrimSpace(model.TeamID)
	requestedRole, err := m.resolveRequestedTeamRole(requestedTeamID, model.Kind)
	if err != nil {
		return nil, err
	}

	if strings.TrimSpace(model.ID) == "" {
		model.ID = uuid.NewString()
	}
	if strings.TrimSpace(model.A2AID) == "" {
		model.A2AID = uuid.NewString()
	}
	model.Name = strings.TrimSpace(model.Name)
	if model.Name == "" {
		return nil, fmt.Errorf("agent name is required")
	}
	if strings.TrimSpace(model.Description) == "" {
		model.Description = model.Name
	}
	if strings.TrimSpace(model.Instructions) == "" {
		model.Instructions = model.Description
	}
	model.Model = strings.TrimSpace(model.Model)
	model.PreferredProvider = strings.TrimSpace(model.PreferredProvider)
	model.PreferredModel = strings.TrimSpace(model.PreferredModel)
	if len(model.MCPTools) == 0 && !isBuiltInLightweightStandaloneAgentName(model.Name) {
		model.MCPTools = defaultMemberMCPTools(model.Name)
	}
	if len(model.MCPTools) > 0 {
		model.EnableMCP = true
	}
	if model.Kind == "" || model.Kind == AgentKindCaptain || model.Kind == AgentKindSpecialist {
		model.Kind = AgentKindAgent
	}
	if model.Kind != AgentKindAgent {
		return nil, fmt.Errorf("invalid agent kind %q", model.Kind)
	}
	model.TeamID = ""
	model.EnableRAG = true
	model.EnableMemory = true
	if model.CreatedAt.IsZero() {
		model.CreatedAt = now
	}
	model.UpdatedAt = now

	if err := m.store.SaveAgentModel(model); err != nil {
		return nil, err
	}
	created, err := m.store.GetAgentModel(model.ID)
	if err != nil {
		return nil, err
	}

	if requestedTeamID != "" {
		if err := m.store.SaveTeamMembership(&TeamMembership{
			AgentID: created.ID,
			TeamID:  requestedTeamID,
			Role:    requestedRole,
		}); err != nil {
			_ = m.store.DeleteAgentModel(created.ID)
			return nil, err
		}
		m.clearCachedAgent(created.Name)
		m.clearTeamCaptainCache(requestedTeamID)
		return m.GetMemberByNameInTeam(created.Name, requestedTeamID)
	}

	return created, nil
}

func (m *TeamManager) resolveRequestedTeamRole(teamID string, requestedRole AgentKind) (AgentKind, error) {
	teamID = strings.TrimSpace(teamID)
	if teamID == "" {
		return "", nil
	}
	if _, err := m.store.GetTeam(teamID); err != nil {
		return "", err
	}

	role := normalizeMembershipRole(requestedRole)
	if role == "" || role == AgentKindAgent {
		role = AgentKindSpecialist
	}
	if role != AgentKindCaptain && role != AgentKindSpecialist {
		return "", fmt.Errorf("invalid team role %q", role)
	}
	if err := m.ensureSingleLeadPerTeam("", teamID, role); err != nil {
		return "", err
	}
	return role, nil
}

func (m *TeamManager) UpdateAgent(_ context.Context, model *AgentModel) (*AgentModel, error) {
	if model == nil {
		return nil, fmt.Errorf("agent model is required")
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

	if strings.TrimSpace(model.Name) != "" {
		current.Name = strings.TrimSpace(model.Name)
	}
	if strings.TrimSpace(model.Description) != "" {
		current.Description = strings.TrimSpace(model.Description)
	}
	if strings.TrimSpace(model.Instructions) != "" {
		current.Instructions = strings.TrimSpace(model.Instructions)
	}
	if strings.TrimSpace(model.Model) != "" {
		current.Model = strings.TrimSpace(model.Model)
	}
	if strings.TrimSpace(model.PreferredProvider) != "" {
		current.PreferredProvider = strings.TrimSpace(model.PreferredProvider)
	}
	if strings.TrimSpace(model.PreferredModel) != "" {
		current.PreferredModel = strings.TrimSpace(model.PreferredModel)
	}
	if model.RequiredLLMCapability > 0 {
		current.RequiredLLMCapability = model.RequiredLLMCapability
	}
	if model.MCPTools != nil {
		current.MCPTools = append([]string(nil), model.MCPTools...)
	}
	if model.Skills != nil {
		current.Skills = append([]string(nil), model.Skills...)
	}
	current.EnableRAG = model.EnableRAG || current.EnableRAG
	current.EnableMemory = model.EnableMemory || current.EnableMemory
	current.EnablePTC = model.EnablePTC || current.EnablePTC
	current.EnableMCP = model.EnableMCP || current.EnableMCP
	current.EnableA2A = model.EnableA2A
	if strings.TrimSpace(model.A2AID) != "" {
		current.A2AID = strings.TrimSpace(model.A2AID)
	}
	current.UpdatedAt = time.Now()

	if err := m.store.SaveAgentModel(current); err != nil {
		return nil, err
	}
	m.clearCachedAgent(current.Name)
	return m.store.GetAgentModel(current.ID)
}

func (m *TeamManager) DeleteAgent(_ context.Context, name string) error {
	model, err := m.store.GetAgentModelByName(strings.TrimSpace(name))
	if err != nil {
		return err
	}
	for _, membership := range model.Teams {
		if err := m.ensureLeadRemovalAllowed(model, membership.TeamID, membership.Role); err != nil {
			return err
		}
	}
	m.clearCachedAgent(model.Name)
	return m.store.DeleteAgentModel(model.ID)
}

func (m *TeamManager) JoinTeam(_ context.Context, name, teamID string, role AgentKind) (*AgentModel, error) {
	model, err := m.store.GetAgentModelByName(strings.TrimSpace(name))
	if err != nil {
		return nil, err
	}
	teamID = strings.TrimSpace(teamID)
	if teamID == "" {
		return nil, fmt.Errorf("team id is required")
	}
	if _, err := m.store.GetTeam(teamID); err != nil {
		return nil, err
	}
	role = normalizeMembershipRole(role)
	if role != AgentKindCaptain && role != AgentKindSpecialist {
		return nil, fmt.Errorf("invalid team role %q", role)
	}
	if err := m.ensureSingleLeadPerTeam(model.ID, teamID, role); err != nil {
		return nil, err
	}
	if err := m.store.SaveTeamMembership(&TeamMembership{
		AgentID: model.ID,
		TeamID:  teamID,
		Role:    role,
	}); err != nil {
		return nil, err
	}
	m.clearCachedAgent(model.Name)
	m.clearTeamCaptainCache(teamID)
	return m.GetMemberByNameInTeam(model.Name, teamID)
}

func (m *TeamManager) LeaveTeam(_ context.Context, name string, teamID ...string) (*AgentModel, error) {
	model, err := m.store.GetAgentModelByName(strings.TrimSpace(name))
	if err != nil {
		return nil, err
	}
	if len(model.Teams) == 0 {
		return nil, fmt.Errorf("agent '%s' is not in a team", model.Name)
	}

	targetTeamID, targetRole, err := resolveLeaveTarget(model, teamID...)
	if err != nil {
		return nil, err
	}
	if err := m.ensureLeadRemovalAllowed(model, targetTeamID, targetRole); err != nil {
		return nil, err
	}
	if err := m.store.DeleteTeamMembership(model.ID, targetTeamID); err != nil {
		return nil, err
	}
	m.clearCachedAgent(model.Name)
	m.clearTeamCaptainCache(targetTeamID)
	return m.store.GetAgentModel(model.ID)
}

func (m *TeamManager) DeleteTeam(_ context.Context, teamID string) error {
	teamID = strings.TrimSpace(teamID)
	if teamID == "" {
		return fmt.Errorf("team id is required")
	}
	if teamID == defaultTeamID {
		return fmt.Errorf("cannot delete the built-in AgentGo Team")
	}
	team, err := m.store.GetTeam(teamID)
	if err != nil {
		return err
	}

	memberships, err := m.store.ListTeamMembershipsByTeam(teamID)
	if err != nil {
		return err
	}
	for _, membership := range memberships {
		model, getErr := m.store.GetAgentModel(membership.AgentID)
		if getErr != nil {
			return getErr
		}
		m.clearCachedAgent(model.Name)
		m.clearTeamCaptainCache(teamID)
		if err := m.store.DeleteTeamMembership(membership.AgentID, teamID); err != nil {
			return err
		}
		remaining, remainingErr := m.store.ListTeamMembershipsByAgent(membership.AgentID)
		if remainingErr != nil {
			return remainingErr
		}
		if len(remaining) == 0 && isAutoGeneratedTeamLeadName(team.Name, model.Name) {
			if err := m.store.DeleteAgentModel(model.ID); err != nil {
				return err
			}
		}
	}
	return m.store.DeleteTeam(teamID)
}

func (m *TeamManager) ensureSingleLeadPerTeam(agentID, teamID string, role AgentKind) error {
	if role != AgentKindCaptain {
		return nil
	}
	memberships, err := m.store.ListTeamMembershipsByTeam(teamID)
	if err != nil {
		return err
	}
	for _, membership := range memberships {
		if membership.Role == AgentKindCaptain && membership.AgentID != agentID {
			return fmt.Errorf("team %s already has a lead agent", teamID)
		}
	}
	return nil
}

func (m *TeamManager) ensureLeadRemovalAllowed(model *AgentModel, teamID string, role AgentKind) error {
	if model == nil || role != AgentKindCaptain || strings.TrimSpace(teamID) == "" {
		return nil
	}
	memberships, err := m.store.ListTeamMembershipsByTeam(teamID)
	if err != nil {
		return err
	}
	leadCount := 0
	for _, membership := range memberships {
		if membership.Role == AgentKindCaptain {
			leadCount++
		}
	}
	if leadCount <= 1 {
		return fmt.Errorf("team %s must keep exactly one lead agent", teamID)
	}
	return nil
}

func resolveLeaveTarget(model *AgentModel, teamIDs ...string) (string, AgentKind, error) {
	requested := ""
	if len(teamIDs) > 0 {
		requested = strings.TrimSpace(teamIDs[0])
	}
	if requested != "" {
		for _, membership := range model.Teams {
			if membership.TeamID == requested {
				return membership.TeamID, membership.Role, nil
			}
		}
		return "", "", fmt.Errorf("agent '%s' is not in team %s", model.Name, requested)
	}
	if len(model.Teams) == 1 {
		return model.Teams[0].TeamID, model.Teams[0].Role, nil
	}
	return "", "", fmt.Errorf("agent '%s' belongs to multiple teams; specify which team to leave", model.Name)
}

func (m *TeamManager) clearCachedAgent(name string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if cancel, exists := m.runningAgents[name]; exists {
		cancel()
		delete(m.runningAgents, name)
	}
	delete(m.services, name)
}

func (m *TeamManager) clearTeamCaptainCache(teamID string) {
	teamID = strings.TrimSpace(teamID)
	if teamID == "" {
		return
	}
	members, err := m.ListTeamAgentsByTeam(teamID)
	if err != nil {
		return
	}
	for _, member := range members {
		if member != nil && member.Kind == AgentKindCaptain {
			m.clearCachedAgent(member.Name)
		}
	}
}
