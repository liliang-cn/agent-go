package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/liliang-cn/agent-go/v2/pkg/agent"
	"github.com/liliang-cn/agent-go/v2/pkg/resource"
	"github.com/liliang-cn/agent-go/v2/pkg/store"
	"github.com/spf13/cobra"
)

var resourcesJSON bool
var resourcesPersist bool
var resourcesStored bool

var resourcesCmd = &cobra.Command{
	Use:   "resources",
	Short: "Inspect mounted AgentGo resources",
}

var resourcesListCmd = &cobra.Command{
	Use:   "list",
	Short: "List resources mounted into the default agent runtime",
	RunE: func(cmd *cobra.Command, args []string) error {
		if resourcesStored {
			db, err := store.NewAgentGoDB(Cfg.AgentDBPath())
			if err != nil {
				return err
			}
			defer db.Close()
			resources, err := db.ListResources()
			if err != nil {
				return err
			}
			return printResources(resources, resourcesJSON)
		}
		svc, err := agent.New(agent.BuiltInDispatcherAgentName).
			WithConfig(Cfg).
			WithDBPath(Cfg.AgentDBPath()).
			Build()
		if err != nil {
			return err
		}
		defer svc.Close()
		resources := svc.Resources(context.Background())
		if resourcesPersist {
			db, err := store.NewAgentGoDB(Cfg.AgentDBPath())
			if err != nil {
				return err
			}
			defer db.Close()
			for _, res := range resources {
				if err := db.SaveResource(res); err != nil {
					return err
				}
			}
		}
		return printResources(resources, resourcesJSON)
	},
}

var resourcesSetExecutionCmd = &cobra.Command{
	Use:   "set-execution [kind] [name] [mode]",
	Short: "Set execution policy for a tool-like resource",
	Args:  cobra.ExactArgs(3),
	RunE: func(cmd *cobra.Command, args []string) error {
		kind := resource.Kind(strings.TrimSpace(args[0]))
		name := strings.TrimSpace(args[1])
		mode := resource.ExecutionMode(strings.TrimSpace(args[2]))
		switch mode {
		case resource.ExecutionDual, resource.ExecutionDirectOnly, resource.ExecutionCodeOnly, resource.ExecutionDisabled:
		default:
			return fmt.Errorf("invalid execution mode: %s", mode)
		}
		if kind == "" || name == "" {
			return fmt.Errorf("kind and name are required")
		}
		db, err := store.NewAgentGoDB(Cfg.AgentDBPath())
		if err != nil {
			return err
		}
		defer db.Close()
		res := resource.Resource{
			ID:        string(kind) + ":" + name,
			Kind:      kind,
			Name:      name,
			Execution: mode,
		}
		if err := db.SaveResource(res); err != nil {
			return err
		}
		fmt.Printf("%s %s execution=%s\n", kind, name, mode)
		return nil
	},
}

func printResources(resources []resource.Resource, asJSON bool) error {
	if asJSON {
		data, err := json.MarshalIndent(resources, "", "  ")
		if err != nil {
			return err
		}
		fmt.Println(string(data))
		return nil
	}
	if len(resources) == 0 {
		fmt.Println("No resources mounted.")
		return nil
	}
	for _, res := range resources {
		line := fmt.Sprintf("%s %-12s %s", res.Kind, valueOrDash(string(res.Execution)), res.Name)
		if strings.TrimSpace(res.Provider) != "" {
			line += " (" + res.Provider + ")"
		}
		fmt.Println(line)
	}
	return nil
}

func init() {
	resourcesCmd.AddCommand(resourcesListCmd)
	resourcesCmd.AddCommand(resourcesSetExecutionCmd)
	resourcesListCmd.Flags().BoolVar(&resourcesJSON, "json", false, "Output as JSON")
	resourcesListCmd.Flags().BoolVar(&resourcesPersist, "persist", false, "Persist mounted resources into agentgo.db")
	resourcesListCmd.Flags().BoolVar(&resourcesStored, "stored", false, "List persisted resources from agentgo.db")
}
