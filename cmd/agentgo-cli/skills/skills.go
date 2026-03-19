package skills

import (
	"context"
	"fmt"
	"sort"
	"sync"

	"github.com/liliang-cn/agent-go/cmd/agentgo-cli/rag"
	"github.com/liliang-cn/agent-go/pkg/domain"
	"github.com/liliang-cn/agent-go/pkg/rag/chunker"
	"github.com/liliang-cn/agent-go/pkg/rag/processor"
	ragstore "github.com/liliang-cn/agent-go/pkg/rag/store"
	"github.com/liliang-cn/agent-go/pkg/services"
	"github.com/liliang-cn/agent-go/pkg/skills"
	"github.com/spf13/cobra"
)

var (
	skillsService *skills.Service
	ragProcessor  *processor.Service
	initOnce      sync.Once
	initErr       error
)

// initializeSkills initializes the skills service and RAG processor
func initializeSkills(cmd *cobra.Command) error {
	initOnce.Do(func() {
		ctx := context.Background()

		// Initialize skills config with multi-path support
		ragCfg := rag.GetConfig()

		var skillCfg *skills.Config
		if ragCfg != nil {
			// Use multi-path from global config
			skillCfg = &skills.Config{
				Enabled:                ragCfg.Skills.Enabled,
				Paths:                  ragCfg.SkillsPaths(),
				AutoLoad:               ragCfg.Skills.AutoLoad,
				CacheEnabled:           true,
				DBPath:                 ragCfg.AgentDBPath(),
				LogLevel:               "info",
				AllowCommandInjection:  ragCfg.Skills.AllowCommandInjection,
				RequireConfirmation:    ragCfg.Skills.RequireConfirmation,
				EnableRAGIntegration:   true,
				EnableMCPIntegration:   true,
				EnableAgentIntegration: true,
			}
		} else {
			// Fallback to default config
			skillCfg = skills.DefaultConfig()
		}

		// Create in-memory store for skills persistence
		skillStore := skills.NewMemoryStore()

		var err error
		skillsService, err = skills.NewService(skillCfg)
		if err != nil {
			initErr = fmt.Errorf("failed to create skills service: %w", err)
			return
		}
		skillsService.SetStore(skillStore)

		if ragCfg != nil {
			// Initialize vector store
			vectorStore, err := ragstore.NewSQLiteStore(ragCfg.CortexDBPath(), ragCfg.Internal.Storage.IndexType)
			if err != nil {
				initErr = fmt.Errorf("failed to create vector store: %w", err)
				return
			}
			docStore := ragstore.NewDocumentStore(vectorStore.GetCortexdbStore())

			// Get embedder service
			embedService, err := services.GetGlobalEmbeddingService(ctx)
			if err != nil {
				initErr = fmt.Errorf("failed to get embedder service: %w", err)
				return
			}

			// Get LLM service (optional, for full RAG)
			var generator domain.Generator = nil
			poolService := services.GetGlobalPoolService()
			if poolService != nil && poolService.IsInitialized() {
				llmService, err := poolService.GetLLMService()
				if err == nil {
					generator = llmService
				}
			}

			chunkerService := chunker.New()

			// Create processor
			ragProcessor = processor.New(
				embedService,
				generator,
				chunkerService,
				vectorStore,
				docStore,
				ragCfg,
				nil, // metadata extractor
				nil, // memory service
			)

			skillsService.SetRAGService(ragProcessor)
		}

		// Auto-load skills
		if err := skillsService.LoadAll(ctx); err != nil {
			initErr = fmt.Errorf("failed to load skills: %w", err)
			return
		}
	})
	return initErr
}

// Cmd represents the skills command
var Cmd = &cobra.Command{
	Use:   "skills",
	Short: "Skills management - load, list, and execute Claude Skills",
	Long: `Manage Claude Skills - folders of instructions, scripts, and resources
that agents can discover and use to do things more accurately and efficiently.

Skills follow the Claude Skills standard with SKILL.md files containing
YAML frontmatter for metadata.`,
}

