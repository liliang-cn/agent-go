package pool

import (
	"testing"

	"github.com/liliang-cn/agent-go/v3/pkg/domain"
)

func TestTokenCounterWithTiktoken(t *testing.T) {
	counter := NewTokenCounter()

	testCases := []struct {
		name      string
		text      string
		model     string
		minTokens int
	}{
		{
			name:      "GPT-4 simple text",
			text:      "Hello, world!",
			model:     "gpt-4",
			minTokens: 2, // Should be around 4 tokens
		},
		{
			name:      "GPT-3.5 turbo longer text",
			text:      "The quick brown fox jumps over the lazy dog. This is a test sentence to count tokens.",
			model:     "gpt-3.5-turbo",
			minTokens: 10, // Should be around 18-20 tokens
		},
		{
			name:      "GPT-4o with code",
			text:      "func main() { fmt.Println(\"Hello, World!\") }",
			model:     "gpt-4o",
			minTokens: 5, // Should be around 12-15 tokens
		},
		{
			name:      "Unknown model fallback",
			text:      "Testing fallback for unknown model",
			model:     "unknown-model",
			minTokens: 3, // Will use fallback estimation
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			tokens := counter.EstimateTokens(tc.text, tc.model)
			if tokens < tc.minTokens {
				t.Errorf("Expected at least %d tokens for model %s, got %d", tc.minTokens, tc.model, tokens)
			}
			t.Logf("Model: %s, Text: %q, Tokens: %d", tc.model, tc.text, tokens)
		})
	}
}

func TestTokenCounterMessages(t *testing.T) {
	tc := NewTokenCounter()

	messages := []domain.Message{
		{Role: "system", Content: "You are a helpful assistant."},
		{Role: "user", Content: "What is the capital of France?"},
		{Role: "assistant", Content: "The capital of France is Paris."},
	}

	models := []string{"gpt-4", "gpt-3.5-turbo", "claude-3-opus"}

	for _, model := range models {
		tokens := tc.EstimateMessagesTokens(messages, model)
		if tokens < 10 {
			t.Errorf("Expected at least 10 tokens for model %s, got %d", model, tokens)
		}
		t.Logf("Model: %s, Total tokens for messages: %d", model, tokens)
	}
}

func TestTokenCounterAccuracy(t *testing.T) {
	tc := NewTokenCounter()

	// Test specific known token counts for GPT models
	// "Hello world" is typically 2 tokens in GPT models
	text := "Hello world"
	model := "gpt-4"

	tokens := tc.EstimateTokens(text, model)
	// Allow some variance but should be close to 2
	if tokens < 2 || tokens > 4 {
		t.Errorf("Expected 2-4 tokens for 'Hello world' with GPT-4, got %d", tokens)
	}

	// Test empty string
	emptyTokens := tc.EstimateTokens("", model)
	if emptyTokens != 0 {
		t.Errorf("Expected 0 tokens for empty string, got %d", emptyTokens)
	}

	// Test single character
	singleCharTokens := tc.EstimateTokens("a", model)
	if singleCharTokens != 1 {
		t.Errorf("Expected 1 token for single character, got %d", singleCharTokens)
	}
}

func BenchmarkTokenCounter(b *testing.B) {
	tc := NewTokenCounter()
	text := "The quick brown fox jumps over the lazy dog. This is a benchmark test for token counting performance."
	model := "gpt-4"

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = tc.EstimateTokens(text, model)
	}
}

func BenchmarkTokenCounterFallback(b *testing.B) {
	tc := NewTokenCounter()
	text := "The quick brown fox jumps over the lazy dog. This is a benchmark test for token counting performance."
	model := "unknown-model" // Will use fallback

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = tc.EstimateTokens(text, model)
	}
}
