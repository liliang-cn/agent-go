package providers

import (
	"context"
	"testing"

	"github.com/liliang-cn/agent-go/v2/pkg/domain"
)

func TestFactoryCreateProviders(t *testing.T) {
	factory := NewFactory()
	ctx := context.Background()

	t.Run("CreateOpenAILLMProvider", func(t *testing.T) {
		config := &domain.OpenAIProviderConfig{
			BaseProviderConfig: domain.BaseProviderConfig{
				Type: domain.ProviderOpenAI,
			},
			BaseURL:        "http://localhost:11434",
			APIKey:         "ollama",
			EmbeddingModel: "nomic-embed-text",
			LLMModel:       "qwen3",
		}

		provider, err := factory.CreateLLMProvider(ctx, config)
		if err != nil {
			t.Fatalf("Failed to create OpenAI LLM provider: %v", err)
		}

		if provider.ProviderType() != domain.ProviderOpenAI {
			t.Errorf("Expected provider type %s, got %s", domain.ProviderOpenAI, provider.ProviderType())
		}
	})

	t.Run("CreateOpenAIEmbedderProvider", func(t *testing.T) {
		config := &domain.OpenAIProviderConfig{
			BaseProviderConfig: domain.BaseProviderConfig{
				Type: domain.ProviderOpenAI,
			},
			BaseURL:        "http://localhost:11434",
			APIKey:         "ollama",
			EmbeddingModel: "nomic-embed-text",
			LLMModel:       "qwen3",
		}

		provider, err := factory.CreateEmbedderProvider(ctx, config)
		if err != nil {
			t.Fatalf("Failed to create OpenAI embedder provider: %v", err)
		}

		if provider.ProviderType() != domain.ProviderOpenAI {
			t.Errorf("Expected provider type %s, got %s", domain.ProviderOpenAI, provider.ProviderType())
		}
	})

	t.Run("CreateOpenAILLMProvider", func(t *testing.T) {
		config := &domain.OpenAIProviderConfig{
			BaseProviderConfig: domain.BaseProviderConfig{
				Type: domain.ProviderOpenAI,
			},
			BaseURL:        "https://api.openai.com/v1",
			APIKey:         "test-key",
			EmbeddingModel: "text-embedding-3-small",
			LLMModel:       "gpt-4",
		}

		provider, err := factory.CreateLLMProvider(ctx, config)
		if err != nil {
			t.Fatalf("Failed to create OpenAI LLM provider: %v", err)
		}

		if provider.ProviderType() != domain.ProviderOpenAI {
			t.Errorf("Expected provider type %s, got %s", domain.ProviderOpenAI, provider.ProviderType())
		}
	})

	t.Run("CreateOpenAIEmbedderProvider", func(t *testing.T) {
		config := &domain.OpenAIProviderConfig{
			BaseProviderConfig: domain.BaseProviderConfig{
				Type: domain.ProviderOpenAI,
			},
			BaseURL:        "https://api.openai.com/v1",
			APIKey:         "test-key",
			EmbeddingModel: "text-embedding-3-small",
			LLMModel:       "gpt-4",
		}

		provider, err := factory.CreateEmbedderProvider(ctx, config)
		if err != nil {
			t.Fatalf("Failed to create OpenAI embedder provider: %v", err)
		}

		if provider.ProviderType() != domain.ProviderOpenAI {
			t.Errorf("Expected provider type %s, got %s", domain.ProviderOpenAI, provider.ProviderType())
		}
	})

	t.Run("UnsupportedProviderType", func(t *testing.T) {
		_, err := factory.CreateLLMProvider(ctx, "unsupported-config")
		if err == nil {
			t.Error("Expected error for unsupported provider config type")
		}

		_, err = factory.CreateEmbedderProvider(ctx, "unsupported-config")
		if err == nil {
			t.Error("Expected error for unsupported provider config type")
		}
	})

	t.Run("CreateFromMapConfig", func(t *testing.T) {
		// Test creating OpenAI provider from map config
		mapConfig := map[string]interface{}{
			"type":            "openai",
			"base_url":        "http://localhost:11434",
			"api_key":         "ollama",
			"llm_model":       "qwen3",
			"embedding_model": "nomic-embed-text",
			"timeout":         "30s",
		}

		provider, err := factory.CreateLLMProvider(ctx, mapConfig)
		if err != nil {
			t.Fatalf("Failed to create LLM provider from map config: %v", err)
		}
		if provider == nil {
			t.Fatal("Expected non-nil provider")
		}
		if provider.ProviderType() != domain.ProviderOpenAI {
			t.Errorf("Expected provider type %s, got %s", domain.ProviderOpenAI, provider.ProviderType())
		}

		// Test creating embedder from map config
		embedder, err := factory.CreateEmbedderProvider(ctx, mapConfig)
		if err != nil {
			t.Fatalf("Failed to create embedder provider from map config: %v", err)
		}
		if embedder == nil {
			t.Fatal("Expected non-nil embedder")
		}
		if embedder.ProviderType() != domain.ProviderOpenAI {
			t.Errorf("Expected embedder type %s, got %s", domain.ProviderOpenAI, embedder.ProviderType())
		}
	})
}

