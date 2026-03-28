package team

import (
	"context"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	agenta2a "github.com/liliang-cn/agent-go/v2/pkg/a2a"
	"github.com/spf13/cobra"
)

var teamA2ACmd = &cobra.Command{
	Use:   "a2a",
	Short: "Manage optional A2A exposure for teams",
	Long: `Manage optional A2A exposure for teams.

Core A2A protocol handling lives in pkg/a2a. Use 'agentgo agent a2a serve' to actually serve both opted-in standalone agents and opted-in teams.`,
}

var (
	teamA2AInvokeBaseURL string
	teamA2AInvokeStream  bool
	teamA2APathPrefix    string
)

var teamA2AListCmd = &cobra.Command{
	Use:   "list",
	Short: "List teams and their A2A exposure state",
	RunE: func(cmd *cobra.Command, args []string) error {
		manager, err := getManager()
		if err != nil {
			return err
		}
		teams, err := manager.ListTeams()
		if err != nil {
			return err
		}
		if len(teams) == 0 {
			fmt.Println("Teams")
			fmt.Println("  (none)")
			return nil
		}

		cfg := agenta2a.DefaultConfig()
		w := tabwriter.NewWriter(os.Stdout, 0, 0, 3, ' ', 0)
		fmt.Fprintln(w, "NAME\tA2A\tCARD")
		for _, team := range teams {
			if team == nil {
				continue
			}
			fmt.Fprintf(w, "%s\t%s\t%s\n",
				team.Name,
				yesNo(team.EnableA2A),
				teamA2ACardPath(cfg.PathPrefix, firstNonEmptyTeam(team.A2AID, team.Name)),
			)
		}
		return w.Flush()
	},
}

var teamA2AEnableCmd = &cobra.Command{
	Use:   "enable [name]",
	Short: "Enable A2A exposure for one team",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		manager, err := getManager()
		if err != nil {
			return err
		}
		team, err := manager.SetTeamA2AEnabled(context.Background(), args[0], true)
		if err != nil {
			return err
		}
		fmt.Printf("Enabled A2A for team '%s'.\n", team.Name)
		return nil
	},
}

var teamA2ADisableCmd = &cobra.Command{
	Use:   "disable [name]",
	Short: "Disable A2A exposure for one team",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		manager, err := getManager()
		if err != nil {
			return err
		}
		team, err := manager.SetTeamA2AEnabled(context.Background(), args[0], false)
		if err != nil {
			return err
		}
		fmt.Printf("Disabled A2A for team '%s'.\n", team.Name)
		return nil
	},
}

var teamA2AInvokeCmd = &cobra.Command{
	Use:   "invoke [name] [prompt]",
	Short: "Invoke one A2A-enabled team through its A2A endpoint",
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		manager, err := getManager()
		if err != nil {
			return err
		}
		team, err := manager.GetTeamByName(args[0])
		if err != nil {
			return err
		}
		if !team.EnableA2A {
			return fmt.Errorf("team %q does not have A2A enabled", team.Name)
		}
		cardURL := strings.TrimRight(strings.TrimSpace(teamA2AInvokeBaseURL), "/") + agenta2a.TeamCardPath(teamA2APathPrefix, firstNonEmptyTeam(team.A2AID, team.Name))
		resolved, err := agenta2a.Connect(cmd.Context(), cardURL, agenta2a.ClientConfig{})
		if err != nil {
			return err
		}
		if teamA2AInvokeStream {
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

func teamA2ACardPath(prefix, name string) string {
	return agenta2a.TeamCardPath(prefix, name)
}

func firstNonEmptyTeam(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func init() {
	teamA2ACmd.AddCommand(teamA2AListCmd)
	teamA2ACmd.AddCommand(teamA2AEnableCmd)
	teamA2ACmd.AddCommand(teamA2ADisableCmd)
	teamA2ACmd.AddCommand(teamA2AInvokeCmd)
	teamA2AInvokeCmd.Flags().StringVar(&teamA2AInvokeBaseURL, "base-url", "http://127.0.0.1:7331", "base URL of a running AgentGo A2A server")
	teamA2AInvokeCmd.Flags().StringVar(&teamA2APathPrefix, "path-prefix", "/a2a", "HTTP path prefix mounted by the A2A server")
	teamA2AInvokeCmd.Flags().BoolVar(&teamA2AInvokeStream, "stream", false, "use A2A streaming invocation")
}
