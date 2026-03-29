package mcp

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/liliang-cn/agent-go/v2/pkg/config"
	agentgomcp "github.com/liliang-cn/agent-go/v2/pkg/mcp"
	"github.com/liliang-cn/agent-go/v2/pkg/services"
	skillsmcp "github.com/liliang-cn/skills-go/mcp"
	"github.com/spf13/cobra"
)

var (
	convertOutputDir string
	convertUseLLM    bool
	convertLLMModel  string
)

// mcpConvertCmd converts MCP servers to Skills
var mcpConvertCmd = &cobra.Command{
	Use:   "convert [server-name]",
	Short: "Convert MCP servers to Skills",
	Long: `Convert MCP servers to Skills using the MCP-to-Skill converter.

The converter connects to MCP servers, discovers their capabilities (tools, resources, prompts),
and generates Skill files that can be loaded by agentgo.

Examples:
  # Convert all MCP servers to Skills
  agentgo mcp convert

  # Convert a specific MCP server
  agentgo mcp convert filesystem

  # Convert with LLM enhancement
  agentgo mcp convert --use-llm --llm-model gpt-4o

  # Convert to custom output directory
  agentgo mcp convert --output-dir ./my-skills`,
	RunE: runMCPConvert,
}

// mcpDiscoverCmd discovers MCP server capabilities without converting
var mcpDiscoverCmd = &cobra.Command{
	Use:   "discover [server-name]",
	Short: "Discover MCP server capabilities",
	Long: `Discover and display MCP server capabilities without converting to Skills.

This command connects to MCP servers and shows available tools, resources, and prompts.

Examples:
  # Discover all servers
  agentgo mcp discover

  # Discover a specific server
  agentgo mcp discover filesystem`,
	RunE: runMCPDiscover,
}

func init() {
	MCPCmd.AddCommand(mcpConvertCmd)
	MCPCmd.AddCommand(mcpDiscoverCmd)

	// Convert flags
	mcpConvertCmd.Flags().StringVar(&convertOutputDir, "output-dir", "", "Output directory for converted skills (default: ~/.agentgo/skills)")
	mcpConvertCmd.Flags().BoolVar(&convertUseLLM, "use-llm", false, "Use LLM for enhanced skill generation")
	mcpConvertCmd.Flags().StringVar(&convertLLMModel, "llm-model", "gpt-4o", "LLM model to use for enhancement")
}

func runMCPConvert(cmd *cobra.Command, args []string) error {
	if Cfg == nil {
		var err error
		Cfg, err = config.Load()
		if err != nil {
			return fmt.Errorf("failed to load config: %w", err)
		}
	}

	ctx := context.Background()

	// Get global LLM service (needed for MCP service)
	poolService := services.GetGlobalPoolService()
	gen, err := poolService.GetLLMService()
	if err != nil {
		return fmt.Errorf("failed to get LLM generator: %w", err)
	}

	// Create MCP service
	mcpService, err := agentgomcp.NewService(&Cfg.MCP, gen)
	if err != nil {
		return fmt.Errorf("failed to create MCP service: %w", err)
	}
	defer mcpService.Close()

	// Create converter config
	convCfg := agentgomcp.DefaultConverterConfig()
	if convertOutputDir != "" {
		convCfg.OutputDir = convertOutputDir
	}
	convCfg.UseLLM = convertUseLLM
	convCfg.LLMModel = convertLLMModel

	// Get LLM API key from pool config if using LLM
	if convertUseLLM {
		providers, listErr := poolService.ListProviders()
		if listErr != nil {
			return fmt.Errorf("failed to list llm providers: %w", listErr)
		}
		for _, provider := range providers {
			if provider == nil || !provider.Enabled {
				continue
			}
			if convertLLMModel == "" || provider.ModelName == convertLLMModel {
				convCfg.LLMAPIKey = provider.Key
				convCfg.LLMBaseURL = provider.BaseURL
				break
			}
			if convCfg.LLMBaseURL == "" {
				convCfg.LLMAPIKey = provider.Key
				convCfg.LLMBaseURL = provider.BaseURL
			}
		}
	}

	// Create converter
	converter, err := agentgomcp.NewConverter(convCfg, mcpService)
	if err != nil {
		return fmt.Errorf("failed to create converter: %w", err)
	}

	// Determine which servers to convert
	serverName := ""
	if len(args) > 0 {
		serverName = args[0]
	}

	// Ensure output directory exists
	if err := os.MkdirAll(convCfg.OutputDir, 0755); err != nil {
		return fmt.Errorf("failed to create output directory: %w", err)
	}

	fmt.Printf("🔄 Converting MCP servers to Skills...\n")
	fmt.Printf("📁 Output directory: %s\n", convCfg.OutputDir)
	if convertUseLLM {
		fmt.Printf("🤖 Using LLM: %s\n", convCfg.LLMModel)
	}
	fmt.Println()

	var converted int
	var failed int

	if serverName != "" {
		// Convert single server
		fmt.Printf("📦 Converting server: %s\n", serverName)
		sk, err := converter.ConvertServer(ctx, serverName)
		if err != nil {
			fmt.Printf("   ❌ Failed: %v\n", err)
			failed++
		} else {
			fmt.Printf("   ✅ Converted: %s -> %s\n", serverName, sk.Path)
			converted++
		}
	} else {
		// Convert all servers
		skills, err := converter.ConvertAllServers(ctx)
		if err != nil {
			fmt.Printf("⚠️  Some conversions failed:\n%v\n", err)
		}

		for _, sk := range skills {
			fmt.Printf("   ✅ Converted: %s -> %s\n", sk.Name, sk.Path)
			converted++
		}
	}

	fmt.Printf("\n✨ Conversion complete: %d succeeded, %d failed\n", converted, failed)
	if converted > 0 {
		fmt.Printf("\n💡 To use the converted skills, run:\n")
		fmt.Printf("   agentgo agent run \"your question here\"\n")
	}

	return nil
}

