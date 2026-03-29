package services

import (
	"context"
	"testing"

	"github.com/liliang-cn/agent-go/v2/pkg/config"
	"github.com/liliang-cn/agent-go/v2/pkg/pool"
	"github.com/liliang-cn/agent-go/v2/pkg/store"
)

type modelNamer interface {
	GetModelName() string
}

func newPoolTestConfig(t *testing.T) *config.Config {
	t.Helper()
	cfg := &config.Config{}
	cfg.Home = t.TempDir()
	cfg.ApplyHomeLayout()
	return cfg
}

func seedPoolDB(t *testing.T, cfg *config.Config, llmProviders []*store.LLMProvider, embeddingProviders []*store.EmbeddingProvider, llmStrategy, embeddingStrategy, embeddingModel string) {
	t.Helper()
	db, err := store.NewAgentGoDB(cfg.AgentDBPath())
	if err != nil {
		t.Fatalf("new agentgo db failed: %v", err)
	}
	defer db.Close()

	for _, provider := range llmProviders {
		if err := db.SaveProvider(provider); err != nil {
			t.Fatalf("save llm provider failed: %v", err)
		}
	}
	for _, provider := range embeddingProviders {
		if err := db.SaveEmbeddingProvider(provider); err != nil {
			t.Fatalf("save embedding provider failed: %v", err)
		}
	}
	if err := db.SaveConfig("llm.strategy", llmStrategy); err != nil {
		t.Fatalf("save llm.strategy failed: %v", err)
	}
	if err := db.SaveConfig("embedding.strategy", embeddingStrategy); err != nil {
		t.Fatalf("save embedding.strategy failed: %v", err)
	}
	if err := db.SaveConfig("rag.embedding_model", embeddingModel); err != nil {
		t.Fatalf("save rag.embedding_model failed: %v", err)
	}
}

func TestGlobalPoolServiceGetLLMByProviderAndModel(t *testing.T) {
	svc := &GlobalPoolService{}
	cfg := newPoolTestConfig(t)
	seedPoolDB(t, cfg,
		[]*store.LLMProvider{
			{Name: "openai_local", BaseURL: "http://local.example/v1", Key: "x", ModelName: "gpt-oss", MaxConcurrency: 2, Capability: 5, Enabled: true},
			{Name: "deepseek", BaseURL: "http://deepseek.example/v1", Key: "x", ModelName: "deepseek-chat", MaxConcurrency: 2, Capability: 4, Enabled: true},
		},
		nil,
		"least_load",
		"round_robin",
		"",
	)

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
	cfg := newPoolTestConfig(t)
	seedPoolDB(t, cfg,
		[]*store.LLMProvider{
			{Name: "openai_local", BaseURL: "http://local.example/v1", Key: "x", ModelName: "gpt-oss", MaxConcurrency: 2, Capability: 5, Enabled: true},
			{Name: "deepseek", BaseURL: "http://deepseek.example/v1", Key: "x", ModelName: "deepseek-chat", MaxConcurrency: 2, Capability: 4, Enabled: true},
		},
		nil,
		"least_load",
		"round_robin",
		"",
	)

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
	cfg := newPoolTestConfig(t)
	cfg.RAG.Enabled = true
	seedPoolDB(t, cfg,
		[]*store.LLMProvider{
			{Name: "openai_local", BaseURL: "http://local.example/v1", Key: "x", ModelName: "gpt-oss", MaxConcurrency: 2, Capability: 5, Enabled: true},
		},
		nil,
		"least_load",
		"round_robin",
		"embed-v1",
	)

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

func TestGlobalPoolServiceSaveEmbeddingProviderUpdatesExistingProvider(t *testing.T) {
	cfg := newPoolTestConfig(t)
	seedPoolDB(t, cfg,
		[]*store.LLMProvider{
			{Name: "openai_local", BaseURL: "http://local.example/v1", Key: "x", ModelName: "gpt-oss", MaxConcurrency: 2, Capability: 5, Enabled: true},
		},
		nil,
		"least_load",
		"round_robin",
		"",
	)

	svc := &GlobalPoolService{}
	if err := svc.Initialize(context.Background(), cfg); err != nil {
		t.Fatalf("Initialize failed: %v", err)
	}

	initial := &store.EmbeddingProvider{
		Name:           "embedder",
		BaseURL:        "http://embed.example/v1",
		Key:            "k1",
		ModelName:      "embed-v1",
		MaxConcurrency: 2,
		Capability:     3,
		Enabled:        true,
	}
	if err := svc.SaveEmbeddingProvider(initial); err != nil {
		t.Fatalf("SaveEmbeddingProvider initial failed: %v", err)
	}

	updated := &store.EmbeddingProvider{
		Name:           "embedder",
		BaseURL:        "http://embed2.example/v1",
		Key:            "k2",
		ModelName:      "embed-v2",
		MaxConcurrency: 4,
		Capability:     4,
		Enabled:        true,
	}
	if err := svc.SaveEmbeddingProvider(updated); err != nil {
		t.Fatalf("SaveEmbeddingProvider update failed: %v", err)
	}

	got, err := svc.GetEmbeddingProvider("embedder")
	if err != nil {
		t.Fatalf("GetEmbeddingProvider failed: %v", err)
	}
	if got.BaseURL != updated.BaseURL {
		t.Fatalf("expected base URL %q, got %q", updated.BaseURL, got.BaseURL)
	}
	if got.ModelName != updated.ModelName {
		t.Fatalf("expected model %q, got %q", updated.ModelName, got.ModelName)
	}

	client, err := svc.embeddingPool.GetByName("embedder")
	if err != nil {
		t.Fatalf("embeddingPool.GetByName failed: %v", err)
	}
	defer svc.ReleaseEmbedding(client)
	if gotModel := client.GetModelName(); gotModel != "embed-v2" {
		t.Fatalf("expected live embedding pool model %q, got %q", "embed-v2", gotModel)
	}
}
