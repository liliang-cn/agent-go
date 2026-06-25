package llm

import (
	"context"
	"fmt"
	"os"

	"github.com/liliang-cn/agent-go/v2/pkg/domain"
	"github.com/liliang-cn/agent-go/v2/pkg/providers"
)

// NewOpenAI builds a ready-to-use LLM Service against any OpenAI-compatible
// endpoint (OpenAI, Azure, DashScope, Together, Ollama's OpenAI shim, ...) in
// one call — no global pool, config files, or provider structs required.
//
//	client, _ := llm.NewOpenAI("https://api.openai.com/v1", key, "gpt-4o-mini")
//	out, _ := client.Ask(ctx, "Say hello in one word")
func NewOpenAI(baseURL, apiKey, model string) (*Service, error) {
	if baseURL == "" || apiKey == "" || model == "" {
		return nil, fmt.Errorf("llm.NewOpenAI: baseURL, apiKey and model are all required")
	}
	gen, err := providers.NewOpenAILLMProvider(&domain.OpenAIProviderConfig{
		BaseURL:  baseURL,
		APIKey:   apiKey,
		LLMModel: model,
	})
	if err != nil {
		return nil, err
	}
	return NewService(gen), nil
}

// NewOpenAIFromEnv is the zero-argument convenience for scripts and demos.
// It reads LLM_BASE_URL, LLM_API_KEY and LLM_MODEL from the environment.
func NewOpenAIFromEnv() (*Service, error) {
	return NewOpenAI(os.Getenv("LLM_BASE_URL"), os.Getenv("LLM_API_KEY"), os.Getenv("LLM_MODEL"))
}

// Ask is a one-shot, stateless completion — the simplest possible call.
// For multi-turn conversations with history use Chat / ChatWithID instead.
func (s *Service) Ask(ctx context.Context, prompt string) (string, error) {
	return s.generator.Generate(ctx, prompt, &domain.GenerationOptions{})
}

// NewOpenAIEmbedder builds an embedder against any OpenAI-compatible /embeddings
// endpoint in one call (OpenAI, Azure, DashScope text-embedding-v*, ...).
//
//	emb, _ := llm.NewOpenAIEmbedder(baseURL, key, "text-embedding-v4")
//	vec, _ := emb.Embed(ctx, "hello")   // []float64
func NewOpenAIEmbedder(baseURL, apiKey, model string) (domain.EmbedderProvider, error) {
	if baseURL == "" || apiKey == "" || model == "" {
		return nil, fmt.Errorf("llm.NewOpenAIEmbedder: baseURL, apiKey and model are all required")
	}
	return providers.NewOpenAIEmbedderProvider(&domain.OpenAIProviderConfig{
		BaseURL:        baseURL,
		APIKey:         apiKey,
		EmbeddingModel: model,
	})
}

// NewOpenAIEmbedderFromEnv reads LLM_BASE_URL, LLM_API_KEY and LLM_EMBED_MODEL.
func NewOpenAIEmbedderFromEnv() (domain.EmbedderProvider, error) {
	return NewOpenAIEmbedder(os.Getenv("LLM_BASE_URL"), os.Getenv("LLM_API_KEY"), os.Getenv("LLM_EMBED_MODEL"))
}
