// Package main demonstrates MCP (Model Context Protocol) tools integration.
//
// MCP allows AgentGo to use external tools from MCP servers like filesystem,
// web search, databases, etc.
//
// This example shows:
//   - Creating an agent with MCP enabled using fluent builder
//   - Loading MCP config from custom path
//   - Using transparent access to MCP service (svc.MCP.*)
//
// Usage:
//
//	go run examples/mcp/basic/main.go
package main

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/liliang-cn/agent-go/v2/pkg/agent"
)

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	fmt.Println("=== MCP Tools Integration Example ===")
	fmt.Println()

	// Create agent with MCP enabled using Builder pattern
	// Use WithMCPConfigPaths to load MCP servers from custom config files
	svc, err := agent.New("mcp-demo").
		WithMCP(agent.WithMCPConfigPaths("examples/mcpServers.json")).
		WithDebug().
		Build()
	if err != nil {
		log.Fatalf("Failed to create agent: %v", err)
	}
	defer svc.Close()

	// === Transparent Access to MCP Service ===
	fmt.Println("--- MCP Service Transparent Access ---")

	// List MCP servers status via transparent access
	if svc.MCP != nil {
		servers := svc.MCP.ListServers()
		fmt.Printf("MCP Servers (%d):\n", len(servers))
		for _, s := range servers {
			status := "stopped"
			if s.Running {
				status = "running"
			}
			fmt.Printf("  - %s: %s (%d tools)\n", s.Name, status, s.ToolCount)
		}
		fmt.Println()

		// List available tools via transparent access
		tools := svc.MCP.GetAvailableTools(ctx)
		fmt.Printf("Available MCP Tools (%d):\n", len(tools))
		for i, t := range tools {
			if i >= 5 {
				fmt.Printf("  ... and %d more\n", len(tools)-5)
				break
			}
			fmt.Printf("  - %s: %s\n", t.Name, t.Description)
		}
		fmt.Println()
	}

	// === Run Task Using MCP Tools ===
	fmt.Println("--- Running Task with MCP Tools ---")

	result, err := svc.Run(ctx, "List the files in the current directory using available tools")
	if err != nil {
		log.Fatalf("Run failed: %v", err)
	}

	fmt.Printf("\nResult: %s\n", result.Text())

	// === Direct Tool Call via Transparent Access ===
	fmt.Println("\n--- Direct Tool Call via Transparent Access ---")

	if svc.MCP != nil {
		// Directly call an MCP tool without going through agent planning
		// Use the filesystem server's list_directory tool
		toolResult, err := svc.MCP.CallTool(ctx, "mcp_filesystem_list_directory", map[string]interface{}{
			"path": ".",
		})
		if err != nil {
			fmt.Printf("Direct tool call failed: %v\n", err)
		} else {
			fmt.Printf("Direct tool call result: %v\n", toolResult)
		}
	}

	fmt.Println("\n✅ MCP integration example completed!")
}
