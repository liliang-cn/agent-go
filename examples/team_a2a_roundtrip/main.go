// Package main demonstrates an in-process A2A round-trip for one team:
// enable A2A on the default team, mount the A2A server, connect an A2A client,
// then send both normal and streaming requests.
//
// Usage:
//
//	go run ./examples/team_a2a_roundtrip
package main

import (
	"context"
	"fmt"
	"log"
	"net/http/httptest"
	"strings"
	"time"

	"github.com/liliang-cn/agent-go/v2/examples/internal/teamdemo"
	agenta2a "github.com/liliang-cn/agent-go/v2/pkg/a2a"
)

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	manager, cfg, err := teamdemo.NewManager("team-a2a-roundtrip")
	if err != nil {
		log.Fatalf("setup manager: %v", err)
	}

	team, err := teamdemo.DefaultTeam(manager)
	if err != nil {
		log.Fatalf("load default team: %v", err)
	}
	team, err = manager.SetTeamA2AEnabled(ctx, team.Name, true)
	if err != nil {
		log.Fatalf("enable team a2a: %v", err)
	}

	server, err := agenta2a.NewServer(teamdemo.A2ACatalog{Manager: manager}, agenta2a.Config{
		Enabled:    true,
		PathPrefix: "/a2a",
	})
	if err != nil {
		log.Fatalf("create a2a server: %v", err)
	}

	httpServer := httptest.NewServer(server)
	defer httpServer.Close()

	cardURL := httpServer.URL + agenta2a.TeamCardPath("/a2a", teamdemo.TeamA2AID(team))
	client, err := agenta2a.Connect(ctx, cardURL, agenta2a.ClientConfig{})
	if err != nil {
		log.Fatalf("connect a2a client: %v", err)
	}

	fmt.Println("=== Team A2A Roundtrip Example ===")
	fmt.Printf("Example home: %s\n", cfg.Home)
	fmt.Printf("Team card URL: %s\n", cardURL)

	text, _, err := client.SendText(ctx, "Reply with exactly: A2A_ROUNDTRIP_OK")
	if err != nil {
		log.Fatalf("a2a send text: %v", err)
	}
	fmt.Printf("SendText final result: %s\n", meaningfulA2AText(text))

	fmt.Println("StreamText events:")
	lastPrinted := ""
	for evt, err := range client.StreamText(ctx, "Reply with exactly: A2A_STREAM_OK") {
		if err != nil {
			log.Fatalf("a2a stream: %v", err)
		}
		cleaned := meaningfulA2AText(evt.Text)
		if cleaned == "" || cleaned == lastPrinted {
			continue
		}
		lastPrinted = cleaned
		fmt.Printf("  - %s\n", cleaned)
	}
}

func meaningfulA2AText(text string) string {
	lines := strings.Split(text, "\n")
	meaningful := make([]string, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(line)
		switch {
		case line == "":
			continue
		case strings.HasPrefix(line, "map["):
			continue
		case strings.HasPrefix(line, "## "):
			continue
		default:
			meaningful = append(meaningful, line)
		}
	}
	if len(meaningful) == 0 {
		return ""
	}
	return meaningful[len(meaningful)-1]
}
