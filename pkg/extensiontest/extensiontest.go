// Package extensiontest lets an extension be tested through the real loop
// without a real model.
//
// An extension is only as good as what it does at the seams, and the seams
// are the runtime's — a unit test that calls BeforeTool by hand proves the
// method works, not that the framework calls it, in order, with the data it
// expects. This package builds a real Service over a scripted model, so a test
// drives an actual run: tool calls happen, lints run, lifecycles fire.
//
//	llm := extensiontest.Script(
//		extensiontest.CallTool("echo", map[string]interface{}{"text": "hi"}),
//		extensiontest.Answer("done"),
//	)
//	svc := extensiontest.NewService(t, llm, extensiontest.EchoTool(), myExtension)
//	out := extensiontest.Run(t, svc, "say hi")
//	// out.Final == "done"; llm.Rounds() is every message list the model saw
package extensiontest

import (
	"context"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/liliang-cn/agent-go/v3/pkg/agent"
	"github.com/liliang-cn/agent-go/v3/pkg/config"
	"github.com/liliang-cn/agent-go/v3/pkg/domain"
)

// ScriptedLLM answers with a fixed sequence of results, one per model turn,
// and records every message list it was sent. Past the end of the script it
// repeats the last reply, so a script that ends in an answer always
// terminates the loop.
//
// It is safe for concurrent runs; the script is consumed in call order across
// all of them, so a concurrent test should script every run's turns.
type ScriptedLLM struct {
	mu      sync.Mutex
	replies []*domain.GenerationResult
	calls   int32
	rounds  [][]domain.Message
}

// Script returns a model that plays replies in order.
func Script(replies ...*domain.GenerationResult) *ScriptedLLM {
	return &ScriptedLLM{replies: replies}
}

// Answer is a reply with final text and no tool calls.
func Answer(text string) *domain.GenerationResult {
	return &domain.GenerationResult{Content: text}
}

// CallTool is a reply that calls one tool.
func CallTool(name string, args map[string]interface{}) *domain.GenerationResult {
	return &domain.GenerationResult{ToolCalls: []domain.ToolCall{{
		ID:   "call-" + name,
		Type: "function",
		Function: domain.FunctionCall{
			Name:      name,
			Arguments: args,
		},
	}}}
}

// Rounds returns every message list the model was sent, in order — what the
// loop actually assembled, including contributed context and tool results.
func (l *ScriptedLLM) Rounds() [][]domain.Message {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([][]domain.Message, len(l.rounds))
	for i, r := range l.rounds {
		out[i] = append([]domain.Message(nil), r...)
	}
	return out
}

// Calls is how many model turns have run.
func (l *ScriptedLLM) Calls() int { return int(atomic.LoadInt32(&l.calls)) }

func (l *ScriptedLLM) next() *domain.GenerationResult {
	idx := int(atomic.AddInt32(&l.calls, 1)) - 1
	if idx >= len(l.replies) {
		idx = len(l.replies) - 1
	}
	if idx < 0 {
		return &domain.GenerationResult{}
	}
	r := *l.replies[idx]
	r.ToolCalls = append([]domain.ToolCall(nil), l.replies[idx].ToolCalls...)
	return &r
}

func (l *ScriptedLLM) record(msgs []domain.Message) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.rounds = append(l.rounds, append([]domain.Message(nil), msgs...))
}

// Generate implements domain.Generator.
func (l *ScriptedLLM) Generate(context.Context, string, *domain.GenerationOptions) (string, error) {
	return "", nil
}

// Stream implements domain.Generator.
func (l *ScriptedLLM) Stream(context.Context, string, *domain.GenerationOptions, func(string)) error {
	return nil
}

// GenerateWithTools implements domain.Generator.
func (l *ScriptedLLM) GenerateWithTools(_ context.Context, messages []domain.Message, _ []domain.ToolDefinition, _ *domain.GenerationOptions) (*domain.GenerationResult, error) {
	l.record(messages)
	return l.next(), nil
}

// StreamWithTools implements domain.Generator.
func (l *ScriptedLLM) StreamWithTools(_ context.Context, messages []domain.Message, _ []domain.ToolDefinition, _ *domain.GenerationOptions, cb domain.ToolCallCallback) error {
	l.record(messages)
	return cb(l.next())
}

