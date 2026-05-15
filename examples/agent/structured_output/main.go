// Package main demonstrates AgentGo's structured-output constraint.
//
// The agent is asked to summarize a piece of text. Without any constraint
// the model returns prose. With a StructuredOutputSpec attached, the
// runtime:
//   - injects a system message describing the required JSON shape
//   - threads response_format through to the provider (Tier B — providers
//     that support OpenAI structured outputs return guaranteed-valid JSON)
//   - validates the final answer against the schema with a built-in lint
//     (Tier A — works on every provider, re-prompts on schema mismatch)
//
// The example shows both API shapes:
//   - WithStructuredOutputType[T]() — derive schema from a Go struct
//   - WithStructuredOutput(spec)    — pass a raw JSON Schema directly
//
// Usage:
//
//	go run ./examples/agent/structured_output            # uses Go-type schema
//	go run ./examples/agent/structured_output raw        # uses raw schema
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

// ArticleSummary is the Go type the model must populate. Field tags drive
// the schema: `json:"name"` controls the property name, `desc:"..."` is
// surfaced to the model as a field description, `omitempty` marks the
// field optional.
type ArticleSummary struct {
	Title    string   `json:"title" desc:"A short, capitalized title for the summary."`
	Sentence string   `json:"sentence" desc:"A single sentence summary, no more than 200 characters."`
	Keywords []string `json:"keywords" desc:"3 to 6 lowercase keyword strings."`
	Sentiment string  `json:"sentiment" desc:"One of: positive, neutral, negative."`
	Confidence float64 `json:"confidence,omitempty" desc:"Optional. Model's confidence between 0 and 1."`
}

const article = `AgentGo is a Go framework for building agentic systems. It includes
an event-loop runtime, output lints, MCP tooling, and a programmatic tool
calling sandbox. The 2.72 release added a unified hook surface and made
DeepSeek's thinking mode controllable from RunConfig. Users have reported
faster integration cycles compared to wrapping a raw LLM API by hand.`

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	svc, err := agent.New("structured-output-demo").
		WithPTC(false).
		WithSystemPrompt("You are a precise text summarizer. Always return a single JSON object — no prose, no fences.").
		WithDebug(os.Getenv("DEBUG") != "").
		Build()
	if err != nil {
		log.Fatalf("build agent: %v", err)
	}
	defer svc.Close()

	mode := "type"
	if len(os.Args) > 1 {
		mode = os.Args[1]
	}

	var runOpts []agent.RunOption
	switch mode {
	case "raw":
		runOpts = []agent.RunOption{
			agent.WithStructuredOutput(&agent.StructuredOutputSpec{
				Name: "article_summary",
				Schema: json.RawMessage(`{
                    "type": "object",
                    "properties": {
                        "title":      {"type": "string"},
                        "sentence":   {"type": "string", "maxLength": 200},
                        "keywords":   {"type": "array", "items": {"type": "string"}, "minItems": 3, "maxItems": 6},
                        "sentiment":  {"type": "string", "enum": ["positive", "neutral", "negative"]},
                        "confidence": {"type": "number", "minimum": 0, "maximum": 1}
                    },
                    "required": ["title", "sentence", "keywords", "sentiment"],
                    "additionalProperties": false
                }`),
				Strict:      true,
				Description: "Summarize the article into a structured record.",
			}),
		}
	default:
		runOpts = []agent.RunOption{
			agent.WithStructuredOutputType[ArticleSummary](),
		}
	}

	prompt := "Summarize the following article:\n\n" + article
	fmt.Println("=== Structured Output Demo ===")
	fmt.Printf("Mode: %s\n\n", mode)

	events, err := svc.RunStreamWithOptions(ctx, prompt, runOpts...)
	if err != nil {
		log.Fatalf("RunStreamWithOptions: %v", err)
	}

	var final string
	for evt := range events {
		switch evt.Type {
		case agent.EventTypeComplete:
			final = evt.Content
		case agent.EventTypeError:
			fmt.Printf("[error] %s\n", evt.Content)
		case agent.EventTypeBlocked:
			fmt.Printf("[blocked] %s\n", evt.Content)
		}
	}

	fmt.Println("--- Raw final ---")
	fmt.Println(final)
	fmt.Println()

	// Demonstrate parsing the result back into the Go type when using the
	// type-derived mode.
	if mode != "raw" {
		var parsed ArticleSummary
		if err := json.Unmarshal([]byte(final), &parsed); err != nil {
			fmt.Printf("(could not parse into ArticleSummary: %v)\n", err)
			return
		}
		fmt.Println("--- Parsed into Go struct ---")
		pretty, _ := json.MarshalIndent(parsed, "", "  ")
		fmt.Println(string(pretty))
	}
}
