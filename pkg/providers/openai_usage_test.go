package providers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/liliang-cn/agent-go/v3/pkg/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The provider must surface provider-reported usage — including the
// prompt-cache hit split — instead of dropping it at the SDK boundary.
func TestGenerateWithToolsSurfacesUsage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id": "cmpl-1",
			"choices": [{"index": 0, "finish_reason": "stop",
				"message": {"role": "assistant", "content": "hello"}}],
			"usage": {
				"prompt_tokens": 1500,
				"completion_tokens": 20,
				"total_tokens": 1520,
				"prompt_tokens_details": {"cached_tokens": 1280},
				"prompt_cache_hit_tokens": 1280,
				"prompt_cache_miss_tokens": 220
			}
		}`))
	}))
	defer srv.Close()

	provider, err := NewOpenAILLMProvider(&domain.OpenAIProviderConfig{
		APIKey:   "test-key",
		BaseURL:  srv.URL,
		LLMModel: "test-model",
	})
	require.NoError(t, err)

	result, err := provider.GenerateWithTools(context.Background(),
		[]domain.Message{{Role: "user", Content: "hi"}}, nil, nil)
	require.NoError(t, err)

	require.NotNil(t, result.Usage, "usage must not be dropped")
	assert.Equal(t, 1500, result.Usage.PromptTokens)
	assert.Equal(t, 20, result.Usage.CompletionTokens)
	assert.Equal(t, 1280, result.Usage.CachedPromptTokens)
}

// Streaming: the request must opt into the final usage chunk, and that chunk
// (empty choices) must reach the callback as a usage-bearing delta instead of
// being skipped.
func TestStreamWithToolsSurfacesUsageChunk(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(
			`data: {"id":"c1","choices":[{"index":0,"delta":{"content":"hi"}}]}` + "\n\n" +
				`data: {"id":"c1","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}` + "\n\n" +
				`data: {"id":"c1","choices":[],"usage":{"prompt_tokens":900,"completion_tokens":10,"prompt_tokens_details":{"cached_tokens":768}}}` + "\n\n" +
				"data: [DONE]\n\n"))
	}))
	defer srv.Close()

	provider, err := NewOpenAILLMProvider(&domain.OpenAIProviderConfig{
		APIKey:   "test-key",
		BaseURL:  srv.URL,
		LLMModel: "test-model",
	})
	require.NoError(t, err)

	var got *domain.TokenUsage
	err = provider.StreamWithTools(context.Background(),
		[]domain.Message{{Role: "user", Content: "hi"}}, nil, nil,
		func(delta *domain.GenerationResult) error {
			if delta.Usage != nil {
				got = delta.Usage
			}
			return nil
		})
	require.NoError(t, err)

	require.NotNil(t, got, "the usage chunk must be forwarded, not skipped")
	assert.Equal(t, 900, got.PromptTokens)
	assert.Equal(t, 10, got.CompletionTokens)
	assert.Equal(t, 768, got.CachedPromptTokens)
}
