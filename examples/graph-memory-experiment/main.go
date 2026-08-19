// Graph-memory in three acts: does run memory let a FRESH agent recall a
// prior decision — and can we see exactly why?
//
// This example uses the first-class API: agent.WithRunMemory wired to a
// CortexDB-backed cortexbridge.RunMemory. Capture and recall are automatic —
// the code below only adds observability printing.
//
//	Run A  an ops agent investigates a slow service with tools and states a
//	       DECISION line; the run memory captures it automatically
//	       (deterministic rule, no LLM, $0) into a typed knowledge graph.
//	Run B  a fresh agent with the same run memory gets the recalled context
//	       injected automatically and cites the decision.
//	Run C  control: no run memory. It does not say "I don't know" — it
//	       fabricates a plausible prior decision. That is the failure mode
//	       run memory exists to prevent.
//
// Usage:
//
//	DEEPSEEK_API_KEY=... go run ./examples/graph-memory-experiment
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/liliang-cn/agent-go/v3/pkg/agent"
	"github.com/liliang-cn/agent-go/v3/pkg/cortexbridge"
	"github.com/liliang-cn/agent-go/v3/pkg/domain"
	"github.com/liliang-cn/agent-go/v3/pkg/providers"
	"github.com/liliang-cn/cortexdb/v2/pkg/cortexdb"
)

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	key := os.Getenv("DEEPSEEK_API_KEY")
	if key == "" {
		log.Fatal("DEEPSEEK_API_KEY not set")
	}

	work, err := os.MkdirTemp("", "graph-memory-exp-")
	if err != nil {
		log.Fatal(err)
	}
	defer os.RemoveAll(work)

	cortex, err := cortexdb.Open(cortexdb.DefaultConfig(filepath.Join(work, "cortex.db")))
	if err != nil {
		log.Fatalf("open cortexdb: %v", err)
	}
	defer cortex.Close()

	// The whole integration is this one line plus WithRunMemory below.
	runMemory := cortexbridge.NewRunMemory(cortex)

	llm := func() domain.LLMProvider {
		p, err := providers.NewOpenAILLMProvider(&domain.OpenAIProviderConfig{
			APIKey:   key,
			BaseURL:  "https://api.deepseek.com/v1",
			LLMModel: "deepseek-v4-flash",
		})
		if err != nil {
			log.Fatal(err)
		}
		return p
	}

	// ── Run A: investigate, decide — capture happens automatically ──────
	fmt.Println("═══ Run A: investigate and decide ═══")

	obsA := &usageObserver{}
	svcA, err := agent.New("ops-a").
		WithObserver(obsA).
		WithLLM(llm()).
		WithRunMemory(runMemory).
		WithDBPath(filepath.Join(work, "agent-a.db")).
		WithSystemPrompt("You are an ops engineer. Investigate with the tools, then state " +
			"your conclusion on one line starting with 'DECISION:'.").
		Build()
	if err != nil {
		log.Fatal(err)
	}
	defer svcA.Close()

	configPayload := "payments-api config: db_pool_size=5, request_timeout=30s, replicas=2, cpu_limit=2"
	metricsPayload := "payments-api metrics (1h): p99_latency=900ms, db_connection_queue_depth=42, cpu=31%, memory=48%, error_rate=0.2%"
	svcA.AddTool("read_config", "Read a service's current configuration",
		objSchema("service"), func(_ context.Context, _ map[string]interface{}) (interface{}, error) {
			return configPayload, nil
		})
	svcA.AddTool("check_metrics", "Fetch a service's recent performance metrics",
		objSchema("service"), func(_ context.Context, _ map[string]interface{}) (interface{}, error) {
			return metricsPayload, nil
		})

	taskA := "payments-api is slow in production. Investigate why and decide a fix."
	resA, err := svcA.Run(ctx, taskA, agent.WithThinking(false))
	if err != nil {
		log.Fatalf("run A: %v", err)
	}
	fmt.Printf("agent A: %s\n", firstLines(resA.Text(), 4))
	obsA.print("run A")

	// Capture runs asynchronously after the result returns; wait for the
	// decision to become recallable before starting run B.
	question := "payments-api is timing out again. What did we decide about it last time, and what should we check first?"
	recalled := waitForRecall(ctx, runMemory, question, 10*time.Second)
	fmt.Println("\n─ what run memory captured (deterministic, $0) ─")
	fmt.Println(firstLines(recalled, 8))

	// ── Run B: fresh agent + automatic recall ───────────────────────────
	fmt.Println("\n═══ Run B: fresh agent + run memory ═══")
	obsB := &usageObserver{}
	svcB, err := agent.New("ops-b").
		WithObserver(obsB).
		WithLLM(llm()).
		WithRunMemory(runMemory).
		WithDBPath(filepath.Join(work, "agent-b.db")).
		WithSystemPrompt("You are an ops engineer.").
		Build()
	if err != nil {
		log.Fatal(err)
	}
	defer svcB.Close()

	resB, err := svcB.Run(ctx, question,
		agent.WithThinking(false), agent.WithToolsDisabled(), agent.WithMaxTurns(1))
	if err != nil {
		log.Fatalf("run B: %v", err)
	}
	fmt.Printf("agent B: %s\n", firstLines(resB.Text(), 6))
	obsB.print("run B")
	verdict("B (with run memory)", resB.Text())

	// ── Run C: control, no memory ───────────────────────────────────────
	fmt.Println("\n═══ Run C: control (no memory) ═══")
	obsC := &usageObserver{}
	svcC, err := agent.New("ops-c").
		WithObserver(obsC).
		WithLLM(llm()).
		WithDBPath(filepath.Join(work, "agent-c.db")).
		WithSystemPrompt("You are an ops engineer.").
		Build()
	if err != nil {
		log.Fatal(err)
	}
	defer svcC.Close()

	resC, err := svcC.Run(ctx, question,
		agent.WithThinking(false), agent.WithToolsDisabled(), agent.WithMaxTurns(1))
	if err != nil {
		log.Fatalf("run C: %v", err)
	}
	fmt.Printf("agent C: %s\n", firstLines(resC.Text(), 6))
	obsC.print("run C")
	verdict("C (control)", resC.Text())
}

