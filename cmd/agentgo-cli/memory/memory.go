package memory

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/liliang-cn/agent-go/v2/pkg/config"
	"github.com/liliang-cn/agent-go/v2/pkg/domain"
	"github.com/liliang-cn/agent-go/v2/pkg/memory"
	"github.com/liliang-cn/agent-go/v2/pkg/providers"
	"github.com/liliang-cn/agent-go/v2/pkg/store"
	"github.com/spf13/cobra"
)

// CommandOptions holds the options for memory commands
type CommandOptions struct {
	DBPath string
}

// NewCommand creates the memory command
func NewCommand(opts *CommandOptions) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "memory",
		Short: "Manage long-term agent memories",
		Long: `Manage long-term agent memories for context-aware interactions.

Memory types:
  - fact: Factual information
  - skill: Skills and procedures
  - pattern: Patterns and trends
  - context: Contextual information
  - preference: User preferences`,
	}

	// Add subcommands
	cmd.AddCommand(newSearchCommand(opts))
	cmd.AddCommand(newGetCommand(opts))
	cmd.AddCommand(newAddCommand(opts))
	cmd.AddCommand(newUpdateCommand(opts))
	cmd.AddCommand(newListCommand(opts))
	cmd.AddCommand(newDeleteCommand(opts))
	cmd.AddCommand(newRebuildCommand(opts))

	return cmd
}

// newSearchCommand creates the search subcommand
func newSearchCommand(opts *CommandOptions) *cobra.Command {
	var limit int

	cmd := &cobra.Command{
		Use:   "search <query>",
		Short: "Search memories by query",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			svc, err := createMemoryService(opts)
			if err != nil {
				return err
			}

			memories, err := svc.Search(cmd.Context(), args[0], limit)
			if err != nil {
				return fmt.Errorf("search failed: %w", err)
			}

			if len(memories) == 0 {
				fmt.Println("No memories found.")
				return nil
			}

			fmt.Printf("Found %d memories:\n\n", len(memories))
			for i, mem := range memories {
				fmt.Printf("[%d] %s (score: %.2f)\n", i+1, mem.Type, mem.Score)
				fmt.Printf("    ID: %s\n", mem.ID)
				fmt.Printf("    Content: %s\n", mem.Content)
				fmt.Printf("    Importance: %.2f | Access Count: %d\n", mem.Importance, mem.AccessCount)
				fmt.Println()
			}

			return nil
		},
	}

	cmd.Flags().IntVarP(&limit, "limit", "l", 5, "Maximum number of results")

	return cmd
}

// newGetCommand creates the get subcommand
func newGetCommand(opts *CommandOptions) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "get <memory-id>",
		Short: "Get a memory by ID",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			svc, err := createMemoryService(opts)
			if err != nil {
				return err
			}

			mem, err := svc.Get(cmd.Context(), args[0])
			if err != nil {
				return fmt.Errorf("get failed: %w", err)
			}

			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			return enc.Encode(mem)
		},
	}

	return cmd
}

// newAddCommand creates the add subcommand
func newAddCommand(opts *CommandOptions) *cobra.Command {
	var (
		memType    string
		importance float64
		sessionID  string
	)

	cmd := &cobra.Command{
		Use:   "add <content>",
		Short: "Add a new memory",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			svc, err := createMemoryService(opts)
			if err != nil {
				return err
			}

			if memType == "" {
				return fmt.Errorf("--type is required")
			}

			if !isValidMemoryType(memType) {
				return fmt.Errorf("invalid memory type: %s (must be one of: fact, skill, pattern, context, preference)", memType)
			}

			mem := &domain.Memory{
				ID:         uuid.New().String(),
				Type:       domain.MemoryType(memType),
				Content:    args[0],
				Importance: importance,
				SessionID:  sessionID,
				CreatedAt:  time.Now(),
				UpdatedAt:  time.Now(),
			}

			if err := svc.Add(cmd.Context(), mem); err != nil {
				return fmt.Errorf("add failed: %w", err)
			}

			fmt.Printf("Memory added successfully:\nID: %s\nType: %s\n", mem.ID, mem.Type)

			return nil
		},
	}

	cmd.Flags().StringVarP(&memType, "type", "t", "", "Memory type (fact, skill, pattern, context, preference)")
	cmd.Flags().Float64VarP(&importance, "importance", "i", 0.5, "Importance score (0-1)")
	cmd.Flags().StringVarP(&sessionID, "session", "s", "", "Session ID to associate with")

	_ = cmd.MarkFlagRequired("type")

	return cmd
}

