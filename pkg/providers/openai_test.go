package providers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/liliang-cn/agent-go/v2/pkg/domain"
	"github.com/stretchr/testify/assert"
)

// Test OpenAI LLM Provider

func TestNewOpenAILLMProvider(t *testing.T) {
	tests := []struct {
		name        string
		config      *domain.OpenAIProviderConfig
		shouldError bool
		errorMsg    string
	}{
		{
			name: "Valid config with API key",
			config: &domain.OpenAIProviderConfig{
				APIKey:   "test-api-key",
				LLMModel: "gpt-4",
			},
			shouldError: false,
		},
		{
			name: "Valid config with base URL",
			config: &domain.OpenAIProviderConfig{
				APIKey:   "test-api-key",
				BaseURL:  "https://custom.openai.com",
				LLMModel: "gpt-4",
			},
			shouldError: false,
		},
		{
			name:        "Nil config",
			config:      nil,
			shouldError: true,
			errorMsg:    "config cannot be nil",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			provider, err := NewOpenAILLMProvider(tt.config)

			if tt.shouldError {
				assert.Error(t, err)
				if tt.errorMsg != "" {
					assert.EqualError(t, err, tt.errorMsg)
				}
				assert.Nil(t, provider)
			} else {
				assert.NoError(t, err)
				assert.NotNil(t, provider)
				assert.Equal(t, domain.ProviderOpenAI, provider.ProviderType())
			}
		})
	}
}

func TestOpenAILLMProvider_Generate(t *testing.T) {
	// Note: These tests require mocking the OpenAI client
	// For now, we'll test the validation logic

	provider := &OpenAILLMProvider{
		config: &domain.OpenAIProviderConfig{
			APIKey:   "test-key",
			LLMModel: "gpt-4",
		},
	}

	tests := []struct {
		name        string
		prompt      string
		opts        *domain.GenerationOptions
		shouldError bool
		errorType   error
	}{
		{
			name:        "Empty prompt",
			prompt:      "",
			opts:        nil,
			shouldError: true,
			errorType:   domain.ErrInvalidInput,
		},
		{
			name:   "Valid prompt with options",
			prompt: "Hello, world!",
			opts: &domain.GenerationOptions{
				Temperature: 0.7,
				MaxTokens:   100,
			},
			shouldError: true, // Will fail without mock client
			errorType:   domain.ErrGenerationFailed,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			_, err := provider.Generate(ctx, tt.prompt, tt.opts)

			if tt.shouldError {
				assert.Error(t, err)
				if tt.errorType != nil {
					assert.True(t, errors.Is(err, tt.errorType))
				}
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestOpenAILLMProvider_Stream(t *testing.T) {
	provider := &OpenAILLMProvider{
		config: &domain.OpenAIProviderConfig{
			APIKey:   "test-key",
			LLMModel: "gpt-4",
		},
	}

	tests := []struct {
		name        string
		prompt      string
		callback    func(string)
		shouldError bool
		errorType   error
	}{
		{
			name:        "Empty prompt",
			prompt:      "",
			callback:    func(s string) {},
			shouldError: true,
			errorType:   domain.ErrInvalidInput,
		},
		{
			name:        "Nil callback",
			prompt:      "test prompt",
			callback:    nil,
			shouldError: true,
			errorType:   domain.ErrInvalidInput,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			err := provider.Stream(ctx, tt.prompt, nil, tt.callback)

			if tt.shouldError {
				assert.Error(t, err)
				if tt.errorType != nil {
					assert.True(t, errors.Is(err, tt.errorType))
				}
			}
		})
	}
}

func TestToOpenAIMessages(t *testing.T) {
	tests := []struct {
		name        string
		messages    []domain.Message
		shouldError bool
		errorMsg    string
	}{
		{
			name: "Mixed message types",
			messages: []domain.Message{
				{Role: "system", Content: "You are a helpful assistant"},
				{Role: "user", Content: "Hello"},
				{Role: "assistant", Content: "Hi there!"},
				{Role: "tool", Content: "Tool result", ToolCallID: "tool-1"},
			},
			shouldError: false,
		},
		{
			name: "Assistant with tool calls",
			messages: []domain.Message{
				{
					Role:    "assistant",
					Content: "Let me help with that",
					ToolCalls: []domain.ToolCall{
						{
							ID:   "call-1",
							Type: "function",
							Function: domain.FunctionCall{
								Name: "get_weather",
								Arguments: map[string]interface{}{
									"location": "New York",
								},
							},
						},
					},
				},
			},
			shouldError: false,
		},
		{
			name: "Unknown role",
			messages: []domain.Message{
				{Role: "unknown", Content: "test"},
			},
			shouldError: true,
			errorMsg:    "unknown message role: unknown",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := toOpenAIMessages(tt.messages)

			if tt.shouldError {
				assert.Error(t, err)
				if tt.errorMsg != "" {
					assert.EqualError(t, err, tt.errorMsg)
				}
				assert.Nil(t, result)
			} else {
				assert.NoError(t, err)
				assert.NotNil(t, result)
				assert.Equal(t, len(tt.messages), len(result))
			}
		})
	}
}

func TestToOpenAIMessagesNormalizesToolCallIDs(t *testing.T) {
	messages := []domain.Message{
		{
			Role:    "assistant",
			Content: "Let me call a tool",
			ToolCalls: []domain.ToolCall{
				{
					ID:   "call_legacy_1",
					Type: "function",
					Function: domain.FunctionCall{
						Name:      "lookup",
						Arguments: map[string]interface{}{"q": "gold"},
					},
				},
			},
		},
		{
			Role:       "tool",
			Content:    "ok",
			ToolCallID: "call_legacy_1",
		},
	}

	result, err := toOpenAIMessages(messages)
	if err != nil {
		t.Fatalf("toOpenAIMessages() error = %v", err)
	}
	if len(result) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(result))
	}

	serialized, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	got := string(serialized)
	if !strings.Contains(got, "\"id\":\"fc_call_legacy_1\"") {
		t.Fatalf("expected normalized assistant tool_call id, got %s", got)
	}
	if !strings.Contains(got, "\"tool_call_id\":\"fc_call_legacy_1\"") {
		t.Fatalf("expected normalized tool message id, got %s", got)
	}
}

