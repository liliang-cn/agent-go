package providers

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"time"

	"github.com/liliang-cn/agent-go/v2/pkg/domain"
)

// Factory implements the ProviderFactory interface
type Factory struct{}

// NewFactory creates a new provider factory
func NewFactory() *Factory {
	return &Factory{}
}

// CreateLLMProvider creates an LLM provider based on the configuration
func (f *Factory) CreateLLMProvider(ctx context.Context, config interface{}) (domain.LLMProvider, error) {
	// Try to handle as a typed configuration first
	switch cfg := config.(type) {
	case *domain.OpenAIProviderConfig:
		return NewOpenAILLMProvider(cfg)
	case map[string]interface{}:
		// Handle dynamic configuration with type field
		return f.CreateLLMProviderFromMap(ctx, cfg)
	default:
		return nil, fmt.Errorf("unsupported LLM provider config type: %T", config)
	}
}

// CreateLLMProviderFromMap creates an LLM provider from a map configuration
func (f *Factory) CreateLLMProviderFromMap(ctx context.Context, configMap map[string]interface{}) (domain.LLMProvider, error) {
	providerType, err := DetectProviderType(configMap)
	if err != nil {
		return nil, err
	}

	switch providerType {
	case domain.ProviderOpenAI:
		cfg := &domain.OpenAIProviderConfig{}
		if err := mapToStruct(configMap, cfg); err != nil {
			return nil, fmt.Errorf("failed to parse openai config: %w", err)
		}
		cfg.Type = domain.ProviderOpenAI
		return NewOpenAILLMProvider(cfg)
	default:
		return nil, fmt.Errorf("unsupported provider type: %s", providerType)
	}
}

// CreateEmbedderProvider creates an embedder provider based on the configuration
func (f *Factory) CreateEmbedderProvider(ctx context.Context, config interface{}) (domain.EmbedderProvider, error) {
	// Try to handle as a typed configuration first
	switch cfg := config.(type) {
	case *domain.OpenAIProviderConfig:
		return NewOpenAIEmbedderProvider(cfg)
	case map[string]interface{}:
		// Handle dynamic configuration with type field
		return f.CreateEmbedderProviderFromMap(ctx, cfg)
	default:
		return nil, fmt.Errorf("unsupported embedder provider config type: %T", config)
	}
}

// CreateEmbedderProviderFromMap creates an embedder provider from a map configuration
func (f *Factory) CreateEmbedderProviderFromMap(ctx context.Context, configMap map[string]interface{}) (domain.EmbedderProvider, error) {
	providerType, err := DetectProviderType(configMap)
	if err != nil {
		return nil, err
	}

	switch providerType {
	case domain.ProviderOpenAI:
		cfg := &domain.OpenAIProviderConfig{}
		if err := mapToStruct(configMap, cfg); err != nil {
			return nil, fmt.Errorf("failed to parse openai config: %w", err)
		}
		cfg.Type = domain.ProviderOpenAI
		return NewOpenAIEmbedderProvider(cfg)
	default:
		return nil, fmt.Errorf("unsupported provider type: %s", providerType)
	}
}

// DetermineProviderType determines the provider type from configuration
func DetermineProviderType(config *domain.ProviderConfig) (domain.ProviderType, error) {
	if config.OpenAI != nil {
		return domain.ProviderOpenAI, nil
	}
	return "", fmt.Errorf("no valid provider configuration found")
}

// GetProviderConfig returns the appropriate provider configuration
func GetProviderConfig(config *domain.ProviderConfig) (interface{}, error) {
	providerType, err := DetermineProviderType(config)
	if err != nil {
		return nil, err
	}

	switch providerType {
	case domain.ProviderOpenAI:
		return config.OpenAI, nil
	default:
		return nil, fmt.Errorf("unsupported provider type: %s", providerType)
	}
}

// GetLLMProviderConfig returns the LLM provider configuration based on default settings
func GetLLMProviderConfig(config *domain.ProviderConfig, defaultProvider string) (interface{}, error) {
	return GetLLMProviderConfigWithCustom(config, defaultProvider, nil)
}

// GetLLMProviderConfigWithCustom returns the LLM provider config with custom providers support
func GetLLMProviderConfigWithCustom(config *domain.ProviderConfig, defaultProvider string, customProviders map[string]interface{}) (interface{}, error) {
	switch defaultProvider {
	case "openai":
		if config.OpenAI == nil {
			return nil, fmt.Errorf("openai provider configuration not found")
		}
		return config.OpenAI, nil
	default:
		// Check custom providers
		if customProviders != nil {
			if providerConfig, exists := customProviders[defaultProvider]; exists {
				return providerConfig, nil
			}
		}
		return nil, fmt.Errorf("unsupported default LLM provider: %s", defaultProvider)
	}
}