// newUpdateCommand creates the update subcommand
func newUpdateCommand(opts *CommandOptions) *cobra.Command {
	var (
		newType    string
		importance float64
	)

	cmd := &cobra.Command{
		Use:   "update <memory-id> [instruction]",
		Short: "Update a memory's content (via LLM), type, or importance",
		Long: `Update a memory. Provide an instruction to rewrite content via LLM.
Use --type to change the memory type directly (no LLM needed).
Use --importance to adjust the importance score directly.`,
		Args: cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			id := args[0]
			instruction := ""
			if len(args) == 2 {
				instruction = args[1]
			}

			svc, err := createMemoryService(opts)
			if err != nil {
				return err
			}

			// Direct patch: type or importance changed without LLM
			if newType != "" || cmd.Flags().Changed("importance") {
				mem, err := svc.Get(cmd.Context(), id)
				if err != nil {
					return fmt.Errorf("memory not found: %w", err)
				}
				if newType != "" {
					if !isValidMemoryType(newType) {
						return fmt.Errorf("invalid type %q: must be one of fact, skill, pattern, context, preference", newType)
					}
					mem.Type = domain.MemoryType(newType)
				}
				if cmd.Flags().Changed("importance") {
					mem.Importance = importance
				}
				if err := svc.Patch(cmd.Context(), mem); err != nil {
					return fmt.Errorf("patch failed: %w", err)
				}
				fmt.Println("Memory updated successfully.")
				return nil
			}

			// LLM-based content update
			if instruction == "" {
				return fmt.Errorf("provide an instruction or use --type / --importance to update directly")
			}
			if err := svc.Update(cmd.Context(), id, instruction); err != nil {
				return fmt.Errorf("update failed: %w", err)
			}

			fmt.Println("Memory updated successfully.")
			return nil
		},
	}

	cmd.Flags().StringVarP(&newType, "type", "t", "", "New memory type (fact, skill, pattern, context, preference)")
	cmd.Flags().Float64VarP(&importance, "importance", "i", 0, "New importance score (0-1)")

	return cmd
}

// newListCommand creates the list subcommand
func newListCommand(opts *CommandOptions) *cobra.Command {
	var (
		limit  int
		offset int
	)

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List all memories",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			svc, err := createMemoryService(opts)
			if err != nil {
				return err
			}

			memories, total, err := svc.List(cmd.Context(), limit, offset)
			if err != nil {
				return fmt.Errorf("list failed: %w", err)
			}

			fmt.Printf("Memories (%d total, showing %d):\n\n", total, len(memories))

			for i, mem := range memories {
				fmt.Printf("[%d] %s\n", offset+i+1, mem.Type)
				fmt.Printf("    ID: %s\n", mem.ID)
				fmt.Printf("    Content: %s\n", truncateString(mem.Content, 100))
				fmt.Printf("    Importance: %.2f | Created: %s\n", mem.Importance, mem.CreatedAt.Format("2006-01-02"))
				fmt.Println()
			}

			return nil
		},
	}

	cmd.Flags().IntVarP(&limit, "limit", "l", 10, "Maximum number of memories to show")
	cmd.Flags().IntVarP(&offset, "offset", "o", 0, "Number of memories to skip")

	return cmd
}

// newDeleteCommand creates the delete subcommand
func newDeleteCommand(opts *CommandOptions) *cobra.Command {
	var confirm bool

	cmd := &cobra.Command{
		Use:   "delete <memory-id>",
		Short: "Delete a memory",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			svc, err := createMemoryService(opts)
			if err != nil {
				return err
			}

			if !confirm {
				fmt.Printf("Are you sure you want to delete memory %s? (y/N): ", args[0])
				var response string
				fmt.Scanln(&response)
				if response != "y" && response != "Y" {
					fmt.Println("Cancelled.")
					return nil
				}
			}

			if err := svc.Delete(cmd.Context(), args[0]); err != nil {
				return fmt.Errorf("delete failed: %w", err)
			}

			fmt.Println("Memory deleted successfully.")

			return nil
		},
	}

	cmd.Flags().BoolVarP(&confirm, "yes", "y", false, "Skip confirmation")

	return cmd
}