func TestOpenAILLMProvider_GenerateWithTools(t *testing.T) {
	provider := &OpenAILLMProvider{
		config: &domain.OpenAIProviderConfig{
			APIKey:   "test-key",
			LLMModel: "gpt-4",
		},
	}

	tests := []struct {
		name        string
		messages    []domain.Message
		tools       []domain.ToolDefinition
		opts        *domain.GenerationOptions
		shouldError bool
		errorType   error
	}{
		{
			name:        "Empty messages",
			messages:    []domain.Message{},
			tools:       []domain.ToolDefinition{},
			shouldError: true,
			errorType:   domain.ErrInvalidInput,
		},
		{
			name: "Valid messages with tools",
			messages: []domain.Message{
				{Role: "user", Content: "What's the weather?"},
			},
			tools: []domain.ToolDefinition{
				{
					Type: "function",
					Function: domain.ToolFunction{
						Name:        "get_weather",
						Description: "Get current weather",
						Parameters: map[string]interface{}{
							"type": "object",
							"properties": map[string]interface{}{
								"location": map[string]interface{}{
									"type":        "string",
									"description": "The city name",
								},
							},
						},
					},
				},
			},
			shouldError: true, // Will fail without mock client
			errorType:   domain.ErrGenerationFailed,
		},
		{
			name: "Tool choice required",
			messages: []domain.Message{
				{Role: "user", Content: "Call a tool"},
			},
			tools: []domain.ToolDefinition{
				{
					Type: "function",
					Function: domain.ToolFunction{
						Name: "test_tool",
					},
				},
			},
			opts: &domain.GenerationOptions{
				ToolChoice: "required",
			},
			shouldError: true, // Will fail without mock client
			errorType:   domain.ErrGenerationFailed,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			_, err := provider.GenerateWithTools(ctx, tt.messages, tt.tools, tt.opts)

			if tt.shouldError {
				assert.Error(t, err)
				if tt.errorType != nil {
					assert.True(t, errors.Is(err, tt.errorType))
				}
			}
		})
	}
}

