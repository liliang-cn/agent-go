// Package main demonstrates the new TeamGateway API:
// submit a team request, subscribe to its typed event stream, then fetch the
// final TeamResponse.
//
// Usage:
//
//	go run ./examples/team_gateway_stream
package main

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/liliang-cn/agent-go/v2/examples/internal/teamdemo"
	"github.com/liliang-cn/agent-go/v2/pkg/agent"
)

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	manager, cfg, err := teamdemo.NewManager("team-gateway-stream")
	if err != nil {
		log.Fatalf("setup manager: %v", err)
	}

	team, err := teamdemo.DefaultTeam(manager)
	if err != nil {
		log.Fatalf("load default team: %v", err)
	}

	fmt.Println("=== Team Gateway Stream Example ===")
	fmt.Printf("Example home: %s\n", cfg.Home)
	fmt.Printf("Team: %s (%s)\n", team.Name, team.ID)

	resp, err := manager.SubmitTeamRequest(ctx, &agent.TeamRequest{
		TeamID: team.ID,
		Prompt: "Reply with exactly: TEAM_GATEWAY_OK",
		Metadata: map[string]any{
			"example": "team_gateway_stream",
		},
	})
	if err != nil {
		log.Fatalf("submit team request: %v", err)
	}

	fmt.Printf("Response ID: %s\n", resp.ID)
	fmt.Printf("Ack: %s\n", resp.AckMessage)
	fmt.Println("Streaming events:")

	events, unsubscribe, err := manager.SubscribeTeamResponse(resp.ID)
	if err != nil {
		log.Fatalf("subscribe team response: %v", err)
	}
	defer unsubscribe()

	for evt := range events {
		switch evt.Type {
		case agent.TeamResponseEventTypeProgress, agent.TeamResponseEventTypeRuntime:
			if evt.Runtime != nil && strings.TrimSpace(evt.Runtime.Content) != "" {
				fmt.Printf("  - %s (%s): %s\n", evt.Type, evt.Runtime.Type, summarizeText(evt.Runtime.Content))
			} else {
				fmt.Printf("  - %s\n", evt.Type)
			}
		default:
			if evt.Message != "" {
				fmt.Printf("  - %s: %s\n", evt.Type, summarizeText(evt.Message))
			} else {
				fmt.Printf("  - %s\n", evt.Type)
			}
		}
	}

	latest, err := manager.GetTeamResponse(resp.ID)
	if err != nil {
		log.Fatalf("get final team response: %v", err)
	}

	fmt.Printf("Final status: %s\n", latest.Status)
	fmt.Printf("Final text: %s\n", latest.ResultText)
}

func summarizeText(text string) string {
	text = strings.Join(strings.Fields(strings.TrimSpace(text)), " ")
	if len(text) <= 120 {
		return text
	}
	return text[:117] + "..."
}