// createMemoryService creates a memory service with default settings
func createMemoryService(opts *CommandOptions) (*memory.Service, error) {
	var memStore domain.MemoryStore
	var memSvc *memory.Service
	var err error

	path := opts.DBPath
	storeType := config.MemoryStoreTypeFile

	if path != "" && (strings.HasSuffix(path, ".db") || strings.HasSuffix(path, ".sqlite")) {
		storeType = config.MemoryStoreTypeVector
	} else if Cfg != nil {
		if path == "" {
			path = Cfg.MemoryPrimaryPath()
		}
		storeType = Cfg.GetMemoryStoreType()
	}

	if path == "" {
		path = "./.agentgo/data/memories"
	}

	// Try to get embedder and LLM from config before creating stores
	var embedder domain.Embedder
	var llm domain.Generator
	if Cfg != nil {
		factory := providers.NewFactory()

		if Cfg.RAG.Enabled {
			switch {
			case len(Cfg.RAG.Embedding.Providers) > 0:
				prov := Cfg.RAG.Embedding.Providers[0]
				provConfig := &domain.OpenAIProviderConfig{
					BaseProviderConfig: domain.BaseProviderConfig{Timeout: 30},
					BaseURL:            prov.BaseURL,
					APIKey:             prov.Key,
					EmbeddingModel:     prov.ModelName,
				}
				embedder, _ = factory.CreateEmbedderProvider(context.Background(), provConfig)
			case len(Cfg.LLM.Providers) > 0:
				prov := Cfg.LLM.Providers[0]
				embeddingModel := Cfg.RAG.EmbeddingModel
				if embeddingModel == "" {
					embeddingModel = prov.ModelName
				}
				provConfig := &domain.OpenAIProviderConfig{
					BaseProviderConfig: domain.BaseProviderConfig{Timeout: 30},
					BaseURL:            prov.BaseURL,
					APIKey:             prov.Key,
					EmbeddingModel:     embeddingModel,
				}
				embedder, _ = factory.CreateEmbedderProvider(context.Background(), provConfig)
			}
		}

		if Cfg.LLM.Enabled && len(Cfg.LLM.Providers) > 0 {
			llmProv := Cfg.LLM.Providers[0]
			llmConfig := &domain.OpenAIProviderConfig{
				BaseProviderConfig: domain.BaseProviderConfig{Timeout: 60},
				BaseURL:            llmProv.BaseURL,
				APIKey:             llmProv.Key,
				LLMModel:           llmProv.ModelName,
			}
			llm, _ = factory.CreateLLMProvider(context.Background(), llmConfig)
		}
	}

	var shadowStore domain.MemoryStore
	switch storeType {
	case config.MemoryStoreTypeVector:
		vectorPath := path
		if Cfg != nil && opts.DBPath == "" {
			vectorPath = Cfg.MemoryVectorDBPath()
		}
		memStore, err = store.NewMemoryStore(vectorPath)
		if err == nil {
			_ = memStore.InitSchema(context.Background())
		}
	case config.MemoryStoreTypeHybrid:
		fileStore, ferr := store.NewFileMemoryStore(path)
		if ferr != nil {
			return nil, fmt.Errorf("failed to create file memory store: %w", ferr)
		}
		if llm != nil {
			fileStore.WithLLM(llm)
		}
		memStore = fileStore
		if Cfg != nil {
			if sqliteStore, serr := store.NewMemoryStore(Cfg.MemoryVectorDBPath()); serr == nil {
				_ = sqliteStore.InitSchema(context.Background())
				shadowStore = sqliteStore
			}
		}
	default:
		memStore, err = store.NewFileMemoryStore(path)
	}

	if err != nil {
		return nil, fmt.Errorf("failed to create memory store: %w", err)
	}

	memCfg := memory.DefaultConfig()
	memSvc = memory.NewService(memStore, llm, embedder, memCfg)

	if shadowStore != nil {
		memSvc.SetShadowIndex(shadowStore)
	}

	return memSvc, nil
}

// isValidMemoryType checks if the memory type is valid
func isValidMemoryType(t string) bool {
	switch t {
	case "fact", "skill", "pattern", "context", "preference":
		return true
	default:
		return false
	}
}

// truncateString truncates a string to a maximum length
func truncateString(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

// newRebuildCommand creates the "memory rebuild" subcommand.
// It rebuilds the _index/ hierarchy from all existing .md files, enabling
// migration of old memory folders to the cognitive layer format.
func newRebuildCommand(opts *CommandOptions) *cobra.Command {
	return &cobra.Command{
		Use:   "rebuild",
		Short: "Rebuild the _index/ hierarchy from all existing memory files",
		Long: `Scans all memory .md files and regenerates the _index/ directory.

Use this command to:
  - Migrate old memory folders to the cognitive layer (adds facts.md, observations.md, etc.)
  - Repair a corrupted or missing _index/ directory
  - Sync the index after manually editing memory files

Example:
  agentgo memory rebuild
  agentgo memory rebuild --db-path ~/.agentgo/data/memories`,
		RunE: func(cmd *cobra.Command, args []string) error {
			path := opts.DBPath
			if path == "" && Cfg != nil {
				path = Cfg.Memory.MemoryPath
			}
			if path == "" {
				path = "./.agentgo/data/memories"
			}

			fileStore, err := store.NewFileMemoryStore(path)
			if err != nil {
				return fmt.Errorf("failed to open memory store at %q: %w", path, err)
			}

			fmt.Printf("Rebuilding _index/ for memory store at: %s\n", path)
			if err := fileStore.RebuildIndex(cmd.Context()); err != nil {
				return fmt.Errorf("rebuild failed: %w", err)
			}

			// Report what was generated
			idx, err := fileStore.ReadIndex(cmd.Context())
			if err != nil {
				fmt.Println("✅ Rebuild complete (could not read final index for summary)")
				return nil
			}

			typeCounts := make(map[string]int)
			for _, e := range idx.Entries {
				typeCounts[string(e.Type)]++
			}

			fmt.Printf("✅ Rebuild complete. Index summary:\n")
			for t, n := range typeCounts {
				fmt.Printf("   %-14s %d entries\n", t+":", n)
			}
			fmt.Printf("   %-14s %d total\n", "total:", len(idx.Entries))
			fmt.Printf("\nIndex files written to: %s/_index/\n", path)
			return nil
		},
	}
}
