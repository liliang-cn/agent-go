package teamdemo

import (
	"context"
	"fmt"
	"os"
	"strings"

	agenta2a "github.com/liliang-cn/agent-go/v2/pkg/a2a"
	"github.com/liliang-cn/agent-go/v2/pkg/agent"
	"github.com/liliang-cn/agent-go/v2/pkg/config"
)

const ExampleHomeEnv = "AGENTGO_EXAMPLE_HOME"

func NewManager(exampleName string) (*agent.TeamManager, *config.Config, error) {
	cfg, err := config.Load("")
	if err != nil {
		return nil, nil, fmt.Errorf("load config: %w", err)
	}

	home := strings.TrimSpace(os.Getenv(ExampleHomeEnv))
	if home == "" {
		home, err = os.MkdirTemp("", exampleName+"-")
		if err != nil {
			return nil, nil, fmt.Errorf("create example home: %w", err)
		}
	} else if err := os.MkdirAll(home, 0755); err != nil {
		return nil, nil, fmt.Errorf("prepare example home: %w", err)
	}

	cfgCopy := *cfg
	cfgCopy.Home = home
	cfgCopy.ApplyHomeLayout()

	store, err := agent.NewStore(cfgCopy.AgentDBPath())
	if err != nil {
		return nil, nil, fmt.Errorf("open agent store: %w", err)
	}

	manager := agent.NewTeamManager(store)
	manager.SetConfig(&cfgCopy)
	if err := manager.SeedDefaultMembers(); err != nil {
		return nil, nil, fmt.Errorf("seed default members: %w", err)
	}

	return manager, &cfgCopy, nil
}

func DefaultTeam(manager *agent.TeamManager) (*agent.Team, error) {
	teams, err := manager.ListTeams()
	if err != nil {
		return nil, err
	}
	if len(teams) == 0 || teams[0] == nil {
		return nil, fmt.Errorf("no teams available")
	}
	return teams[0], nil
}

func TeamA2AID(team *agent.Team) string {
	if team == nil {
		return ""
	}
	if strings.TrimSpace(team.A2AID) != "" {
		return strings.TrimSpace(team.A2AID)
	}
	return strings.TrimSpace(team.Name)
}

type A2ACatalog struct {
	Manager *agent.TeamManager
}

func (c A2ACatalog) ListAgents() ([]*agent.AgentModel, error) {
	return c.Manager.ListAgents()
}

func (c A2ACatalog) GetAgentByName(name string) (*agent.AgentModel, error) {
	return c.Manager.GetAgentByName(name)
}

func (c A2ACatalog) GetAgentByA2AID(a2aID string) (*agent.AgentModel, error) {
	return c.Manager.GetAgentByA2AID(a2aID)
}

func (c A2ACatalog) GetAgentService(name string) (agenta2a.AgentRunner, error) {
	return c.Manager.GetAgentService(name)
}

func (c A2ACatalog) ListTeams() ([]*agent.Team, error) {
	return c.Manager.ListTeams()
}

func (c A2ACatalog) GetTeamByName(name string) (*agent.Team, error) {
	return c.Manager.GetTeamByName(name)
}

func (c A2ACatalog) GetTeamByA2AID(a2aID string) (*agent.Team, error) {
	return c.Manager.GetTeamByA2AID(a2aID)
}

func (c A2ACatalog) GetLeadAgentForTeam(teamID string) (*agent.AgentModel, error) {
	return c.Manager.GetLeadAgentForTeam(teamID)
}

func (c A2ACatalog) SubmitTeamRequest(ctx context.Context, req *agent.TeamRequest) (*agent.TeamResponse, error) {
	return c.Manager.SubmitTeamRequest(ctx, req)
}

func (c A2ACatalog) GetTeamResponse(responseID string) (*agent.TeamResponse, error) {
	return c.Manager.GetTeamResponse(responseID)
}

func (c A2ACatalog) SubscribeTeamResponse(responseID string) (<-chan *agent.TeamResponseEvent, func(), error) {
	return c.Manager.SubscribeTeamResponse(responseID)
}

func (c A2ACatalog) CancelTeamResponse(ctx context.Context, responseID string) (*agent.TeamResponse, error) {
	return c.Manager.CancelTeamResponse(ctx, responseID)
}
