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

	// Create agent service using fluent builder
	homeDir, _ := os.UserHomeDir()
	agentDBPath := filepath.Join(homeDir, ".agentgo", "data", "compact_session.db")

	os.MkdirAll(filepath.Dir(agentDBPath), 0755)

	svc, err := agent.New("compact-session-agent").
		WithDBPath(agentDBPath).
		Build()
	if err != nil {
		log.Fatalf("Failed to create agent: %v", err)
	}
	defer svc.Close()

	// Start a chat to create a session
	fmt.Println("--- Starting Conversation ---")
	res1, err := svc.Chat(ctx, "Hello, my name is Alice. I'm a software engineer from Shanghai.")
	if err != nil {
		log.Fatalf("Chat failed: %v", err)
	}
	fmt.Printf("Agent: %s\n\n", res1.Text())

	res2, err := svc.Chat(ctx, "I like hiking and reading science fiction. I'm currently learning Go programming.")
	if err != nil {
		log.Fatalf("Chat failed: %v", err)
	}
	fmt.Printf("Agent: %s\n\n", res2.Text())

	sessionID := svc.CurrentSessionID()
	fmt.Printf("Current Session ID: %s\n\n", sessionID)

	// Compact the session
	fmt.Println("--- Compacting Session ---")
	summary, err := svc.CompactSession(ctx, sessionID)
	if err != nil {
		log.Fatalf("CompactSession failed: %v", err)
	}
	fmt.Printf("Summary: %s\n\n", summary)

	// Verify the summary is used in a new prompt
	fmt.Println("--- Verifying Summary Usage ---")

	res3, err := svc.Run(ctx, "Based on what we've discussed, what do you know about me?", agent.WithSessionID(sessionID))
	if err != nil {
		log.Fatalf("Chat failed: %v", err)
	}
	fmt.Printf("Agent: %s\n", res3.Text())

	fmt.Println("\nSession compaction example completed successfully!")
}