func TestOpenAILLMProvider_StreamWithTools(t *testing.T) {
	provider := &OpenAILLMProvider{
		config: &domain.OpenAIProviderConfig{
			APIKey:   "test-key",
			LLMModel: "gpt-4",
		},
	}

	tests := []struct {
		name        string
		messages    []domain.Message
		tools       []domain.ToolDefinition
		callback    domain.ToolCallCallback
		shouldError bool
		errorType   error
	}{
		{
			name:        "Empty messages",
			messages:    []domain.Message{},
			callback:    func(delta *domain.GenerationResult) error { return nil },
			shouldError: true,
			errorType:   domain.ErrInvalidInput,
		},
		{
			name: "Nil callback",
			messages: []domain.Message{
				{Role: "user", Content: "test"},
			},
			callback:    nil,
			shouldError: true,
			errorType:   domain.ErrInvalidInput,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			err := provider.StreamWithTools(ctx, tt.messages, tt.tools, nil, tt.callback)

			if tt.shouldError {
				assert.Error(t, err)
				if tt.errorType != nil {
					assert.True(t, errors.Is(err, tt.errorType))
				}
			}
		})
	}
}

func TestOpenAILLMProvider_GenerateStructured(t *testing.T) {
	provider := &OpenAILLMProvider{
		config: &domain.OpenAIProviderConfig{
			APIKey:   "test-key",
			LLMModel: "gpt-4",
		},
	}

	type TestSchema struct {
		Name string `json:"name"`
		Age  int    `json:"age"`
	}

	tests := []struct {
		name        string
		prompt      string
		schema      interface{}
		opts        *domain.GenerationOptions
		shouldError bool
	}{
		{
			name:        "Empty prompt",
			prompt:      "",
			schema:      &TestSchema{},
			shouldError: true,
		},
		{
			name:        "Nil schema",
			prompt:      "Generate data",
			schema:      nil,
			shouldError: true,
		},
		{
			name:   "Valid request",
			prompt: "Generate a person",
			schema: &TestSchema{},
			opts: &domain.GenerationOptions{
				Temperature: 0.5,
				MaxTokens:   100,
			},
			shouldError: true, // Will fail without mock client
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			_, err := provider.GenerateStructured(ctx, tt.prompt, tt.schema, tt.opts)

			if tt.shouldError {
				assert.Error(t, err)
			}
		})
	}
}

func TestOpenAILLMProvider_ExtractMetadata(t *testing.T) {
	provider, err := NewOpenAILLMProvider(&domain.OpenAIProviderConfig{
		APIKey:   "test-key",
		LLMModel: "gpt-4",
	})
	assert.NoError(t, err)
	assert.NotNil(t, provider)

	tests := []struct {
		name        string
		content     string
		model       string
		shouldError bool
		errorType   error
	}{
		{
			name:        "Empty content",
			content:     "",
			shouldError: true,
			errorType:   domain.ErrInvalidInput,
		},
		{
			name:        "Valid content",
			content:     "This is a test document about AI and machine learning.",
			model:       "gpt-4",
			shouldError: true, // Will fail without mock client
			errorType:   domain.ErrGenerationFailed,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			_, err := provider.ExtractMetadata(ctx, tt.content, tt.model)

			if tt.shouldError {
				assert.Error(t, err)
				if tt.errorType != nil {
					assert.True(t, errors.Is(err, tt.errorType))
				}
			}
		})
	}
}

// Test OpenAI Embedder Provider

func TestNewOpenAIEmbedderProvider(t *testing.T) {
	tests := []struct {
		name        string
		config      *domain.OpenAIProviderConfig
		shouldError bool
		errorMsg    string
	}{
		{
			name: "Valid config",
			config: &domain.OpenAIProviderConfig{
				APIKey:         "test-api-key",
				EmbeddingModel: "text-embedding-ada-002",
			},
			shouldError: false,
		},
		{
			name: "Valid config with base URL",
			config: &domain.OpenAIProviderConfig{
				APIKey:         "test-api-key",
				BaseURL:        "https://custom.openai.com",
				EmbeddingModel: "text-embedding-ada-002",
			},
			shouldError: false,
		},
		{
			name:        "Nil config",
			config:      nil,
			shouldError: true,
			errorMsg:    "config cannot be nil",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			provider, err := NewOpenAIEmbedderProvider(tt.config)

			if tt.shouldError {
				assert.Error(t, err)
				if tt.errorMsg != "" {
					assert.EqualError(t, err, tt.errorMsg)
				}
				assert.Nil(t, provider)
			} else {
				assert.NoError(t, err)
				assert.NotNil(t, provider)
				assert.Equal(t, domain.ProviderOpenAI, provider.ProviderType())
			}
		})
	}
}

