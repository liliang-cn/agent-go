// Example flow-trace: one request, two "processes", one trace.
//
// A front service receives a request with a traceparent, runs an agent whose
// tool hands work to a worker the way a host would over its own link (here a
// map standing in for gRPC metadata), and the worker continues the same flow.
// Both write JSONL traces; agentgo-trace rebuilds the whole flow from them:
//
//	go run ./examples/flow-trace
//	go run ./cmd/agentgo-trace /tmp/flow-trace --trace <the printed trace id>
package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/liliang-cn/agent-go/v3/pkg/agent"
	"github.com/liliang-cn/agent-go/v3/pkg/domain"
	"github.com/liliang-cn/agent-go/v3/pkg/trace"
)

// scripted answers without a provider: the front agent calls "hand_off"
// once, then answers; the worker answers at once.
type scripted struct{ turns int }

func (s *scripted) Generate(context.Context, string, *domain.GenerationOptions) (string, error) {
	return "", nil
}
func (s *scripted) Stream(context.Context, string, *domain.GenerationOptions, func(string)) error {
	return nil
}
func (s *scripted) GenerateWithTools(ctx context.Context, m []domain.Message, t []domain.ToolDefinition, o *domain.GenerationOptions) (*domain.GenerationResult, error) {
	return s.answer(t), nil
}
func (s *scripted) StreamWithTools(ctx context.Context, m []domain.Message, t []domain.ToolDefinition, o *domain.GenerationOptions, cb domain.ToolCallCallback) error {
	return cb(s.answer(t))
}
func (s *scripted) answer(tools []domain.ToolDefinition) *domain.GenerationResult {
	s.turns++
	for _, t := range tools {
		if t.Function.Name == "hand_off" && s.turns == 1 {
			return &domain.GenerationResult{ToolCalls: []domain.ToolCall{{ID: "call_1", Type: "function",
				Function: domain.FunctionCall{Name: "hand_off", Arguments: map[string]any{"job": "summarise"}}}}}
		}
	}
	return &domain.GenerationResult{Content: "done", Streamed: true}
}
func (s *scripted) GenerateStructured(context.Context, string, interface{}, *domain.GenerationOptions) (*domain.StructuredResult, error) {
	return &domain.StructuredResult{Valid: true, Raw: "{}"}, nil
}
func (s *scripted) RecognizeIntent(context.Context, string) (*domain.IntentResult, error) {
	return &domain.IntentResult{Intent: domain.IntentAction}, nil
}

func service(name string, traceFile string, tools ...*agent.Tool) (*agent.Service, func()) {
	f, err := os.Create(traceFile)
	if err != nil {
		panic(err)
	}
	b := agent.New(name).WithLLM(&scripted{}).WithObserver(agent.NewTraceWriter(f)).WithConstraintExtraction(false)
	for _, t := range tools {
		b = b.WithTool(t)
	}
	svc, err := b.Build()
	if err != nil {
		panic(err)
	}
	return svc, func() { svc.Close(); f.Close() }
}

func main() {
	dir := filepath.Join(os.TempDir(), "flow-trace")
	_ = os.MkdirAll(dir, 0o755)

	worker, closeWorker := service("worker", filepath.Join(dir, "worker.jsonl"))
	defer closeWorker()

	// The worker's side of the link: take the traceparent out of the
	// metadata and run under it.
	serveWorker := func(md map[string]string, job string) (string, error) {
		ctx := trace.Extract(context.Background(), func(k string) string { return md[k] })
		res, err := worker.Run(ctx, "worker job: "+job)
		if err != nil {
			return "", err
		}
		return res.Text(), nil
	}

	handOff := agent.NewTool("hand_off", "Hand a job to the worker", func(ctx context.Context, p *struct {
		Job string `json:"job"`
	}) (any, error) {
		// The front's side: put the tool call's span on the outgoing call.
		md := map[string]string{}
		trace.Inject(ctx, func(k, v string) { md[k] = v })
		return serveWorker(md, p.Job)
	})
	front, closeFront := service("front", filepath.Join(dir, "front.jsonl"), handOff)
	defer closeFront()

	// A request arrived with a traceparent; without one, the run starts a flow.
	ctx := trace.WithTraceparent(context.Background(), "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01")
	if _, err := front.Run(ctx, "summarise the week"); err != nil {
		panic(err)
	}
	fmt.Printf("traces in %s\nflow 0af7651916cd43dd8448eb211c80319c\n", dir)
}
