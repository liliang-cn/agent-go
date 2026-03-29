package handler

import (
	"encoding/json"
	"fmt"

	"github.com/liliang-cn/agent-go/v2/pkg/config"
	"github.com/liliang-cn/agent-go/v2/pkg/pool"
	"github.com/liliang-cn/agent-go/v2/pkg/store"
)

func withAgentGoDB(cfg *config.Config, fn func(*store.AgentGoDB) error) error {
	if cfg == nil {
		return fmt.Errorf("config is required")
	}
	db, err := store.NewAgentGoDB(cfg.AgentDBPath())
	if err != nil {
		return fmt.Errorf("open agentgo db: %w", err)
	}
	defer db.Close()
	return fn(db)
}

func saveDBConfigValue(cfg *config.Config, key, value string) error {
	return withAgentGoDB(cfg, func(db *store.AgentGoDB) error {
		if err := db.SaveConfig(key, value); err != nil {
			return fmt.Errorf("save %s: %w", key, err)
		}
		return nil
	})
}

func saveDBStringSliceValue(cfg *config.Config, key string, values []string) error {
	raw, err := json.Marshal(values)
	if err != nil {
		return fmt.Errorf("marshal %s: %w", key, err)
	}
	return saveDBConfigValue(cfg, key, string(raw))
}

func runtimeInitialized(cfg *config.Config) bool {
	initialized := false
	_ = withAgentGoDB(cfg, func(db *store.AgentGoDB) error {
		providers, err := db.ListProviders()
		if err != nil {
			return err
		}
		for _, provider := range providers {
			if provider != nil && provider.Enabled {
				initialized = true
				break
			}
		}
		return nil
	})
	return initialized
}

func saveSetupProviderState(cfg *config.Config, provider SetupProvider) error {
	return withAgentGoDB(cfg, func(db *store.AgentGoDB) error {
		if err := db.SaveConfig("llm.strategy", string(pool.StrategyRoundRobin)); err != nil {
			return fmt.Errorf("save llm.strategy: %w", err)
		}
		if err := db.SaveProvider(&store.LLMProvider{
			Name:           provider.Name,
			BaseURL:        provider.BaseURL,
			Key:            provider.APIKey,
			ModelName:      provider.ModelName,
			MaxConcurrency: provider.MaxConcurrency,
			Capability:     provider.Capability,
			Enabled:        true,
		}); err != nil {
			return fmt.Errorf("save llm provider: %w", err)
		}

		if err := db.SaveConfig("rag.embedding_model", provider.EmbeddingModel); err != nil {
			return fmt.Errorf("save rag.embedding_model: %w", err)
		}

		if err := db.SaveConfig("embedding.strategy", string(pool.StrategyRoundRobin)); err != nil {
			return fmt.Errorf("save embedding.strategy: %w", err)
		}
		if provider.EmbeddingModel == "" {
			if existing, err := db.GetEmbeddingProvider(provider.Name); err == nil {
				existing.Enabled = false
				if err := db.SaveEmbeddingProvider(existing); err != nil {
					return fmt.Errorf("disable embedding provider: %w", err)
				}
			}
			return nil
		}

		if err := db.SaveEmbeddingProvider(&store.EmbeddingProvider{
			Name:           provider.Name,
			BaseURL:        provider.BaseURL,
			Key:            provider.APIKey,
			ModelName:      provider.EmbeddingModel,
			MaxConcurrency: provider.MaxConcurrency,
			Capability:     provider.Capability,
			Enabled:        true,
		}); err != nil {
			return fmt.Errorf("save embedding provider: %w", err)
		}
		return nil
	})
}
