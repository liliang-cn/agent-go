package services

import (
	"context"
	"testing"

	"github.com/liliang-cn/agent-go/pkg/config"
	"github.com/liliang-cn/agent-go/pkg/pool"
)

type modelNamer interface {
	GetModelName() string
}

func TestGlobalPoolServiceGetLLMByProviderAndModel(t *testing.T) {
	svc := &GlobalPoolService{}
	cfg := &config.Config{}
	cfg.Home = t.TempDir()
	cfg.ApplyHomeLayout()
	cfg.LLM.Enabled = true
	cfg.LLM.Strategy = pool.StrategyLeastLoad
	cfg.LLM.Providers = []pool.Provider{
		{Name: "openai_local", BaseURL: "http://local.example/v1", Key: "x", ModelName: "gpt-oss", MaxConcurrency: 2, Capability: 5},
		{Name: "deepseek", BaseURL: "http://deepseek.example/v1", Key: "x", ModelName: "deepseek-chat", MaxConcurrency: 2, Capability: 4},
	}
	cfg.RAG.Enabled = false

	if err := svc.Initialize(context.Background(), cfg); err != nil {
		t.Fatalf("Initialize failed: %v", err)
	}

	clientByProvider, err := svc.GetLLMByProvider("openai_local")
	if err != nil {
		t.Fatalf("GetLLMByProvider failed: %v", err)
	}
	defer svc.ReleaseLLM(clientByProvider)
	if got := clientByProvider.GetModelName(); got != "gpt-oss" {
		t.Fatalf("expected provider-selected model gpt-oss, got %q", got)
	}

	clientByModel, err := svc.GetLLMByModel("deepseek-chat")
	if err != nil {
		t.Fatalf("GetLLMByModel failed: %v", err)
	}
	defer svc.ReleaseLLM(clientByModel)
	if got := clientByModel.GetModelName(); got != "deepseek-chat" {
		t.Fatalf("expected model-selected model deepseek-chat, got %q", got)
	}
}

func TestGlobalPoolServiceGetLLMServiceByProviderAndModel(t *testing.T) {
	svc := &GlobalPoolService{}
	cfg := &config.Config{}
	cfg.Home = t.TempDir()
	cfg.ApplyHomeLayout()
	cfg.LLM.Enabled = true
	cfg.LLM.Strategy = pool.StrategyLeastLoad
	cfg.LLM.Providers = []pool.Provider{
		{Name: "openai_local", BaseURL: "http://local.example/v1", Key: "x", ModelName: "gpt-oss", MaxConcurrency: 2, Capability: 5},
		{Name: "deepseek", BaseURL: "http://deepseek.example/v1", Key: "x", ModelName: "deepseek-chat", MaxConcurrency: 2, Capability: 4},
	}
	cfg.RAG.Enabled = false

	if err := svc.Initialize(context.Background(), cfg); err != nil {
		t.Fatalf("Initialize failed: %v", err)
	}

	llmByProvider, err := svc.GetLLMServiceByProvider("openai_local")
	if err != nil {
		t.Fatalf("GetLLMServiceByProvider failed: %v", err)
	}
	namedProvider, ok := llmByProvider.(modelNamer)
	if !ok {
		t.Fatalf("expected provider service to expose GetModelName")
	}
	if got := namedProvider.GetModelName(); got != "gpt-oss" {
		t.Fatalf("expected provider-selected service model gpt-oss, got %q", got)
	}

	llmByModel, err := svc.GetLLMServiceByModel("deepseek-chat")
	if err != nil {
		t.Fatalf("GetLLMServiceByModel failed: %v", err)
	}
	namedModel, ok := llmByModel.(modelNamer)
	if !ok {
		t.Fatalf("expected model service to expose GetModelName")
	}
	if got := namedModel.GetModelName(); got != "deepseek-chat" {
		t.Fatalf("expected model-selected service model deepseek-chat, got %q", got)
	}
}