func init() {
	Cmd.AddCommand(listCmd)
	Cmd.AddCommand(showCmd)
	Cmd.AddCommand(runCmd)
	Cmd.AddCommand(loadCmd)

	listCmd.Flags().Bool("flat", false, "Show a flat skill list without collection grouping")
	listCmd.Flags().Bool("grouped", false, "Show skills grouped by collection")
}

// listCmd lists available skills
var listCmd = &cobra.Command{
	Use:   "list",
	Short: "List all available skills",
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := initializeSkills(cmd); err != nil {
			return err
		}

		flat, _ := cmd.Flags().GetBool("flat")
		grouped, _ := cmd.Flags().GetBool("grouped")
		if flat && grouped {
			return fmt.Errorf("--flat and --grouped cannot be used together")
		}

		// Show search paths if requested
		showPaths, _ := cmd.Flags().GetBool("paths")
		if showPaths {
			ragCfg := rag.GetConfig()
			if ragCfg != nil {
				fmt.Println("Skills search paths:")
				for i, p := range ragCfg.SkillsPaths() {
					fmt.Printf("  %d. %s\n", i+1, p)
				}
				fmt.Println()
			}
		}

		allSkills, err := skillsService.ListSkills(cmd.Context(), skills.SkillFilter{})
		if err != nil {
			return fmt.Errorf("failed to list skills: %w", err)
		}

		if len(allSkills) == 0 {
			fmt.Println("No skills loaded. Use 'agentgo skills load' to load skills.")
			fmt.Println("\nSearch paths (configure in agentgo.toml or use --paths):")
			ragCfg := rag.GetConfig()
			if ragCfg != nil {
				for _, p := range ragCfg.SkillsPaths() {
					fmt.Printf("  - %s\n", p)
				}
			}
			return nil
		}

		fmt.Printf("Found %d skills:\n\n", len(allSkills))
		collections, err := skillsService.ListCollections(cmd.Context())
		if err != nil {
			return fmt.Errorf("failed to list collections: %w", err)
		}

		useGrouped := grouped || !flat
		if useGrouped && len(collections) > 0 {
			for _, collection := range collections {
				fmt.Printf("Collection: %s (%d)\n", collection.Name, len(collection.Skills))
				if collection.Path != "" {
					fmt.Printf("  Path: %s\n", collection.Path)
				}
				fmt.Println()
				for _, skill := range collection.Skills {
					printSkillSummary(skill, "  ")
				}
			}

			var standalone []*skills.Skill
			for _, skill := range allSkills {
				if skill.Collection == "" {
					standalone = append(standalone, skill)
				}
			}
			sort.Slice(standalone, func(i, j int) bool {
				return standalone[i].Name < standalone[j].Name
			})
			if len(standalone) > 0 {
				fmt.Printf("Standalone (%d)\n\n", len(standalone))
				for _, skill := range standalone {
					printSkillSummary(skill, "  ")
				}
			}
			return nil
		}

		for _, skill := range allSkills {
			printSkillSummary(skill, "  ")
		}

		return nil
	},
}

