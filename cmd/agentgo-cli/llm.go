package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/liliang-cn/agent-go/v2/pkg/pool"
	"github.com/liliang-cn/agent-go/v2/pkg/services"
	"github.com/liliang-cn/agent-go/v2/pkg/store"
	"github.com/spf13/cobra"
)

var (
	llmProvider string
	llmStream   bool

	// flags for add/update
	llmFlagName           string
	llmFlagURL            string
	llmFlagKey            string
	llmFlagModel          string
	llmFlagCapability     int
	llmFlagMaxConcurrency int
	llmFlagEnabled        bool
	llmFlagEmbeddingModel string

	// flags for config set
	llmFlagStrategy    string
	llmFlagPoolEnabled bool
)

// llmCmd represents the LLM command group
var llmCmd = &cobra.Command{
	Use:   "llm",
	Short: "LLM provider and model management",
	Long:  `Manage LLM providers (stored in agentgo.db) and interact with language models.`,
}

// llmChatCmd handles chat interactions
var llmChatCmd = &cobra.Command{
	Use:   "chat [message]",
	Short: "Chat with an LLM",
	Args:  cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		message := strings.Join(args, " ")
		ctx := context.Background()

		svc := services.GetGlobalPoolService()
		if !svc.IsInitialized() {
			return fmt.Errorf("pool service not initialized")
		}

		chatOpts := services.ChatOptions{
			Provider:  strings.TrimSpace(llmProvider),
			MaxTokens: 30000,
		}

		if llmStream {
			err := svc.StreamChat(ctx, message, chatOpts, func(chunk string) {
				fmt.Print(chunk)
			})
			if err != nil {
				return fmt.Errorf("streaming chat failed: %w", err)
			}
			fmt.Println()
		} else {
			resp, err := svc.Chat(ctx, message, chatOpts)
			if err != nil {
				return fmt.Errorf("chat failed: %w", err)
			}
			fmt.Println(resp)
		}
		return nil
	},
}

// llmListCmd lists providers from the database
var llmListCmd = &cobra.Command{
	Use:   "list",
	Short: "List all LLM providers",
	RunE: func(cmd *cobra.Command, args []string) error {
		svc := services.GetGlobalPoolService()
		if !svc.IsInitialized() {
			return fmt.Errorf("pool service not initialized")
		}

		providers, err := svc.ListProviders()
		if err != nil {
			return fmt.Errorf("failed to list providers: %w", err)
		}

		cfg, err := svc.GetLLMPoolConfig()
		if err != nil {
			return fmt.Errorf("failed to load llm config: %w", err)
		}

		embCfg, err := svc.GetEmbeddingPoolConfig()
		if err != nil {
			return fmt.Errorf("failed to load embedding config: %w", err)
		}

		embProviders, err := svc.ListEmbeddingProviders()
		if err != nil {
			return fmt.Errorf("failed to list embedding providers: %w", err)
		}

		if len(providers) == 0 {
			fmt.Println("No providers configured. Use 'llm add' to add one.")
		} else {
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
		}

		fmt.Printf("\nLLM Strategy: %s\n", cfg.Strategy)
		fmt.Printf("Embedding Pool: enabled=%v strategy=%s providers=%d\n", embCfg.Enabled, embCfg.Strategy, len(embProviders))
		if len(embProviders) > 0 {
			fmt.Println("\nEmbedding Providers:")
			fmt.Printf("%-20s %-10s %-40s %-35s %-12s %-5s\n",
				"NAME", "ENABLED", "URL", "MODEL", "CONCURRENCY", "CAP")
			fmt.Println(strings.Repeat("-", 130))
			for _, p := range embProviders {
				enabled := "yes"
				if !p.Enabled {
					enabled = "no"
				}
				fmt.Printf("%-20s %-10s %-40s %-35s %-12d %-5d\n",
					p.Name, enabled, p.BaseURL, p.ModelName, p.MaxConcurrency, p.Capability)
			}
		}
		return nil
	},
}

