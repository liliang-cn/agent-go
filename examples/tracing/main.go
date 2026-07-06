// Package main demonstrates exporting agent-go Observer callbacks as
// OpenTelemetry / OpenInference spans to Arize Phoenix.
//
//	go run ./examples/tracing
//
// It is fully offline and deterministic: a scripted mock LLM emits one tool
// call and then a final answer, so it runs in CI with no provider configured.
// It also runs fine with Phoenix OFFLINE — the batch exporter buffers/drops and
// the program exits 0; the spans just never appear.
//
// To see the traces, run Phoenix locally first:
//
//	pip install arize-phoenix && phoenix serve
//	# or: docker run -p 6006:6006 arizephoenix/phoenix:latest
//	# then open http://localhost:6006
package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"

	"github.com/liliang-cn/agent-go/v2/pkg/agent"
	"github.com/liliang-cn/agent-go/v2/pkg/config"
	"github.com/liliang-cn/agent-go/v2/pkg/domain"
	"github.com/liliang-cn/agent-go/v2/pkg/otelobserver"
)

// ---- scripted offline LLM ------------------------------------------------

type scriptedLLM struct{ calls int32 }

func (l *scriptedLLM) turn() *domain.GenerationResult {
	if atomic.AddInt32(&l.calls, 1) == 1 {
		return &domain.GenerationResult{
			ToolCalls: []domain.ToolCall{{
				ID:       "call_weather_1",
				Function: domain.FunctionCall{Name: "get_weather", Arguments: map[string]interface{}{"city": "Tokyo"}},
			}},
		}
	}
	return &domain.GenerationResult{Content: "It is 22°C and sunny in Tokyo."}
}

func (l *scriptedLLM) Generate(context.Context, string, *domain.GenerationOptions) (string, error) {
	return "", nil
}
func (l *scriptedLLM) Stream(context.Context, string, *domain.GenerationOptions, func(string)) error {
	return nil
}
func (l *scriptedLLM) GenerateWithTools(context.Context, []domain.Message, []domain.ToolDefinition, *domain.GenerationOptions) (*domain.GenerationResult, error) {
	return l.turn(), nil
}
func (l *scriptedLLM) StreamWithTools(_ context.Context, _ []domain.Message, _ []domain.ToolDefinition, _ *domain.GenerationOptions, cb domain.ToolCallCallback) error {
	return cb(l.turn())
}
func (l *scriptedLLM) GenerateStructured(context.Context, string, interface{}, *domain.GenerationOptions) (*domain.StructuredResult, error) {
	return &domain.StructuredResult{Valid: true, Raw: `{}`}, nil
}
func (l *scriptedLLM) RecognizeIntent(context.Context, string) (*domain.IntentResult, error) {
	return &domain.IntentResult{Intent: domain.IntentAction, Confidence: 0.9}, nil
}

// ---- tool ----------------------------------------------------------------

type weatherParams struct {
	City string `json:"city" desc:"City to look up"`
}

func main() {
	ctx := context.Background()

	// Wire the Phoenix OTLP exporter + Observer. Safe even when Phoenix is down.
	obs, shutdown, err := otelobserver.Phoenix(ctx,
		otelobserver.WithPhoenixServiceName("agent-go-tracing-example"),
	)
	if err != nil {
		panic(err)
	}
	var shutdownOnce sync.Once
	flush := func() { shutdownOnce.Do(func() { _ = shutdown(ctx) }) }
	defer flush()

	home, err := os.MkdirTemp("", "tracing-example-*")
	if err != nil {
		panic(err)
	}
	defer os.RemoveAll(home)

	cfg := &config.Config{
		Home: home,
		RAG:  config.RAGConfig{Enabled: false},
		Memory: config.MemoryConfig{
			StoreType:  "file",
			MemoryPath: filepath.Join(home, "data", "memories"),
		},
	}
	cfg.ApplyHomeLayout()

	svc, err := agent.New("weather-bot").
		WithPTC(false).
		WithConfig(cfg).
		WithLLM(&scriptedLLM{}).
		WithObserver(obs).
		WithTool(agent.NewTool("get_weather", "Get the current weather for a city",
			func(_ context.Context, p *weatherParams) (any, error) {
				return map[string]any{"city": p.City, "temp_c": 22, "sky": "sunny"}, nil
			})).
		Build()
	if err != nil {
		panic(err)
	}
	defer svc.Close()

	events, err := svc.RunStream(ctx, "What's the weather in Tokyo?")
	if err != nil {
		panic(err)
	}
	final := agent.Concat(events)
	fmt.Printf("Final answer: %s\n", final)

	// Flush before exit so batched spans are delivered.
	flush()

	fmt.Println("Traces sent to Phoenix. Open http://localhost:6006")
	fmt.Println("(Runs fine with Phoenix offline — spans just don't appear.)")
}
