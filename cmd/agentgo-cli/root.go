package main

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/liliang-cn/agent-go/cmd/agentgo-cli/acp"
	"github.com/liliang-cn/agent-go/cmd/agentgo-cli/agent"
	cachecmd "github.com/liliang-cn/agent-go/cmd/agentgo-cli/cache"
	"github.com/liliang-cn/agent-go/cmd/agentgo-cli/mcp"
	"github.com/liliang-cn/agent-go/cmd/agentgo-cli/memory"
	"github.com/liliang-cn/agent-go/cmd/agentgo-cli/ptc"
	"github.com/liliang-cn/agent-go/cmd/agentgo-cli/rag"
	"github.com/liliang-cn/agent-go/cmd/agentgo-cli/skills"
	"github.com/liliang-cn/agent-go/cmd/agentgo-cli/squad"
	"github.com/liliang-cn/agent-go/pkg/config"
	agentgolog "github.com/liliang-cn/agent-go/pkg/log"
	"github.com/liliang-cn/agent-go/pkg/services"
	"github.com/spf13/cobra"
)

var (
	cfgFile string
	verbose bool
	debug   bool
	quiet   bool
	Cfg     *config.Config
	version string = "dev"
)

var RootCmd = &cobra.Command{
	Use:   "agentgo",
	Short: "AgentGo - AI Agent SDK & CLI for Go developers",
	Long: `AgentGo is a modular AI development platform that empowers Go applications with:
  • Agent  - Autonomous planning and execution with multi-turn reasoning
  • RAG    - Hybrid retrieval using Vector search and Knowledge Graphs
  • LLM    - Unified API for Ollama, OpenAI, DeepSeek, and more
  • MCP    - Standardized tool integration via Model Context Protocol
  • Skills - Expert capabilities via Claude-compatible markdown skills
  • Status - Real-time monitoring of provider health and system status`,
	PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
		// Skip config loading for commands that don't need existing config
		if cmd.Name() == "version" || cmd.Name() == "init" {
			return nil
		}

		var err error
		Cfg, err = config.Load(cfgFile)
		if err != nil {
			return fmt.Errorf("failed to load configuration: %w", err)
		}

		switch {
		case quiet:
			agentgolog.SetLevel(slog.LevelError)
		case debug || Cfg.Debug:
			agentgolog.SetLevel(slog.LevelDebug)
		case verbose:
			agentgolog.SetLevel(slog.LevelInfo)
		default:
			agentgolog.SetLevel(slog.LevelWarn)
		}

		if commandNeedsGlobalPool(cmd) {
			globalPoolService := services.GetGlobalPoolService()
			ctx := context.Background()
			if err := globalPoolService.Initialize(ctx, Cfg); err != nil {
				return fmt.Errorf("failed to initialize global pool service: %w", err)
			}
		}

		// Pass shared variables to all packages
		rag.SetSharedVariables(Cfg, verbose, quiet, version)
		mcp.SetSharedVariables(Cfg, verbose, quiet)
		agent.SetSharedVariables(Cfg, verbose)
		memory.SetSharedVariables(Cfg, verbose)
		ptc.SetSharedVariables(Cfg, verbose)
		acp.SetSharedVariables(Cfg, verbose)
		cachecmd.SetSharedVariables(Cfg, verbose)

		return nil
	},
}

func commandNeedsGlobalPool(cmd *cobra.Command) bool {
	path := cmd.CommandPath()
	return !strings.HasPrefix(path, "agentgo cache")
}

func Execute() error {
	return RootCmd.Execute()
}

// GetRootCmd returns the root cobra command for testing purposes.
func GetRootCmd() *cobra.Command {
	return RootCmd
}

// SetVersion sets the version for the CLI
func SetVersion(v string) {
	version = v
	RootCmd.Version = v
}

var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Print the version number",
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Printf("AgentGo version %s\n", version)
	},
}

func init() {
	RootCmd.PersistentFlags().StringVar(&cfgFile, "config", "", "configuration file path (default: ./agentgo.toml or ~/.agentgo/config/agentgo.toml)")
	RootCmd.PersistentFlags().BoolVarP(&verbose, "verbose", "v", false, "verbose logging output")
	RootCmd.PersistentFlags().BoolVarP(&debug, "debug", "d", false, "enable debug logging")
	RootCmd.PersistentFlags().BoolVarP(&quiet, "quiet", "q", false, "quiet mode")

	RootCmd.AddCommand(versionCmd)

	// Add RAG parent command from rag package
	RootCmd.AddCommand(rag.RagCmd)

	// Add MCP parent command from mcp package
	RootCmd.AddCommand(mcp.MCPCmd)

	// Add Agent command
	RootCmd.AddCommand(agent.AgentCmd)

	// Add agent registry command
	RootCmd.AddCommand(squad.SquadCmd)

	// Add ACP command
	RootCmd.AddCommand(acp.Cmd)

	// Add Skills command
	RootCmd.AddCommand(skills.Cmd)

	// Add PTC command
	RootCmd.AddCommand(ptc.Cmd)

	// Add Cache command
	RootCmd.AddCommand(cachecmd.Cmd)

	RootCmd.AddCommand(llmCmd)
	RootCmd.AddCommand(embeddingCmd)
	RootCmd.AddCommand(statusCmd)

	// Add Memory command
	memoryOpts := &memory.CommandOptions{}
	memoryCmd := memory.NewCommand(memoryOpts)
	memoryCmd.PersistentFlags().StringVar(&memoryOpts.DBPath, "db-path", "", "Memory database path (default: ./.agentgo/data/memory.db)")
	RootCmd.AddCommand(memoryCmd)
}
