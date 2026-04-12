package main

import (
	"encoding/json"
	"fmt"
	"sort"

	"github.com/liliang-cn/agent-go/v2/pkg/store"
	"github.com/spf13/cobra"
)

var configCmd = &cobra.Command{
	Use:   "config",
	Short: "Manage runtime config stored in agentgo.db",
}

var configListCmd = &cobra.Command{
	Use:   "list",
	Short: "List runtime config",
	RunE: func(cmd *cobra.Command, args []string) error {
		db, err := store.NewAgentGoDB(Cfg.AgentDBPath())
		if err != nil {
			return err
		}
		defer db.Close()
		values, err := db.ListConfig()
		if err != nil {
			return err
		}
		keys := make([]string, 0, len(values))
		for key := range values {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			fmt.Printf("%s=%s\n", key, values[key])
		}
		return nil
	},
}

var configGetCmd = &cobra.Command{
	Use:   "get [key]",
	Short: "Get a runtime config value",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		db, err := store.NewAgentGoDB(Cfg.AgentDBPath())
		if err != nil {
			return err
		}
		defer db.Close()
		value, err := db.GetConfig(args[0])
		if err != nil {
			return err
		}
		fmt.Println(value)
		return nil
	},
}

var configSetJSON bool

var configSetCmd = &cobra.Command{
	Use:   "set [key] [value]",
	Short: "Set a runtime config value",
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		key, value := args[0], args[1]
		if configSetJSON {
			var decoded any
			if err := json.Unmarshal([]byte(value), &decoded); err != nil {
				return fmt.Errorf("invalid JSON value: %w", err)
			}
		}
		db, err := store.NewAgentGoDB(Cfg.AgentDBPath())
		if err != nil {
			return err
		}
		defer db.Close()
		if err := db.SaveConfig(key, value); err != nil {
			return err
		}
		fmt.Printf("%s=%s\n", key, value)
		return nil
	},
}

func init() {
	configCmd.AddCommand(configListCmd)
	configCmd.AddCommand(configGetCmd)
	configCmd.AddCommand(configSetCmd)
	configSetCmd.Flags().BoolVar(&configSetJSON, "json", false, "Validate value as JSON before saving")
}
