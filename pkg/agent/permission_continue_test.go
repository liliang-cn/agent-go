package agent

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/liliang-cn/agent-go/v3/pkg/domain"
)

// deniedOnceLLM calls rm_file, then answers with whatever it was told about
// that call, so a test can see the denial reached the model.
type deniedOnceLLM struct {
	calls int32
	mu    sync.Mutex
	seen  []domain.Message
}

func (l *deniedOnceLLM) turn(msgs []domain.Message) *domain.GenerationResult {
	l.mu.Lock()
	l.seen = append([]domain.Message(nil), msgs...)
	l.mu.Unlock()
	if atomic.AddInt32(&l.calls, 1) == 1 {
		return &domain.GenerationResult{ToolCalls: []domain.ToolCall{
			{ID: "call_rm", Function: domain.FunctionCall{Name: "rm_file", Arguments: map[string]interface{}{"path": "a.txt"}}},
		}}
	}
	return &domain.GenerationResult{Content: "did it another way"}
}

func (l *deniedOnceLLM) Generate(context.Context, string, *domain.GenerationOptions) (string, error) {
	return "", nil
}
func (l *deniedOnceLLM) Stream(context.Context, string, *domain.GenerationOptions, func(string)) error {
	return nil
}
func (l *deniedOnceLLM) GenerateWithTools(_ context.Context, m []domain.Message, _ []domain.ToolDefinition, _ *domain.GenerationOptions) (*domain.GenerationResult, error) {
	return l.turn(m), nil
}
func (l *deniedOnceLLM) StreamWithTools(_ context.Context, m []domain.Message, _ []domain.ToolDefinition, _ *domain.GenerationOptions, cb domain.ToolCallCallback) error {
	return cb(l.turn(m))
}
func (l *deniedOnceLLM) GenerateStructured(context.Context, string, interface{}, *domain.GenerationOptions) (*domain.StructuredResult, error) {
	return &domain.StructuredResult{Valid: true, Raw: `{}`}, nil
}
func (l *deniedOnceLLM) RecognizeIntent(context.Context, string) (*domain.IntentResult, error) {
	return &domain.IntentResult{Intent: domain.IntentAction, Confidence: 0.9}, nil
}

func runDenied(t *testing.T, resp PermissionResponse) (*deniedOnceLLM, []*Event, bool) {
	t.Helper()
	llm := &deniedOnceLLM{}
	var ran atomic.Bool
	svc, err := New("permission-continue-agent").
		WithConfig(testAgentConfig(t.TempDir())).
		WithLLM(llm).
		WithTool(NewTool("rm_file", "Delete a file", func(_ context.Context, p *rmParams) (any, error) {
			ran.Store(true)
			return map[string]any{"deleted": p.Path}, nil
		}).WithDestructive(true)).
		Build()
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	defer svc.Close()
	svc.SetPermissionPolicy(DefaultPermissionPolicy)
	svc.SetPermissionHandler(func(context.Context, PermissionRequest) (*PermissionResponse, error) {
		r := resp
		return &r, nil
	})
	ch, err := svc.RunStreamWithOptions(context.Background(), "clean up")
	if err != nil {
		t.Fatalf("RunStream: %v", err)
	}
	var evs []*Event
	for ev := range ch {
		evs = append(evs, ev)
	}
	return llm, evs, ran.Load()
}

func last(evs []*Event, types ...EventType) *Event {
	for i := len(evs) - 1; i >= 0; i-- {
		for _, ty := range types {
			if evs[i].Type == ty {
				return evs[i]
			}
		}
	}
	return nil
}

// Nobody answered the approval: the model is told and the run carries on.
func TestADenialThatContinuesIsTheToolsResult(t *testing.T) {
	llm, evs, ran := runDenied(t, PermissionResponse{Allowed: false, Reason: "nobody approved rm_file within 2m0s", ContinueRun: true})
	if ran {
		t.Fatal("the denied tool ran")
	}
	end := last(evs, EventTypeComplete, EventTypeBlocked, EventTypeError)
	if end == nil || end.Type != EventTypeComplete || !strings.Contains(end.Content, "another way") {
		t.Fatalf("run ended with %+v", end)
	}
	told := false
	for _, m := range llm.seen {
		if strings.Contains(m.Content, "nobody approved rm_file") {
			told = true
		}
	}
	if !told {
		t.Fatalf("the model never saw the denial: %+v", llm.seen)
	}
}

// Without ContinueRun a denial still blocks the run, as it always has.
func TestADenialStillBlocksByDefault(t *testing.T) {
	llm, evs, ran := runDenied(t, PermissionResponse{Allowed: false, Reason: "not that one"})
	if ran {
		t.Fatal("the denied tool ran")
	}
	end := last(evs, EventTypeComplete, EventTypeBlocked, EventTypeError)
	if end == nil || end.Type != EventTypeBlocked || !strings.Contains(end.Content, "not that one") {
		t.Fatalf("run ended with %+v", end)
	}
	if atomic.LoadInt32(&llm.calls) != 1 {
		t.Fatalf("the model was asked again after a blocking denial: %d calls", llm.calls)
	}
}
