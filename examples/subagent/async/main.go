// Package main demonstrates async SubAgent execution with real-time event streaming,
// timeout handling, and the preset convenience functions.
//
// This example creates a "data pipeline" SubAgent that processes items through
// multiple stages (validate, transform, load). It showcases:
//   - RunAsync() for non-blocking execution with event channel
//   - SubAgentShortTimeout() preset for 30-second timeout
//   - SubAgentWithRetry() for automatic retry on failure
//   - Real-time event consumption from the returned channel
//   - Context inheritance via WithSubAgentContext
//
// A second SubAgent demonstrates timeout behaviour — it's given a goal that
// requires many turns but has a very short timeout, so it times out gracefully.
//
// Usage:
//
//	go run examples/subagent_async/main.go
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"math/rand"
	"os"
	"time"

	"github.com/liliang-cn/agent-go/v2/pkg/agent"
)

// ── Mock data pipeline ───────────────────────────────────────────────────────

type PipelineRecord struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Email  string `json:"email"`
	Status string `json:"status"`
	Score  int    `json:"score"`
}

var rawRecords = []PipelineRecord{
	{ID: "rec_001", Name: "alice", Email: "alice@example.com", Score: 85},
	{ID: "rec_002", Name: "bob", Email: "invalid-email", Score: 42},
	{ID: "rec_003", Name: "carol", Email: "carol@example.com", Score: 91},
	{ID: "rec_004", Name: "", Email: "anon@example.com", Score: 0},
	{ID: "rec_005", Name: "dave", Email: "dave@example.com", Score: 73},
}

// ── Tool handlers ────────────────────────────────────────────────────────────

func handleValidateRecords(_ context.Context, _ map[string]interface{}) (interface{}, error) {
	time.Sleep(time.Duration(50+rand.Intn(100)) * time.Millisecond)

	var valid, invalid []PipelineRecord
	for _, r := range rawRecords {
		r.Status = "valid"
		if r.Name == "" || r.Email == "" || r.Score <= 0 {
			r.Status = "invalid"
			invalid = append(invalid, r)
		} else if len(r.Email) < 5 || r.Email[0] == '@' {
			r.Status = "invalid"
			invalid = append(invalid, r)
		} else {
			valid = append(valid, r)
		}
	}

	return map[string]interface{}{
		"total":         len(rawRecords),
		"valid":         valid,
		"invalid":       invalid,
		"valid_count":   len(valid),
		"invalid_count": len(invalid),
	}, nil
}

func handleTransformRecords(_ context.Context, args map[string]interface{}) (interface{}, error) {
	time.Sleep(time.Duration(50+rand.Intn(100)) * time.Millisecond)

	// Simulate transformation: uppercase names, add timestamp
	recordsRaw, _ := json.Marshal(args["records"])
	var records []PipelineRecord
	if err := json.Unmarshal(recordsRaw, &records); err != nil {
		// If records aren't passed, transform the valid ones from rawRecords
		for _, r := range rawRecords {
			if r.Name != "" && r.Score > 0 {
				records = append(records, r)
			}
		}
	}

	type TransformedRecord struct {
		PipelineRecord
		Grade       string `json:"grade"`
		ProcessedAt string `json:"processed_at"`
	}

	var transformed []TransformedRecord
	now := time.Now().Format(time.RFC3339)
	for _, r := range records {
		grade := "C"
		if r.Score >= 90 {
			grade = "A"
		} else if r.Score >= 70 {
			grade = "B"
		}
		transformed = append(transformed, TransformedRecord{
			PipelineRecord: r,
			Grade:          grade,
			ProcessedAt:    now,
		})
	}

	return map[string]interface{}{
		"transformed": transformed,
		"count":       len(transformed),
	}, nil
}

func handleLoadRecords(_ context.Context, args map[string]interface{}) (interface{}, error) {
	time.Sleep(time.Duration(50+rand.Intn(100)) * time.Millisecond)

	// Simulate loading into a database
	count := 0
	if c, ok := args["count"].(float64); ok {
		count = int(c)
	} else {
		count = 3 // default
	}

	return map[string]interface{}{
		"loaded":      count,
		"destination": "warehouse.records",
		"status":      "success",
		"batch_id":    fmt.Sprintf("batch_%d", time.Now().UnixMilli()),
	}, nil
}

// Slow tool for timeout demo
func handleSlowAnalysis(_ context.Context, _ map[string]interface{}) (interface{}, error) {
	time.Sleep(3 * time.Second) // intentionally slow
	return map[string]interface{}{"status": "done"}, nil
}

// ── Helpers ──────────────────────────────────────────────────────────────────

