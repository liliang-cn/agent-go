package agent

import (
	"context"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/liliang-cn/agent-go/v2/pkg/agent"
	"github.com/liliang-cn/agent-go/v2/pkg/config"
	"github.com/spf13/cobra"
)

var (
	runAgentName        string
	agentDescription    string
	agentInstructions   string
	agentProvider       string
	agentModel          string
	agentA2AEnabled     bool
	agentUpdateName     string
	agentUpdateRole     string
	agentUpdateTeamID   string
	agentUpdateTeamName string
)

var agentListCmd = &cobra.Command{
	Use:   "list",
	Short: "List agents",
	RunE: func(cmd *cobra.Command, args []string) error {
		manager, err := getManager()
		if err != nil {
			return err
		}
		displayCfg := Cfg
		if displayCfg == nil {
			loaded, loadErr := config.Load("")
			if loadErr == nil {
				displayCfg = loaded
			}
		}
		agents, err := manager.ListAgents()
		if err != nil {
			return err
		}
		if len(agents) == 0 {
			fmt.Println("Agents")
			fmt.Println("  (none)")
			return nil
		}

		teams, err := manager.ListTeams()
		if err != nil {
			return err
		}
		teamNames := make(map[string]string, len(teams))
		for _, team := range teams {
			teamNames[team.ID] = team.Name
		}

		w := tabwriter.NewWriter(os.Stdout, 0, 0, 3, ' ', 0)
		fmt.Fprintln(w, "NAME\tKIND\tTEAMS\tMODEL\tBUILT-IN\tA2A")
		for _, model := range agents {
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n",
				model.Name,
				kindDisplay(model.Kind),
				teamMembershipDisplay(model, teamNames),
				effectiveModelDisplay(model, displayCfg),
				boolFlag(isBuiltInAgent(model, teamNames)),
				boolFlag(model.EnableA2A),
			)
		}
		w.Flush()
		return nil
	},
}

var agentShowCmd = &cobra.Command{
	Use:   "show [name]",
	Short: "Show agent details",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		manager, err := getManager()
		if err != nil {
			return err
		}
		displayCfg := Cfg
		if displayCfg == nil {
			loaded, loadErr := config.Load("")
			if loadErr == nil {
				displayCfg = loaded
			}
		}
		teams, err := manager.ListTeams()
		if err != nil {
			return err
		}
		teamNames := make(map[string]string, len(teams))
		for _, team := range teams {
			teamNames[team.ID] = team.Name
		}
		model, err := manager.GetAgentByName(args[0])
		if err != nil {
			return err
		}

		fmt.Printf("Name: %s\n", model.Name)
		fmt.Printf("Base Kind: %s\n", kindDisplay(model.Kind))
		fmt.Printf("Teams: %s\n", teamMembershipDisplay(model, teamNames))
		fmt.Printf("Model: %s\n", effectiveModelDisplay(model, displayCfg))
		fmt.Printf("Preferred Provider: %s\n", valueOrDash(strings.TrimSpace(model.PreferredProvider)))
		fmt.Printf("Preferred Model: %s\n", valueOrDash(strings.TrimSpace(model.PreferredModel)))
		fmt.Printf("Built-in: %s\n", boolFlag(isBuiltInAgent(model, teamNames)))
		fmt.Printf("Description: %s\n", valueOrDash(model.Description))
		fmt.Printf("RAG: %s\n", enabledState(model.EnableRAG))
		fmt.Printf("Memory: %s\n", enabledState(model.EnableMemory))
		fmt.Printf("MCP: %s\n", enabledState(model.EnableMCP))
		fmt.Printf("PTC: %s\n", enabledState(model.EnablePTC))
		fmt.Printf("A2A: %s\n", enabledState(model.EnableA2A))
		fmt.Printf("Skills: %s\n", joinOrDash(model.Skills))
		fmt.Printf("MCP Tools: %s\n", joinOrDash(model.MCPTools))
		fmt.Printf("Created: %s\n", formatTimestamp(model.CreatedAt))
		fmt.Printf("Updated: %s\n", formatTimestamp(model.UpdatedAt))
		if strings.TrimSpace(model.Instructions) != "" {
			fmt.Printf("\nInstructions:\n%s\n", model.Instructions)
		}
		return nil
	},
}

var agentAddCmd = &cobra.Command{
	Use:     "add [name]",
	Aliases: []string{"create"},
	Short:   "Add a standalone agent",
	Args:    cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		manager, err := getManager()
		if err != nil {
			return err
		}

		name := strings.TrimSpace(args[0])
		if name == "" {
			return fmt.Errorf("agent name is required")
		}
		description := strings.TrimSpace(agentDescription)
		if description == "" {
			description = name
		}
		instructions := strings.TrimSpace(agentInstructions)
		if instructions == "" {
			instructions = description
		}

		model, err := manager.CreateAgent(context.Background(), &agent.AgentModel{
			Name:              name,
			Kind:              agent.AgentKindAgent,
			Description:       description,
			Instructions:      instructions,
			PreferredProvider: strings.TrimSpace(agentProvider),
			PreferredModel:    strings.TrimSpace(agentModel),
			Model:             strings.TrimSpace(agentModel),
			EnableA2A:         agentA2AEnabled,
		})
		if err != nil {
			return err
		}
		fmt.Printf("Added agent '%s'.\n", model.Name)
		return nil
	},
}

