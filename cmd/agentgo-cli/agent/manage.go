package agent

import (
	"context"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/liliang-cn/agent-go/pkg/agent"
	"github.com/liliang-cn/agent-go/pkg/config"
	"github.com/spf13/cobra"
)

var (
	runAgentName         string
	agentDescription     string
	agentInstructions    string
	agentProvider        string
	agentModel           string
	agentUpdateName      string
	agentUpdateRole      string
	agentUpdateSquadID   string
	agentUpdateSquadName string
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

		squads, err := manager.ListSquads()
		if err != nil {
			return err
		}
		squadNames := make(map[string]string, len(squads))
		for _, squad := range squads {
			squadNames[squad.ID] = squad.Name
		}

		w := tabwriter.NewWriter(os.Stdout, 0, 0, 3, ' ', 0)
		fmt.Fprintln(w, "NAME\tKIND\tSQUADS\tMODEL\tBUILT-IN")
		for _, model := range agents {
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n",
				model.Name,
				kindDisplay(model.Kind),
				squadMembershipDisplay(model, squadNames),
				effectiveModelDisplay(model, displayCfg),
				boolFlag(isBuiltInAgent(model, squadNames)),
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
		squads, err := manager.ListSquads()
		if err != nil {
			return err
		}
		squadNames := make(map[string]string, len(squads))
		for _, squad := range squads {
			squadNames[squad.ID] = squad.Name
		}
		model, err := manager.GetAgentByName(args[0])
		if err != nil {
			return err
		}

		fmt.Printf("Name: %s\n", model.Name)
		fmt.Printf("Base Kind: %s\n", kindDisplay(model.Kind))
		fmt.Printf("Squads: %s\n", squadMembershipDisplay(model, squadNames))
		fmt.Printf("Model: %s\n", effectiveModelDisplay(model, displayCfg))
		fmt.Printf("Preferred Provider: %s\n", valueOrDash(strings.TrimSpace(model.PreferredProvider)))
		fmt.Printf("Preferred Model: %s\n", valueOrDash(strings.TrimSpace(model.PreferredModel)))
		fmt.Printf("Built-in: %s\n", boolFlag(isBuiltInAgent(model, squadNames)))
		fmt.Printf("Description: %s\n", valueOrDash(model.Description))
		fmt.Printf("RAG: %s\n", enabledState(model.EnableRAG))
		fmt.Printf("Memory: %s\n", enabledState(model.EnableMemory))
		fmt.Printf("MCP: %s\n", enabledState(model.EnableMCP))
		fmt.Printf("PTC: %s\n", enabledState(model.EnablePTC))
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
	Short: "Join an agent to a squad",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		manager, err := getManager()
		if err != nil {
			return err
		}
		squadID, err := resolveAgentSquadID(manager, strings.TrimSpace(agentUpdateSquadID), strings.TrimSpace(agentUpdateSquadName))
		if err != nil {
			return err
		}
		if squadID == "" {
			return fmt.Errorf("use --squad or --squad-id")
		}
		role, err := normalizeAgentRole(strings.TrimSpace(agentUpdateRole))
		if err != nil {
			return err
		}
		model, err := manager.JoinSquad(context.Background(), args[0], squadID, role)
		if err != nil {
			return err
		}
		fmt.Printf("Agent '%s' joined squad '%s' as %s.\n", model.Name, squadID, kindDisplay(model.Kind))
		return nil
	},
}

var agentLeaveCmd = &cobra.Command{
	Use:   "leave [name]",
	Short: "Remove an agent from its squad",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		manager, err := getManager()
		if err != nil {
			return err
		}
		model, err := manager.LeaveSquad(context.Background(), args[0])
		if err != nil {
			return err
		}
		fmt.Printf("Agent '%s' left its squad.\n", model.Name)
		return nil
	},
}

func getManager() (*agent.SquadManager, error) {
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
	manager := agent.NewSquadManager(store)
	if err := manager.SeedDefaultMembers(); err != nil {
		return nil, err
	}
	return manager, nil
}

func resolveAgentSquadID(manager *agent.SquadManager, squadID, squadName string) (string, error) {
	if squadID != "" && squadName != "" {
		return "", fmt.Errorf("use either --squad-id or --squad, not both")
	}
	if squadID != "" {
		return squadID, nil
	}
	if squadName == "" {
		return "", nil
	}
	squads, err := manager.ListSquads()
	if err != nil {
		return "", err
	}
	for _, squad := range squads {
		if strings.EqualFold(strings.TrimSpace(squad.Name), squadName) {
			return squad.ID, nil
		}
	}
	return "", fmt.Errorf("unknown squad: %s", squadName)
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

func squadDisplay(squadID string, squadNames map[string]string) string {
	squadID = strings.TrimSpace(squadID)
	if squadID == "" {
		return "-"
	}
	if squadName := strings.TrimSpace(squadNames[squadID]); squadName != "" {
		return squadName + " (" + squadID + ")"
	}
	return squadID
}

func squadMembershipDisplay(model *agent.AgentModel, squadNames map[string]string) string {
	if model == nil || len(model.Squads) == 0 {
		return "-"
	}
	items := make([]string, 0, len(model.Squads))
	for _, membership := range model.Squads {
		squadID := strings.TrimSpace(membership.SquadID)
		if squadID == "" {
			continue
		}
		squadName := strings.TrimSpace(squadNames[squadID])
		if squadName == "" {
			squadName = squadID
		}
		role := kindDisplay(membership.Role)
		items = append(items, fmt.Sprintf("%s (%s, %s)", squadName, squadID, role))
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

func isBuiltInAgent(model *agent.AgentModel, squadNames map[string]string) bool {
	if model == nil {
		return false
	}
	switch strings.TrimSpace(model.ID) {
	case "agent-concierge-001", "agent-assistant-001", "agent-operator-001", "agent-captain-001", "agent-stakeholder-001":
		return true
	}
	if strings.EqualFold(model.Name, "Concierge") && len(model.Squads) == 0 {
		return true
	}
	if strings.EqualFold(model.Name, "Assistant") && len(model.Squads) == 0 {
		return true
	}
	if strings.EqualFold(model.Name, "Operator") && len(model.Squads) == 0 {
		return true
	}
	if strings.EqualFold(model.Name, "Stakeholder") && len(model.Squads) == 0 {
		return true
	}
	for _, membership := range model.Squads {
		if membership.Role == agent.AgentKindCaptain && strings.EqualFold(model.Name, "Captain") && strings.TrimSpace(membership.SquadID) == "squad-default-001" {
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
