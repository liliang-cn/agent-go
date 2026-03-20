// Package main demonstrates PTC with real MCP tools and Skills integration.
//
// This example shows:
//   - Real MCP filesystem server providing file tools
//   - Real Skills service with registered skills
//   - PTC orchestrating both via callTool() in JavaScript
//
// Prerequisites:
//   - npx installed (for MCP filesystem server)
//   - Valid LLM provider configured (OPENAI_API_KEY, ANTHROPIC_API_KEY, or Ollama running)
//
// Usage:
//
//	go run examples/ptc/mcp_skills/main.go
//	DEBUG=1 go run examples/ptc/mcp_skills/main.go  # Verbose output
//	go run examples/ptc/mcp_skills/main.go "Read go.mod and summarize"
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/liliang-cn/agent-go/v2/pkg/agent"
)

// List of scenarios to demonstrate PTC power
var scenarios = []struct {
	name     string
	question string
}{
	{
		name: "Scenario 1: MCP Tool Orchestration",
		question: `Use PTC to echo multiple messages and aggregate them:
1. Call mcp_everything_echo three times with different messages ('Hello', 'PTC', 'World').
2. In JS, combine these responses into a single string.
3. MANDATORY: return { combined: combinedString, individualResponses: [r1, r2, r3] };`,
	},
	{
		name: "Scenario 2: Skill-based Code Review",
		question: `Analyze this Go code snippet using the code-reviewer skill:
const code = "func sum(a, b int) int { return a + b }";
1. Call callTool('code-reviewer', { code: code }).
2. Return the review result along with the original code.
3. MANDATORY: return { code, review: reviewResult };`,
	},
	{
		name: "Scenario 3: Multi-Provider Integration",
		question: `Combined MCP and Skill workflow:
1. Use mcp_everything_echo to generate a "message of the day".
2. Use the 'test-skill' to greet a user 'Admin' with that message.
3. MANDATORY: return { motd: message, greeting: finalGreeting };`,
	},
}

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Second)
	defer cancel()

	debug := os.Getenv("DEBUG") != ""

	fmt.Println("=== PTC Multi-Scenario (Stable Suite) ===")
	fmt.Println()

	// Get absolute path for skills
	pwd, _ := os.Getwd()
	skillsPath := filepath.Join(pwd, "examples", ".skills")

	// Create agent
	svc, err := agent.New("ptc-stable-suite").
		WithMCP(agent.WithMCPConfigPaths("examples/mcpServers.json")).
		WithSkills(agent.WithSkillsPaths(skillsPath)).
		WithPTC().
		WithDebug(debug).
		Build()

	if err != nil {
		log.Fatalf("Failed to create agent: %v", err)
	}
	defer svc.Close()

	// Show agent info
	info := svc.Info()
	fmt.Printf("--- Agent Configuration ---\n")
	fmt.Printf("  Model:    %s\n", info.Model)
	fmt.Printf("  Base URL: %s\n", info.BaseURL)
	fmt.Printf("  PTC:      %v\n", info.PTCEnabled)
	fmt.Printf("  MCP:      %v\n", info.MCPEnabled)
	fmt.Printf("  Skills:   %v\n", info.SkillsEnabled)
	fmt.Println("---------------------------")
	fmt.Println()

	for i, s := range scenarios {
		fmt.Printf("\n--- [%d/%d] %s ---\n", i+1, len(scenarios), s.name)
		fmt.Printf("Question: %s\n\n", s.question)

		result, err := svc.ChatWithPTC(ctx, s.question)
		if err != nil {
			fmt.Printf("❌ Failed: %v\n", err)
			continue
		}

		// Display results
		if result.PTCUsed && result.PTCResult != nil {
			fmt.Printf("PTC Status: %s\n", result.PTCResult.Type)
			if result.PTCResult.ExecutionResult != nil {
				exec := result.PTCResult.ExecutionResult
				if exec.ReturnValue != nil {
					fmt.Printf("📦 Result Data: %+v\n", exec.ReturnValue)
				}
				if len(exec.ToolCalls) > 0 {
					fmt.Printf("🛠️ Tools Called: %d\n", len(exec.ToolCalls))
				}
			}
			fmt.Println("\n📝 Formatted for LLM:")
			fmt.Println(result.PTCResult.FormatForLLM())
		} else {
			fmt.Printf("⚠️ PTC not used. LLM Response:\n%s\n", result.LLMResponse)
		}

		fmt.Println("-------------------------------------------")
	}

	fmt.Println("\n✅ All scenarios completed!")
}
