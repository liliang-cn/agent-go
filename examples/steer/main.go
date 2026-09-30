// Package main shows a host putting a message into a run that is already
// going. The run is told to inspect a service with a slow tool; while the
// tool runs, a second party — here a goroutine standing in for another agent
// in a hive, or a person at the keyboard — has something to add. Steer queues
// it, and the loop appends it to the conversation at the next round boundary:
// after the tool's result, before the next model call.
//
// The other case worth seeing is a message that lands while the model is
// writing its final answer. The run does not end there: the answer becomes the
// assistant's turn, the message follows it, and the model gets one more round.
//
//	LLM_BASE_URL=https://api.deepseek.com/v1 LLM_API_KEY=$DEEPSEEK_API_KEY \
//	LLM_MODEL=deepseek-v4-flash go run ./examples/steer
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/liliang-cn/agent-go/v3/pkg/agent"
	"github.com/liliang-cn/agent-go/v3/pkg/domain"
	"github.com/liliang-cn/agent-go/v3/pkg/pool"
)

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	baseURL, key, model := os.Getenv("LLM_BASE_URL"), os.Getenv("LLM_API_KEY"), os.Getenv("LLM_MODEL")
	if baseURL == "" || model == "" {
		log.Fatal("set LLM_BASE_URL, LLM_API_KEY and LLM_MODEL")
	}
	home, err := os.MkdirTemp("", "agentgo-steer-*")
	if err != nil {
		log.Fatal(err)
	}
	defer os.RemoveAll(home)
	os.Setenv("AGENTGO_HOME", home)

	llm, err := pool.NewClient("main", baseURL, key, model)
	if err != nil {
		log.Fatal(err)
	}
	svc, err := agent.New("operator").WithLLM(llm).Build()
	if err != nil {
		log.Fatal(err)
	}
	defer svc.Close()

	toolStarted := make(chan struct{}, 1)
	svc.AddToolWithMetadata("inspect_service",
		"Inspect a service and report its state. Slow: it walks the whole host.",
		map[string]interface{}{
			"type":       "object",
			"properties": map[string]interface{}{"name": map[string]interface{}{"type": "string"}},
			"required":   []string{"name"},
		},
		func(ctx context.Context, args map[string]interface{}) (interface{}, error) {
			select {
			case toolStarted <- struct{}{}:
			default:
			}
			select {
			case <-time.After(3 * time.Second):
			case <-ctx.Done():
				return nil, ctx.Err()
			}
			return fmt.Sprintf("%v: running, 2 restarts in the last hour, memory at 91%%", args["name"]), nil
		},
		agent.ToolMetadata{ReadOnly: true, InterruptBehavior: agent.InterruptBehaviorCancel},
	)

	session := uuid.NewString()

	// The other party. It waits for the tool to be running, then steers.
	go func() {
		<-toolStarted
		ok := svc.Steer(session, domain.Message{
			Content: "[message from worker-2] The ledger service was just redeployed; restarts before 14:00 are expected. Do not recommend a rollback for those.",
		})
		fmt.Printf("\n>> steer queued while the tool ran: %v\n\n", ok)
	}()

	events, err := svc.RunStreamWithOptions(ctx,
		"Inspect the ledger service and tell me whether it needs attention.",
		agent.WithSessionID(session))
	if err != nil {
		log.Fatal(err)
	}
	var final string
	for ev := range events {
		switch ev.Type {
		case agent.EventTypeToolCall:
			fmt.Printf("tool>  %s\n", ev.ToolName)
		case agent.EventTypeToolResult:
			fmt.Printf("tool<  %s\n", ev.ToolName)
		case agent.EventTypeSteer:
			fmt.Printf("steer> landed in the conversation: %s\n", ev.Content)
		case agent.EventTypeSteerDropped:
			fmt.Printf("steer! dropped, start a turn with it instead: %s\n", ev.Content)
		case agent.EventTypeComplete:
			final = ev.Content
		}
	}
	fmt.Println("\n" + strings.TrimSpace(final))

	// Nothing running now: the host starts a turn itself.
	fmt.Printf("\n>> steer with no run in flight: %v\n", svc.Steer(session, domain.Message{Content: "hello?"}))
}