// showCmd shows details of a specific skill
var showCmd = &cobra.Command{
	Use:   "show [skill-id]",
	Short: "Show details of a specific skill",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := initializeSkills(cmd); err != nil {
			return err
		}

		skillID := args[0]
		skill, err := skillsService.GetSkill(cmd.Context(), skillID)
		if err != nil {
			return fmt.Errorf("skill not found: %s", skillID)
		}

		fmt.Printf("Skill: %s\n", skill.Name)
		fmt.Printf("ID: %s\n", skill.ID)
		if skill.Description != "" {
			fmt.Printf("Description: %s\n", skill.Description)
		}
		if skill.Version != "" {
			fmt.Printf("Version: %s\n", skill.Version)
		}
		if skill.Author != "" {
			fmt.Printf("Author: %s\n", skill.Author)
		}
		if skill.Category != "" {
			fmt.Printf("Category: %s\n", skill.Category)
		}
		if skill.Collection != "" {
			fmt.Printf("Collection: %s\n", skill.Collection)
		}
		if skill.CollectionPath != "" {
			fmt.Printf("Collection Path: %s\n", skill.CollectionPath)
		}
		if len(skill.Tags) > 0 {
			fmt.Printf("Tags: %v\n", skill.Tags)
		}
		if skill.Command != "" {
			fmt.Printf("Command: %s\n", skill.Command)
		}
		if skill.Path != "" {
			fmt.Printf("Path: %s\n", skill.Path)
		}

		if len(skill.Variables) > 0 {
			fmt.Printf("\nVariables:\n")
			for _, v := range skill.Variables {
				required := ""
				if v.Required {
					required = " (required)"
				}
				fmt.Printf("  %s%s: %s\n", v.Name, required, v.Description)
			}
		}

		if len(skill.Steps) > 0 {
			fmt.Printf("\nSteps:\n")
			for i, step := range skill.Steps {
				fmt.Printf("  %d. %s\n", i+1, step.Title)
			}
		}

		return nil
	},
}

func printSkillSummary(skill *skills.Skill, indent string) {
	status := "enabled"
	if !skill.Enabled {
		status = "disabled"
	}
	fmt.Printf("%s/%s [%s]\n", indent, skill.Name, status)
	if skill.Description != "" {
		fmt.Printf("%s  %s\n", indent, skill.Description)
	}
	if skill.Category != "" {
		fmt.Printf("%s  Category: %s\n", indent, skill.Category)
	}
	fmt.Println()
}

// runCmd executes a skill
var runCmd = &cobra.Command{
	Use:   "run [skill-id] [--var key=value...]",
	Short: "Execute a skill",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := initializeSkills(cmd); err != nil {
			return err
		}

		skillID := args[0]

		// Parse variables
		variables, _ := cmd.Flags().GetStringArray("var")
		varMap := make(map[string]interface{})
		for _, v := range variables {
			// Simple key=value parsing
			parts := splitAt(v, '=', 2)
			if len(parts) == 2 {
				varMap[parts[0]] = parts[1]
			}
		}

		req := &skills.ExecutionRequest{
			SkillID:     skillID,
			Variables:   varMap,
			Interactive: true,
		}

		result, err := skillsService.Execute(cmd.Context(), req)
		if err != nil {
			return fmt.Errorf("execution failed: %w", err)
		}

		fmt.Printf("Skill: %s\n", result.SkillID)
		if result.Output != "" {
			fmt.Printf("Output:\n%s\n", result.Output)
		}
		fmt.Printf("Duration: %v\n", result.Duration)

		return nil
	},
}

func init() {
	runCmd.Flags().StringArray("var", []string{}, "Variables in format key=value")
	listCmd.Flags().Bool("paths", false, "Show skills search paths")
}

// loadCmd loads skills from configured paths
var loadCmd = &cobra.Command{
	Use:   "load",
	Short: "Load skills from configured paths",
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := initializeSkills(cmd); err != nil {
			return err
		}

		err := skillsService.LoadAll(cmd.Context())
		if err != nil {
			return fmt.Errorf("failed to load skills: %w", err)
		}

		allSkills, _ := skillsService.ListSkills(cmd.Context(), skills.SkillFilter{})
		fmt.Printf("Loaded %d skills\n", len(allSkills))

		return nil
	},
}

func splitAt(s string, sep rune, limit int) []string {
	var result []string
	current := ""
	count := 0

	for _, c := range s {
		if c == sep && count < limit-1 {
			result = append(result, current)
			current = ""
			count++
		} else {
			current += string(c)
		}
	}
	result = append(result, current)

	return result
}
