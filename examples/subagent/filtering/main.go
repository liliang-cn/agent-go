// Package main demonstrates SubAgent tool filtering with allowlists and denylists.
//
// An Agent has four tools (read_data, write_data, delete_data, list_data) but each
// SubAgent is configured with different access levels:
//   - "ReadOnly" SubAgent: allowlist restricts to read_data + list_data only
//   - "NoDelete" SubAgent: denylist blocks delete_data, everything else allowed
//   - "WriteOnly" SubAgent: allowlist restricts to write_data only
//
// This demonstrates the two-level filtering:
//  1. collectFilteredTools removes disallowed tools from the LLM's tool list
//  2. executeTool re-checks at call time (belt-and-suspenders)
//
// Usage:
//
//	go run examples/subagent_filtering/main.go
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

// ── In-memory data store ─────────────────────────────────────────────────────

type DataStore struct {
	mu   sync.RWMutex
	data map[string]string
}

func NewDataStore() *DataStore {
	return &DataStore{
		data: map[string]string{
			"config.env":   "DATABASE_URL=postgres://localhost:5432/app\nREDIS_URL=redis://localhost:6379",
			"readme.md":    "# My Project\nA sample project for testing SubAgent tool filtering.",
			"notes.txt":    "Remember to review the Q3 metrics before the board meeting.",
			"secrets.yaml": "api_key: sk-REDACTED\ndb_password: REDACTED",
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

// ── Tool definitions ─────────────────────────────────────────────────────────

func makeToolSchema(props map[string]interface{}, required []string) map[string]interface{} {
	schema := map[string]interface{}{
		"type":       "object",
		"properties": props,
	}
	if len(required) > 0 {
		schema["required"] = required
	}
	return schema
}

// addAllTools registers all four CRUD tools on an Agent, backed by the given store.
func addAllTools(a *agent.Agent, store *DataStore) {
	keyProp := map[string]interface{}{
		"key": map[string]interface{}{"type": "string", "description": "File/key name"},
	}
	valueProp := map[string]interface{}{
		"key":   map[string]interface{}{"type": "string", "description": "File/key name"},
		"value": map[string]interface{}{"type": "string", "description": "Content to write"},
	}

	a.AddTool("read_data", "Read a file/key from the data store. Returns its content.",
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

	a.AddTool("write_data", "Write/overwrite a file/key in the data store.",
		makeToolSchema(valueProp, []string{"key", "value"}),
		func(_ context.Context, args map[string]interface{}) (interface{}, error) {
			key, _ := args["key"].(string)
			value, _ := args["value"].(string)
			if key == "" {
				return nil, fmt.Errorf("key is required")
			}
			store.Write(key, value)
			return map[string]interface{}{"status": "written", "key": key, "bytes": len(value)}, nil
		},
	)

	a.AddTool("delete_data", "Delete a file/key from the data store.",
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

	a.AddTool("list_data", "List all file/key names in the data store.",
		makeToolSchema(nil, nil),
		func(_ context.Context, _ map[string]interface{}) (interface{}, error) {
			return map[string]interface{}{"keys": store.List()}, nil
		},
	)
}

// ── SubAgent runner ──────────────────────────────────────────────────────────

type subAgentSpec struct {
	label string
	goal  string
	opts  []agent.SubAgentOption
}

func runSubAgent(ctx context.Context, svc *agent.Service, store *DataStore, spec subAgentSpec) {
	fmt.Printf("\n--- %s ---\n", spec.label)

	// Create a fresh Agent with all tools (filtering is done by SubAgent options)
	a := agent.NewAgent(spec.label)
	a.SetInstructions("You are a data management assistant. Use the available tools to accomplish the goal.")
	addAllTools(a, store)

	// Show which tools the Agent has before filtering
	fmt.Printf("Agent tools:  %s\n", strings.Join(a.GetToolNames(), ", "))

	sa := svc.CreateSubAgent(a, spec.goal, spec.opts...)

	fmt.Printf("Goal:         %s\n", spec.goal)
	fmt.Printf("SubAgent ID:  %s\n", sa.ID())

	result, err := sa.Run(ctx)

	fmt.Printf("State:        %s\n", sa.GetState())
	fmt.Printf("Turns:        %d\n", sa.GetCurrentTurn())
	fmt.Printf("Duration:     %s\n", sa.GetDuration().Round(time.Millisecond))

	if err != nil {
		fmt.Printf("Error:        %v\n", err)
	} else {
		resultJSON, _ := json.MarshalIndent(result, "", "  ")
		fmt.Printf("Result:\n%s\n", string(resultJSON))
	}
}

// ── Main ─────────────────────────────────────────────────────────────────────

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	fmt.Println("=== SubAgent Tool Filtering Example ===")
	fmt.Println("Demonstrating allowlist and denylist tool filtering.")

	svc, err := agent.New("FilterOrchestrator").
		WithDebug(os.Getenv("DEBUG") != "").
		Build()
	if err != nil {
		log.Fatalf("Failed to create service: %v", err)
	}
	defer svc.Close()

	store := NewDataStore()

	specs := []subAgentSpec{
		{
			label: "ReadOnly SubAgent",
			goal:  "List all available files, then read the contents of readme.md and notes.txt.",
			opts: []agent.SubAgentOption{
				agent.WithSubAgentMaxTurns(5),
				agent.WithSubAgentToolAllowlist([]string{"read_data", "list_data"}),
				agent.WithSubAgentProgressCallback(progressLogger("ReadOnly")),
			},
		},
		{
			label: "NoDelete SubAgent",
			goal:  "List all files. Then write a new file called 'report.txt' with content 'Q3 report draft'. Do NOT delete anything.",
			opts: []agent.SubAgentOption{
				agent.WithSubAgentMaxTurns(5),
				agent.WithSubAgentToolDenylist([]string{"delete_data"}),
				agent.WithSubAgentProgressCallback(progressLogger("NoDelete")),
			},
		},
		{
			label: "WriteOnly SubAgent",
			goal:  "Write a file called 'greeting.txt' with the content 'Hello from SubAgent!'.",
			opts: []agent.SubAgentOption{
				agent.SubAgentQuick(), // MaxTurns=3, Foreground
				agent.WithSubAgentToolAllowlist([]string{"write_data"}),
				agent.WithSubAgentProgressCallback(progressLogger("WriteOnly")),
			},
		},
	}

	for _, spec := range specs {
		runSubAgent(ctx, svc, store, spec)
	}

	// 3. Show final store state
	fmt.Println("\n--- Final Data Store State ---")
	for _, key := range store.List() {
		content, _ := store.Read(key)
		preview := content
		if len(preview) > 80 {
			preview = preview[:80] + "..."
		}
		fmt.Printf("  %s: %s\n", key, preview)
	}
}

func progressLogger(label string) agent.SubAgentProgressCallback {
	return func(p agent.SubAgentProgress) {
		fmt.Printf("  [%s] turn=%d/%d %s\n", label, p.CurrentTurn, p.MaxTurns, p.Message)
	}
}
