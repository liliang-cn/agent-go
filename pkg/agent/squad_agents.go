package agent

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

func (m *SquadManager) ListAgents() ([]*AgentModel, error) {
	return m.store.ListAgentModels()
}

func (m *SquadManager) ListStandaloneAgents() ([]*AgentModel, error) {
	agents, err := m.store.ListAgentModels()
	if err != nil {
		return nil, err
	}
	standalone := make([]*AgentModel, 0, len(agents))
	for _, model := range agents {
		if len(model.Squads) == 0 {
			standalone = append(standalone, model)
		}
	}
	return standalone, nil
}

func (m *SquadManager) ListSquadAgentsForSquad(squadID string) ([]*AgentModel, error) {
	squadID = strings.TrimSpace(squadID)
	if squadID == "" {
		return nil, fmt.Errorf("squad id is required")
	}
	memberships, err := m.store.ListSquadMembershipsBySquad(squadID)
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

func (m *SquadManager) GetLeadAgentForSquad(squadID string) (*AgentModel, error) {
	agents, err := m.ListSquadAgentsForSquad(squadID)
	if err != nil {
		return nil, err
	}
	for _, model := range agents {
		if model.Kind == AgentKindCaptain {
			return model, nil
		}
	}
	return nil, fmt.Errorf("squad %s has no lead agent", squadID)
}

func (m *SquadManager) GetAgentByName(name string) (*AgentModel, error) {
	return m.store.GetAgentModelByName(strings.TrimSpace(name))
}

func (m *SquadManager) GetAgentByA2AID(a2aID string) (*AgentModel, error) {
	return m.store.GetAgentModelByA2AID(strings.TrimSpace(a2aID))
}

func (m *SquadManager) GetAgentService(name string) (*Service, error) {
	return m.getOrBuildService(strings.TrimSpace(name))
}

func (m *SquadManager) CreateAgent(ctx context.Context, model *AgentModel) (*AgentModel, error) {
	if model == nil {
		return nil, fmt.Errorf("agent model is required")
	}

	now := time.Now()
	requestedSquadID := strings.TrimSpace(model.TeamID)
	requestedRole, err := m.resolveRequestedSquadRole(requestedSquadID, model.Kind)
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
	if len(model.MCPTools) == 0 && !strings.EqualFold(model.Name, BuiltInConciergeAgentName) {
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

	if requestedSquadID != "" {
		if err := m.store.SaveSquadMembership(&SquadMembership{
			AgentID: created.ID,
			SquadID: requestedSquadID,
			Role:    requestedRole,
		}); err != nil {
			_ = m.store.DeleteAgentModel(created.ID)
			return nil, err
		}
		m.clearCachedAgent(created.Name)
		m.clearSquadCaptainCache(requestedSquadID)
		return m.GetMemberByNameInSquad(created.Name, requestedSquadID)
	}

	return created, nil
}

func (m *SquadManager) resolveRequestedSquadRole(squadID string, requestedRole AgentKind) (AgentKind, error) {
	squadID = strings.TrimSpace(squadID)
	if squadID == "" {
		return "", nil
	}
	if _, err := m.store.GetTeam(squadID); err != nil {
		return "", err
	}

	role := normalizeMembershipRole(requestedRole)
	if role == "" || role == AgentKindAgent {
		role = AgentKindSpecialist
	}
	if role != AgentKindCaptain && role != AgentKindSpecialist {
		return "", fmt.Errorf("invalid squad role %q", role)
	}
	if err := m.ensureSingleLeadPerSquad("", squadID, role); err != nil {
		return "", err
	}
	return role, nil
}

func (m *SquadManager) UpdateAgent(_ context.Context, model *AgentModel) (*AgentModel, error) {
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

func (m *SquadManager) DeleteAgent(_ context.Context, name string) error {
	model, err := m.store.GetAgentModelByName(strings.TrimSpace(name))
	if err != nil {
		return err
	}
	for _, membership := range model.Squads {
		if err := m.ensureLeadRemovalAllowed(model, membership.SquadID, membership.Role); err != nil {
			return err
		}
	}
	m.clearCachedAgent(model.Name)
	return m.store.DeleteAgentModel(model.ID)
}

func (m *SquadManager) JoinSquad(_ context.Context, name, squadID string, role AgentKind) (*AgentModel, error) {
	model, err := m.store.GetAgentModelByName(strings.TrimSpace(name))
	if err != nil {
		return nil, err
	}
	squadID = strings.TrimSpace(squadID)
	if squadID == "" {
		return nil, fmt.Errorf("squad id is required")
	}
	if _, err := m.store.GetTeam(squadID); err != nil {
		return nil, err
	}
	role = normalizeMembershipRole(role)
	if role != AgentKindCaptain && role != AgentKindSpecialist {
		return nil, fmt.Errorf("invalid squad role %q", role)
	}
	if err := m.ensureSingleLeadPerSquad(model.ID, squadID, role); err != nil {
		return nil, err
	}
	if err := m.store.SaveSquadMembership(&SquadMembership{
		AgentID: model.ID,
		SquadID: squadID,
		Role:    role,
	}); err != nil {
		return nil, err
	}
	m.clearCachedAgent(model.Name)
	m.clearSquadCaptainCache(squadID)
	return m.GetMemberByNameInSquad(model.Name, squadID)
}

func (m *SquadManager) LeaveSquad(_ context.Context, name string, squadID ...string) (*AgentModel, error) {
	model, err := m.store.GetAgentModelByName(strings.TrimSpace(name))
	if err != nil {
		return nil, err
	}
	if len(model.Squads) == 0 {
		return nil, fmt.Errorf("agent '%s' is not in a squad", model.Name)
	}

	targetSquadID, targetRole, err := resolveLeaveTarget(model, squadID...)
	if err != nil {
		return nil, err
	}
	if err := m.ensureLeadRemovalAllowed(model, targetSquadID, targetRole); err != nil {
		return nil, err
	}
	if err := m.store.DeleteSquadMembership(model.ID, targetSquadID); err != nil {
		return nil, err
	}
	m.clearCachedAgent(model.Name)
	m.clearSquadCaptainCache(targetSquadID)
	return m.store.GetAgentModel(model.ID)
}

func (m *SquadManager) DeleteSquad(_ context.Context, squadID string) error {
	squadID = strings.TrimSpace(squadID)
	if squadID == "" {
		return fmt.Errorf("squad id is required")
	}
	if squadID == defaultSquadID {
		return fmt.Errorf("cannot delete the built-in AgentGo Squad")
	}
	squad, err := m.store.GetTeam(squadID)
	if err != nil {
		return err
	}

	memberships, err := m.store.ListSquadMembershipsBySquad(squadID)
	if err != nil {
		return err
	}
	for _, membership := range memberships {
		model, getErr := m.store.GetAgentModel(membership.AgentID)
		if getErr != nil {
			return getErr
		}
		m.clearCachedAgent(model.Name)
		m.clearSquadCaptainCache(squadID)
		if err := m.store.DeleteSquadMembership(membership.AgentID, squadID); err != nil {
			return err
		}
		remaining, remainingErr := m.store.ListSquadMembershipsByAgent(membership.AgentID)
		if remainingErr != nil {
			return remainingErr
		}
		if len(remaining) == 0 && isAutoGeneratedSquadLeadName(squad.Name, model.Name) {
			if err := m.store.DeleteAgentModel(model.ID); err != nil {
				return err
			}
		}
	}
	return m.store.DeleteTeam(squadID)
}

func (m *SquadManager) ensureSingleLeadPerSquad(agentID, squadID string, role AgentKind) error {
	if role != AgentKindCaptain {
		return nil
	}
	memberships, err := m.store.ListSquadMembershipsBySquad(squadID)
	if err != nil {
		return err
	}
	for _, membership := range memberships {
		if membership.Role == AgentKindCaptain && membership.AgentID != agentID {
			return fmt.Errorf("squad %s already has a lead agent", squadID)
		}
	}
	return nil
}

func (m *SquadManager) ensureLeadRemovalAllowed(model *AgentModel, squadID string, role AgentKind) error {
	if model == nil || role != AgentKindCaptain || strings.TrimSpace(squadID) == "" {
		return nil
	}
	memberships, err := m.store.ListSquadMembershipsBySquad(squadID)
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
		return fmt.Errorf("squad %s must keep exactly one lead agent", squadID)
	}
	return nil
}

func resolveLeaveTarget(model *AgentModel, squadIDs ...string) (string, AgentKind, error) {
	requested := ""
	if len(squadIDs) > 0 {
		requested = strings.TrimSpace(squadIDs[0])
	}
	if requested != "" {
		for _, membership := range model.Squads {
			if membership.SquadID == requested {
				return membership.SquadID, membership.Role, nil
			}
		}
		return "", "", fmt.Errorf("agent '%s' is not in squad %s", model.Name, requested)
	}
	if len(model.Squads) == 1 {
		return model.Squads[0].SquadID, model.Squads[0].Role, nil
	}
	return "", "", fmt.Errorf("agent '%s' belongs to multiple squads; specify which squad to leave", model.Name)
}

func (m *SquadManager) clearCachedAgent(name string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if cancel, exists := m.runningAgents[name]; exists {
		cancel()
		delete(m.runningAgents, name)
	}
	delete(m.services, name)
}

func (m *SquadManager) clearSquadCaptainCache(squadID string) {
	squadID = strings.TrimSpace(squadID)
	if squadID == "" {
		return
	}
	members, err := m.ListSquadAgentsForSquad(squadID)
	if err != nil {
		return
	}
	for _, member := range members {
		if member != nil && member.Kind == AgentKindCaptain {
			m.clearCachedAgent(member.Name)
		}
	}
}