// llmAddCmd adds a new provider
var llmAddCmd = &cobra.Command{
	Use:   "add",
	Short: "Add a new LLM provider",
	RunE: func(cmd *cobra.Command, args []string) error {
		if llmFlagName == "" || llmFlagURL == "" || llmFlagModel == "" {
			return fmt.Errorf("--name, --url, and --model are required")
		}

		svc := services.GetGlobalPoolService()
		if !svc.IsInitialized() {
			return fmt.Errorf("pool service not initialized")
		}

		maxConc := llmFlagMaxConcurrency
		if maxConc <= 0 {
			maxConc = 5
		}
		cap := llmFlagCapability
		if cap <= 0 {
			cap = 3
		}

		p := &store.LLMProvider{
			Name:           llmFlagName,
			BaseURL:        llmFlagURL,
			Key:            llmFlagKey,
			ModelName:      llmFlagModel,
			MaxConcurrency: maxConc,
			Capability:     cap,
			Enabled:        true,
		}

		if err := svc.SaveProvider(p); err != nil {
			return fmt.Errorf("failed to add provider: %w", err)
		}
		fmt.Printf("Provider %q added.\n", llmFlagName)
		return nil
	},
}

// llmUpdateCmd updates an existing provider
var llmUpdateCmd = &cobra.Command{
	Use:   "update --name <name> [flags]",
	Short: "Update an existing LLM provider",
	RunE: func(cmd *cobra.Command, args []string) error {
		if llmFlagName == "" {
			return fmt.Errorf("--name is required")
		}

		svc := services.GetGlobalPoolService()
		if !svc.IsInitialized() {
			return fmt.Errorf("pool service not initialized")
		}

		existing, err := svc.GetProvider(llmFlagName)
		if err != nil {
			return fmt.Errorf("provider %q not found: %w", llmFlagName, err)
		}

		// Patch only flags that were explicitly set
		if cmd.Flags().Changed("url") {
			existing.BaseURL = llmFlagURL
		}
		if cmd.Flags().Changed("key") {
			existing.Key = llmFlagKey
		}
		if cmd.Flags().Changed("model") {
			existing.ModelName = llmFlagModel
		}
		if cmd.Flags().Changed("capability") {
			existing.Capability = llmFlagCapability
		}
		if cmd.Flags().Changed("max-concurrency") {
			existing.MaxConcurrency = llmFlagMaxConcurrency
		}
		if cmd.Flags().Changed("enabled") {
			existing.Enabled = llmFlagEnabled
		}

		if err := svc.SaveProvider(existing); err != nil {
			return fmt.Errorf("failed to update provider: %w", err)
		}
		fmt.Printf("Provider %q updated.\n", llmFlagName)
		return nil
	},
}

// llmDeleteCmd removes a provider
var llmDeleteCmd = &cobra.Command{
	Use:   "delete <name>",
	Short: "Delete an LLM provider",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		name := args[0]

		svc := services.GetGlobalPoolService()
		if !svc.IsInitialized() {
			return fmt.Errorf("pool service not initialized")
		}

		if err := svc.DeleteProvider(name); err != nil {
			return fmt.Errorf("failed to delete provider: %w", err)
		}
		fmt.Printf("Provider %q deleted.\n", name)
		return nil
	},
}

// llmConfigCmd groups pool-level config commands.
var llmConfigCmd = &cobra.Command{
	Use:   "config",
	Short: "Manage LLM pool configuration (strategy, enabled)",
}

var llmConfigShowCmd = &cobra.Command{
	Use:   "show",
	Short: "Show current LLM pool configuration",
	RunE: func(cmd *cobra.Command, args []string) error {
		svc := services.GetGlobalPoolService()
		if !svc.IsInitialized() {
			return fmt.Errorf("pool service not initialized")
		}
		cfg, err := svc.GetLLMPoolConfig()
		if err != nil {
			return err
		}
		embeddingModel := cfg.EmbeddingModel
		if strings.TrimSpace(embeddingModel) == "" {
			embeddingModel = "-"
		}
		fmt.Printf("strategy:        %s\n", cfg.Strategy)
		fmt.Printf("enabled:         %v\n", cfg.Enabled)
		fmt.Printf("embedding_model: %s\n", embeddingModel)
		return nil
	},
}

