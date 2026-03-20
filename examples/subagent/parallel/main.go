// Package main demonstrates running multiple SubAgents in parallel to decompose
// a complex task into independent sub-tasks.
//
// Scenario: An "orchestrator" splits a project health check into three parallel
// SubAgents, each responsible for a different aspect:
//   - CodeQuality SubAgent: checks code metrics (complexity, coverage, lint)
//   - Security SubAgent: checks dependency vulnerabilities
//   - Performance SubAgent: checks latency and throughput benchmarks
//
// Each SubAgent runs via RunAsync() in background mode, emitting events on a
// channel. The main goroutine fans in all event channels and aggregates results.
//
// Usage:
//
//	go run examples/subagent_parallel/main.go
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"math/rand"
	"os"
	"sync"
	"time"

	"github.com/liliang-cn/agent-go/v2/pkg/agent"
)

// ── Mock project metrics ─────────────────────────────────────────────────────

type CodeMetrics struct {
	Complexity int     `json:"complexity"`
	Coverage   float64 `json:"coverage_pct"`
	LintErrors int     `json:"lint_errors"`
	LintWarns  int     `json:"lint_warnings"`
	TotalFiles int     `json:"total_files"`
	TotalLines int     `json:"total_lines"`
}

type VulnReport struct {
	Critical int      `json:"critical"`
	High     int      `json:"high"`
	Medium   int      `json:"medium"`
	Low      int      `json:"low"`
	Details  []string `json:"details"`
}

type PerfMetrics struct {
	P50Latency  float64 `json:"p50_latency_ms"`
	P99Latency  float64 `json:"p99_latency_ms"`
	Throughput  float64 `json:"throughput_rps"`
	ErrorRate   float64 `json:"error_rate_pct"`
	MemoryUsage float64 `json:"memory_usage_mb"`
}

// ── Tool handlers ────────────────────────────────────────────────────────────

func handleCheckCodeMetrics(_ context.Context, _ map[string]interface{}) (interface{}, error) {
	time.Sleep(time.Duration(100+rand.Intn(200)) * time.Millisecond) // simulate latency
	return CodeMetrics{
		Complexity: 14,
		Coverage:   78.5,
		LintErrors: 3,
		LintWarns:  12,
		TotalFiles: 142,
		TotalLines: 18500,
	}, nil
}

func handleCheckVulnerabilities(_ context.Context, _ map[string]interface{}) (interface{}, error) {
	time.Sleep(time.Duration(150+rand.Intn(250)) * time.Millisecond)
	return VulnReport{
		Critical: 0,
		High:     1,
		Medium:   3,
		Low:      7,
		Details: []string{
			"HIGH: CVE-2025-1234 in dependency X v1.2.3 - upgrade to v1.2.4",
			"MEDIUM: CVE-2025-2345 in dependency Y v2.0.0 - no fix available",
			"MEDIUM: Outdated TLS config in config/tls.go",
			"MEDIUM: SQL injection risk in legacy handler (pkg/legacy/query.go)",
		},
	}, nil
}

func handleCheckPerformance(_ context.Context, _ map[string]interface{}) (interface{}, error) {
	time.Sleep(time.Duration(100+rand.Intn(150)) * time.Millisecond)
	return PerfMetrics{
		P50Latency:  12.3,
		P99Latency:  145.8,
		Throughput:  2450.0,
		ErrorRate:   0.12,
		MemoryUsage: 384.5,
	}, nil
}

// ── Agent factory ────────────────────────────────────────────────────────────

type agentSpec struct {
	name         string
	instructions string
	goal         string
	toolName     string
	toolDesc     string
	handler      func(context.Context, map[string]interface{}) (interface{}, error)
}

func createSpecialistAgent(spec agentSpec) *agent.Agent {
	a := agent.NewAgent(spec.name)
	a.SetInstructions(spec.instructions)

	a.AddTool(spec.toolName, spec.toolDesc,
		map[string]interface{}{
			"type":       "object",
			"properties": map[string]interface{}{},
		},
		spec.handler,
	)
	return a
}