// GenerateStructured implements domain.Generator. It reports an empty valid
// object, which the runtime's constraint extraction reads as "no
// constraints".
func (l *ScriptedLLM) GenerateStructured(context.Context, string, interface{}, *domain.GenerationOptions) (*domain.StructuredResult, error) {
	return &domain.StructuredResult{Valid: true, Raw: `{}`}, nil
}

// RecognizeIntent implements domain.Generator.
func (l *ScriptedLLM) RecognizeIntent(context.Context, string) (*domain.IntentResult, error) {
	return nil, nil
}

// NewService builds a Service over llm with the extensions installed, in a
// temporary home that is removed when the test ends. The service is closed
// at cleanup, so Lifecycle.Stop runs.
func NewService(t testing.TB, llm domain.Generator, exts ...agent.Extension) *agent.Service {
	t.Helper()
	svc, err := NewServiceWithBuilder(agent.New("extensiontest").WithLLM(llm).WithExtensions(exts...), t.TempDir())
	if err != nil {
		t.Fatalf("extensiontest: build service: %v", err)
	}
	t.Cleanup(func() { _ = svc.Close() })
	return svc
}

// NewServiceWithBuilder finishes a builder the caller has already configured
// with a self-contained config under home: file memory, no RAG, no MCP, no
// skills. Use it when the test needs builder options NewService does not set.
func NewServiceWithBuilder(b *agent.Builder, home string) (*agent.Service, error) {
	cfg := &config.Config{
		Home: home,
		RAG:  config.RAGConfig{Enabled: false},
		Memory: config.MemoryConfig{
			StoreType:  "file",
			MemoryPath: filepath.Join(home, "data", "memories"),
		},
	}
	cfg.ApplyHomeLayout()
	return b.WithConfig(cfg).Build()
}

// Outcome is how a test run ended.
type Outcome struct {
	// Final is the completed answer; empty if the run did not complete.
	Final string
	// Blocked is the blocker text; empty if the run was not blocked.
	Blocked string
	// Errors are the error events seen along the way.
	Errors []string
	// Events is every event the run emitted.
	Events []*agent.Event
}

// Run drives one run through the real loop and collects how it ended.
func Run(t testing.TB, svc *agent.Service, goal string, opts ...agent.RunOption) Outcome {
	t.Helper()
	events, err := svc.RunStreamWithOptions(context.Background(), goal, opts...)
	if err != nil {
		t.Fatalf("extensiontest: run: %v", err)
	}
	var out Outcome
	for ev := range events {
		out.Events = append(out.Events, ev)
		switch ev.Type {
		case agent.EventTypeComplete:
			out.Final = ev.Content
		case agent.EventTypeBlocked:
			out.Blocked = ev.Content
		case agent.EventTypeError:
			out.Errors = append(out.Errors, ev.Content)
		}
	}
	return out
}

// ToolModule is an extension that registers one tool. Pair it with CallTool
// in the script to exercise the tool seams.
func ToolModule(name, description string, handler func(ctx context.Context, args map[string]interface{}) (interface{}, error)) agent.Extension {
	return &toolModule{name: name, description: description, handler: handler}
}

// EchoTool registers "echo", which returns {"echoed": args.text}.
func EchoTool() agent.Extension {
	return ToolModule("echo", "echoes its input", func(_ context.Context, args map[string]interface{}) (interface{}, error) {
		return map[string]interface{}{"echoed": args["text"]}, nil
	})
}

type toolModule struct {
	name, description string
	handler           func(ctx context.Context, args map[string]interface{}) (interface{}, error)
}

func (m *toolModule) Name() string { return "tool:" + m.name }
func (m *toolModule) ID() string   { return "tool:" + m.name }
func (m *toolModule) RegisterTools(reg *agent.ToolRegistry) error {
	reg.Register(domain.ToolDefinition{
		Type: "function",
		Function: domain.ToolFunction{
			Name:        m.name,
			Description: m.description,
			Parameters: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"text": map[string]interface{}{"type": "string"},
				},
			},
		},
	}, agent.ToolHandler(m.handler), "extensiontest")
	return nil
}

// ToolMessages returns the tool-role messages of one round — what the model
// was shown as tool results.
func ToolMessages(round []domain.Message) []domain.Message {
	var out []domain.Message
	for _, m := range round {
		if m.Role == "tool" {
			out = append(out, m)
		}
	}
	return out
}
