package agent

import (
	"context"
	"testing"

	"github.com/liliang-cn/agent-go/v3/pkg/config"
	"github.com/liliang-cn/agent-go/v3/pkg/domain"
	memorypkg "github.com/liliang-cn/agent-go/v3/pkg/memory"
	"github.com/liliang-cn/agent-go/v3/pkg/pool"
)

type testModelInfoLLM struct {
	model   string
	baseURL string
	fast    bool
}

func (t *testModelInfoLLM) GetModelName() string { return t.model }
func (t *testModelInfoLLM) GetBaseURL() string   { return t.baseURL }
func (t *testModelInfoLLM) IsFastModel() bool    { return t.fast }

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
	result, err := t.GenerateWithTools(ctx, messages, tools, opts)
	if err != nil {
		return err
	}
	return callback(result)
}

func (t *testModelInfoLLM) GenerateStructured(ctx context.Context, prompt string, schema interface{}, opts *domain.GenerationOptions) (*domain.StructuredResult, error) {
	return &domain.StructuredResult{}, nil
}

func (t *testModelInfoLLM) RecognizeIntent(ctx context.Context, request string) (*domain.IntentResult, error) {
	return &domain.IntentResult{}, nil
}

type testModelIdentityOnlyLLM struct {
	model   string
	baseURL string
}

func (t *testModelIdentityOnlyLLM) GetModelName() string { return t.model }
func (t *testModelIdentityOnlyLLM) GetBaseURL() string   { return t.baseURL }

func (t *testModelIdentityOnlyLLM) Generate(ctx context.Context, prompt string, opts *domain.GenerationOptions) (string, error) {
	return "", nil
}

func (t *testModelIdentityOnlyLLM) Stream(ctx context.Context, prompt string, opts *domain.GenerationOptions, callback func(string)) error {
	return nil
}

func (t *testModelIdentityOnlyLLM) GenerateWithTools(ctx context.Context, messages []domain.Message, tools []domain.ToolDefinition, opts *domain.GenerationOptions) (*domain.GenerationResult, error) {
	return &domain.GenerationResult{}, nil
}

func (t *testModelIdentityOnlyLLM) StreamWithTools(ctx context.Context, messages []domain.Message, tools []domain.ToolDefinition, opts *domain.GenerationOptions, callback domain.ToolCallCallback) error {
	result, err := t.GenerateWithTools(ctx, messages, tools, opts)
	if err != nil {
		return err
	}
	return callback(result)
}

func (t *testModelIdentityOnlyLLM) GenerateStructured(ctx context.Context, prompt string, schema interface{}, opts *domain.GenerationOptions) (*domain.StructuredResult, error) {
	return &domain.StructuredResult{}, nil
}

func (t *testModelIdentityOnlyLLM) RecognizeIntent(ctx context.Context, request string) (*domain.IntentResult, error) {
	return &domain.IntentResult{}, nil
}

func TestResolveServiceModelInfoPrefersInjectedLLMMetadata(t *testing.T) {
	llm := &testModelInfoLLM{
		model:   "actual-model",
		baseURL: "https://example.test/v1",
		fast:    true,
	}
	cfg := &config.Config{}
	cfg.LLM.Providers = []pool.Provider{
		{ModelName: "config-model", BaseURL: "https://config.test/v1"},
	}

	modelName, baseURL, isFastModel := resolveServiceModelInfo(llm, cfg)
	if modelName != "actual-model" {
		t.Fatalf("expected actual model, got %q", modelName)
	}
	if baseURL != "https://example.test/v1" {
		t.Fatalf("expected actual base URL, got %q", baseURL)
	}
	if !isFastModel {
		t.Fatal("expected injected llm metadata to report fast model")
	}
}

