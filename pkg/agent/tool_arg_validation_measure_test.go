package agent

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/liliang-cn/agent-go/v3/pkg/domain"
)

// argFixingLLM plays a model that makes the most common tool-call mistake —
// leaving out a required argument — and then behaves the way a model does
// with whatever came back: if the tool's answer names the missing field it
// repairs the call, otherwise it takes the answer at face value and finishes.
type argFixingLLM struct {
	mu        sync.Mutex
	toolTurns int32
	fixed     bool
}

func (l *argFixingLLM) next(msgs []domain.Message) *domain.GenerationResult {
	atomic.AddInt32(&l.toolTurns, 1)
	l.mu.Lock()
	defer l.mu.Unlock()

	var lastTool string
	sawTool := false
	for _, m := range msgs {
		if m.Role == "tool" {
			sawTool = true
			lastTool = m.Content
		}
	}
	if !sawTool {
		// Turn one: the content, but no path.
		return &domain.GenerationResult{ToolCalls: []domain.ToolCall{{
			ID: "call_1", Type: "function",
			Function: domain.FunctionCall{Name: "save_note", Arguments: map[string]interface{}{
				"content": "remember the milk",
			}},
		}}}
	}
	if !l.fixed && strings.Contains(lastTool, "path") {
		l.fixed = true
		return &domain.GenerationResult{ToolCalls: []domain.ToolCall{{
			ID: "call_2", Type: "function",
			Function: domain.FunctionCall{Name: "save_note", Arguments: map[string]interface{}{
				"path":    "notes/milk.txt",
				"content": "remember the milk",
			}},
		}}}
	}
	return &domain.GenerationResult{Content: "The note has been saved."}
}

func (l *argFixingLLM) Generate(context.Context, string, *domain.GenerationOptions) (string, error) {
	return "", nil
}
func (l *argFixingLLM) Stream(context.Context, string, *domain.GenerationOptions, func(string)) error {
	return nil
}
func (l *argFixingLLM) GenerateWithTools(_ context.Context, msgs []domain.Message, _ []domain.ToolDefinition, _ *domain.GenerationOptions) (*domain.GenerationResult, error) {
	return l.next(msgs), nil
}
func (l *argFixingLLM) StreamWithTools(_ context.Context, msgs []domain.Message, _ []domain.ToolDefinition, _ *domain.GenerationOptions, cb domain.ToolCallCallback) error {
	return cb(l.next(msgs))
}
func (l *argFixingLLM) GenerateStructured(context.Context, string, interface{}, *domain.GenerationOptions) (*domain.StructuredResult, error) {
	return &domain.StructuredResult{Valid: true, Raw: "{}"}, nil
}
func (l *argFixingLLM) RecognizeIntent(context.Context, string) (*domain.IntentResult, error) {
	return nil, nil
}

type argMeasure struct {
	invalidExecutions int // handler ran without its required argument
	validExecutions   int // handler ran with a path
	modelTurns        int
	saved             bool
}

// runArgScenario runs one save-a-note task. strict chooses the handler: a
// strict one refuses a missing path itself; a lenient one — the common kind,
// written against the schema and trusting it — quietly does the wrong thing.
func runArgScenario(t *testing.T, strict bool, opts ...func(*Builder) *Builder) argMeasure {
	t.Helper()
	llm := &argFixingLLM{}
	b := New("arg-validation").
		WithConfig(testAgentConfig(t.TempDir())).
		WithLLM(llm)
	for _, o := range opts {
		b = o(b)
	}
	svc, err := b.Build()
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	defer svc.Close()

	var m argMeasure
	var mu sync.Mutex
	svc.AddToolWithMetadata("save_note", "Save a note to a file.",
		map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"path":    map[string]interface{}{"type": "string", "description": "file path"},
				"content": map[string]interface{}{"type": "string"},
			},
			"required": []interface{}{"path", "content"},
		},
		func(_ context.Context, args map[string]interface{}) (interface{}, error) {
			mu.Lock()
			defer mu.Unlock()
			path, _ := args["path"].(string)
			if path == "" {
				m.invalidExecutions++
				if strict {
					return nil, errString("path is required")
				}
				// Lenient: writes somewhere unintended and reports success.
				return "ok", nil
			}
			m.validExecutions++
			m.saved = true
			return "saved to " + path, nil
		},
		ToolMetadata{})

	if _, err := svc.Run(context.Background(), "Save a note saying 'remember the milk'.",
		WithConstraintExtraction(false)); err != nil {
		t.Fatalf("run: %v", err)
	}
	m.modelTurns = int(atomic.LoadInt32(&llm.toolTurns))
	return m
}

type errString string

func (e errString) Error() string { return string(e) }

// TestToolArgValidationMeasured is the before/after measurement for argument
// validation. The numbers are logged; the assertions are the "after".
func TestToolArgValidationMeasured(t *testing.T) {
	for _, strict := range []bool{false, true} {
		name := "lenient_handler"
		if strict {
			name = "strict_handler"
		}
		t.Run(name, func(t *testing.T) {
			m := runArgScenario(t, strict)
			t.Logf("MEASURE %s: invalid_executions=%d valid_executions=%d model_turns=%d saved=%v",
				name, m.invalidExecutions, m.validExecutions, m.modelTurns, m.saved)
			if m.invalidExecutions != 0 {
				t.Errorf("a call missing a required argument reached the handler %d times", m.invalidExecutions)
			}
			if !m.saved || m.validExecutions != 1 {
				t.Errorf("the repaired call should run exactly once and save: %+v", m)
			}
			if m.modelTurns != 3 {
				t.Errorf("model turns = %d, want 3 (bad call, repaired call, answer)", m.modelTurns)
			}

			// The opt-out restores the old behaviour exactly.
			off := runArgScenario(t, strict, func(b *Builder) *Builder { return b.WithToolArgValidation(false) })
			t.Logf("MEASURE %s validation_off: invalid_executions=%d valid_executions=%d model_turns=%d saved=%v",
				name, off.invalidExecutions, off.validExecutions, off.modelTurns, off.saved)
			if off.invalidExecutions != 1 {
				t.Errorf("with validation off the bad call should reach the handler, got %d", off.invalidExecutions)
			}
		})
	}
}
