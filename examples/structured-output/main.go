// Package main demonstrates AgentGo's structured-output (JSON) support — making
// the agent's final answer a guaranteed-valid JSON document.
//
// Two ways to constrain the shape:
//
//   - agent.RunTyped[T](ctx, svc, goal)   → derive a JSON Schema from a Go
//     struct (reflection) and get a parsed T back. The typed counterpart to
//     Service.Run; no manual json.Unmarshal.
//   - agent.WithStructuredOutput(spec)     → pass a hand-written JSON Schema as
//     a RunOption when you want full control (or Strict mode).
//
// The runtime enforces the shape two ways:
//   - Tier B (native): every LLM call carries the equivalent response_format,
//     so OpenAI-compatible providers return valid JSON in one shot (with an
//     automatic fallback when a provider rejects the field).
//   - Tier A (post-validation): a deterministic lint validates the final text
//     against the schema and re-prompts on mismatch — works on every provider.
//
// Needs an LLM configured in AGENTGO_HOME / agentgo.toml (same as `agentgo chat`).
//
// Usage:
//
//	go run ./examples/structured-output
//	DEBUG=1 go run ./examples/structured-output
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/liliang-cn/agent-go/v2/pkg/agent"
)

// StockBrief is the shape we want the model to return. The struct tags drive
// the derived schema: `json` names each field, `desc` gives the model
// field-level intent (a bare schema can't convey meaning), and `omitempty`
// marks a field optional (everything else is required).
type StockBrief struct {
	Ticker    string   `json:"ticker"              desc:"uppercase stock symbol, e.g. NVDA"`
	Company   string   `json:"company"             desc:"full company name"`
	Sentiment string   `json:"sentiment"           desc:"exactly one of: bullish, neutral, bearish"`
	KeyPoints []string `json:"key_points"          desc:"3-4 short, factual takeaways"`
	RiskFlag  bool     `json:"risk_flag,omitempty" desc:"true if there is a notable near-term risk"`
}

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	// Structured output is about the final answer's shape, not multi-step
	// tools — so disable PTC and tell the model to answer directly from its
	// own knowledge instead of delegating or calling tools.
	svc, err := agent.New("structured-output-demo").
		WithPTC(false).
		WithSystemPrompt("You are a concise equity analyst. Answer ONLY from your own knowledge. " +
			"Do NOT call any tools, do NOT delegate to sub-agents, do NOT search the web. " +
			"Respond immediately with the required JSON. Base answers on widely-known facts; " +
			"do not invent precise prices or figures.").
		WithDebug(os.Getenv("DEBUG") != "").
		Build()
	if err != nil {
		log.Fatalf("failed to build agent service: %v", err)
	}
	defer svc.Close()

	// 1) RunTyped — schema derived from StockBrief, a parsed value returned.
	fmt.Println("=== RunTyped[StockBrief] ===")
	brief, err := agent.RunTyped[StockBrief](ctx, svc,
		"Give a brief on NVIDIA (NVDA): overall sentiment and 3-4 key points.")
	if err != nil {
		log.Fatalf("RunTyped failed: %v", err)
	}
	fmt.Printf("Ticker:    %s (%s)\n", brief.Ticker, brief.Company)
	fmt.Printf("Sentiment: %s   RiskFlag: %v\n", brief.Sentiment, brief.RiskFlag)
	for i, p := range brief.KeyPoints {
		fmt.Printf("  %d. %s\n", i+1, p)
	}
	if pretty, mErr := json.MarshalIndent(brief, "", "  "); mErr == nil {
		fmt.Printf("\nRaw JSON:\n%s\n", pretty)
	}

	// 2) RunTyped with an overriding option — pass a hand-written
	// StructuredOutputSpec to control the schema name / strict mode /
	// field-intent Description while still getting a typed result back.
	fmt.Println("\n=== RunTyped[StockBrief] with Strict + Description ===")
	strict, err := agent.RunTyped[StockBrief](ctx, svc,
		"Brief on Apple (AAPL): sentiment and 3 key points.",
		agent.WithStructuredOutput(&agent.StructuredOutputSpec{
			Name:        "stock_brief",
			Schema:      schemaFor[StockBrief](),
			Strict:      true, // block instead of best-effort if it can't comply
			Description: "An equity brief; sentiment is one of bullish/neutral/bearish.",
		}),
	)
	if err != nil {
		log.Fatalf("RunTyped (strict) failed: %v", err)
	}
	fmt.Printf("%s: %s (%d key points)\n", strict.Ticker, strict.Sentiment, len(strict.KeyPoints))
}

// schemaFor reflects a JSON Schema out of T, the same way RunTyped does
// internally — handy when you want to tweak the spec before passing it.
func schemaFor[T any]() json.RawMessage {
	var zero T
	spec, err := agent.SchemaSpecFromType(zero)
	if err != nil {
		return nil
	}
	return spec.Schema
}
