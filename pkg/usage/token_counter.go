package usage

import (
	"strings"
	"sync"
	"unicode/utf8"

	tiktoken "github.com/pkoukk/tiktoken-go"
)

// TokenCounter provides methods for counting tokens
type TokenCounter struct {
	// Model-specific token counting configurations
	modelConfig map[string]float64 // tokens per character ratio (fallback)
	// Tiktoken encoders cache
	encoders map[string]*tiktoken.Tiktoken
	mu       sync.RWMutex
}

// NewTokenCounter creates a new token counter
func NewTokenCounter() *TokenCounter {
	return &TokenCounter{
		modelConfig: map[string]float64{
			// Approximate tokens per character for different models (fallback)
			"gpt-3.5-turbo":   0.25,
			"gpt-4":           0.25,
			"gpt-4-turbo":     0.25,
			"claude-3-opus":   0.25,
			"claude-3-sonnet": 0.25,
			"claude-3-haiku":  0.25,
			"llama2":          0.3,
			"llama3":          0.28,
			"mixtral":         0.27,
			"qwen":            0.3,
			"qwen2":           0.28,
			"qwen3":           0.28,
			"default":         0.25, // Default ratio
		},
		encoders: make(map[string]*tiktoken.Tiktoken),
	}
}

// EstimateTokens estimates the number of tokens in a text using tiktoken when possible
func (tc *TokenCounter) EstimateTokens(text string, model string) int {
	if text == "" {
		return 0
	}

	// Try to use tiktoken for accurate counting
	encoder, err := tc.getOrCreateEncoder(model)
	if err == nil && encoder != nil {
		// Use tiktoken for accurate token counting
		tokens := encoder.Encode(text, nil, nil)
		return len(tokens)
	}

	// Fallback to character-based estimation
	// Get model-specific ratio
	ratio, exists := tc.modelConfig[strings.ToLower(model)]
	if !exists {
		// Try to match partial model names
		modelLower := strings.ToLower(model)
		for key, val := range tc.modelConfig {
			if strings.Contains(modelLower, key) {
				ratio = val
				break
			}
		}
		if ratio == 0 {
			ratio = tc.modelConfig["default"]
		}
	}

	// Count characters
	charCount := utf8.RuneCountInString(text)

	// Estimate tokens based on character count
	estimatedTokens := int(float64(charCount) * ratio)

	// Ensure at least 1 token for non-empty text
	if estimatedTokens == 0 && text != "" {
		estimatedTokens = 1
	}

	return estimatedTokens
}

// getOrCreateEncoder gets or creates a tiktoken encoder for the model
func (tc *TokenCounter) getOrCreateEncoder(model string) (*tiktoken.Tiktoken, error) {
	tc.mu.RLock()
	// Check if we already have an encoder for this model
	if encoder, exists := tc.encoders[model]; exists {
		tc.mu.RUnlock()
		return encoder, nil
	}
	tc.mu.RUnlock()

	// Map model names to tiktoken encoding names
	var encodingName string
	modelLower := strings.ToLower(model)

	// OpenAI models
	if strings.Contains(modelLower, "gpt-4o") {
		encodingName = "o200k_base"
	} else if strings.Contains(modelLower, "gpt-4") {
		encodingName = "cl100k_base"
	} else if strings.Contains(modelLower, "gpt-3.5-turbo") {
		encodingName = "cl100k_base"
	} else if strings.Contains(modelLower, "text-embedding-3") {
		encodingName = "cl100k_base"
	} else if strings.Contains(modelLower, "text-embedding-ada-002") {
		encodingName = "cl100k_base"
	} else if strings.Contains(modelLower, "davinci") || strings.Contains(modelLower, "curie") {
		encodingName = "p50k_base"
	} else {
		// Default to cl100k_base for most modern models
		encodingName = "cl100k_base"
	}

	// Try to get encoder by encoding name
	encoder, err := tiktoken.GetEncoding(encodingName)
	if err != nil {
		// Try to get encoder for model directly
		encoder, err = tiktoken.EncodingForModel(model)
		if err != nil {
			// Cannot create encoder, will use fallback
			return nil, err
		}
	}

	// Cache the encoder
	tc.mu.Lock()
	tc.encoders[model] = encoder
	tc.mu.Unlock()
	return encoder, nil
}

