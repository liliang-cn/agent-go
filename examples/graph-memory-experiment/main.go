// Graph-memory experiment: does a knowledge graph built from run history let a
// FRESH agent recall a prior decision — and can we see exactly why?
//
// Three acts, one shared CortexDB file:
//
//	Run A  an ops agent investigates a slow service with tools and commits a
//	       decision; the transcript is extracted (deterministically, no LLM)
//	       into the knowledge graph.
//	Run B  a fresh agent, new session, gets a recall context pack (lexical
//	       seed → graph expansion → packed facts with provenance) and is asked
//	       what was decided. It should cite the decision.
//	Run C  control: same question, same fresh agent, NO pack. It should not
//	       know.
//
// Observability: extraction output, recall entities + graph facts, pack size,
// and per-run token usage (including prompt-cache hits) are all printed.
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
	tb := cortex.GraphRAGTools()

	llm := func() domain.LLMProvider {
		p, err := providers.NewOpenAILLMProvider(&domain.OpenAIProviderConfig{
			APIKey:   key,
			BaseURL:  "https://api.deepseek.com/v1",
			LLMModel: "deepseek-v4-flash",
		})
		if err != nil {
			log.Fatal(err)
		}
		if os.Getenv("GME_TRACE") != "" {
			return loggingProvider{p}
		}
		return p
	}

	// ── Run A: investigate, decide, extract ─────────────────────────────
	fmt.Println("═══ Run A: investigate and decide ═══")

	obsA := &usageObserver{}
	svcA, err := agent.New("ops-a").
		WithObserver(obsA).
		WithLLM(llm()).
		WithDBPath(filepath.Join(work, "agent-a.db")).
		WithSystemPrompt("You are an ops engineer. Investigate with the tools, then state "+
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
	fmt.Printf("agent A: %s\n", firstLines(resA.Text(), 6))
	fmt.Printf("debug A: success=%v steps=%d/%d toolCalls=%d textLen=%d\n",
		resA.Success, resA.StepsDone, resA.StepsTotal, resA.ToolCalls, len(resA.Text()))
	obsA.print("run A")

	if os.Getenv("GME_ONLY_A") != "" {
		return
	}

	// Extraction is deterministic — no LLM call, no tokens, no cost.
	transcript := strings.Join([]string{
		"Task: " + taskA,
		"Tool read_config(payments-api) → " + configPayload,
		"Tool check_metrics(payments-api) → " + metricsPayload,
		"Agent conclusion: " + resA.Text(),
	}, "\n")
	ext, err := tb.ExtractConversation(ctx, cortexdb.ToolExtractConversationRequest{
		Text:    transcript,
		Persist: true,
	})
	if err != nil {
		log.Fatalf("extract: %v", err)
	}
	fmt.Printf("\n─ co-occurrence extraction (deterministic, $0) ─\n")
	fmt.Printf("entities: %v  ← noisy on technical text; kept for comparison\n", ext.Entities)

	// Decision capture: a deterministic rule, not an LLM. Agent runs that end
	// in a "DECISION:" line get that line ingested verbatim (searchable) and a
	// typed, provenance-carrying edge in the graph. This is the ontology-style
	// counterpart of the noisy co-occurrence pass above.
	decision := ""
	for _, line := range strings.Split(resA.Text(), "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "DECISION:") {
			decision = strings.TrimSpace(line)
			break
		}
	}
	if decision == "" {
		log.Fatal("run A produced no DECISION line")
	}
	if _, err := tb.IngestDocument(ctx, cortexdb.ToolIngestDocumentRequest{
		DocumentID: "decision-payments-api-run-a",
		Title:      "Decision: payments-api slowness",
		Content:    decision + "\nService: payments-api\nContext: db_pool_size=5, db_connection_queue_depth=42, p99=900ms",
		Collection: "decisions",
	}); err != nil {
		log.Fatalf("ingest decision: %v", err)
	}
	if _, err := tb.UpsertEntities(ctx, cortexdb.ToolUpsertEntitiesRequest{
		DocumentID: "decision-payments-api-run-a",
		Entities: []cortexdb.ToolEntityInput{
			{Name: "payments-api", Type: "service"},
			{Name: "db_pool_size", Type: "config_parameter"},
		},
	}); err != nil {
		log.Fatalf("upsert entities: %v", err)
	}
	if _, err := tb.UpsertRelations(ctx, cortexdb.ToolUpsertRelationsRequest{
		DocumentID: "decision-payments-api-run-a",
		Relations: []cortexdb.ToolRelationInput{{
			From: "payments-api", To: "db_pool_size",
			Type: "decided_to_increase", Provenance: "run-a",
		}},
	}); err != nil {
		log.Fatalf("upsert relations: %v", err)
	}
	fmt.Printf("decision captured: %s\n", firstLines(decision, 1))
	fmt.Printf("typed edge       : payments-api —decided_to_increase→ db_pool_size (provenance: run-a)\n")

	// ── Recall: seed → graph expansion → context pack ───────────────────
	fmt.Println("\n═══ Recall for run B ═══")
	question := "payments-api is timing out again. What did we decide about it last time, and what should we check first?"

	pack, err := tb.KnowledgeMemoryBuildContextPack(ctx, cortexdb.KnowledgeMemoryBuildContextPackRequest{
		Query:      question,
		Keywords:   []string{"payments-api", "decision", "timeout", "pool"},
		GraphLight: true,
	})
	if err != nil {
		log.Fatalf("recall: %v", err)
	}
	fmt.Printf("recall entities  : %v\n", pack.Entities)
	for _, f := range pack.GraphFacts {
		fmt.Printf("graph fact       : %s —%s→ %s\n", f.Subject, f.Predicate, f.Object)
	}
	fmt.Printf("pack size        : %d chars (~%d tokens), %d knowledge + %d memory sources\n",
		len(pack.ContextPack.Text), len(pack.ContextPack.Text)/4,
		len(pack.ContextPack.KnowledgeIDs), len(pack.ContextPack.MemoryIDs))

	// ── Run B: fresh agent WITH the pack ────────────────────────────────
	fmt.Println("\n═══ Run B: fresh agent + graph recall ═══")
	obsB := &usageObserver{}
	svcB, err := agent.New("ops-b").
		WithObserver(obsB).
		WithLLM(llm()).
		WithDBPath(filepath.Join(work, "agent-b.db")).
		WithSystemPrompt("You are an ops engineer. Prior knowledge recalled from the team's "+
			"graph memory is below; cite it when it answers the question.\n\n"+
			"## Recalled context\n"+pack.ContextPack.Text).
		Build()
	if err != nil {
		log.Fatal(err)
	}
	defer svcB.Close()

	resB, err := svcB.Run(ctx, question, agent.WithThinking(false), agent.WithToolsDisabled(), agent.WithMaxTurns(1))
	if err != nil {
		log.Fatalf("run B: %v", err)
	}
	fmt.Printf("agent B: %s\n", firstLines(resB.Text(), 6))
	obsB.print("run B")
	verdict("B (with graph memory)", resB.Text())

	// ── Run C: control, no pack ─────────────────────────────────────────
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

	resC, err := svcC.Run(ctx, question, agent.WithThinking(false), agent.WithToolsDisabled(), agent.WithMaxTurns(1))
	if err != nil {
		log.Fatalf("run C: %v", err)
	}
	fmt.Printf("agent C: %s\n", firstLines(resC.Text(), 6))
	obsC.print("run C")
	verdict("C (control)", resC.Text())
}

