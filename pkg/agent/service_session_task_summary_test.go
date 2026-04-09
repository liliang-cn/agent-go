package agent

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/liliang-cn/agent-go/v2/pkg/domain"
)

type compactSessionTestLLM struct{}

func (c *compactSessionTestLLM) Generate(ctx context.Context, prompt string, opts *domain.GenerationOptions) (string, error) {
	return "task-summary", nil
}

func (c *compactSessionTestLLM) Stream(ctx context.Context, prompt string, opts *domain.GenerationOptions, callback func(string)) error {
	return nil
}

func (c *compactSessionTestLLM) GenerateWithTools(ctx context.Context, messages []domain.Message, tools []domain.ToolDefinition, opts *domain.GenerationOptions) (*domain.GenerationResult, error) {
	return &domain.GenerationResult{}, nil
}

func (c *compactSessionTestLLM) StreamWithTools(ctx context.Context, messages []domain.Message, tools []domain.ToolDefinition, opts *domain.GenerationOptions, callback domain.ToolCallCallback) error {
	return nil
}

func (c *compactSessionTestLLM) GenerateStructured(ctx context.Context, prompt string, schema interface{}, opts *domain.GenerationOptions) (*domain.StructuredResult, error) {
	return &domain.StructuredResult{Valid: true}, nil
}

func (c *compactSessionTestLLM) RecognizeIntent(ctx context.Context, request string) (*domain.IntentResult, error) {
	return nil, nil
}

func TestCompactSessionStoresTaskSummaryForCurrentTask(t *testing.T) {
	t.Parallel()

	svc, err := NewService(&compactSessionTestLLM{}, nil, nil, filepath.Join(t.TempDir(), "agent.db"), nil)
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}

	session := NewSession("agent-1")
	session.SetSummary("global-summary")
	session.SetContext(sessionContextTaskID, "task-2")
	session.AddMessage(domain.Message{Role: "user", Content: "task-1-user", TaskID: "task-1"})
	session.AddMessage(domain.Message{Role: "assistant", Content: "task-1-assistant", TaskID: "task-1"})
	session.AddMessage(domain.Message{Role: "user", Content: "task-2-user", TaskID: "task-2"})
	session.AddMessage(domain.Message{Role: "assistant", Content: "task-2-assistant", TaskID: "task-2"})

	if err := svc.store.SaveSession(session); err != nil {
		t.Fatalf("SaveSession() error = %v", err)
	}

	summary, err := svc.CompactSession(context.Background(), session.GetID())
	if err != nil {
		t.Fatalf("CompactSession() error = %v", err)
	}
	if summary != "task-summary" {
		t.Fatalf("CompactSession() summary = %q, want %q", summary, "task-summary")
	}

	reloaded, err := svc.store.GetSession(session.GetID())
	if err != nil {
		t.Fatalf("GetSession() error = %v", err)
	}
	if got := taskSummary(reloaded, "task-2"); got != "task-summary" {
		t.Fatalf("taskSummary(task-2) = %q, want %q", got, "task-summary")
	}
	if got := reloaded.GetSummary(); got != "global-summary" {
		t.Fatalf("global summary = %q, want %q", got, "global-summary")
	}
}