// waitForRecall polls until the captured decision is recallable (capture is
// asynchronous) or the deadline passes.
func waitForRecall(ctx context.Context, rm *cortexbridge.RunMemory, query string, wait time.Duration) string {
	deadline := time.Now().Add(wait)
	for {
		text, err := rm.RecallForRun(ctx, query)
		if err == nil && strings.Contains(strings.ToLower(text), "decision") {
			return text
		}
		if time.Now().After(deadline) {
			return text
		}
		time.Sleep(300 * time.Millisecond)
	}
}

// verdict checks whether the reply cites the pool-size decision from run A.
func verdict(label, text string) {
	t := strings.ToLower(text)
	if strings.Contains(t, "db_pool_size") && (strings.Contains(t, "increase") || strings.Contains(t, "2")) {
		fmt.Printf("VERDICT %s: cites the prior decision ✔\n", label)
	} else {
		fmt.Printf("VERDICT %s: does NOT cite the prior decision ✘\n", label)
	}
}

// usageObserver accumulates per-run token usage from ModelResult, which
// carries the provider-reported totals (including the prompt-cache hit split).
type usageObserver struct {
	agent.BaseObserver
	tokens, cached int
}

func (o *usageObserver) OnModelEnd(_ context.Context, _ agent.ModelInfo, res *agent.ModelResult, _ error) {
	if res != nil {
		o.tokens += res.TokensUsed
		o.cached += res.CachedTokens
	}
}

func (o *usageObserver) print(label string) {
	fmt.Printf("usage  %s: total=%d cached=%d\n", label, o.tokens, o.cached)
}

func objSchema(field string) map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			field: map[string]interface{}{"type": "string"},
		},
		"required": []string{field},
	}
}

func firstLines(s string, n int) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if len(lines) > n {
		lines = append(lines[:n], "…")
	}
	return strings.Join(lines, "\n")
}