func TestOpenAIEmbedderProvider_Embed(t *testing.T) {
	provider := &OpenAIEmbedderProvider{
		config: &domain.OpenAIProviderConfig{
			APIKey:         "test-key",
			EmbeddingModel: "text-embedding-ada-002",
		},
	}

	tests := []struct {
		name        string
		text        string
		shouldError bool
		errorType   error
	}{
		{
			name:        "Empty text",
			text:        "",
			shouldError: true,
			errorType:   domain.ErrInvalidInput,
		},
		{
			name:        "Valid text",
			text:        "This is a test sentence for embedding.",
			shouldError: true, // Will fail without mock client
			errorType:   domain.ErrEmbeddingFailed,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			_, err := provider.Embed(ctx, tt.text)

			if tt.shouldError {
				assert.Error(t, err)
				if tt.errorType != nil {
					assert.True(t, errors.Is(err, tt.errorType))
				}
			}
		})
	}
}

func TestOpenAIProvider_Health(t *testing.T) {
	t.Run("LLM Provider Health", func(t *testing.T) {
		provider := &OpenAILLMProvider{
			config: &domain.OpenAIProviderConfig{
				APIKey:   "test-key",
				LLMModel: "gpt-4",
			},
		}

		ctx := context.Background()
		err := provider.Health(ctx)

		// Will fail without valid API key
		assert.Error(t, err)
		assert.True(t, errors.Is(err, domain.ErrServiceUnavailable))
	})

	t.Run("Embedder Provider Health", func(t *testing.T) {
		provider := &OpenAIEmbedderProvider{
			config: &domain.OpenAIProviderConfig{
				APIKey:         "test-key",
				EmbeddingModel: "text-embedding-ada-002",
			},
		}

		ctx := context.Background()
		err := provider.Health(ctx)

		// Will fail without valid API key
		assert.Error(t, err)
		assert.True(t, errors.Is(err, domain.ErrServiceUnavailable))
	})
}

// Test edge cases and error scenarios

func TestOpenAIProvider_EdgeCases(t *testing.T) {
	t.Run("Generate with zero temperature", func(t *testing.T) {
		provider := &OpenAILLMProvider{
			config: &domain.OpenAIProviderConfig{
				APIKey:   "test-key",
				LLMModel: "gpt-4",
			},
		}

		opts := &domain.GenerationOptions{
			Temperature: 0,
			MaxTokens:   10,
		}

		ctx := context.Background()
		_, err := provider.Generate(ctx, "test", opts)

		// Should handle zero temperature
		assert.Error(t, err) // Will fail due to missing client
		assert.True(t, errors.Is(err, domain.ErrGenerationFailed))
	})

	t.Run("Generate with negative temperature (should be ignored)", func(t *testing.T) {
		provider := &OpenAILLMProvider{
			config: &domain.OpenAIProviderConfig{
				APIKey:   "test-key",
				LLMModel: "gpt-4",
			},
		}

		opts := &domain.GenerationOptions{
			Temperature: -1, // Should be ignored
		}

		ctx := context.Background()
		_, err := provider.Generate(ctx, "test", opts)

		assert.Error(t, err) // Will fail due to missing client
	})

	t.Run("Tool calls with empty tools array", func(t *testing.T) {
		provider := &OpenAILLMProvider{
			config: &domain.OpenAIProviderConfig{
				APIKey:   "test-key",
				LLMModel: "gpt-4",
			},
		}

		messages := []domain.Message{
			{Role: "user", Content: "Hello"},
		}

		ctx := context.Background()
		_, err := provider.GenerateWithTools(ctx, messages, []domain.ToolDefinition{}, nil)

		assert.Error(t, err) // Will fail due to missing client
	})
}

// Test message conversion details

