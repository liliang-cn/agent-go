package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/liliang-cn/agent-go/v2/pkg/agent"
	"github.com/spf13/cobra"
)

var explainRoutingJSON bool

var explainRoutingCmd = &cobra.Command{
	Use:   "explain-routing [message]",
	Short: "Explain how AgentGo routes a request",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		svc, err := agent.New(agent.BuiltInDispatcherAgentName).
			WithConfig(Cfg).
			WithDBPath(Cfg.AgentDBPath()).
			WithPTC(false).
			Build()
		if err != nil {
			return err
		}
		defer svc.Close()
		explanation, err := svc.ExplainRouting(context.Background(), args[0], nil)
		if err != nil {
			return err
		}
		if explainRoutingJSON {
			data, err := json.MarshalIndent(explanation, "", "  ")
			if err != nil {
				return err
			}
			fmt.Println(string(data))
			return nil
		}
		fmt.Printf("Goal: %s\n", explanation.Goal)
		fmt.Println("Signals:")
		for _, signal := range explanation.Signals {
			if signal.Intent == nil {
				continue
			}
			fmt.Printf("  - %s: intent=%s confidence=%.2f preferred=%s transition=%s topic=%s\n",
				signal.Source,
				signal.Intent.IntentType,
				signal.Intent.Confidence,
				valueOrDash(strings.TrimSpace(signal.Intent.PreferredAgent)),
				valueOrDash(strings.TrimSpace(signal.Intent.Transition)),
				valueOrDash(strings.TrimSpace(signal.Intent.Topic)),
			)
		}
		if explanation.Selected != nil {
			fmt.Printf("Selected: intent=%s confidence=%.2f preferred=%s transition=%s\n",
				explanation.Selected.IntentType,
				explanation.Selected.Confidence,
				valueOrDash(strings.TrimSpace(explanation.Selected.PreferredAgent)),
				valueOrDash(strings.TrimSpace(explanation.Selected.Transition)),
			)
		}
		return nil
	},
}

func init() {
	explainRoutingCmd.Flags().BoolVar(&explainRoutingJSON, "json", false, "Output as JSON")
}
