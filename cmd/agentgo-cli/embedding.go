package main

import (
	"fmt"
	"strings"

	"github.com/liliang-cn/agent-go/pkg/pool"
	"github.com/liliang-cn/agent-go/pkg/services"
	"github.com/liliang-cn/agent-go/pkg/store"
	"github.com/spf13/cobra"
)

var (
	embFlagName           string
	embFlagURL            string
	embFlagKey            string
	embFlagModel          string
	embFlagCapability     int
	embFlagMaxConcurrency int
	embFlagEnabled        bool
	embFlagStrategy       string
	embFlagPoolEnabled    bool
)

var embeddingCmd = &cobra.Command{
	Use:   "embedding",
	Short: "Embedding provider management",
	Long:  `Manage embedding providers (stored in agentgo.db) used for RAG and vector search.`,
}

var embeddingListCmd = &cobra.Command{
	Use:   "list",
	Short: "List all embedding providers",
	RunE: func(cmd *cobra.Command, args []string) error {
		svc := services.GetGlobalPoolService()
		if !svc.IsInitialized() {
			return fmt.Errorf("pool service not initialized")
		}

		providers, err := svc.ListEmbeddingProviders()
		if err != nil {
			return fmt.Errorf("failed to list providers: %w", err)
		}

		if len(providers) == 0 {
			fmt.Println("No embedding providers configured. Use 'embedding add' to add one.")
			return nil
		}

		fmt.Printf("%-20s %-10s %-40s %-35s %-12s %-5s\n",
			"NAME", "ENABLED", "URL", "MODEL", "CONCURRENCY", "CAP")
		fmt.Println(strings.Repeat("-", 130))
		for _, p := range providers {
			enabled := "yes"
			if !p.Enabled {
				enabled = "no"
			}
			fmt.Printf("%-20s %-10s %-40s %-35s %-12d %-5d\n",
				p.Name, enabled, p.BaseURL, p.ModelName, p.MaxConcurrency, p.Capability)
		}
		return nil
	},
}

var embeddingAddCmd = &cobra.Command{
	Use:   "add",
	Short: "Add a new embedding provider",
	RunE: func(cmd *cobra.Command, args []string) error {
		if embFlagName == "" || embFlagURL == "" || embFlagModel == "" {
			return fmt.Errorf("--name, --url, and --model are required")
		}

		svc := services.GetGlobalPoolService()
		if !svc.IsInitialized() {
			return fmt.Errorf("pool service not initialized")
		}

		maxConc := embFlagMaxConcurrency
		if maxConc <= 0 {
			maxConc = 10
		}
		cap := embFlagCapability
		if cap <= 0 {
			cap = 3
		}

		p := &store.EmbeddingProvider{
			Name:           embFlagName,
			BaseURL:        embFlagURL,
			Key:            embFlagKey,
			ModelName:      embFlagModel,
			MaxConcurrency: maxConc,
			Capability:     cap,
			Enabled:        true,
		}

		if err := svc.SaveEmbeddingProvider(p); err != nil {
			return fmt.Errorf("failed to add provider: %w", err)
		}
		fmt.Printf("Embedding provider %q added.\n", embFlagName)
		return nil
	},
}

var embeddingUpdateCmd = &cobra.Command{
	Use:   "update --name <name> [flags]",
	Short: "Update an existing embedding provider",
	RunE: func(cmd *cobra.Command, args []string) error {
		if embFlagName == "" {
			return fmt.Errorf("--name is required")
		}

		svc := services.GetGlobalPoolService()
		if !svc.IsInitialized() {
			return fmt.Errorf("pool service not initialized")
		}

		existing, err := svc.GetEmbeddingProvider(embFlagName)
		if err != nil {
			return fmt.Errorf("provider %q not found: %w", embFlagName, err)
		}

		if cmd.Flags().Changed("url") {
			existing.BaseURL = embFlagURL
		}
		if cmd.Flags().Changed("key") {
			existing.Key = embFlagKey
		}
		if cmd.Flags().Changed("model") {
			existing.ModelName = embFlagModel
		}
		if cmd.Flags().Changed("capability") {
			existing.Capability = embFlagCapability
		}
		if cmd.Flags().Changed("max-concurrency") {
			existing.MaxConcurrency = embFlagMaxConcurrency
		}
		if cmd.Flags().Changed("enabled") {
			existing.Enabled = embFlagEnabled
		}

		if err := svc.SaveEmbeddingProvider(existing); err != nil {
			return fmt.Errorf("failed to update provider: %w", err)
		}
		fmt.Printf("Embedding provider %q updated.\n", embFlagName)
		return nil
	},
}