var agentUpdateCmd = &cobra.Command{
	Use:   "update [name]",
	Short: "Update agent metadata",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		manager, err := getManager()
		if err != nil {
			return err
		}
		current, err := manager.GetAgentByName(args[0])
		if err != nil {
			return err
		}
		updated := &agent.AgentModel{
			ID:                current.ID,
			Name:              current.Name,
			Kind:              current.Kind,
			TeamID:            current.TeamID,
			Description:       current.Description,
			Instructions:      current.Instructions,
			PreferredProvider: current.PreferredProvider,
			PreferredModel:    current.PreferredModel,
			Model:             current.Model,
			MCPTools:          current.MCPTools,
			Skills:            current.Skills,
			EnableRAG:         current.EnableRAG,
			EnableMemory:      current.EnableMemory,
			EnablePTC:         current.EnablePTC,
			EnableMCP:         current.EnableMCP,
			EnableA2A:         current.EnableA2A,
		}
		if strings.TrimSpace(agentUpdateName) != "" {
			updated.Name = strings.TrimSpace(agentUpdateName)
		}
		if strings.TrimSpace(agentDescription) != "" {
			updated.Description = strings.TrimSpace(agentDescription)
		}
		if strings.TrimSpace(agentInstructions) != "" {
			updated.Instructions = strings.TrimSpace(agentInstructions)
		}
		if strings.TrimSpace(agentProvider) != "" {
			updated.PreferredProvider = strings.TrimSpace(agentProvider)
		}
		if strings.TrimSpace(agentModel) != "" {
			updated.PreferredModel = strings.TrimSpace(agentModel)
			updated.Model = strings.TrimSpace(agentModel)
		}
		if agentUpdateRole != "" {
			role, normalizeErr := normalizeAgentRole(strings.TrimSpace(agentUpdateRole))
			if normalizeErr != nil {
				return normalizeErr
			}
			updated.Kind = role
		}
		if cmd.Flags().Changed("a2a") {
			updated.EnableA2A = agentA2AEnabled
		}
		model, err := manager.UpdateAgent(context.Background(), updated)
		if err != nil {
			return err
		}
		fmt.Printf("Updated agent '%s'.\n", model.Name)
		return nil
	},
}

var agentDeleteCmd = &cobra.Command{
	Use:     "delete [name]",
	Aliases: []string{"remove", "rm"},
	Short:   "Delete an agent",
	Args:    cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		manager, err := getManager()
		if err != nil {
			return err
		}
		if err := manager.DeleteAgent(context.Background(), args[0]); err != nil {
			return err
		}
		fmt.Printf("Deleted agent '%s'.\n", strings.TrimSpace(args[0]))
		return nil
	},
}

var agentJoinCmd = &cobra.Command{
	Use:   "join [name]",
	Short: "Join an agent to a team",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		manager, err := getManager()
		if err != nil {
			return err
		}
		teamID, err := resolveAgentTeamID(manager, strings.TrimSpace(agentUpdateTeamID), strings.TrimSpace(agentUpdateTeamName))
		if err != nil {
			return err
		}
		if teamID == "" {
			return fmt.Errorf("use --team or --team-id")
		}
		role, err := normalizeAgentRole(strings.TrimSpace(agentUpdateRole))
		if err != nil {
			return err
		}
		model, err := manager.JoinTeam(context.Background(), args[0], teamID, role)
		if err != nil {
			return err
		}
		fmt.Printf("Agent '%s' joined team '%s' as %s.\n", model.Name, teamID, kindDisplay(model.Kind))
		return nil
	},
}

var agentLeaveCmd = &cobra.Command{
	Use:   "leave [name]",
	Short: "Remove an agent from its team",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		manager, err := getManager()
		if err != nil {
			return err
		}
		model, err := manager.LeaveTeam(context.Background(), args[0])
		if err != nil {
			return err
		}
		fmt.Printf("Agent '%s' left its team.\n", model.Name)
		return nil
	},
}

func getManager() (*agent.TeamManager, error) {
	cfg := Cfg
	if cfg == nil {
		loaded, err := config.Load("")
		if err != nil {
			return nil, err
		}
		cfg = loaded
	}
	store, err := agent.NewStore(cfg.AgentDBPath())
	if err != nil {
		return nil, err
	}
	manager := agent.NewTeamManager(store)
	manager.SetConfig(cfg)
	if err := manager.SeedDefaultMembers(); err != nil {
		return nil, err
	}
	return manager, nil
}

