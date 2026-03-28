package agent

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"strings"
	"text/tabwriter"

	agenta2a "github.com/liliang-cn/agent-go/v2/pkg/a2a"
	"github.com/liliang-cn/agent-go/v2/pkg/agent"
	"github.com/spf13/cobra"
)

var (
	a2aHost             string
	a2aPort             int
	a2aPublicBaseURL    string
	a2aPathPrefix       string
	a2aIncludeBuiltIn   bool
	a2aIncludeCustom    bool
	a2aAgentVersion     string
	a2aProviderOrg      string
	a2aProviderURL      string
	a2aDocumentationURL string
	a2aInvokeBaseURL    string
	a2aInvokeStream     bool
)

type cliA2ACatalog struct {
	manager *agent.TeamManager
}

func (c cliA2ACatalog) ListAgents() ([]*agent.AgentModel, error) {
	return c.manager.ListAgents()
}

func (c cliA2ACatalog) GetAgentByName(name string) (*agent.AgentModel, error) {
	return c.manager.GetAgentByName(name)
}

func (c cliA2ACatalog) GetAgentByA2AID(a2aID string) (*agent.AgentModel, error) {
	return c.manager.GetAgentByA2AID(a2aID)
}

func (c cliA2ACatalog) GetAgentService(name string) (agenta2a.AgentRunner, error) {
	return c.manager.GetAgentService(name)
}

func (c cliA2ACatalog) ListTeams() ([]*agent.Team, error) {
	return c.manager.ListTeams()
}

func (c cliA2ACatalog) GetTeamByName(name string) (*agent.Team, error) {
	return c.manager.GetTeamByName(name)
}

func (c cliA2ACatalog) GetTeamByA2AID(a2aID string) (*agent.Team, error) {
	return c.manager.GetTeamByA2AID(a2aID)
}

func (c cliA2ACatalog) GetLeadAgentForTeam(teamID string) (*agent.AgentModel, error) {
	return c.manager.GetLeadAgentForTeam(teamID)
}

var agentA2ACmd = &cobra.Command{
	Use:   "a2a",
	Short: "Manage optional A2A exposure for standalone agents",
	Long: `Manage optional A2A exposure for standalone agents.

Core A2A protocol handling lives in pkg/a2a. This command only manages exposure and starts the HTTP endpoint.`,
}

var agentA2AListCmd = &cobra.Command{
	Use:   "list",
	Short: "List standalone agents and their A2A exposure state",
	RunE: func(cmd *cobra.Command, args []string) error {
		manager, err := getManager()
		if err != nil {
			return err
		}
		agents, err := manager.ListStandaloneAgents()
		if err != nil {
			return err
		}
		if len(agents) == 0 {
			fmt.Println("Standalone agents")
			fmt.Println("  (none)")
			return nil
		}

		cfg := agenta2a.DefaultConfig()
		if strings.TrimSpace(a2aPathPrefix) != "" {
			cfg.PathPrefix = a2aPathPrefix
		}

		w := tabwriter.NewWriter(os.Stdout, 0, 0, 3, ' ', 0)
		fmt.Fprintln(w, "NAME\tBUILT-IN\tA2A\tCARD")
		for _, model := range agents {
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\n",
				model.Name,
				boolFlag(isBuiltInAgent(model, nil)),
				boolFlag(model.EnableA2A),
				a2aCardPath(cfg.PathPrefix, firstNonEmpty(model.A2AID, model.Name)),
			)
		}
		return w.Flush()
	},
}

var agentA2AEnableCmd = &cobra.Command{
	Use:   "enable [name]",
	Short: "Enable A2A exposure for one standalone agent",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		manager, err := getManager()
		if err != nil {
			return err
		}
		model, err := manager.GetAgentByName(args[0])
		if err != nil {
			return err
		}
		if len(model.Teams) != 0 {
			return fmt.Errorf("agent %q is not a standalone agent; team A2A endpoints are not enabled yet", model.Name)
		}
		updated, err := manager.SetAgentA2AEnabled(context.Background(), model.Name, true)
		if err != nil {
			return err
		}
		fmt.Printf("Enabled A2A for agent '%s'.\n", updated.Name)
		return nil
	},
}