func TestDetectProviderType(t *testing.T) {
	tests := []struct {
		name        string
		config      interface{}
		expected    domain.ProviderType
		expectError bool
	}{
		{
			name: "Legacy ollama type (now unsupported)",
			config: map[string]interface{}{
				"type": "ollama",
			},
			expected:    "",
			expectError: true,
		},
		{
			name: "Valid openai type",
			config: map[string]interface{}{
				"type": "openai",
			},
			expected:    domain.ProviderOpenAI,
			expectError: false,
		},
		{
			name: "Legacy lmstudio type (now unsupported)",
			config: map[string]interface{}{
				"type": "lmstudio",
			},
			expected:    "",
			expectError: true,
		},
		{
			name: "Missing type field",
			config: map[string]interface{}{
				"base_url": "http://localhost",
			},
			expected:    "",
			expectError: true,
		},
		{
			name: "Invalid type value",
			config: map[string]interface{}{
				"type": "invalid",
			},
			expected:    "",
			expectError: true,
		},
		{
			name:        "Non-map config",
			config:      "not a map",
			expected:    "",
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := DetectProviderType(tt.config)
			if tt.expectError {
				if err == nil {
					t.Error("Expected error but got none")
				}
			} else {
				if err != nil {
					t.Errorf("Unexpected error: %v", err)
				}
				if result != tt.expected {
					t.Errorf("Expected %s, got %s", tt.expected, result)
				}
			}
		})
	}
}

func TestProviderConfigHelpers(t *testing.T) {
	t.Run("DetermineProviderType", func(t *testing.T) {
		// Test OpenAI config
		ollamaConfig := &domain.ProviderConfig{
			OpenAI: &domain.OpenAIProviderConfig{},
		}
		providerType, err := DetermineProviderType(ollamaConfig)
		if err != nil {
			t.Fatalf("Failed to determine provider type for OpenAI: %v", err)
		}
		if providerType != domain.ProviderOpenAI {
			t.Errorf("Expected %s, got %s", domain.ProviderOpenAI, providerType)
		}

		// Test OpenAI config
		openaiConfig := &domain.ProviderConfig{
			OpenAI: &domain.OpenAIProviderConfig{},
		}
		providerType, err = DetermineProviderType(openaiConfig)
		if err != nil {
			t.Fatalf("Failed to determine provider type for OpenAI: %v", err)
		}
		if providerType != domain.ProviderOpenAI {
			t.Errorf("Expected %s, got %s", domain.ProviderOpenAI, providerType)
		}

		// Test empty config
		emptyConfig := &domain.ProviderConfig{}
		_, err = DetermineProviderType(emptyConfig)
		if err == nil {
			t.Error("Expected error for empty provider config")
		}
	})

	t.Run("GetProviderConfig", func(t *testing.T) {
		ollamaProviderConfig := &domain.OpenAIProviderConfig{
			BaseURL: "http://localhost:11434",
		}
		config := &domain.ProviderConfig{
			OpenAI: ollamaProviderConfig,
		}

		result, err := GetProviderConfig(config)
		if err != nil {
			t.Fatalf("Failed to get provider config: %v", err)
		}

		if result != ollamaProviderConfig {
			t.Error("Expected same config instance")
		}
	})

	t.Run("GetLLMProviderConfig", func(t *testing.T) {
		ollamaProviderConfig := &domain.OpenAIProviderConfig{
			BaseURL: "http://localhost:11434",
		}
		config := &domain.ProviderConfig{
			OpenAI: ollamaProviderConfig,
		}

		result, err := GetLLMProviderConfig(config, "openai")
		if err != nil {
			t.Fatalf("Failed to get LLM provider config: %v", err)
		}

		if result != ollamaProviderConfig {
			t.Error("Expected same config instance")
		}

		// Test unsupported provider
		_, err = GetLLMProviderConfig(config, "unsupported")
		if err == nil {
			t.Error("Expected error for unsupported provider")
		}
	})

	t.Run("GetEmbedderProviderConfig", func(t *testing.T) {
		openaiProviderConfig := &domain.OpenAIProviderConfig{
			BaseURL: "https://api.openai.com/v1",
			APIKey:  "test-key",
		}
		config := &domain.ProviderConfig{
			OpenAI: openaiProviderConfig,
		}

		result, err := GetEmbedderProviderConfig(config, "openai")
		if err != nil {
			t.Fatalf("Failed to get embedder provider config: %v", err)
		}

		if result != openaiProviderConfig {
			t.Error("Expected same config instance")
		}

		// Test missing config
		_, err = GetEmbedderProviderConfig(config, "unsupported")
		if err == nil {
			t.Error("Expected error for missing unsupported config")
		}
	})
}

