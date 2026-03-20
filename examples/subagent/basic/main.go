// Package main demonstrates basic foreground SubAgent execution with custom tools.
//
// A SubAgent wraps an Agent with isolated execution — its own session, tool
// filtering, lifecycle states, and progress tracking. This example creates a
// simple "research analyst" SubAgent that uses two tools (search_knowledge and
// summarize_findings) to answer a question in a single blocking Run() call.
//
// Key concepts:
//   - agent.NewAgent() creates the Agent, with tools added via AddTool()
//   - svc.CreateSubAgent(agent, goal, opts...) wires the Agent to the Service
//   - sa.Run(ctx) blocks until the SubAgent completes (foreground mode)
//   - Progress callback reports each turn in real time
//
// Usage:
//
//	go run examples/subagent_basic/main.go
//	go run examples/subagent_basic/main.go "What are the key benefits of microservices?"
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/liliang-cn/agent-go/v2/pkg/agent"
)

// ── Mock knowledge base ──────────────────────────────────────────────────────

type Article struct {
	ID      string   `json:"id"`
	Title   string   `json:"title"`
	Content string   `json:"content"`
	Tags    []string `json:"tags"`
}

var knowledgeBase = []Article{
	{
		ID:    "art_001",
		Title: "Introduction to Microservices",
		Content: "Microservices architecture breaks applications into small, independently deployable " +
			"services. Each service owns its data and communicates via APIs. Benefits include " +
			"independent scaling, technology diversity, and fault isolation.",
		Tags: []string{"microservices", "architecture", "distributed"},
	},
	{
		ID:    "art_002",
		Title: "Microservices vs Monoliths",
		Content: "Monolithic applications are simpler to develop and deploy initially but become " +
			"harder to scale and maintain as they grow. Microservices trade initial complexity " +
			"for long-term flexibility, enabling teams to deploy independently.",
		Tags: []string{"microservices", "monolith", "comparison"},
	},
	{
		ID:    "art_003",
		Title: "Event-Driven Architecture",
		Content: "Event-driven systems use asynchronous message passing between services. " +
			"This decouples producers from consumers and improves resilience. Common patterns " +
			"include event sourcing and CQRS.",
		Tags: []string{"events", "architecture", "async"},
	},
	{
		ID:    "art_004",
		Title: "Container Orchestration with Kubernetes",
		Content: "Kubernetes automates deployment, scaling, and management of containerized " +
			"applications. It works well with microservices by providing service discovery, " +
			"load balancing, and self-healing capabilities.",
		Tags: []string{"kubernetes", "containers", "microservices", "devops"},
	},
}

// ── Tool handlers ────────────────────────────────────────────────────────────

func handleSearchKnowledge(_ context.Context, args map[string]interface{}) (interface{}, error) {
	query, _ := args["query"].(string)
	if query == "" {
		return nil, fmt.Errorf("query is required")
	}

	query = strings.ToLower(query)
	var results []Article
	for _, art := range knowledgeBase {
		titleMatch := strings.Contains(strings.ToLower(art.Title), query)
		contentMatch := strings.Contains(strings.ToLower(art.Content), query)
		tagMatch := false
		for _, tag := range art.Tags {
			if strings.Contains(tag, query) {
				tagMatch = true
				break
			}
		}
		if titleMatch || contentMatch || tagMatch {
			results = append(results, art)
		}
	}

	return map[string]interface{}{
		"query":   query,
		"results": results,
		"count":   len(results),
	}, nil
}

func handleSummarizeFindings(_ context.Context, args map[string]interface{}) (interface{}, error) {
	findingsRaw, _ := args["findings"].(string)
	if findingsRaw == "" {
		return nil, fmt.Errorf("findings text is required")
	}

	// Simulate summarization: just return a structured response.
	// In production this would call another LLM or NLP service.
	wordCount := len(strings.Fields(findingsRaw))
	return map[string]interface{}{
		"summary":    fmt.Sprintf("Summary of %d words of findings: %s", wordCount, truncate(findingsRaw, 200)),
		"word_count": wordCount,
		"status":     "summarized",
	}, nil
}

func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

// ── Main ─────────────────────────────────────────────────────────────────────

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	question := "What are the key benefits of microservices architecture?"
	if len(os.Args) > 1 {
		question = strings.Join(os.Args[1:], " ")
	}

	fmt.Println("=== SubAgent Basic Example ===")
	fmt.Printf("Goal: %s\n\n", question)

	svc, err := agent.New("Orchestrator").
		WithDebug(os.Getenv("DEBUG") != "").
		Build()
	if err != nil {
		log.Fatalf("Failed to create service: %v", err)
	}
	defer svc.Close()

	researchAgent := agent.NewAgent("ResearchAnalyst")
	researchAgent.SetInstructions(
		"You are a research analyst. Use search_knowledge to find relevant articles, " +
			"then use summarize_findings to produce a concise summary. " +
			"Always search first, then summarize the results.")

	researchAgent.AddTool(
		"search_knowledge",
		"Search the knowledge base for articles matching a query. Returns matching articles with id, title, content, and tags.",
		map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"query": map[string]interface{}{
					"type":        "string",
					"description": "Search query string",
				},
			},
			"required": []string{"query"},
		},
		handleSearchKnowledge,
	)

	researchAgent.AddTool(
		"summarize_findings",
		"Summarize a block of research findings into a concise report.",
		map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"findings": map[string]interface{}{
					"type":        "string",
					"description": "The raw findings text to summarize",
				},
			},
			"required": []string{"findings"},
		},
		handleSummarizeFindings,
	)

	// 3. Create the SubAgent with options
	sa := svc.CreateSubAgent(
		researchAgent,
		question,
		agent.WithSubAgentMaxTurns(5),
		agent.WithSubAgentProgressCallback(func(p agent.SubAgentProgress) {
			fmt.Printf("  [progress] turn=%d/%d state=%s msg=%s elapsed=%s\n",
				p.CurrentTurn, p.MaxTurns, p.State, p.Message, p.ElapsedTime.Round(time.Millisecond))
		}),
	)

	fmt.Printf("SubAgent ID:    %s\n", sa.ID())
	fmt.Printf("SubAgent Name:  %s\n", sa.Name())
	fmt.Printf("Initial State:  %s\n\n", sa.GetState())

	// 4. Run (blocking foreground execution)
	result, err := sa.Run(ctx)
	if err != nil {
		log.Fatalf("SubAgent execution failed: %v", err)
	}

	// 5. Print results
	fmt.Printf("\n=== Execution Complete ===\n")
	fmt.Printf("Final State:   %s\n", sa.GetState())
	fmt.Printf("Turns Used:    %d\n", sa.GetCurrentTurn())
	fmt.Printf("Duration:      %s\n", sa.GetDuration().Round(time.Millisecond))

	fmt.Printf("\n=== Result ===\n")
	resultJSON, _ := json.MarshalIndent(result, "", "  ")
	fmt.Println(string(resultJSON))
}
