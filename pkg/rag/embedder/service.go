package embedder

import (
	"context"

	"github.com/liliang-cn/agent-go/v2/pkg/domain"
)

// Service wraps a provider-based embedder
type Service struct {
	provider domain.EmbedderProvider
}

// NewService creates a new embedder service with a provider
func NewService(provider domain.EmbedderProvider) *Service {
	return &Service{
		provider: provider,
	}
}

// Embed generates embeddings using the configured provider
func (s *Service) Embed(ctx context.Context, text string) ([]float64, error) {
	return s.provider.Embed(ctx, text)
}

// EmbedBatch generates embeddings for multiple texts using the configured provider
func (s *Service) EmbedBatch(ctx context.Context, texts []string) ([][]float64, error) {
	return s.provider.EmbedBatch(ctx, texts)
}

// Health checks the health of the underlying provider
func (s *Service) Health(ctx context.Context) error {
	return s.provider.Health(ctx)
}

// ProviderType returns the provider type being used
func (s *Service) ProviderType() domain.ProviderType {
	return s.provider.ProviderType()
}
