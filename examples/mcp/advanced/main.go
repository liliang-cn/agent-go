// Package main demonstrates advanced MCP multi-server integration.
//
// This example shows:
//   - Connecting to multiple MCP servers simultaneously
//   - Orchestrating tool calls across different servers
//   - Using Roots to define filesystem boundaries
//   - Chaining tool results between servers
//   - Error handling and graceful degradation
//
// Usage:
//
//	go run examples/mcp/advanced/main.go
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/liliang-cn/agent-go/v2/pkg/mcp"
)

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	fmt.Println("=== Advanced MCP Multi-Server Integration ===")
	fmt.Println()

	// 1. Load configuration with multiple servers
	config := loadMultiServerConfig()

	// 2. Create manager and start all servers
	manager := mcp.NewManager(config)

	fmt.Println("--- Starting MCP Servers ---")
	if err := startAllServers(ctx, manager); err != nil {
		log.Fatalf("Failed to start servers: %v", err)
	}
	defer manager.Close()

	// 3. Display available tools from all servers
	displayAllTools(ctx, manager)

	// 4. Scenario 1: Cross-server file analysis
	fmt.Println("\n=== Scenario 1: Cross-Server File Analysis ===")
	crossServerAnalysis(ctx, manager)

	// 5. Scenario 2: Git status + File operations
	fmt.Println("\n=== Scenario 2: Git + File Operations ===")
	gitAndFileOperations(ctx, manager)

	// 6. Scenario 3: Search and Read workflow
	fmt.Println("\n=== Scenario 3: Search and Read Workflow ===")
	searchAndReadWorkflow(ctx, manager)

	fmt.Println("\n✅ Advanced MCP example completed!")
}

// loadMultiServerConfig creates configuration for multiple servers
func loadMultiServerConfig() *mcp.Config {
	// Create server configurations
	servers := []mcp.ServerConfig{
		{
			Name:        "filesystem",
			Description: "File system operations",
			Type:        mcp.ServerTypeStdio,
			Command:     []string{"npx"},
			Args:        []string{"-y", "@modelcontextprotocol/server-filesystem", "."},
			AutoStart:   true,
		},
		{
			Name:        "git",
			Description: "Git version control",
			Type:        mcp.ServerTypeStdio,
			Command:     []string{"uvx"},
			Args:        []string{"mcp-server-git", "--repository", "."},
			AutoStart:   true,
		},
		{
			Name:        "everything",
			Description: "MCP test server with all features",
			Type:        mcp.ServerTypeStdio,
			Command:     []string{"npx"},
			Args:        []string{"-y", "@modelcontextprotocol/server-everything"},
			AutoStart:   true,
		},
	}

	return &mcp.Config{
		Enabled:        true,
		DefaultTimeout: 30 * time.Second,
		LoadedServers:  servers,
	}
}

// startAllServers starts all configured MCP servers
func startAllServers(ctx context.Context, manager *mcp.Manager) error {
	servers := manager.ListClients()
	for name := range servers {
		fmt.Printf("  Server '%s' already running\n", name)
	}

	// Start each server from config
	config := mcp.DefaultConfig()
	config.LoadedServers = []mcp.ServerConfig{
		{Name: "filesystem", Type: mcp.ServerTypeStdio, Command: []string{"npx"}, Args: []string{"-y", "@modelcontextprotocol/server-filesystem", "."}},
		{Name: "git", Type: mcp.ServerTypeStdio, Command: []string{"uvx"}, Args: []string{"mcp-server-git", "--repository", "."}},
		{Name: "everything", Type: mcp.ServerTypeStdio, Command: []string{"npx"}, Args: []string{"-y", "@modelcontextprotocol/server-everything"}},
	}

	// Start servers
	serverNames := []string{"filesystem", "git", "everything"}
	for _, name := range serverNames {
		client, err := manager.StartServer(ctx, name)
		if err != nil {
			log.Printf("[WARN] Failed to start server '%s': %v", name, err)
			continue
		}
		toolCount := len(client.GetTools())
		fmt.Printf("  ✅ Started '%s' (%d tools)\n", name, toolCount)
	}

	return nil
}

// displayAllTools shows all available tools from all servers
func displayAllTools(ctx context.Context, manager *mcp.Manager) {
	tools := manager.GetAvailableTools(ctx)
	fmt.Printf("\n--- Available Tools (%d total) ---\n", len(tools))

	// Group by server
	byServer := make(map[string][]mcp.AgentToolInfo)
	for _, tool := range tools {
		byServer[tool.ServerName] = append(byServer[tool.ServerName], tool)
	}

	for server, serverTools := range byServer {
		fmt.Printf("\n  [%s] (%d tools):\n", server, len(serverTools))
		for i, tool := range serverTools {
			if i >= 3 {
				fmt.Printf("    ... and %d more\n", len(serverTools)-3)
				break
			}
			desc := tool.Description
			if len(desc) > 50 {
				desc = desc[:50] + "..."
			}
			fmt.Printf("    - %s: %s\n", tool.ActualName, desc)
		}
	}
}

