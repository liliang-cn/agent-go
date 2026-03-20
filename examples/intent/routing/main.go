package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"

	"github.com/liliang-cn/agent-go/v2/pkg/agent"
)

func main() {
	ctx := context.Background()

	homeDir, _ := os.UserHomeDir()
	agentDBPath := filepath.Join(homeDir, ".agentgo", "data", "intent_routing.db")
	os.MkdirAll(filepath.Dir(agentDBPath), 0755)

	svc, err := agent.New("intent-routing-agent").
		WithDBPath(agentDBPath).
		WithRouter(agent.WithRouterThreshold(0.5)).
		Build()
	if err != nil {
		log.Fatalf("Failed to create agent: %v", err)
	}
	defer svc.Close()

	// Load custom intents for the demo
	intentDir := filepath.Join(".", "examples", ".intents")
	if err := svc.Router.LoadIntentsFromDir(intentDir); err != nil {
		log.Printf("Note: Could not load intents from %s: %v", intentDir, err)
	}

	fmt.Println("--- Intent Routing Demo ---")

	intents := svc.Router.ListIntents()
	fmt.Printf("Found %d intents:\n", len(intents))
	for _, intent := range intents {
		fmt.Printf("- %s\n", intent.Name)
	}

	hasWeather := false
	for _, intent := range intents {
		if intent.Name == "weather_lookup" {
			hasWeather = true
			break
		}
	}

	if hasWeather {
		fmt.Println("\n--- Testing Weather Intent Routing ---")
		queries := []string{
			"深圳天气如何？",
			"查一下明天的天气",
			"我想看看广州的天气预报",
		}

		for _, query := range queries {
			fmt.Printf("\nQuery: %s\n", query)
			result, err := svc.Router.Route(ctx, query)
			if err != nil {
				log.Printf("Routing failed: %v", err)
				continue
			}

			if result.Matched {
				fmt.Printf("Matched Intent: %s (Score: %.2f)\n", result.IntentName, result.Score)
				fmt.Printf("Mapped Tool: %s\n", result.ToolName)
			} else {
				fmt.Println("No match found.")
			}
		}
	} else {
		fmt.Println("\nNote: Create .intents/check_weather.md to enable weather routing demo.")
	}

	fmt.Println("\nIntent routing example completed successfully!")
}