var embeddingDeleteCmd = &cobra.Command{
	Use:   "delete <name>",
	Short: "Delete an embedding provider",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		svc := services.GetGlobalPoolService()
		if !svc.IsInitialized() {
			return fmt.Errorf("pool service not initialized")
		}
		if err := svc.DeleteEmbeddingProvider(args[0]); err != nil {
			return fmt.Errorf("failed to delete provider: %w", err)
		}
		fmt.Printf("Embedding provider %q deleted.\n", args[0])
		return nil
	},
}

var embeddingConfigCmd = &cobra.Command{
	Use:   "config",
	Short: "Manage embedding pool configuration (strategy, enabled)",
}

var embeddingConfigShowCmd = &cobra.Command{
	Use:   "show",
	Short: "Show current embedding pool configuration",
	RunE: func(cmd *cobra.Command, args []string) error {
		svc := services.GetGlobalPoolService()
		if !svc.IsInitialized() {
			return fmt.Errorf("pool service not initialized")
		}
		cfg, err := svc.GetEmbeddingPoolConfig()
		if err != nil {
			return err
		}
		fmt.Printf("strategy: %s\n", cfg.Strategy)
		fmt.Printf("enabled:  %v\n", cfg.Enabled)
		return nil
	},
}

var embeddingConfigSetCmd = &cobra.Command{
	Use:   "set [--strategy <s>] [--enabled <bool>]",
	Short: "Update embedding pool configuration",
	Long: `Persist embedding pool-level settings to the database.
Changes take effect on the next restart.

Valid strategies: round_robin, random, least_load, capability, failover`,
	RunE: func(cmd *cobra.Command, args []string) error {
		svc := services.GetGlobalPoolService()
		if !svc.IsInitialized() {
			return fmt.Errorf("pool service not initialized")
		}

		current, err := svc.GetEmbeddingPoolConfig()
		if err != nil {
			return err
		}

		if cmd.Flags().Changed("strategy") {
			current.Strategy = pool.SelectionStrategy(embFlagStrategy)
		}
		if cmd.Flags().Changed("enabled") {
			current.Enabled = embFlagPoolEnabled
		}

		if err := svc.SaveEmbeddingPoolConfig(*current); err != nil {
			return fmt.Errorf("failed to save config: %w", err)
		}
		fmt.Printf("strategy: %s\n", current.Strategy)
		fmt.Printf("enabled:  %v\n", current.Enabled)
		fmt.Println("Saved. Restart to apply.")
		return nil
	},
}

func init() {
	embeddingCmd.AddCommand(embeddingListCmd)
	embeddingCmd.AddCommand(embeddingAddCmd)
	embeddingCmd.AddCommand(embeddingUpdateCmd)
	embeddingCmd.AddCommand(embeddingDeleteCmd)
	embeddingCmd.AddCommand(embeddingConfigCmd)
	embeddingConfigCmd.AddCommand(embeddingConfigShowCmd)
	embeddingConfigCmd.AddCommand(embeddingConfigSetCmd)

	// add flags
	embeddingAddCmd.Flags().StringVar(&embFlagName, "name", "", "Provider name (required)")
	embeddingAddCmd.Flags().StringVar(&embFlagURL, "url", "", "Base URL (required)")
	embeddingAddCmd.Flags().StringVar(&embFlagKey, "key", "", "API key")
	embeddingAddCmd.Flags().StringVar(&embFlagModel, "model", "", "Model name (required)")
	embeddingAddCmd.Flags().IntVar(&embFlagCapability, "capability", 3, "Capability level 1-5")
	embeddingAddCmd.Flags().IntVar(&embFlagMaxConcurrency, "max-concurrency", 10, "Max concurrent requests")

	// update flags
	embeddingUpdateCmd.Flags().StringVar(&embFlagName, "name", "", "Provider name to update (required)")
	embeddingUpdateCmd.Flags().StringVar(&embFlagURL, "url", "", "New base URL")
	embeddingUpdateCmd.Flags().StringVar(&embFlagKey, "key", "", "New API key")
	embeddingUpdateCmd.Flags().StringVar(&embFlagModel, "model", "", "New model name")
	embeddingUpdateCmd.Flags().IntVar(&embFlagCapability, "capability", 0, "New capability level")
	embeddingUpdateCmd.Flags().IntVar(&embFlagMaxConcurrency, "max-concurrency", 0, "New max concurrent requests")
	embeddingUpdateCmd.Flags().BoolVar(&embFlagEnabled, "enabled", true, "Enable or disable the provider")

	// config set flags
	embeddingConfigSetCmd.Flags().StringVar(&embFlagStrategy, "strategy", "", "Selection strategy (round_robin|random|least_load|capability|failover)")
	embeddingConfigSetCmd.Flags().BoolVar(&embFlagPoolEnabled, "enabled", true, "Enable or disable the embedding pool")
}