var agentA2ADisableCmd = &cobra.Command{
	Use:   "disable [name]",
	Short: "Disable A2A exposure for one standalone agent",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		manager, err := getManager()
		if err != nil {
			return err
		}
		updated, err := manager.SetAgentA2AEnabled(context.Background(), args[0], false)
		if err != nil {
			return err
		}
		fmt.Printf("Disabled A2A for agent '%s'.\n", updated.Name)
		return nil
	},
}

var agentA2AServeCmd = &cobra.Command{
	Use:   "serve",
	Short: "Serve A2A endpoints for opted-in standalone agents",
	RunE: func(cmd *cobra.Command, args []string) error {
		manager, err := getManager()
		if err != nil {
			return err
		}

		cfg := agenta2a.DefaultConfig()
		cfg.Enabled = true
		cfg.PublicBaseURL = strings.TrimSpace(a2aPublicBaseURL)
		cfg.PathPrefix = a2aPathPrefix
		cfg.IncludeBuiltInAgents = a2aIncludeBuiltIn
		cfg.IncludeCustomAgents = a2aIncludeCustom
		cfg.AgentVersion = strings.TrimSpace(a2aAgentVersion)
		cfg.ProviderOrganization = strings.TrimSpace(a2aProviderOrg)
		cfg.ProviderURL = strings.TrimSpace(a2aProviderURL)
		cfg.DocumentationURL = strings.TrimSpace(a2aDocumentationURL)

		server, err := agenta2a.NewServer(cliA2ACatalog{manager: manager}, cfg)
		if err != nil {
			return err
		}

		mux := http.NewServeMux()
		server.Mount(mux)

		addr := fmt.Sprintf("%s:%d", strings.TrimSpace(a2aHost), a2aPort)
		fmt.Printf("A2A server listening on %s\n", addr)
		fmt.Printf("A2A prefix: %s\n", cfg.PathPrefix)
		if cfg.PublicBaseURL != "" {
			fmt.Printf("Public base URL: %s\n", cfg.PublicBaseURL)
		}

		exposed, err := manager.ListA2AAgents()
		if err == nil {
			if len(exposed) == 0 {
				fmt.Println("Exposed standalone agents: none")
			} else {
				fmt.Println("Exposed standalone agents:")
				for _, model := range exposed {
					if model == nil {
						continue
					}
					if isBuiltInAgent(model, nil) && !cfg.IncludeBuiltInAgents {
						continue
					}
					if !isBuiltInAgent(model, nil) && !cfg.IncludeCustomAgents {
						continue
					}
					fmt.Printf("  - %s\n", model.Name)
				}
			}
		}

		return http.ListenAndServe(addr, mux)
	},
}

var agentA2AInvokeCmd = &cobra.Command{
	Use:   "invoke [name] [prompt]",
	Short: "Invoke one A2A-enabled standalone agent through its A2A endpoint",
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		manager, err := getManager()
		if err != nil {
			return err
		}
		model, err := manager.GetAgentByName(args[0])
		if err != nil {
			return err
		}
		if !model.EnableA2A {
			return fmt.Errorf("agent %q does not have A2A enabled", model.Name)
		}
		cardURL := strings.TrimRight(strings.TrimSpace(a2aInvokeBaseURL), "/") + agenta2a.AgentCardPath(a2aPathPrefix, firstNonEmpty(model.A2AID, model.Name))
		resolved, err := agenta2a.Connect(cmd.Context(), cardURL, agenta2a.ClientConfig{})
		if err != nil {
			return err
		}

		if a2aInvokeStream {
			for event, err := range resolved.StreamText(cmd.Context(), args[1]) {
				if err != nil {
					return err
				}
				if event.Text != "" {
					fmt.Println(event.Text)
				}
			}
			return nil
		}

		text, _, err := resolved.SendText(cmd.Context(), args[1])
		if err != nil {
			return err
		}
		fmt.Println(strings.TrimSpace(text))
		return nil
	},
}

func a2aCardPath(prefix, name string) string {
	return agenta2a.AgentCardPath(prefix, name)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func init() {
	agentA2ACmd.AddCommand(agentA2AListCmd)
	agentA2ACmd.AddCommand(agentA2AEnableCmd)
	agentA2ACmd.AddCommand(agentA2ADisableCmd)
	agentA2ACmd.AddCommand(agentA2AServeCmd)
	agentA2ACmd.AddCommand(agentA2AInvokeCmd)
}
