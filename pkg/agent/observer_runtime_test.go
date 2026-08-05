package agent

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/liliang-cn/agent-go/v3/pkg/domain"
)

// observerToolLLM emits a single tool call on its first streamed turn, then a
// plain final answer on the second — enough to exercise the model + tool
// observer seams end-to-end through RunStream.
type observerToolLLM struct {
	calls int32
}

func (l *observerToolLLM) turn() *domain.GenerationResult {
	n := atomic.AddInt32(&l.calls, 1)
	if n == 1 {
		return &domain.GenerationResult{
			ToolCalls: []domain.ToolCall{{
				ID:       "call_ping_1",
				Function: domain.FunctionCall{Name: "ping", Arguments: map[string]interface{}{"msg": "hi"}},
			}},
		}
	}
	return &domain.GenerationResult{Content: "all done"}
}

func (l *observerToolLLM) Generate(context.Context, string, *domain.GenerationOptions) (string, error) {
	return "", nil
}
func (l *observerToolLLM) Stream(context.Context, string, *domain.GenerationOptions, func(string)) error {
	return nil
}
func (l *observerToolLLM) GenerateWithTools(context.Context, []domain.Message, []domain.ToolDefinition, *domain.GenerationOptions) (*domain.GenerationResult, error) {
	return l.turn(), nil
}
func (l *observerToolLLM) StreamWithTools(ctx context.Context, _ []domain.Message, _ []domain.ToolDefinition, _ *domain.GenerationOptions, cb domain.ToolCallCallback) error {
	return cb(l.turn())
}
func (l *observerToolLLM) GenerateStructured(context.Context, string, interface{}, *domain.GenerationOptions) (*domain.StructuredResult, error) {
	return &domain.StructuredResult{Valid: true, Raw: `{}`}, nil
}
func (l *observerToolLLM) RecognizeIntent(context.Context, string) (*domain.IntentResult, error) {
	return &domain.IntentResult{Intent: domain.IntentAction, Confidence: 0.9}, nil
}

type pingParams struct {
	Msg string `json:"msg" desc:"message"`
}

func buildObserverTestService(t *testing.T, obs ...Observer) *Service {
	t.Helper()
	b := New("observer-runtime-agent").
		WithPTC(false).
		WithConfig(testAgentConfig(t.TempDir())).
		WithLLM(&observerToolLLM{}).
		WithTool(NewTool("ping", "Ping back", func(_ context.Context, p *pingParams) (any, error) {
			return map[string]any{"ok": true, "echo": p.Msg}, nil
		}))
	for _, o := range obs {
		b = b.WithObserver(o)
	}
	svc, err := b.Build()
	if err != nil {
		t.Fatalf("build failed: %v", err)
	}
	return svc
}

func TestRuntimeObserverModelAndToolSeams(t *testing.T) {
	rec := &recordingObserver{}
	svc := buildObserverTestService(t, rec)
	defer svc.Close()

	events, err := svc.RunStream(context.Background(), "please ping")
	if err != nil {
		t.Fatalf("RunStream failed: %v", err)
	}
	final := Concat(events)
	if final != "all done" {
		t.Fatalf("expected final text 'all done', got %q", final)
	}

	rec.mu.Lock()
	defer rec.mu.Unlock()

	if len(rec.modelStarts) == 0 {
		t.Fatal("expected at least one OnModelStart")
	}
	if len(rec.modelStarts) != len(rec.modelEnds) {
		t.Fatalf("model start/end mismatch: starts=%d ends=%d", len(rec.modelStarts), len(rec.modelEnds))
	}
	// Every model span id must pair start<->end.
	for i, id := range rec.modelStarts {
		if id == "" {
			t.Fatalf("model start %d has empty SpanID", i)
		}
		if rec.modelEnds[i] != id {
			t.Fatalf("model span %d not paired: start=%q end=%q", i, id, rec.modelEnds[i])
		}
	}

	if len(rec.toolStarts) != 1 || len(rec.toolEnds) != 1 {
		t.Fatalf("expected exactly one tool start+end, got starts=%d ends=%d", len(rec.toolStarts), len(rec.toolEnds))
	}
	if rec.toolStarts[0] != rec.toolEnds[0] || rec.toolStarts[0] == "" {
		t.Fatalf("tool call not paired by CallID: start=%q end=%q", rec.toolStarts[0], rec.toolEnds[0])
	}
}