// crossServerAnalysis demonstrates using multiple servers together
func crossServerAnalysis(ctx context.Context, manager *mcp.Manager) {
	// Step 1: List files using filesystem server
	fmt.Println("Step 1: Listing files from filesystem server...")
	_, err := manager.CallTool(ctx, "mcp_filesystem_list_directory", map[string]interface{}{
		"path": ".",
	})
	if err != nil {
		fmt.Printf("  ❌ Error: %v\n", err)
		return
	}
	fmt.Printf("  ✅ Files listed\n")

	// Step 2: Check git status using git server
	fmt.Println("Step 2: Checking git status...")
	gitResult, err := manager.CallTool(ctx, "mcp_git_git_status", map[string]interface{}{})
	if err != nil {
		fmt.Printf("  ❌ Error: %v\n", err)
		return
	}

	if gitResult.Success && gitResult.Data != nil {
		status := fmt.Sprintf("%v", gitResult.Data)
		if strings.Contains(status, "clean") || !strings.Contains(status, "modified") {
			fmt.Printf("  ✅ Git status: Working tree clean\n")
		} else {
			fmt.Printf("  ✅ Git status: Has changes\n")
		}
	}

	// Step 3: Get sample output from everything server
	fmt.Println("Step 3: Getting sample data from everything server...")
	echoResult, err := manager.CallTool(ctx, "mcp_everything_echo", map[string]interface{}{
		"message": "Multi-server coordination works!",
	})
	if err != nil {
		fmt.Printf("  ❌ Error: %v\n", err)
		return
	}
	if echoResult.Success && echoResult.Data != nil {
		fmt.Printf("  ✅ Echo response: %v\n", echoResult.Data)
	}

	fmt.Println("\n  📊 Cross-server analysis complete!")
}

// gitAndFileOperations shows git + filesystem coordination
func gitAndFileOperations(ctx context.Context, manager *mcp.Manager) {
	// Step 1: Get current branch
	fmt.Println("Step 1: Getting current git branch...")
	branchResult, err := manager.CallTool(ctx, "mcp_git_git_branch", map[string]interface{}{})
	if err != nil {
		fmt.Printf("  ⚠️ Git branch error: %v\n", err)
	} else if branchResult.Success {
		// Extract branch info
		data := fmt.Sprintf("%v", branchResult.Data)
		lines := strings.Split(data, "\n")
		for _, line := range lines {
			if strings.Contains(line, "*") {
				fmt.Printf("  ✅ Current branch: %s\n", strings.TrimSpace(strings.Trim(line, "* ")))
				break
			}
		}
	}

	// Step 2: List allowed directories
	fmt.Println("Step 2: Checking filesystem access...")
	dirResult, err := manager.CallTool(ctx, "mcp_filesystem_list_allowed_directories", map[string]interface{}{})
	if err != nil {
		fmt.Printf("  ⚠️ Directory list error: %v\n", err)
	} else if dirResult.Success {
		fmt.Printf("  ✅ Allowed directories: %v\n", dirResult.Data)
	}

	// Step 3: Read a config file
	fmt.Println("Step 3: Reading go.mod...")
	readResult, err := manager.CallTool(ctx, "mcp_filesystem_read_text_file", map[string]interface{}{
		"path": "go.mod",
	})
	if err != nil {
		fmt.Printf("  ⚠️ Read error: %v\n", err)
	} else if readResult.Success && readResult.Data != nil {
		content := fmt.Sprintf("%v", readResult.Data)
		lines := strings.Split(content, "\n")
		if len(lines) > 0 {
			fmt.Printf("  ✅ First line: %s\n", strings.TrimSpace(lines[0]))
		}
	}

	fmt.Println("\n  📁 Git + File operations complete!")
}

// searchAndReadWorkflow demonstrates search then read pattern
func searchAndReadWorkflow(ctx context.Context, manager *mcp.Manager) {
	// Step 1: Search for Go files
	fmt.Println("Step 1: Searching for Go files...")
	searchResult, err := manager.CallTool(ctx, "mcp_filesystem_search_files", map[string]interface{}{
		"path":    ".",
		"pattern": "**/*.go",
	})
	if err != nil {
		fmt.Printf("  ⚠️ Search error: %v\n", err)
		return
	}

	var files []string
	if searchResult.Success && searchResult.Data != nil {
		data := fmt.Sprintf("%v", searchResult.Data)
		lines := strings.Split(data, "\n")
		for _, line := range lines {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			// Clean up file path (remove [FILE] prefix if present)
			if strings.HasPrefix(line, "[FILE]") {
				line = strings.TrimPrefix(line, "[FILE] ")
			}
			line = strings.TrimSpace(line)
			if line != "" && strings.HasSuffix(line, ".go") {
				files = append(files, line)
			}
		}
		fmt.Printf("  ✅ Found %d Go files\n", len(files))
	}

	// Step 2: Read first few files (just the first line)
	if len(files) > 0 {
		fmt.Println("Step 2: Reading first lines of found files...")
		count := 0
		for _, file := range files {
			if count >= 3 {
				break
			}

			// Use relative path
			relPath := file
			if idx := strings.Index(file, "/"); idx > 0 {
				// Keep relative path
				relPath = "." + file[strings.Index(file, "/base/agentgo")+len("/base/agentgo"):]
			}

			readResult, err := manager.CallTool(ctx, "mcp_filesystem_read_text_file", map[string]interface{}{
				"path": file,
				"head": 1,
			})
			if err != nil {
				continue
			}

			if readResult.Success && readResult.Data != nil {
				content := fmt.Sprintf("%v", readResult.Data)
				firstLine := strings.Split(content, "\n")[0]
				if len(firstLine) > 60 {
					firstLine = firstLine[:60] + "..."
				}
				fmt.Printf("  📄 %s: %s\n", relPath, firstLine)
				count++
			}
		}
	}

	fmt.Println("\n  🔍 Search and read workflow complete!")
}

// Helper function to check if running in a git repo
func init() {
	if _, err := os.Stat(".git"); os.IsNotExist(err) {
		log.Println("[INFO] Not in a git repository, some features may be limited")
	}
}