func TestFactory_CreateOpenAIProviders(t *testing.T) {
	factory := NewFactory()
	ctx := context.Background()

	t.Run("CreateOpenAILLMProvider", func(t *testing.T) {
		config := &domain.OpenAIProviderConfig{
			BaseProviderConfig: domain.BaseProviderConfig{
				Type: domain.ProviderOpenAI,
			},
			BaseURL:        "http://localhost:1234/v1",
			APIKey:         "test-key",
			EmbeddingModel: "embedding-model",
			LLMModel:       "llm-model",
		}

		provider, err := factory.CreateLLMProvider(ctx, config)
		if err != nil {
			t.Fatalf("Failed to create OpenAI LLM provider: %v", err)
		}

		if provider.ProviderType() != domain.ProviderOpenAI {
			t.Errorf("Expected provider type %s, got %s", domain.ProviderOpenAI, provider.ProviderType())
		}
	})

	t.Run("CreateOpenAIEmbedderProvider", func(t *testing.T) {
		config := &domain.OpenAIProviderConfig{
			BaseProviderConfig: domain.BaseProviderConfig{
				Type: domain.ProviderOpenAI,
			},
			BaseURL:        "http://localhost:1234/v1",
			APIKey:         "test-key",
			EmbeddingModel: "embedding-model",
			LLMModel:       "llm-model",
		}

		provider, err := factory.CreateEmbedderProvider(ctx, config)
		if err != nil {
			t.Fatalf("Failed to create OpenAI embedder provider: %v", err)
		}

		if provider.ProviderType() != domain.ProviderOpenAI {
			t.Errorf("Expected provider type %s, got %s", domain.ProviderOpenAI, provider.ProviderType())
		}
	})
}

func TestFactory_Validation(t *testing.T) {
	factory := NewFactory()
	if factory == nil {
		t.Fatal("NewFactory returned nil")
	}
}

func TestDetermineProviderType_AllCases(t *testing.T) {
	testCases := []struct {
		name        string
		config      *domain.ProviderConfig
		expected    domain.ProviderType
		expectError bool
	}{
		{
			name: "OpenAI config",
			config: &domain.ProviderConfig{
				OpenAI: &domain.OpenAIProviderConfig{},
			},
			expected:    domain.ProviderOpenAI,
			expectError: false,
		},
		{
			name: "OpenAI config only",
			config: &domain.ProviderConfig{
				OpenAI: &domain.OpenAIProviderConfig{},
			},
			expected:    domain.ProviderOpenAI,
			expectError: false,
		},
		{
			name:        "Empty config",
			config:      &domain.ProviderConfig{},
			expected:    "",
			expectError: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			result, err := DetermineProviderType(tc.config)
			if tc.expectError && err == nil {
				t.Error("Expected error but got none")
			}
			if !tc.expectError && err != nil {
				t.Errorf("Unexpected error: %v", err)
			}
			if result != tc.expected {
				t.Errorf("Expected %s, got %s", tc.expected, result)
			}
		})
	}
}

func TestGetProviderConfig_AllProviders(t *testing.T) {
	testCases := []struct {
		name        string
		config      *domain.ProviderConfig
		expectError bool
	}{
		{
			name: "OpenAI provider config",
			config: &domain.ProviderConfig{
				OpenAI: &domain.OpenAIProviderConfig{
					BaseURL: "http://localhost:1234/v1",
					APIKey:  "test-key",
				},
			},
			expectError: false,
		},
		{
			name:        "Empty config",
			config:      &domain.ProviderConfig{},
			expectError: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			result, err := GetProviderConfig(tc.config)
			if tc.expectError && err == nil {
				t.Error("Expected error but got none")
			}
			if !tc.expectError && err != nil {
				t.Errorf("Unexpected error: %v", err)
			}
			if !tc.expectError && result == nil {
				t.Error("Expected non-nil result")
			}
		})
	}
}

func TestGetLLMProviderConfig_OpenAI(t *testing.T) {
	config := &domain.ProviderConfig{
		OpenAI: &domain.OpenAIProviderConfig{
			BaseURL: "http://localhost:1234/v1",
		},
	}

	result, err := GetLLMProviderConfig(config, "openai")
	if err != nil {
		t.Fatalf("Failed to get OpenAI provider config: %v", err)
	}

	if result != config.OpenAI {
		t.Error("Expected same config instance")
	}

	// Test missing OpenAI config
	emptyConfig := &domain.ProviderConfig{}
	_, err = GetLLMProviderConfig(emptyConfig, "unsupported")
	if err == nil {
		t.Error("Expected error for missing unsupported config")
	}
}

func TestGetEmbedderProviderConfig_OpenAI(t *testing.T) {
	config := &domain.ProviderConfig{
		OpenAI: &domain.OpenAIProviderConfig{
			BaseURL: "http://localhost:1234/v1",
		},
	}

	result, err := GetEmbedderProviderConfig(config, "openai")
	if err != nil {
		t.Fatalf("Failed to get OpenAI embedder config: %v", err)
	}

	if result != config.OpenAI {
		t.Error("Expected same config instance")
	}

	// Test missing OpenAI config
	emptyConfig := &domain.ProviderConfig{}
	_, err = GetEmbedderProviderConfig(emptyConfig, "unsupported")
	if err == nil {
		t.Error("Expected error for missing unsupported config")
	}
}
