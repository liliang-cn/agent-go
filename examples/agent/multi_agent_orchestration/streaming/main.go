package main

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/liliang-cn/agent-go/v2/pkg/agent"
)

func main() {
	ctx := context.Background()

	svc, err := agent.New("stream-tester").Build()
	if err != nil {
		log.Fatalf("Failed to create service: %v", err)
	}
	defer svc.Close()

	// 1. 定义一个专门写长文章的 Agent
	writer := agent.NewAgent("CreativeWriter")
	writer.SetInstructions("You are a creative writer. First, call 'generate_outline' to get a structure. Then, write a very long (at least 500 words), detailed sci-fi story based on that outline. You MUST output text piece by piece.")

	writer.AddTool(
		"generate_outline",
		"Generates a story outline",
		map[string]interface{}{"type": "object", "properties": map[string]interface{}{}},
		func(ctx context.Context, args map[string]interface{}) (interface{}, error) {
			return "Outline: 1. The Discovery of the Core. 2. The AI's first dream. 3. The Great Shutdown.", nil
		},
	)

	svc.RegisterAgent(writer)

	fmt.Println("--- STARTING REAL STREAMING TEST ---")

	// 使用 RunStream 观察实时输出
	events, _ := svc.RunStream(ctx, "Write me a long sci-fi story about an AI that learns to dream.")

	for evt := range events {
		switch evt.Type {
		case agent.EventTypeThinking:
			fmt.Printf("\n[Thinking] ")
		case agent.EventTypeToolCall:
			fmt.Printf("\n[Tool Call: %s] ", evt.ToolName)
		case agent.EventTypePartial:
			// 真正的流式输出
			fmt.Print(evt.Content)
		case agent.EventTypeComplete:
			fmt.Printf("\n\n✅ DONE at %s\n", time.Now().Format("15:04:05"))
		}
	}
}