func TestMessageConversion(t *testing.T) {
	t.Run("Complex tool call arguments", func(t *testing.T) {
		messages := []domain.Message{
			{
				Role:    "assistant",
				Content: "Processing",
				ToolCalls: []domain.ToolCall{
					{
						ID:   "call-1",
						Type: "function",
						Function: domain.FunctionCall{
							Name: "complex_function",
							Arguments: map[string]interface{}{
								"nested": map[string]interface{}{
									"key":   "value",
									"array": []string{"a", "b", "c"},
								},
								"number": 42,
								"bool":   true,
							},
						},
					},
				},
			},
		}

		result, err := toOpenAIMessages(messages)
		assert.NoError(t, err)
		assert.NotNil(t, result)
		assert.Len(t, result, 1)
	})

	t.Run("Empty tool calls array", func(t *testing.T) {
		messages := []domain.Message{
			{
				Role:      "assistant",
				Content:   "No tools needed",
				ToolCalls: []domain.ToolCall{},
			},
		}

		result, err := toOpenAIMessages(messages)
		assert.NoError(t, err)
		assert.NotNil(t, result)
		assert.Len(t, result, 1)
	})
}

func TestOpenAILLMProviderGenerateWithToolsIncludesNativeWebSearch(t *testing.T) {
	var captured map[string]interface{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		assert.Equal(t, "/chat/completions", r.URL.Path)
		assert.NoError(t, json.NewDecoder(r.Body).Decode(&captured))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"ok"}}]}`))
	}))
	defer server.Close()

	provider, err := NewOpenAILLMProvider(&domain.OpenAIProviderConfig{
		APIKey:   "test-key",
		BaseURL:  server.URL,
		LLMModel: "gpt-4o-mini",
	})
	assert.NoError(t, err)

	result, err := provider.GenerateWithTools(context.Background(), []domain.Message{
		{Role: "user", Content: "latest news"},
	}, []domain.ToolDefinition{
		{
			Type: "function",
			Function: domain.ToolFunction{
				Name:        "mcp_websearch_search",
				Description: "search the web",
				Parameters: map[string]interface{}{
					"type": "object",
				},
			},
		},
	}, &domain.GenerationOptions{
		WebSearchMode:        domain.WebSearchModeNative,
		WebSearchContextSize: "high",
	})
	assert.NoError(t, err)
	assert.Equal(t, "ok", result.Content)

	webSearchOptions, ok := captured["web_search_options"].(map[string]interface{})
	assert.True(t, ok)
	assert.Equal(t, "high", webSearchOptions["search_context_size"])
}

func TestOpenAILLMProviderGenerateWithToolsRetriesAutoWithoutNativeWebSearch(t *testing.T) {
	var requestCount atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		requestCount.Add(1)
		var payload map[string]interface{}
		assert.NoError(t, json.NewDecoder(r.Body).Decode(&payload))

		if _, ok := payload["web_search_options"]; ok {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"message":"unsupported parameter: web_search_options"}}`))
			return
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"fallback ok"}}]}`))
	}))
	defer server.Close()

	provider, err := NewOpenAILLMProvider(&domain.OpenAIProviderConfig{
		APIKey:   "test-key",
		BaseURL:  server.URL,
		LLMModel: "gpt-4o-mini",
	})
	assert.NoError(t, err)

	result, err := provider.GenerateWithTools(context.Background(), []domain.Message{
		{Role: "user", Content: "today's news"},
	}, nil, &domain.GenerationOptions{
		WebSearchMode:        domain.WebSearchModeAuto,
		WebSearchContextSize: "medium",
	})
	assert.NoError(t, err)
	assert.Equal(t, "fallback ok", result.Content)
	assert.EqualValues(t, 2, requestCount.Load())
}

// Benchmark tests

func BenchmarkToOpenAIMessages(b *testing.B) {
	messages := []domain.Message{
		{Role: "system", Content: "You are a helpful assistant"},
		{Role: "user", Content: "Hello"},
		{Role: "assistant", Content: "Hi there!"},
		{Role: "user", Content: "How are you?"},
		{Role: "assistant", Content: "I'm doing well, thanks!"},
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = toOpenAIMessages(messages)
	}
}

func BenchmarkOpenAIProvider_Creation(b *testing.B) {
	config := &domain.OpenAIProviderConfig{
		APIKey:         "test-key",
		LLMModel:       "gpt-4",
		EmbeddingModel: "text-embedding-ada-002",
	}

	b.Run("LLM Provider", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			_, _ = NewOpenAILLMProvider(config)
		}
	})

	b.Run("Embedder Provider", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			_, _ = NewOpenAIEmbedderProvider(config)
		}
	})
}
