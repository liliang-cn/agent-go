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

func TestGlobalPoolServiceSaveLLMPoolConfigPersistsEmbeddingModel(t *testing.T) {
	cfg := &config.Config{}
	cfg.Home = t.TempDir()
	cfg.ApplyHomeLayout()
	cfg.LLM.Enabled = true
	cfg.LLM.Strategy = pool.StrategyLeastLoad
	cfg.LLM.Providers = []pool.Provider{
		{Name: "openai_local", BaseURL: "http://local.example/v1", Key: "x", ModelName: "gpt-oss", MaxConcurrency: 2, Capability: 5},
	}
	cfg.RAG.Enabled = true
	cfg.RAG.EmbeddingModel = "embed-v1"

	svc := &GlobalPoolService{}
	if err := svc.Initialize(context.Background(), cfg); err != nil {
		t.Fatalf("Initialize failed: %v", err)
	}

	llmCfg, err := svc.GetLLMPoolConfig()
	if err != nil {
		t.Fatalf("GetLLMPoolConfig failed: %v", err)
	}
	if llmCfg.EmbeddingModel != "embed-v1" {
		t.Fatalf("expected initial embedding model embed-v1, got %q", llmCfg.EmbeddingModel)
	}

	llmCfg.Strategy = pool.StrategyRandom
	llmCfg.EmbeddingModel = "embed-v2"
	if err := svc.SaveLLMPoolConfig(*llmCfg); err != nil {
		t.Fatalf("SaveLLMPoolConfig failed: %v", err)
	}

	savedCfg, err := svc.GetLLMPoolConfig()
	if err != nil {
		t.Fatalf("GetLLMPoolConfig after save failed: %v", err)
	}
	if savedCfg.Strategy != pool.StrategyRandom {
		t.Fatalf("expected saved strategy %q, got %q", pool.StrategyRandom, savedCfg.Strategy)
	}
	if savedCfg.EmbeddingModel != "embed-v2" {
		t.Fatalf("expected saved embedding model embed-v2, got %q", savedCfg.EmbeddingModel)
	}

	restartedCfg := &config.Config{}
	restartedCfg.Home = cfg.Home
	restartedCfg.ApplyHomeLayout()
	restartedCfg.LLM.Enabled = true
	restartedCfg.LLM.Strategy = pool.StrategyLeastLoad
	restartedCfg.LLM.Providers = cfg.LLM.Providers
	restartedCfg.RAG.Enabled = true

	restarted := &GlobalPoolService{}
	if err := restarted.Initialize(context.Background(), restartedCfg); err != nil {
		t.Fatalf("restarted Initialize failed: %v", err)
	}

	persistedCfg, err := restarted.GetLLMPoolConfig()
	if err != nil {
		t.Fatalf("restarted GetLLMPoolConfig failed: %v", err)
	}
	if persistedCfg.EmbeddingModel != "embed-v2" {
		t.Fatalf("expected persisted embedding model embed-v2, got %q", persistedCfg.EmbeddingModel)
	}

	embeddingClient, err := restarted.GetEmbedding()
	if err != nil {
		t.Fatalf("GetEmbedding failed: %v", err)
	}
	defer restarted.ReleaseEmbedding(embeddingClient)

	if got := embeddingClient.GetModelName(); got != "embed-v2" {
		t.Fatalf("expected embedding client model embed-v2, got %q", got)
	}
}