// CreateLLMPool creates a pool of LLM providers from configuration
func (f *Factory) CreateLLMPool(ctx context.Context, providerConfigs map[string]interface{}, poolConfig LLMPoolConfig) (*LLMPool, error) {
	providers := make(map[string]domain.LLMProvider)

	for name, config := range providerConfigs {
		provider, err := f.CreateLLMProvider(ctx, config)
		if err != nil {
			return nil, fmt.Errorf("failed to create provider %s: %w", name, err)
		}
		providers[name] = provider
	}

	return NewLLMPool(providers, poolConfig)
}

// GetEmbedderProviderConfig returns the embedder provider configuration based on default settings
func GetEmbedderProviderConfig(config *domain.ProviderConfig, defaultProvider string) (interface{}, error) {
	return GetEmbedderProviderConfigWithCustom(config, defaultProvider, nil)
}

// GetEmbedderProviderConfigWithCustom returns the embedder provider config with custom providers support
func GetEmbedderProviderConfigWithCustom(config *domain.ProviderConfig, defaultProvider string, customProviders map[string]interface{}) (interface{}, error) {
	// If no specific default embedder is set, use the LLM provider
	if defaultProvider == "" {
		return GetLLMProviderConfigWithCustom(config, defaultProvider, customProviders)
	}

	switch defaultProvider {
	case "openai":
		if config.OpenAI == nil {
			return nil, fmt.Errorf("openai provider configuration not found")
		}
		return config.OpenAI, nil
	default:
		// Check custom providers
		if customProviders != nil {
			if providerConfig, exists := customProviders[defaultProvider]; exists {
				return providerConfig, nil
			}
		}
		return nil, fmt.Errorf("unsupported default embedder provider: %s", defaultProvider)
	}
}

// GetProviderConfigByName returns a provider configuration by name from dynamic providers map
func GetProviderConfigByName(providers map[string]interface{}, name string) (interface{}, error) {
	if providers == nil {
		return nil, fmt.Errorf("no providers configured")
	}

	config, exists := providers[name]
	if !exists {
		return nil, fmt.Errorf("provider %s not found", name)
	}

	return config, nil
}

// DetectProviderType detects the provider type from a configuration map
func DetectProviderType(config interface{}) (domain.ProviderType, error) {
	configMap, ok := config.(map[string]interface{})
	if !ok {
		return "", fmt.Errorf("invalid provider configuration format")
	}

	typeStr, exists := configMap["type"]
	if !exists {
		return "", fmt.Errorf("provider type not specified in configuration")
	}

	typeString, ok := typeStr.(string)
	if !ok {
		return "", fmt.Errorf("provider type must be a string")
	}

	switch typeString {
	case "openai":
		return domain.ProviderOpenAI, nil
	default:
		return "", fmt.Errorf("unsupported provider type: %s", typeString)
	}
}

// mapToStruct converts a map to a struct using reflection
func mapToStruct(m map[string]interface{}, s interface{}) error {
	structValue := reflect.ValueOf(s).Elem()
	structType := structValue.Type()

	for i := 0; i < structType.NumField(); i++ {
		field := structType.Field(i)
		fieldValue := structValue.Field(i)

		// Get the mapstructure tag or use field name
		tagName := field.Tag.Get("mapstructure")
		if tagName == "" || tagName == ",squash" {
			tagName = toSnakeCase(field.Name)
		} else {
			// Handle tags with options like "timeout,omitempty"
			parts := strings.Split(tagName, ",")
			tagName = parts[0]
		}

		// Skip if field is not settable
		if !fieldValue.CanSet() {
			continue
		}

		// Get value from map
		value, exists := m[tagName]
		if !exists {
			continue
		}

		// Handle different types
		switch fieldValue.Kind() {
		case reflect.String:
			if str, ok := value.(string); ok {
				fieldValue.SetString(str)
			}
		case reflect.Int, reflect.Int64:
			switch v := value.(type) {
			case float64:
				fieldValue.SetInt(int64(v))
			case int:
				fieldValue.SetInt(int64(v))
			case int64:
				fieldValue.SetInt(v)
			case string:
				// Handle duration strings for time.Duration fields
				if field.Type == reflect.TypeOf(time.Duration(0)) {
					if d, err := time.ParseDuration(v); err == nil {
						fieldValue.SetInt(int64(d))
					}
				}
			}
		case reflect.Bool:
			if b, ok := value.(bool); ok {
				fieldValue.SetBool(b)
			}
		case reflect.Struct:
			// Handle embedded structs
			if field.Anonymous {
				if err := mapToStruct(m, fieldValue.Addr().Interface()); err != nil {
					return err
				}
			}
		}
	}

	return nil
}

// toSnakeCase converts a CamelCase string to snake_case
func toSnakeCase(s string) string {
	var result []rune
	for i, r := range s {
		if i > 0 && r >= 'A' && r <= 'Z' {
			result = append(result, '_')
		}
		result = append(result, rune(strings.ToLower(string(r))[0]))
	}
	return string(result)
}
