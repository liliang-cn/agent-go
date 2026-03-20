package providers

import (
	"fmt"

	"github.com/liliang-cn/agent-go/v2/pkg/domain"
)

// Common error messages and utilities for providers

// WrapProviderError wraps provider-specific errors with consistent formatting
func WrapProviderError(providerType domain.ProviderType, operation string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s %s failed: %w", providerType, operation, err)
}

// WrapStructuredOutputError creates consistent structured output error messages
func WrapStructuredOutputError(providerType domain.ProviderType, err error) error {
	return WrapProviderError(providerType, "structured generation", err)
}

// WrapGenerationError creates consistent generation error messages
func WrapGenerationError(providerType domain.ProviderType, err error) error {
	return WrapProviderError(providerType, "generation", err)
}

// WrapStreamError creates consistent streaming error messages
func WrapStreamError(providerType domain.ProviderType, err error) error {
	return WrapProviderError(providerType, "streaming", err)
}

// WrapHealthError creates consistent health check error messages
func WrapHealthError(providerType domain.ProviderType, err error) error {
	return WrapProviderError(providerType, "health check", err)
}

// Common validation functions

// ValidateGenerationOptions validates generation options across providers
func ValidateGenerationOptions(opts *domain.GenerationOptions) error {
	if opts == nil {
		return nil
	}

	if opts.Temperature < 0 || opts.Temperature > 2 {
		return fmt.Errorf("temperature must be between 0 and 2, got %f", opts.Temperature)
	}

	if opts.MaxTokens < 0 {
		return fmt.Errorf("max_tokens must be non-negative, got %d", opts.MaxTokens)
	}

	return nil
}

// ValidateStructuredRequest validates structured output requests
func ValidateStructuredRequest(prompt string, schema interface{}) error {
	if prompt == "" {
		return fmt.Errorf("%w: empty prompt", domain.ErrInvalidInput)
	}

	if schema == nil {
		return fmt.Errorf("%w: nil schema", domain.ErrInvalidInput)
	}

	return nil
}

// ValidateMessages validates message arrays for chat completions
func ValidateMessages(messages []domain.Message) error {
	if len(messages) == 0 {
		return fmt.Errorf("%w: empty messages", domain.ErrInvalidInput)
	}

	for i, msg := range messages {
		if msg.Role == "" {
			return fmt.Errorf("%w: empty role in message %d", domain.ErrInvalidInput, i)
		}
		if msg.Content == "" && len(msg.ToolCalls) == 0 {
			return fmt.Errorf("%w: empty content and no tool calls in message %d", domain.ErrInvalidInput, i)
		}
	}

	return nil
}

// Common timeout and retry logic

// DefaultGenerationOptions returns sensible defaults for generation
func DefaultGenerationOptions() *domain.GenerationOptions {
	return &domain.GenerationOptions{
		Temperature: 0.7,
		MaxTokens:   2000,
	}
}

// DefaultStructuredOptions returns sensible defaults for structured output
func DefaultStructuredOptions() *domain.GenerationOptions {
	return &domain.GenerationOptions{
		Temperature: 0.1, // Lower temperature for more consistent JSON
		MaxTokens:   4000,
	}
}
