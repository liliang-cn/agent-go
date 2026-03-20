package main

import (
	"context"
	"fmt"
	"log"

	"github.com/google/uuid"
	"github.com/liliang-cn/agent-go/v2/pkg/domain"
	"github.com/liliang-cn/agent-go/v2/pkg/memory"
	"github.com/liliang-cn/agent-go/v2/pkg/providers"
	"github.com/liliang-cn/agent-go/v2/pkg/store"
)

func main() {
	ctx := context.Background()

	memStore, _ := store.NewMemoryStore("/tmp/agentgo-test-retrieve.db")
	defer memStore.Close()

	provConfig := &domain.OpenAIProviderConfig{
		BaseProviderConfig: domain.BaseProviderConfig{Timeout: 30},
		BaseURL:            "http://localhost:11434/v1",
		APIKey:             "ollama",
		EmbeddingModel:     "qwen3-embedding:8b",
	}
	factory := providers.NewFactory()
	embedder, _ := factory.CreateEmbedderProvider(ctx, provConfig)

	cfg := memory.DefaultConfig()
	cfg.ScoringConfig = nil
	cfg.NoiseFilterConfig = nil
	memSvc := memory.NewService(memStore, nil, embedder, cfg)

	// Add 4 memories
	mems := []struct {
		content string
		typ     domain.MemoryType
	}{
		{"我住在上海浦东", domain.MemoryTypeFact},
		{"我是一名软件工程师", domain.MemoryTypeFact},
		{"我擅长 Python 编程", domain.MemoryTypeSkill},
		{"我喜欢吃火锅", domain.MemoryTypePreference},
	}
	for _, m := range mems {
		memSvc.Add(ctx, &domain.Memory{
			ID: uuid.New().String(), Type: m.typ, Content: m.content,
			Importance: 0.8, SessionID: "user-001",
		})
	}

	// Test exact sequence from before
	queries := []string{
		"我住在哪个城市？",
		"上海",
		"编程",
	}

	for _, q := range queries {
		fmt.Printf("\n=== Query: %s ===\n", q)
		results, err := memSvc.Search(ctx, q, 5)
		if err != nil {
			log.Printf("Error: %v", err)
		}
		fmt.Printf("Results: %d\n", len(results))
		for i, r := range results {
			fmt.Printf("  %d. [%.2f] %s\n", i+1, r.Score, r.Content)
		}
	}
}
