// Package main demonstrates the unified Observer / Aspect layer plus the
// built-in TracingObserver.
//
// It is fully offline and deterministic: a scripted mock LLM emits one tool
// call and then a final answer, so you can run it in CI without any provider
// configured.
//
//	go run ./examples/observer
//
// What it shows:
//   - a custom Observer (embedding agent.BaseObserver) that logs every
//     model / tool / sub-agent / checkpoint event with timing
//   - agent.NewTracingObserver(tracer) driving the built-in Tracer
//   - dumping the collected trace via agent.TraceExporter
package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"
	"time"

	"github.com/liliang-cn/agent-go/v2/pkg/agent"
	"github.com/liliang-cn/agent-go/v2/pkg/config"
	"github.com/liliang-cn/agent-go/v2/pkg/domain"
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
func (l *scriptedLLM) StreamWithTools(ctx context.Context, _ []domain.Message, _ []domain.ToolDefinition, _ *domain.GenerationOptions, cb domain.ToolCallCallback) error {
	return cb(l.turn())
}
func (l *scriptedLLM) GenerateStructured(context.Context, string, interface{}, *domain.GenerationOptions) (*domain.StructuredResult, error) {
	return &domain.StructuredResult{Valid: true, Raw: `{}`}, nil
}
func (l *scriptedLLM) RecognizeIntent(context.Context, string) (*domain.IntentResult, error) {
	return &domain.IntentResult{Intent: domain.IntentAction, Confidence: 0.9}, nil
}

// ---- custom logging observer --------------------------------------------

type loggingObserver struct {
	agent.BaseObserver
	start time.Time
}

func (o *loggingObserver) elapsed() string {
	return fmt.Sprintf("%6.1fms", float64(time.Since(o.start).Microseconds())/1000)
}

func (o *loggingObserver) OnModelStart(_ context.Context, info agent.ModelInfo) {
	fmt.Printf("[%s] model  START  round=%d span=%s msgs=%d tools=%d\n",
		o.elapsed(), info.Round, short(info.SpanID), info.Messages, info.Tools)
}
func (o *loggingObserver) OnModelEnd(_ context.Context, info agent.ModelInfo, res *agent.ModelResult, err error) {
	toolCalls, tokens := 0, 0
	if res != nil {
		toolCalls, tokens = res.ToolCalls, res.TokensUsed
	}
	fmt.Printf("[%s] model  END    span=%s toolCalls=%d tokens=%d err=%v\n",
		o.elapsed(), short(info.SpanID), toolCalls, tokens, err)
}
func (o *loggingObserver) OnToolStart(_ context.Context, info agent.ToolInfo) {
	fmt.Printf("[%s] tool   START  %s call=%s args=%v\n", o.elapsed(), info.Tool, short(info.CallID), info.Args)
}
func (o *loggingObserver) OnToolEnd(_ context.Context, info agent.ToolInfo, result any, err error) {
	fmt.Printf("[%s] tool   END    %s call=%s result=%v err=%v\n", o.elapsed(), info.Tool, short(info.CallID), result, err)
}
func (o *loggingObserver) OnSubAgentStart(_ context.Context, info agent.SubAgentInfo) {
	fmt.Printf("[%s] subagt START  %s goal=%q\n", o.elapsed(), info.Name, info.Goal)
}
func (o *loggingObserver) OnSubAgentEnd(_ context.Context, info agent.SubAgentInfo, _ any, err error) {
	fmt.Printf("[%s] subagt END    %s err=%v\n", o.elapsed(), info.Name, err)
}
func (o *loggingObserver) OnCheckpoint(_ context.Context, info agent.CheckpointInfo) {
	fmt.Printf("[%s] chkpt         reason=%s round=%d msgs=%d\n", o.elapsed(), info.Reason, info.Round, info.Messages)
}

func short(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

// ---- tool ----------------------------------------------------------------

type weatherParams struct {
	City string `json:"city" desc:"City to look up"`
}

func main() {
	home, err := os.MkdirTemp("", "observer-example-*")
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

	tracer := agent.NewTracer()

	svc, err := agent.New("weather-bot").
		WithPTC(false).
		WithConfig(cfg).
		WithLLM(&scriptedLLM{}).
		WithObserver(&loggingObserver{start: time.Now()}).
		WithObserver(agent.NewTracingObserver(tracer)).
		WithTool(agent.NewTool("get_weather", "Get the current weather for a city",
			func(_ context.Context, p *weatherParams) (any, error) {
				return map[string]any{"city": p.City, "temp_c": 22, "sky": "sunny"}, nil
			})).
		Build()
	if err != nil {
		panic(err)
	}
	defer svc.Close()

	fmt.Println("=== Observer callbacks ===")
	events, err := svc.RunStream(context.Background(), "What's the weather in Tokyo?")
	if err != nil {
		panic(err)
	}
	final := agent.Concat(events)
	fmt.Printf("\nFinal answer: %s\n", final)

	fmt.Println("\n=== Trace dump (TracingObserver -> Tracer -> TraceExporter) ===")
	exporter := agent.NewTraceExporter(tracer)
	out, err := exporter.ExportAll(agent.ExportFormatPretty)
	if err != nil {
		panic(err)
	}
	fmt.Print(out)

	// Also show MergeEvents fanning two RunStream channels into one drain.
	fmt.Println("\n=== MergeEvents: two runs, one drain ===")
	s1, _ := svc.RunStream(context.Background(), "weather run A")
	s2, _ := svc.RunStream(context.Background(), "weather run B")
	merged := agent.MergeEvents(s1, s2)
	complete := 0
	for evt := range merged {
		if evt.Type == agent.EventTypeComplete {
			complete++
		}
	}
	fmt.Printf("drained both merged streams; saw %d completion events\n", complete)
}