func runMCPDiscover(cmd *cobra.Command, args []string) error {
	if Cfg == nil {
		var err error
		Cfg, err = config.Load()
		if err != nil {
			return fmt.Errorf("failed to load config: %w", err)
		}
	}

	ctx := context.Background()

	// Get global LLM service
	poolService := services.GetGlobalPoolService()
	gen, err := poolService.GetLLMService()
	if err != nil {
		return fmt.Errorf("failed to get LLM generator: %w", err)
	}

	// Create MCP service
	mcpService, err := agentgomcp.NewService(&Cfg.MCP, gen)
	if err != nil {
		return fmt.Errorf("failed to create MCP service: %w", err)
	}
	defer mcpService.Close()

	// Create converter
	convCfg := agentgomcp.DefaultConverterConfig()
	homeDir, _ := os.UserHomeDir()
	convCfg.OutputDir = filepath.Join(homeDir, ".agentgo", "skills")
	converter, err := agentgomcp.NewConverter(convCfg, mcpService)
	if err != nil {
		return fmt.Errorf("failed to create converter: %w", err)
	}

	// Determine which servers to discover
	serverName := ""
	if len(args) > 0 {
		serverName = args[0]
	}

	fmt.Printf("🔍 Discovering MCP server capabilities...\n\n")

	if serverName != "" {
		// Discover single server
		caps, err := converter.DiscoverServer(ctx, serverName)
		if err != nil {
			return fmt.Errorf("failed to discover server %s: %w", serverName, err)
		}
		printCapabilities(serverName, caps)
	} else {
		// Discover all servers
		servers := Cfg.MCP.GetLoadedServers()
		for _, s := range servers {
			fmt.Printf("📦 Server: %s\n", s.Name)
			caps, err := converter.DiscoverServer(ctx, s.Name)
			if err != nil {
				fmt.Printf("   ❌ Failed to discover: %v\n\n", err)
				continue
			}
			printCapabilities(s.Name, caps)
			fmt.Println()
		}
	}

	return nil
}

func printCapabilities(serverName string, caps *skillsmcp.ServerCapabilities) {
	if caps == nil {
		return
	}

	// Tools
	if len(caps.Tools) > 0 {
		fmt.Printf("   🔧 Tools (%d):\n", len(caps.Tools))
		for _, t := range caps.Tools {
			fmt.Printf("      - %s", t.Name)
			if t.Description != "" {
				fmt.Printf(": %s", t.Description)
			}
			fmt.Println()
		}
	}

	// Resources
	if len(caps.Resources) > 0 {
		fmt.Printf("   📄 Resources (%d):\n", len(caps.Resources))
		for _, r := range caps.Resources {
			fmt.Printf("      - %s (%s)", r.Name, r.URI)
			if r.Description != "" {
				fmt.Printf(": %s", r.Description)
			}
			fmt.Println()
		}
	}

	// Resource Templates
	if len(caps.ResourceTemplates) > 0 {
		fmt.Printf("   📋 Resource Templates (%d):\n", len(caps.ResourceTemplates))
		for _, t := range caps.ResourceTemplates {
			fmt.Printf("      - %s: %s", t.Name, t.URITemplate)
			if t.Description != "" {
				fmt.Printf(": %s", t.Description)
			}
			fmt.Println()
		}
	}

	// Prompts
	if len(caps.Prompts) > 0 {
		fmt.Printf("   💬 Prompts (%d):\n", len(caps.Prompts))
		for _, p := range caps.Prompts {
			fmt.Printf("      - %s", p.Name)
			if p.Description != "" {
				fmt.Printf(": %s", p.Description)
			}
			if len(p.Arguments) > 0 {
				fmt.Printf(" (args: %d)", len(p.Arguments))
			}
			fmt.Println()
		}
	}
}
