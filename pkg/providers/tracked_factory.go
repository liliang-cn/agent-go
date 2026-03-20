package providers

import (
	"context"

	"github.com/liliang-cn/agent-go/v2/pkg/domain"
	"github.com/liliang-cn/agent-go/v2/pkg/usage"
)

// TrackedFactory implements the ProviderFactory interface with usage tracking
type TrackedFactory struct {
	*Factory
	usageService *usage.Service
}

// NewTrackedFactory creates a new provider factory with usage tracking
func NewTrackedFactory(usageService *usage.Service) *TrackedFactory {
	return &TrackedFactory{
		Factory:      NewFactory(),
		usageService: usageService,
	}
}

// CreateLLMProvider creates an LLM provider with usage tracking
func (f *TrackedFactory) CreateLLMProvider(ctx context.Context, config interface{}) (domain.LLMProvider, error) {
	// Create the base provider using the parent factory
	provider, err := f.Factory.CreateLLMProvider(ctx, config)
	if err != nil {
		return nil, err
	}

	// Wrap with tracking if usage service is available
	if f.usageService != nil {
		provider = NewTrackedLLMProvider(provider, f.usageService)
	}

	return provider, nil
}

// CreateLLMProviderFromMap creates an LLM provider from a map configuration with usage tracking
func (f *TrackedFactory) CreateLLMProviderFromMap(ctx context.Context, configMap map[string]interface{}) (domain.LLMProvider, error) {
	// Create the base provider using the parent factory
	provider, err := f.Factory.CreateLLMProviderFromMap(ctx, configMap)
	if err != nil {
		return nil, err
	}

	// Wrap with tracking if usage service is available
	if f.usageService != nil {
		provider = NewTrackedLLMProvider(provider, f.usageService)
	}

	return provider, nil
}

// CreateEmbedderProvider creates an embedder provider with usage tracking
func (f *TrackedFactory) CreateEmbedderProvider(ctx context.Context, config interface{}) (domain.EmbedderProvider, error) {
	// Create the base provider using the parent factory
	provider, err := f.Factory.CreateEmbedderProvider(ctx, config)
	if err != nil {
		return nil, err
	}

	// Wrap with tracking if usage service is available
	if f.usageService != nil {
		provider = NewTrackedEmbedderProvider(provider, f.usageService)
	}

	return provider, nil
}

// CreateEmbedderProviderFromMap creates an embedder provider from a map configuration with usage tracking
func (f *TrackedFactory) CreateEmbedderProviderFromMap(ctx context.Context, configMap map[string]interface{}) (domain.EmbedderProvider, error) {
	// Create the base provider using the parent factory
	provider, err := f.Factory.CreateEmbedderProviderFromMap(ctx, configMap)
	if err != nil {
		return nil, err
	}

	// Wrap with tracking if usage service is available
	if f.usageService != nil {
		provider = NewTrackedEmbedderProvider(provider, f.usageService)
	}

	return provider, nil
}

// GetUsageService returns the usage service
func (f *TrackedFactory) GetUsageService() *usage.Service {
	return f.usageService
}

// SetUsageService sets the usage service
func (f *TrackedFactory) SetUsageService(service *usage.Service) {
	f.usageService = service
}

// WithUsageTracking is a helper function to enable usage tracking for any provider factory
func WithUsageTracking(factory domain.ProviderFactory, usageService *usage.Service) domain.ProviderFactory {
	// If it's already a TrackedFactory, just update the usage service
	if tf, ok := factory.(*TrackedFactory); ok {
		tf.SetUsageService(usageService)
		return tf
	}

	// Otherwise, create a wrapper
	return &trackedFactoryWrapper{
		ProviderFactory: factory,
		usageService:    usageService,
	}
}

// trackedFactoryWrapper wraps any ProviderFactory with usage tracking
type trackedFactoryWrapper struct {
	domain.ProviderFactory
	usageService *usage.Service
}

// CreateLLMProvider creates an LLM provider with usage tracking
func (w *trackedFactoryWrapper) CreateLLMProvider(ctx context.Context, config interface{}) (domain.LLMProvider, error) {
	provider, err := w.ProviderFactory.CreateLLMProvider(ctx, config)
	if err != nil {
		return nil, err
	}

	if w.usageService != nil {
		provider = NewTrackedLLMProvider(provider, w.usageService)
	}

	return provider, nil
}

// CreateEmbedderProvider creates an embedder provider with usage tracking
func (w *trackedFactoryWrapper) CreateEmbedderProvider(ctx context.Context, config interface{}) (domain.EmbedderProvider, error) {
	provider, err := w.ProviderFactory.CreateEmbedderProvider(ctx, config)
	if err != nil {
		return nil, err
	}

	if w.usageService != nil {
		provider = NewTrackedEmbedderProvider(provider, w.usageService)
	}

	return provider, nil
}
