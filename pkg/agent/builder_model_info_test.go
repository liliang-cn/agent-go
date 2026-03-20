package agent

import (
	"context"
	"testing"

	"github.com/liliang-cn/agent-go/v2/pkg/config"
	"github.com/liliang-cn/agent-go/v2/pkg/domain"
	"github.com/liliang-cn/agent-go/v2/pkg/pool"
)

type testModelInfoLLM struct {
	model   string
	baseURL string
}

func (t *testModelInfoLLM) GetModelName() string { return t.model }
func (t *testModelInfoLLM) GetBaseURL() string   { return t.baseURL }

func (t *testModelInfoLLM) Generate(ctx context.Context, prompt string, opts *domain.GenerationOptions) (string, error) {
	return "", nil
}

func (t *testModelInfoLLM) Stream(ctx context.Context, prompt string, opts *domain.GenerationOptions, callback func(string)) error {
	return nil
}

func (t *testModelInfoLLM) GenerateWithTools(ctx context.Context, messages []domain.Message, tools []domain.ToolDefinition, opts *domain.GenerationOptions) (*domain.GenerationResult, error) {
	return &domain.GenerationResult{}, nil
}

func (t *testModelInfoLLM) StreamWithTools(ctx context.Context, messages []domain.Message, tools []domain.ToolDefinition, opts *domain.GenerationOptions, callback domain.ToolCallCallback) error {
	return nil
}

func (t *testModelInfoLLM) GenerateStructured(ctx context.Context, prompt string, schema interface{}, opts *domain.GenerationOptions) (*domain.StructuredResult, error) {
	return &domain.StructuredResult{}, nil
}

func (t *testModelInfoLLM) RecognizeIntent(ctx context.Context, request string) (*domain.IntentResult, error) {
	return &domain.IntentResult{}, nil
}

func TestResolveServiceModelInfoPrefersInjectedLLMMetadata(t *testing.T) {
	llm := &testModelInfoLLM{
		model:   "actual-model",
		baseURL: "https://example.test/v1",
	}
	cfg := &config.Config{}
	cfg.LLM.Providers = []pool.Provider{
		{ModelName: "config-model", BaseURL: "https://config.test/v1"},
	}

	modelName, baseURL := resolveServiceModelInfo(llm, cfg)
	if modelName != "actual-model" {
		t.Fatalf("expected actual model, got %q", modelName)
	}
	if baseURL != "https://example.test/v1" {
		t.Fatalf("expected actual base URL, got %q", baseURL)
	}
}

func TestResolveServiceModelInfoFallsBackToConfig(t *testing.T) {
	cfg := &config.Config{}
	cfg.LLM.Providers = []pool.Provider{
		{ModelName: "config-model", BaseURL: "https://config.test/v1"},
	}

	modelName, baseURL := resolveServiceModelInfo(nil, cfg)
	if modelName != "config-model" {
		t.Fatalf("expected config model, got %q", modelName)
	}
	if baseURL != "https://config.test/v1" {
		t.Fatalf("expected config base URL, got %q", baseURL)
	}
}