// EstimateMessagesTokens estimates tokens for a list of messages
func (tc *TokenCounter) EstimateMessagesTokens(messages []Message, model string) int {
	totalTokens := 0
	for _, msg := range messages {
		// Add tokens for role (usually 1-2 tokens)
		totalTokens += 2
		// Add tokens for content
		totalTokens += tc.EstimateTokens(msg.Content, model)
		// Add separator tokens (usually 3-4 tokens)
		totalTokens += 3
	}
	return totalTokens
}

// EstimateConversationTokens estimates tokens for a conversation
func (tc *TokenCounter) EstimateConversationTokens(messages []Message, model string) int {
	return tc.EstimateMessagesTokens(messages, model)
}

// ExtractTokensFromResponse extracts token counts from provider responses
// Different providers return token counts in different formats
func ExtractTokensFromResponse(provider string, response interface{}) (input int, output int, total int) {
	// This function should be implemented based on the actual response structure
	// from different providers. For now, returning placeholder values

	switch provider {
	case "openai":
		// Extract from response.usage.prompt_tokens, completion_tokens, total_tokens
		if resp, ok := response.(map[string]interface{}); ok {
			if usage, ok := resp["usage"].(map[string]interface{}); ok {
				if val, ok := usage["prompt_tokens"].(float64); ok {
					input = int(val)
				}
				if val, ok := usage["completion_tokens"].(float64); ok {
					output = int(val)
				}
				if val, ok := usage["total_tokens"].(float64); ok {
					total = int(val)
				}
			}
		}
	case "anthropic":
		// Extract from response.usage.input_tokens, output_tokens
		if resp, ok := response.(map[string]interface{}); ok {
			if usage, ok := resp["usage"].(map[string]interface{}); ok {
				if val, ok := usage["input_tokens"].(float64); ok {
					input = int(val)
				}
				if val, ok := usage["output_tokens"].(float64); ok {
					output = int(val)
				}
				total = input + output
			}
		}
	case "ollama":
		// Ollama may not return token counts, estimate them
		// This would need to be implemented based on actual Ollama response structure
	default:
		// For unknown providers, return zeros
	}

	return input, output, total
}

// CalculateCost calculates the cost based on token usage and model pricing
func CalculateCost(model string, inputTokens, outputTokens int) float64 {
	// Model pricing in USD per 1K tokens
	// These are example prices and should be updated with actual pricing
	// Per-1K-token USD prices. Substring match: e.g. "gpt-4-turbo-2024-04-09"
	// matches "gpt-4-turbo". Add new entries when a provider lands.
	inputPricing := map[string]float64{
		"gpt-3.5-turbo":     0.0005,
		"gpt-4":             0.03,
		"gpt-4-turbo":       0.01,
		"gpt-4o":            0.005,
		"claude-3-opus":     0.015,
		"claude-3-sonnet":   0.003,
		"claude-3-haiku":    0.00025,
		"claude-3.5-sonnet": 0.003,
		"claude-4":          0.005,
		"deepseek-v4-flash": 0.00027,
		"deepseek-v4-pro":   0.00054,
		"deepseek-chat":     0.00027,
		"deepseek-coder":    0.00054,
		"deepseek-reasoner": 0.00055,
	}

	outputPricing := map[string]float64{
		"gpt-3.5-turbo":     0.0015,
		"gpt-4":             0.06,
		"gpt-4-turbo":       0.03,
		"gpt-4o":            0.015,
		"claude-3-opus":     0.075,
		"claude-3-sonnet":   0.015,
		"claude-3-haiku":    0.00125,
		"claude-3.5-sonnet": 0.015,
		"claude-4":          0.025,
		"deepseek-v4-flash": 0.0011,
		"deepseek-v4-pro":   0.00218,
		"deepseek-chat":     0.0011,
		"deepseek-coder":    0.00218,
		"deepseek-reasoner": 0.0022,
	}

	modelLower := strings.ToLower(model)
	var inputPrice, outputPrice float64

	// Find matching pricing
	for key, price := range inputPricing {
		if strings.Contains(modelLower, key) {
			inputPrice = price
			break
		}
	}

	for key, price := range outputPricing {
		if strings.Contains(modelLower, key) {
			outputPrice = price
			break
		}
	}

	// Calculate cost
	inputCost := float64(inputTokens) / 1000.0 * inputPrice
	outputCost := float64(outputTokens) / 1000.0 * outputPrice

	return inputCost + outputCost
}
