package agent

import "context"

// AddLeadAgent is the generic public API for adding a lead-role agent directly into a team.
func (m *TeamManager) AddLeadAgent(ctx context.Context, teamID, name, description, instructions string) (*AgentModel, error) {
	return m.AddCaptain(ctx, teamID, name, description, instructions)
}

// ListLeadAgents is the generic public API for listing all lead-role team agents.
func (m *TeamManager) ListLeadAgents() ([]*AgentModel, error) {
	return m.ListCaptains()
}

// AddTeamAgent is the generic public API for adding an agent directly into a team with a role.
func (m *TeamManager) AddTeamAgent(ctx context.Context, teamID, name string, role AgentKind, description, instructions string) (*AgentModel, error) {
	if role == "" {
		role = AgentKindSpecialist
	}
	return m.CreateMember(ctx, &AgentModel{
		TeamID:       teamID,
		Name:         name,
		Kind:         role,
		Description:  description,
		Instructions: instructions,
	})
}

// CreateTeamAgent is the public alias for creating an agent directly inside a team.
func (m *TeamManager) CreateTeamAgent(ctx context.Context, model *AgentModel) (*AgentModel, error) {
	return m.CreateMember(ctx, model)
}

// ListTeamAgents is the public alias for listing all agents that currently belong to teams.
func (m *TeamManager) ListTeamAgents() ([]*AgentModel, error) {
	return m.ListMembers()
}

// GetTeamAgentByName is the public alias for resolving a team agent by name.
func (m *TeamManager) GetTeamAgentByName(name string) (*AgentModel, error) {
	return m.GetMemberByName(name)
}
