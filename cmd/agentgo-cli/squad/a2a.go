package squad

import (
	"context"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	agenta2a "github.com/liliang-cn/agent-go/v2/pkg/a2a"
	"github.com/spf13/cobra"
)

var squadA2ACmd = &cobra.Command{
	Use:   "a2a",
	Short: "Manage optional A2A exposure for squads",
	Long: `Manage optional A2A exposure for squads.

Core A2A protocol handling lives in pkg/a2a. Use 'agentgo agent a2a serve' to actually serve both opted-in standalone agents and opted-in squads.`,
}

var (
	squadA2AInvokeBaseURL string
	squadA2AInvokeStream  bool
	squadA2APathPrefix    string
)

var squadA2AListCmd = &cobra.Command{
	Use:   "list",
	Short: "List squads and their A2A exposure state",
	RunE: func(cmd *cobra.Command, args []string) error {
		manager, err := getManager()
		if err != nil {
			return err
		}
		squads, err := manager.ListSquads()
		if err != nil {
			return err
		}
		if len(squads) == 0 {
			fmt.Println("Squads")
			fmt.Println("  (none)")
			return nil
		}

		cfg := agenta2a.DefaultConfig()
		w := tabwriter.NewWriter(os.Stdout, 0, 0, 3, ' ', 0)
		fmt.Fprintln(w, "NAME\tA2A\tCARD")
		for _, squad := range squads {
			if squad == nil {
				continue
			}
			fmt.Fprintf(w, "%s\t%s\t%s\n",
				squad.Name,
				yesNo(squad.EnableA2A),
				squadA2ACardPath(cfg.PathPrefix, firstNonEmptySquad(squad.A2AID, squad.Name)),
			)
		}
		return w.Flush()
	},
}

var squadA2AEnableCmd = &cobra.Command{
	Use:   "enable [name]",
	Short: "Enable A2A exposure for one squad",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		manager, err := getManager()
		if err != nil {
			return err
		}
		squad, err := manager.SetSquadA2AEnabled(context.Background(), args[0], true)
		if err != nil {
			return err
		}
		fmt.Printf("Enabled A2A for squad '%s'.\n", squad.Name)
		return nil
	},
}

var squadA2ADisableCmd = &cobra.Command{
	Use:   "disable [name]",
	Short: "Disable A2A exposure for one squad",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		manager, err := getManager()
		if err != nil {
			return err
		}
		squad, err := manager.SetSquadA2AEnabled(context.Background(), args[0], false)
		if err != nil {
			return err
		}
		fmt.Printf("Disabled A2A for squad '%s'.\n", squad.Name)
		return nil
	},
}

var squadA2AInvokeCmd = &cobra.Command{
	Use:   "invoke [name] [prompt]",
	Short: "Invoke one A2A-enabled squad through its A2A endpoint",
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		manager, err := getManager()
		if err != nil {
			return err
		}
		squad, err := manager.GetSquadByName(args[0])
		if err != nil {
			return err
		}
		if !squad.EnableA2A {
			return fmt.Errorf("squad %q does not have A2A enabled", squad.Name)
		}
		cardURL := strings.TrimRight(strings.TrimSpace(squadA2AInvokeBaseURL), "/") + agenta2a.SquadCardPath(squadA2APathPrefix, firstNonEmptySquad(squad.A2AID, squad.Name))
		resolved, err := agenta2a.Connect(cmd.Context(), cardURL, agenta2a.ClientConfig{})
		if err != nil {
			return err
		}
		if squadA2AInvokeStream {
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

func squadA2ACardPath(prefix, name string) string {
	return agenta2a.SquadCardPath(prefix, name)
}

func firstNonEmptySquad(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func init() {
	squadA2ACmd.AddCommand(squadA2AListCmd)
	squadA2ACmd.AddCommand(squadA2AEnableCmd)
	squadA2ACmd.AddCommand(squadA2ADisableCmd)
	squadA2ACmd.AddCommand(squadA2AInvokeCmd)
	squadA2AInvokeCmd.Flags().StringVar(&squadA2AInvokeBaseURL, "base-url", "http://127.0.0.1:7331", "base URL of a running AgentGo A2A server")
	squadA2AInvokeCmd.Flags().StringVar(&squadA2APathPrefix, "path-prefix", "/a2a", "HTTP path prefix mounted by the A2A server")
	squadA2AInvokeCmd.Flags().BoolVar(&squadA2AInvokeStream, "stream", false, "use A2A streaming invocation")
}
