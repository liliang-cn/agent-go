package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/liliang-cn/agent-go/v2/pkg/agent"
)

type DataStore struct {
	mu   sync.RWMutex
	data map[string]string
}

func NewDataStore() *DataStore {
	return &DataStore{
		data: map[string]string{
			"readme": "# Project\nThis is a sample project for testing agent delegation.",
			"config": "debug=false\nport=8080\nhost=localhost",
			"notes":  "TODO: Add more tests, improve documentation",
		},
	}
}

func (ds *DataStore) Read(key string) (string, bool) {
	ds.mu.RLock()
	defer ds.mu.RUnlock()
	v, ok := ds.data[key]
	return v, ok
}

func (ds *DataStore) Write(key, value string) {
	ds.mu.Lock()
	defer ds.mu.Unlock()
	ds.data[key] = value
}

func (ds *DataStore) Delete(key string) bool {
	ds.mu.Lock()
	defer ds.mu.Unlock()
	if _, ok := ds.data[key]; !ok {
		return false
	}
	delete(ds.data, key)
	return true
}

func (ds *DataStore) List() []string {
	ds.mu.RLock()
	defer ds.mu.RUnlock()
	keys := make([]string, 0, len(ds.data))
	for k := range ds.data {
		keys = append(keys, k)
	}
	return keys
}

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	fmt.Println("=== Agent -> SubAgent Delegation Example ===")
	fmt.Println("Demonstrating how an Agent can delegate tasks to SubAgents.")

	svc, err := agent.New("DelegationOrchestrator").
		WithSystemPrompt(`You are a task orchestrator. You can delegate work to sub-agents using the delegate_to_subagent tool.

When delegating:
1. Provide a clear, specific goal for the sub-agent
2. Use tools_denylist to restrict dangerous operations when appropriate
3. After the sub-agent completes, you can continue with additional work

You also have direct access to data tools (read_data, write_data, list_data, delete_data) for simple operations.`).
		WithDebug(os.Getenv("DEBUG") != "").
		Build()
	if err != nil {
		log.Fatalf("Failed to create service: %v", err)
	}
	defer svc.Close()

	store := NewDataStore()

	keyProp := map[string]interface{}{
		"key": map[string]interface{}{"type": "string", "description": "Data key"},
	}
	valueProp := map[string]interface{}{
		"key":   map[string]interface{}{"type": "string", "description": "Data key"},
		"value": map[string]interface{}{"type": "string", "description": "Content to write"},
	}

	makeToolSchema := func(props map[string]interface{}, required []string) map[string]interface{} {
		schema := map[string]interface{}{
			"type":       "object",
			"properties": props,
		}
		if len(required) > 0 {
			schema["required"] = required
		}
		return schema
	}

	svc.AddTool("read_data", "Read data from the store by key",
		makeToolSchema(keyProp, []string{"key"}),
		func(_ context.Context, args map[string]interface{}) (interface{}, error) {
			key, _ := args["key"].(string)
			if key == "" {
				return nil, fmt.Errorf("key is required")
			}
			content, ok := store.Read(key)
			if !ok {
				return map[string]interface{}{"found": false, "key": key}, nil
			}
			return map[string]interface{}{"found": true, "key": key, "content": content}, nil
		},
	)

	svc.AddTool("write_data", "Write data to the store",
		makeToolSchema(valueProp, []string{"key", "value"}),
		func(_ context.Context, args map[string]interface{}) (interface{}, error) {
			key, _ := args["key"].(string)
			value, _ := args["value"].(string)
			if key == "" {
				return nil, fmt.Errorf("key is required")
			}
			store.Write(key, value)
			return map[string]interface{}{"status": "written", "key": key}, nil
		},
	)

	svc.AddTool("delete_data", "Delete data from the store",
		makeToolSchema(keyProp, []string{"key"}),
		func(_ context.Context, args map[string]interface{}) (interface{}, error) {
			key, _ := args["key"].(string)
			if key == "" {
				return nil, fmt.Errorf("key is required")
			}
			ok := store.Delete(key)
			return map[string]interface{}{"deleted": ok, "key": key}, nil
		},
	)

	svc.AddTool("list_data", "List all keys in the store",
		makeToolSchema(nil, nil),
		func(_ context.Context, _ map[string]interface{}) (interface{}, error) {
			return map[string]interface{}{"keys": store.List()}, nil
		},
	)

	fmt.Println("--- Scenario: Multi-step Task with Delegation ---")
	fmt.Println("Task: Research data via SubAgent (read-only), then write a summary")
	fmt.Println()

	goal := `You have access to a data store with keys: readme, config, notes.

Your task:
1. Use delegate_to_subagent to delegate a READ-ONLY research task:
   - The sub-agent should list all keys and read their contents
   - The sub-agent should return a summary of all data found
   - IMPORTANT: The sub-agent must NOT be able to write or delete anything (use tools_denylist)
2. After the sub-agent returns with the summary, write a new key called "summary" with a brief combined summary of all the data.

Use delegate_to_subagent with tools_denylist=["write_data", "delete_data"] to ensure the sub-agent is read-only.`

	svc.SetProgressCallback(func(e agent.ProgressEvent) {
		switch e.Type {
		case "tool_call":
			if e.Tool == "delegate_to_subagent" {
				fmt.Printf("  [MainAgent] Delegating task to SubAgent...\n")
			} else {
				fmt.Printf("  [MainAgent] Tool: %s\n", e.Tool)
			}
		case "tool_result":
			if e.Tool == "delegate_to_subagent" {
				fmt.Printf("  [MainAgent] SubAgent completed\n")
			}
		case "thinking":
			if strings.Contains(e.Message, "Thinking") {
				fmt.Printf("  [MainAgent] Thinking...\n")
			}
		}
	})

	fmt.Println("Starting execution...")

	result, err := svc.Run(ctx, goal)

	fmt.Println("\n=== Execution Complete ===")
	if err != nil {
		fmt.Printf("Error: %v\n", err)
	} else {
		resultJSON, _ := json.MarshalIndent(result, "", "  ")
		fmt.Printf("Result:\n%s\n", string(resultJSON))
	}

	fmt.Println("\n=== Data Store Final State ===")
	for _, key := range store.List() {
		content, _ := store.Read(key)
		preview := content
		if len(preview) > 100 {
			preview = preview[:100] + "..."
		}
		fmt.Printf("  %s: %s\n", key, preview)
	}
}
