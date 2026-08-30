package pool

import (
	"strings"
	"sync"
	"unicode/utf8"

	tiktoken "github.com/pkoukk/tiktoken-go"

	"github.com/liliang-cn/agent-go/v3/pkg/domain"
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
func (tc *TokenCounter) EstimateMessagesTokens(messages []domain.Message, model string) int {
	totalTokens := 0
	for _, msg := range messages {
		// Add tokens for role (usually 1-2 tokens)
		totalTokens += 2
		// Add tokens for content
		totalTokens += tc.EstimateTokens(msg.Content, model)

		// Everything else a message can carry. This used to count Content
		// alone, and Content is empty on exactly the messages a tool-using
		// agent makes largest: an assistant turn whose payload is a tool
		// call. An fs_write carries a whole source file in its arguments and
		// estimated at five tokens.
		//
		// The consequence was not a slightly-off number. Auto-compaction
		// fires when this estimate crosses CompactionDefaultThresholdTokens,
		// so on a coding history the estimate read ~1.5% of the truth and the
		// threshold was never reached: measured on a real run, twenty-five
		// rounds grew the prompt from 5.5k to 30.6k provider-reported tokens
		// and compacted zero times. Compaction was not misconfigured, it was
		// unreachable.
		totalTokens += tc.EstimateTokens(msg.ReasoningContent, model)
		for _, part := range msg.Parts {
			totalTokens += tc.EstimateTokens(part.Text, model)
		}
		for _, call := range msg.ToolCalls {
			totalTokens += tc.EstimateTokens(call.Function.Name, model)
			totalTokens += estimateToolArgumentTokens(tc, call.Function.Arguments, model)
			// id + type + the JSON scaffolding the provider serialises around
			// every call.
			totalTokens += 8
		}
		if msg.ToolCallID != "" {
			totalTokens += 4
		}

		// Add separator tokens (usually 3-4 tokens)
		totalTokens += 3
	}
	return totalTokens
}

// estimateToolArgumentTokens sizes a tool call's arguments. Values arrive as
// decoded JSON, so it walks the structure rather than guessing from the map's
// length — a single string value can be an entire file.
func estimateToolArgumentTokens(tc *TokenCounter, args map[string]interface{}, model string) int {
	total := 0
	var walk func(v interface{})
	walk = func(v interface{}) {
		switch t := v.(type) {
		case string:
			total += tc.EstimateTokens(t, model)
		case map[string]interface{}:
			for k, sub := range t {
				total += tc.EstimateTokens(k, model) + 2
				walk(sub)
			}
		case []interface{}:
			for _, sub := range t {
				walk(sub)
			}
		case nil:
			total++
		default:
			// Numbers and booleans serialise short; one token each is close
			// enough and cannot be wrong by much.
			total++
		}
	}
	for k, v := range args {
		total += tc.EstimateTokens(k, model) + 2
		walk(v)
	}
	return total
}

// EstimateConversationTokens estimates tokens for a conversation
func (tc *TokenCounter) EstimateConversationTokens(messages []domain.Message, model string) int {
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