var llmConfigSetCmd = &cobra.Command{
	Use:   "set [--strategy <s>] [--enabled <bool>] [--embedding-model <model>]",
	Short: "Update LLM pool configuration",
	Long: `Persist pool-level settings to the database.
Changes take effect on the next restart.

Valid strategies: round_robin, random, least_load, capability, failover

embedding-model is used as the fallback embedding model when no dedicated
embedding providers are configured.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		svc := services.GetGlobalPoolService()
		if !svc.IsInitialized() {
			return fmt.Errorf("pool service not initialized")
		}

		current, err := svc.GetLLMPoolConfig()
		if err != nil {
			return err
		}

		if cmd.Flags().Changed("strategy") {
			current.Strategy = pool.SelectionStrategy(llmFlagStrategy)
		}
		if cmd.Flags().Changed("enabled") {
			current.Enabled = llmFlagPoolEnabled
		}
		if cmd.Flags().Changed("embedding-model") {
			current.EmbeddingModel = llmFlagEmbeddingModel
		}

		if err := svc.SaveLLMPoolConfig(*current); err != nil {
			return fmt.Errorf("failed to save config: %w", err)
		}
		embeddingModel := current.EmbeddingModel
		if strings.TrimSpace(embeddingModel) == "" {
			embeddingModel = "-"
		}
		fmt.Printf("strategy:        %s\n", current.Strategy)
		fmt.Printf("enabled:         %v\n", current.Enabled)
		fmt.Printf("embedding_model: %s\n", embeddingModel)
		fmt.Println("Saved. Restart to apply.")
		return nil
	},
}

// llmRankCmd runs 5 capability tests and prints (optionally saves) the CAP score.
var llmRankCmd = &cobra.Command{
	Use:   "rank [provider-name]",
	Short: "Rank an LLM provider by running 5 capability tests",
	Long: `Runs 6 progressive tests against a provider to compute its capability score (CAP 1-6):
  1. basic_echo         - strict single-word instruction following
  2. json_output        - exact JSON output compliance
  3. math_reasoning     - arithmetic reasoning (17 × 23 = 391)
  4. tool_calling       - function/tool call support
  5. system_instruction - system-prompt adherence + JSON extraction
  6. ptc_roleplay       - PTC: generate <code>callTool(...)</code> JS with explicit return

Use --save to persist the computed CAP to the database.`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx := context.Background()
		save, _ := cmd.Flags().GetBool("save")

		svc := services.GetGlobalPoolService()
		if !svc.IsInitialized() {
			return fmt.Errorf("pool service not initialized")
		}

		// Collect provider names to rank
		var targets []string
		if len(args) == 1 {
			targets = []string{args[0]}
		} else {
			// No arg — rank all known providers
			providers, err := svc.ListProviders()
			if err != nil {
				return fmt.Errorf("failed to list providers: %w", err)
			}
			for _, p := range providers {
				if p.Enabled {
					targets = append(targets, p.Name)
				}
			}
			if len(targets) == 0 {
				return fmt.Errorf("no enabled providers found")
			}
		}

		for i, providerName := range targets {
			if i > 0 {
				fmt.Println()
			}
			if err := runRank(ctx, svc, providerName, save); err != nil {
				fmt.Fprintf(cmd.ErrOrStderr(), "rank %q: %v\n", providerName, err)
			}
		}
		return nil
	},
}

func runRank(ctx context.Context, svc *services.GlobalPoolService, providerName string, save bool) error {
	fmt.Printf("=== %s ===\n", providerName)

	result, err := svc.RankProvider(ctx, providerName)
	if err != nil {
		return fmt.Errorf("rank failed: %w", err)
	}

	total := len(result.Tests)
	fmt.Printf("%-25s %-8s %-12s %s\n", "TEST", "RESULT", "LATENCY", "DETAILS")
	fmt.Println(strings.Repeat("-", 90))
	for _, t := range result.Tests {
		status := "PASS"
		if !t.Passed {
			status = "FAIL"
		}
		fmt.Printf("%-25s %-8s %-12s %s\n",
			t.Name, status,
			fmt.Sprintf("%dms", t.Latency.Milliseconds()),
			t.Details,
		)
		if debug && (t.Prompt != "" || t.RawResponse != "") {
			fmt.Println()
			if t.Prompt != "" {
				fmt.Println("  >> PROMPT:")
				for _, line := range strings.Split(t.Prompt, "\n") {
					fmt.Printf("     %s\n", line)
				}
			}
			if t.RawResponse != "" {
				fmt.Println("  << RESPONSE:")
				for _, line := range strings.Split(t.RawResponse, "\n") {
					fmt.Printf("     %s\n", line)
				}
			}
			fmt.Println()
		}
	}
	fmt.Printf("\nScore: %d/%d   CAP: %d\n", result.Score, total, result.CAP)

	if save {
		existing, err := svc.GetProvider(providerName)
		if err != nil {
			return fmt.Errorf("cannot save: %w", err)
		}
		existing.Capability = result.CAP
		if err := svc.SaveProvider(existing); err != nil {
			return fmt.Errorf("failed to save CAP: %w", err)
		}
		fmt.Printf("Saved CAP=%d for %q.\n", result.CAP, providerName)
	}
	return nil
}

// llmTestCmd tests connectivity to a provider
var llmTestCmd = &cobra.Command{
	Use:   "test [provider-name]",
	Short: "Test connectivity to an LLM provider",
	Args:  cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx := context.Background()

		svc := services.GetGlobalPoolService()
		if !svc.IsInitialized() {
			return fmt.Errorf("pool service not initialized")
		}

		testMsg := "Reply with only the word 'ok'."
		opts := services.ChatOptions{MaxTokens: 10}

		if len(args) == 1 {
			opts.Provider = args[0]
			fmt.Printf("Testing provider %q...\n", args[0])
		} else {
			fmt.Println("Testing default provider...")
		}

		resp, err := svc.Chat(ctx, testMsg, opts)
		if err != nil {
			return fmt.Errorf("test failed: %w", err)
		}
		fmt.Printf("Response: %s\n", strings.TrimSpace(resp))
		return nil
	},
}

func init() {
	llmCmd.AddCommand(llmChatCmd)
	llmCmd.AddCommand(llmListCmd)
	llmCmd.AddCommand(llmAddCmd)
	llmCmd.AddCommand(llmUpdateCmd)
	llmCmd.AddCommand(llmDeleteCmd)
	llmCmd.AddCommand(llmTestCmd)
	llmCmd.AddCommand(llmRankCmd)
	llmCmd.AddCommand(llmConfigCmd)
	llmConfigCmd.AddCommand(llmConfigShowCmd)
	llmConfigCmd.AddCommand(llmConfigSetCmd)

	// rank flags
	llmRankCmd.Flags().Bool("save", false, "Save computed CAP to database after ranking")

	// config set flags
	llmConfigSetCmd.Flags().StringVar(&llmFlagStrategy, "strategy", "", "Selection strategy (round_robin|random|least_load|capability|failover)")
	llmConfigSetCmd.Flags().BoolVar(&llmFlagPoolEnabled, "enabled", true, "Enable or disable the LLM pool")
	llmConfigSetCmd.Flags().StringVar(&llmFlagEmbeddingModel, "embedding-model", "", "Fallback embedding model used when no dedicated embedding providers exist")

	// chat flags
	llmChatCmd.Flags().StringVarP(&llmProvider, "provider", "p", "", "LLM provider to use")
	llmChatCmd.Flags().BoolVarP(&llmStream, "stream", "s", false, "Stream the response")

	// add flags
	llmAddCmd.Flags().StringVar(&llmFlagName, "name", "", "Provider name (required)")
	llmAddCmd.Flags().StringVar(&llmFlagURL, "url", "", "Base URL (required)")
	llmAddCmd.Flags().StringVar(&llmFlagKey, "key", "", "API key")
	llmAddCmd.Flags().StringVar(&llmFlagModel, "model", "", "Model name (required)")
	llmAddCmd.Flags().IntVar(&llmFlagCapability, "capability", 3, "Capability level 1-5")
	llmAddCmd.Flags().IntVar(&llmFlagMaxConcurrency, "max-concurrency", 5, "Max concurrent requests")

	// update flags (same set, all optional)
	llmUpdateCmd.Flags().StringVar(&llmFlagName, "name", "", "Provider name to update (required)")
	llmUpdateCmd.Flags().StringVar(&llmFlagURL, "url", "", "New base URL")
	llmUpdateCmd.Flags().StringVar(&llmFlagKey, "key", "", "New API key")
	llmUpdateCmd.Flags().StringVar(&llmFlagModel, "model", "", "New model name")
	llmUpdateCmd.Flags().IntVar(&llmFlagCapability, "capability", 0, "New capability level 1-5")
	llmUpdateCmd.Flags().IntVar(&llmFlagMaxConcurrency, "max-concurrency", 0, "New max concurrent requests")
	llmUpdateCmd.Flags().BoolVar(&llmFlagEnabled, "enabled", true, "Enable or disable the provider")
}