func TestResolveServiceModelInfoFallsBackToConfig(t *testing.T) {
	cfg := &config.Config{}
	cfg.LLM.Providers = []pool.Provider{
		{ModelName: "config-model", BaseURL: "https://config.test/v1"},
	}

	modelName, baseURL, isFastModel := resolveServiceModelInfo(nil, cfg)
	if modelName != "config-model" {
		t.Fatalf("expected config model, got %q", modelName)
	}
	if baseURL != "https://config.test/v1" {
		t.Fatalf("expected config base URL, got %q", baseURL)
	}
	if isFastModel {
		t.Fatal("expected config-model to not be classified as fast")
	}
}

func TestResolveServiceModelInfoClassifiesFastConfigModel(t *testing.T) {
	cfg := &config.Config{}
	cfg.LLM.Providers = []pool.Provider{
		{ModelName: "gpt-5-mini", BaseURL: "https://config.test/v1"},
	}

	modelName, baseURL, isFastModel := resolveServiceModelInfo(nil, cfg)
	if modelName != "gpt-5-mini" {
		t.Fatalf("expected config model, got %q", modelName)
	}
	if baseURL != "https://config.test/v1" {
		t.Fatalf("expected config base URL, got %q", baseURL)
	}
	if !isFastModel {
		t.Fatal("expected gpt-5-mini to be classified as fast")
	}
}

func TestResolveServiceModelInfoFastModelIsOptional(t *testing.T) {
	llm := &testModelIdentityOnlyLLM{
		model:   "gpt-5-mini",
		baseURL: "https://example.test/v1",
	}

	modelName, baseURL, isFastModel := resolveServiceModelInfo(llm, nil)
	if modelName != "gpt-5-mini" {
		t.Fatalf("expected model name, got %q", modelName)
	}
	if baseURL != "https://example.test/v1" {
		t.Fatalf("expected base URL, got %q", baseURL)
	}
	if !isFastModel {
		t.Fatal("expected fast model to be inferred from model name when IsFastModel is not implemented")
	}
}

func TestBuildMemoryServiceKeepsCortexStoreWithoutEmbedder(t *testing.T) {
	home := t.TempDir()
	cfg := testAgentConfig(home)
	if err := cfg.SetMemoryStoreTypeString("cortex"); err != nil {
		t.Fatalf("SetMemoryStoreTypeString() error = %v", err)
	}

	builder := New("memory-agent").WithConfig(cfg).WithMemory(WithMemoryStoreType("cortex"))

	memSvc, storeType, err := builder.buildMemoryService(cfg, nil, nil)
	if err != nil {
		t.Fatalf("buildMemoryService() error = %v", err)
	}
	if storeType != "cortex" {
		t.Fatalf("buildMemoryService() storeType = %q, want cortex", storeType)
	}
	if _, ok := memSvc.(*memorypkg.Service); !ok {
		t.Fatalf("buildMemoryService() returned %T, want *memory.Service", memSvc)
	}
}

func TestBuildMemoryServiceRejectsVectorStoreType(t *testing.T) {
	home := t.TempDir()
	cfg := testAgentConfig(home)
	if err := cfg.SetMemoryStoreTypeString("vector"); err == nil {
		t.Fatal("expected vector store type to be rejected")
	}
}

// usageOnlyLLM knows its model only the way a usage meter asks — the shape
// providers.OpenAILLMProvider has.
type usageOnlyLLM struct{ captureStreamLLM }

func (u *usageOnlyLLM) UsageModel() string { return "usage-only-model" }

// A provider that reports its model for usage accounting must not leave the
// service nameless: with no name every turn is unpriced and the cost
// ceilings do nothing.
func TestServiceModelFallsBackToUsageModel(t *testing.T) {
	var gen interface {
		domain.Generator
		UsageModel() string
	} = &usageOnlyLLM{}
	svc, err := New("usage-only").WithConfig(testAgentConfig(t.TempDir())).WithLLM(gen).Build()
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
	if got := svc.Info().Model; got != "usage-only-model" {
		t.Fatalf("Info().Model = %q, want the usage model", got)
	}
}
