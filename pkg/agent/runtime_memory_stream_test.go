package agent

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/liliang-cn/agent-go/v2/pkg/domain"
)

type streamMemorySaveTestLLM struct{}

func (s *streamMemorySaveTestLLM) Generate(ctx context.Context, prompt string, opts *domain.GenerationOptions) (string, error) {
	return "", nil
}

func (s *streamMemorySaveTestLLM) Stream(ctx context.Context, prompt string, opts *domain.GenerationOptions, callback func(string)) error {
	return nil
}

func (s *streamMemorySaveTestLLM) GenerateWithTools(ctx context.Context, messages []domain.Message, tools []domain.ToolDefinition, opts *domain.GenerationOptions) (*domain.GenerationResult, error) {
	return &domain.GenerationResult{Content: "我会记住这件事。"}, nil
}

func (s *streamMemorySaveTestLLM) StreamWithTools(ctx context.Context, messages []domain.Message, tools []domain.ToolDefinition, opts *domain.GenerationOptions, callback domain.ToolCallCallback) error {
	return callback(&domain.GenerationResult{Content: "我会记住这件事。"})
}

func (s *streamMemorySaveTestLLM) GenerateStructured(ctx context.Context, prompt string, schema interface{}, opts *domain.GenerationOptions) (*domain.StructuredResult, error) {
	switch {
	case schemaHasProperty(schema, "intent_type"):
		return structuredJSON(map[string]interface{}{
			"intent_type": "memory_save",
			"confidence":  0.95,
		}), nil
	case schemaHasProperty(schema, "should_store"):
		return structuredJSON(map[string]interface{}{
			"should_store": false,
			"memories":     []map[string]interface{}{},
		}), nil
	default:
		return structuredJSON(map[string]interface{}{}), nil
	}
}

func (s *streamMemorySaveTestLLM) RecognizeIntent(ctx context.Context, request string) (*domain.IntentResult, error) {
	return &domain.IntentResult{Intent: domain.IntentAction, Confidence: 0.95}, nil
}

func TestRunStreamPersistsImplicitScheduleMemorySave(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	home := t.TempDir()

	svc, err := New("memory-agent").
		WithPTC(false).
		WithConfig(testAgentConfig(home)).
		WithLLM(&streamMemorySaveTestLLM{}).
		WithMemory().
		Build()
	if err != nil {
		t.Fatalf("build failed: %v", err)
	}
	defer svc.Close()

	events, err := svc.RunStream(ctx, "明天下午17：00去万达广场吃饭。")
	if err != nil {
		t.Fatalf("RunStream failed: %v", err)
	}
	for range events {
	}

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		mems, total, listErr := svc.MemoryService().List(ctx, 10, 0)
		if listErr != nil {
			t.Fatalf("list memories failed: %v", listErr)
		}
		if total > 0 {
			for _, mem := range mems {
				if strings.Contains(mem.Content, "万达广场吃饭") {
					return
				}
			}
		}
		time.Sleep(50 * time.Millisecond)
	}

	mems, _, err := svc.MemoryService().List(ctx, 10, 0)
	if err != nil {
		t.Fatalf("list memories failed: %v", err)
	}
	t.Fatalf("expected streamed implicit schedule memory to be persisted, got %+v", mems)
}