// ── Main ─────────────────────────────────────────────────────────────────────

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	fmt.Println("=== Parallel SubAgents Example ===")
	fmt.Println("Running 3 specialist SubAgents concurrently for project health check.")

	svc, err := agent.New("HealthCheckOrchestrator").
		WithDebug(os.Getenv("DEBUG") != "").
		Build()
	if err != nil {
		log.Fatalf("Failed to create service: %v", err)
	}
	defer svc.Close()

	specs := []agentSpec{
		{
			name:         "CodeQualityAnalyst",
			instructions: "You are a code quality analyst. Call check_code_metrics to get the project's code quality metrics, then provide a brief assessment.",
			goal:         "Analyze the project's code quality. Call the check_code_metrics tool and report the results with a brief assessment of the coverage and lint status.",
			toolName:     "check_code_metrics",
			toolDesc:     "Run static analysis and return code quality metrics: complexity, coverage, lint errors/warnings, file/line counts.",
			handler:      handleCheckCodeMetrics,
		},
		{
			name:         "SecurityAuditor",
			instructions: "You are a security auditor. Call check_vulnerabilities to scan dependencies, then provide a risk assessment.",
			goal:         "Perform a security audit. Call check_vulnerabilities and report the findings with severity breakdown and recommended actions.",
			toolName:     "check_vulnerabilities",
			toolDesc:     "Scan project dependencies and code for known vulnerabilities. Returns severity counts and details.",
			handler:      handleCheckVulnerabilities,
		},
		{
			name:         "PerformanceEngineer",
			instructions: "You are a performance engineer. Call check_performance to get runtime metrics, then assess the system's health.",
			goal:         "Evaluate system performance. Call check_performance and report latency, throughput, error rate, and memory usage with recommendations.",
			toolName:     "check_performance",
			toolDesc:     "Run performance benchmarks and return latency percentiles, throughput, error rate, and memory usage.",
			handler:      handleCheckPerformance,
		},
	}

	// 3. Launch all SubAgents concurrently using RunAsync
	type subAgentResult struct {
		name   string
		result interface{}
		err    error
		sa     *agent.SubAgent
	}

	var wg sync.WaitGroup
	resultsCh := make(chan subAgentResult, len(specs))

	for _, spec := range specs {
		a := createSpecialistAgent(spec)
		sa := svc.CreateSubAgent(a, spec.goal,
			agent.WithSubAgentMaxTurns(5),
			agent.WithSubAgentContext(map[string]interface{}{
				"project":     "agentgo",
				"environment": "staging",
			}),
			agent.WithSubAgentProgressCallback(func(name string) agent.SubAgentProgressCallback {
				return func(p agent.SubAgentProgress) {
					fmt.Printf("  [%s] turn=%d/%d %s\n", name, p.CurrentTurn, p.MaxTurns, p.Message)
				}
			}(spec.name)),
		)

		wg.Add(1)
		go func(name string, sa *agent.SubAgent) {
			defer wg.Done()
			fmt.Printf("[launch] %s (id=%s)\n", name, sa.ID())

			// Use Run() in goroutines for simplest parallel execution
			result, err := sa.Run(ctx)
			resultsCh <- subAgentResult{name: name, result: result, err: err, sa: sa}
		}(spec.name, sa)
	}

	// Wait for all to finish
	wg.Wait()
	close(resultsCh)

	// 4. Aggregate and display results
	fmt.Println("\n=== Health Check Results ===")
	for res := range resultsCh {
		fmt.Printf("\n--- %s ---\n", res.name)
		fmt.Printf("State:    %s\n", res.sa.GetState())
		fmt.Printf("Turns:    %d\n", res.sa.GetCurrentTurn())
		fmt.Printf("Duration: %s\n", res.sa.GetDuration().Round(time.Millisecond))

		if res.err != nil {
			fmt.Printf("Error:    %v\n", res.err)
		} else {
			resultJSON, _ := json.MarshalIndent(res.result, "", "  ")
			output := string(resultJSON)
			if len(output) > 500 {
				output = output[:500] + "..."
			}
			fmt.Printf("Result:\n%s\n", output)
		}
	}
}