func addPipelineTools(a *agent.Agent) {
	emptySchema := map[string]interface{}{
		"type":       "object",
		"properties": map[string]interface{}{},
	}

	a.AddTool("validate_records",
		"Validate raw pipeline records. Returns valid and invalid record lists with counts.",
		emptySchema, handleValidateRecords)

	a.AddTool("transform_records",
		"Transform validated records: assign grades based on score, add processing timestamp.",
		map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"records": map[string]interface{}{
					"type":        "array",
					"description": "Array of validated records to transform (optional, uses defaults if omitted)",
				},
			},
		}, handleTransformRecords)

	a.AddTool("load_records",
		"Load transformed records into the data warehouse. Returns batch info.",
		map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"count": map[string]interface{}{
					"type":        "number",
					"description": "Number of records to load",
				},
			},
		}, handleLoadRecords)
}

// ── Main ─────────────────────────────────────────────────────────────────────

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	fmt.Println("=== Async SubAgent with Events & Timeout Example ===")

	svc, err := agent.New("PipelineOrchestrator").
		WithDebug(os.Getenv("DEBUG") != "").
		Build()
	if err != nil {
		log.Fatalf("Failed to create service: %v", err)
	}
	defer svc.Close()

	fmt.Println("--- Demo 1: Async Data Pipeline ---")

	pipelineAgent := agent.NewAgent("DataPipelineRunner")
	pipelineAgent.SetInstructions(
		"You are a data pipeline operator. Execute the pipeline in order: " +
			"1) validate_records to check data quality, " +
			"2) transform_records to grade and timestamp valid records, " +
			"3) load_records to store them. Report results at each stage.")
	addPipelineTools(pipelineAgent)

	pipelineSA := svc.CreateSubAgent(
		pipelineAgent,
		"Run the full data pipeline: validate, transform, then load records. Report the results of each stage.",
		agent.WithSubAgentMaxTurns(8),
		agent.WithSubAgentContext(map[string]interface{}{
			"pipeline_name": "daily_user_import",
			"environment":   "staging",
		}),
		agent.SubAgentWithRetry(1), // retry once on failure
		agent.WithSubAgentProgressCallback(func(p agent.SubAgentProgress) {
			fmt.Printf("  [pipeline] turn=%d/%d state=%s %s (elapsed=%s)\n",
				p.CurrentTurn, p.MaxTurns, p.State, p.Message,
				p.ElapsedTime.Round(time.Millisecond))
		}),
	)

	fmt.Printf("SubAgent: %s (id=%s)\n", pipelineSA.Name(), pipelineSA.ID())

	// RunAsync returns an event channel
	events := pipelineSA.RunAsync(ctx)

	fmt.Println("Consuming events asynchronously:")
	for evt := range events {
		content := evt.Content
		if len(content) > 120 {
			content = content[:120] + "..."
		}
		fmt.Printf("  [event] type=%-15s agent=%-20s content=%s\n",
			evt.Type, evt.AgentName, content)
	}

	fmt.Printf("\nPipeline Final State: %s\n", pipelineSA.GetState())
	fmt.Printf("Pipeline Duration:    %s\n", pipelineSA.GetDuration().Round(time.Millisecond))
	fmt.Printf("Pipeline Turns:       %d\n", pipelineSA.GetCurrentTurn())

	// ── Demo 2: Timeout demonstration ────────────────────────────────────

	fmt.Println("\n--- Demo 2: Timeout Handling ---")

	slowAgent := agent.NewAgent("SlowAnalyzer")
	slowAgent.SetInstructions("Call slow_analysis to analyze the data. This may take a while.")
	slowAgent.AddTool("slow_analysis",
		"Run a slow analysis that takes several seconds.",
		map[string]interface{}{
			"type":       "object",
			"properties": map[string]interface{}{},
		},
		handleSlowAnalysis,
	)

	timeoutSA := svc.CreateSubAgent(
		slowAgent,
		"Run the slow analysis and report results.",
		agent.WithSubAgentTimeout(5*time.Second), // very short timeout
		agent.WithSubAgentMaxTurns(3),
		agent.WithSubAgentProgressCallback(func(p agent.SubAgentProgress) {
			fmt.Printf("  [timeout-demo] turn=%d/%d state=%s %s\n",
				p.CurrentTurn, p.MaxTurns, p.State, p.Message)
		}),
	)

	fmt.Printf("SubAgent: %s (id=%s) timeout=5s\n", timeoutSA.Name(), timeoutSA.ID())

	result, err := timeoutSA.Run(ctx)
	fmt.Printf("State:    %s\n", timeoutSA.GetState())
	fmt.Printf("Duration: %s\n", timeoutSA.GetDuration().Round(time.Millisecond))
	if err != nil {
		fmt.Printf("Error:    %v (this is expected for timeout demo)\n", err)
	} else {
		resultJSON, _ := json.MarshalIndent(result, "", "  ")
		fmt.Printf("Result:   %s\n", string(resultJSON))
	}

	fmt.Println("\n=== Done ===")
}