func resolveAgentTeamID(manager *agent.TeamManager, teamID, teamName string) (string, error) {
	if teamID != "" && teamName != "" {
		return "", fmt.Errorf("use either --team-id or --team, not both")
	}
	if teamID != "" {
		return teamID, nil
	}
	if teamName == "" {
		return "", nil
	}
	teams, err := manager.ListTeams()
	if err != nil {
		return "", err
	}
	for _, team := range teams {
		if strings.EqualFold(strings.TrimSpace(team.Name), teamName) {
			return team.ID, nil
		}
	}
	return "", fmt.Errorf("unknown team: %s", teamName)
}

func formatTimestamp(ts time.Time) string {
	if ts.IsZero() {
		return "-"
	}
	return ts.Format(time.RFC3339)
}

func valueOrDash(v string) string {
	if strings.TrimSpace(v) == "" {
		return "-"
	}
	return v
}

func joinOrDash(values []string) string {
	if len(values) == 0 {
		return "-"
	}
	return strings.Join(values, ", ")
}

func enabledState(v bool) string {
	if v {
		return "enabled"
	}
	return "disabled"
}

func teamDisplay(teamID string, teamNames map[string]string) string {
	teamID = strings.TrimSpace(teamID)
	if teamID == "" {
		return "-"
	}
	if teamName := strings.TrimSpace(teamNames[teamID]); teamName != "" {
		return teamName + " (" + teamID + ")"
	}
	return teamID
}

func teamMembershipDisplay(model *agent.AgentModel, teamNames map[string]string) string {
	if model == nil || len(model.Teams) == 0 {
		return "-"
	}
	items := make([]string, 0, len(model.Teams))
	for _, membership := range model.Teams {
		teamID := strings.TrimSpace(membership.TeamID)
		if teamID == "" {
			continue
		}
		teamName := strings.TrimSpace(teamNames[teamID])
		if teamName == "" {
			teamName = teamID
		}
		role := kindDisplay(membership.Role)
		items = append(items, fmt.Sprintf("%s (%s, %s)", teamName, teamID, role))
	}
	if len(items) == 0 {
		return "-"
	}
	return strings.Join(items, "; ")
}

func boolFlag(v bool) string {
	if v {
		return "yes"
	}
	return "no"
}

func effectiveModelDisplay(model *agent.AgentModel, cfg *config.Config) string {
	if model != nil {
		preferredProvider := strings.TrimSpace(model.PreferredProvider)
		preferredModel := strings.TrimSpace(model.PreferredModel)
		switch {
		case preferredProvider != "" && preferredModel != "":
			return preferredModel + " via " + preferredProvider
		case preferredModel != "":
			return preferredModel
		case preferredProvider != "":
			return preferredProvider + " (provider)"
		case strings.TrimSpace(model.Model) != "":
			return model.Model
		}
	}
	if cfg == nil || len(cfg.LLM.Providers) == 0 {
		return "-"
	}
	defaultProvider := cfg.LLM.Providers[0]
	if strings.TrimSpace(defaultProvider.ModelName) == "" {
		return "-"
	}
	return defaultProvider.ModelName + " (default)"
}

func isBuiltInAgent(model *agent.AgentModel, teamNames map[string]string) bool {
	if model == nil {
		return false
	}
	switch strings.TrimSpace(model.ID) {
	case "agent-concierge-001", "agent-assistant-001", "agent-operator-001", "agent-captain-001", "agent-stakeholder-001", "agent-archivist-001", "agent-verifier-001":
		return true
	}
	if strings.EqualFold(model.Name, "Concierge") && len(model.Teams) == 0 {
		return true
	}
	if strings.EqualFold(model.Name, "Assistant") && len(model.Teams) == 0 {
		return true
	}
	if strings.EqualFold(model.Name, "Operator") && len(model.Teams) == 0 {
		return true
	}
	if strings.EqualFold(model.Name, "Stakeholder") && len(model.Teams) == 0 {
		return true
	}
	if strings.EqualFold(model.Name, "Archivist") && len(model.Teams) == 0 {
		return true
	}
	if strings.EqualFold(model.Name, "Verifier") && len(model.Teams) == 0 {
		return true
	}
	for _, membership := range model.Teams {
		if membership.Role == agent.AgentKindCaptain && strings.EqualFold(model.Name, "Captain") && strings.TrimSpace(membership.TeamID) == "team-default-001" {
			return true
		}
	}
	return false
}

func normalizeAgentRole(input string) (agent.AgentKind, error) {
	switch strings.ToLower(strings.TrimSpace(input)) {
	case "":
		return agent.AgentKindSpecialist, nil
	case "agent":
		return agent.AgentKindAgent, nil
	case "specialist":
		return agent.AgentKindSpecialist, nil
	case "captain", "lead", "lead-agent", "leader":
		return agent.AgentKindCaptain, nil
	default:
		return "", fmt.Errorf("invalid role %q: use agent, captain, or specialist", input)
	}
}

func kindDisplay(kind agent.AgentKind) string {
	switch kind {
	case agent.AgentKindCaptain:
		return "captain"
	case agent.AgentKindSpecialist:
		return "specialist"
	case agent.AgentKindAgent:
		return "agent"
	default:
		return strings.ToLower(string(kind))
	}
}