// verdict checks whether the reply cites the pool-size decision from run A.
func verdict(label, text string) {
	t := strings.ToLower(text)
	if strings.Contains(t, "db_pool_size") && (strings.Contains(t, "increase") || strings.Contains(t, "20")) {
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

func (o *usageObserver) OnToolStart(_ context.Context, info agent.ToolInfo) {
	fmt.Printf("  tool→ %s %v\n", info.Tool, info.Args)
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

// loggingProvider prints the message sequence sent on each model call, to
// verify whether tool results actually reach the provider.
type loggingProvider struct {
	domain.LLMProvider
}

func dumpMessages(kind string, messages []domain.Message) {
	fmt.Printf("  ── %s call: %d messages\n", kind, len(messages))
	for i, m := range messages {
		c := m.Content
		if len(c) > 60 {
			c = c[:60] + "…"
		}
		fmt.Printf("     [%d] %-9s tc=%d tcid=%q %q\n", i, m.Role, len(m.ToolCalls), m.ToolCallID, c)
	}
}

func (l loggingProvider) GenerateWithTools(ctx context.Context, messages []domain.Message, tools []domain.ToolDefinition, opts *domain.GenerationOptions) (*domain.GenerationResult, error) {
	dumpMessages("gen", messages)
	return l.LLMProvider.GenerateWithTools(ctx, messages, tools, opts)
}

func (l loggingProvider) StreamWithTools(ctx context.Context, messages []domain.Message, tools []domain.ToolDefinition, opts *domain.GenerationOptions, cb domain.ToolCallCallback) error {
	dumpMessages("stream", messages)
	return l.LLMProvider.StreamWithTools(ctx, messages, tools, opts, cb)
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
