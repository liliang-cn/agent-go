package embedder

import (
	"context"
	"errors"
	"testing"

	"github.com/liliang-cn/agent-go/v2/pkg/domain"
)

// MockEmbedderProvider for testing
type MockEmbedderProvider struct {
	embedFunc    func(ctx context.Context, text string) ([]float64, error)
	healthFunc   func(ctx context.Context) error
	providerType domain.ProviderType
}

func (m *MockEmbedderProvider) Embed(ctx context.Context, text string) ([]float64, error) {
	if m.embedFunc != nil {
		return m.embedFunc(ctx, text)
	}
	// Default implementation
	return []float64{0.1, 0.2, 0.3, 0.4, 0.5}, nil
}

func (m *MockEmbedderProvider) EmbedBatch(ctx context.Context, texts []string) ([][]float64, error) {
	result := make([][]float64, len(texts))
	for i, text := range texts {
		v, err := m.Embed(ctx, text)
		if err != nil {
			return nil, err
		}
		result[i] = v
	}
	return result, nil
}

func (m *MockEmbedderProvider) Health(ctx context.Context) error {
	if m.healthFunc != nil {
		return m.healthFunc(ctx)
	}
	return nil
}

func (m *MockEmbedderProvider) ProviderType() domain.ProviderType {
	return m.providerType
}

func TestNewService(t *testing.T) {
	provider := &MockEmbedderProvider{
		providerType: domain.ProviderOpenAI,
	}

	service := NewService(provider)
	if service == nil {
		t.Fatal("NewService returned nil")
	}

	if service.provider != provider {
		t.Error("Service provider not set correctly")
	}
}

func TestService_Embed(t *testing.T) {
	testCases := []struct {
		name     string
		provider *MockEmbedderProvider
		text     string
		wantLen  int
		wantErr  bool
	}{
		{
			name: "successful embedding",
			provider: &MockEmbedderProvider{
				embedFunc: func(ctx context.Context, text string) ([]float64, error) {
					if text == "test text" {
						return []float64{0.1, 0.2, 0.3}, nil
					}
					return nil, errors.New("unexpected text")
				},
			},
			text:    "test text",
			wantLen: 3,
			wantErr: false,
		},
		{
			name: "embedding error",
			provider: &MockEmbedderProvider{
				embedFunc: func(ctx context.Context, text string) ([]float64, error) {
					return nil, errors.New("embedding failed")
				},
			},
			text:    "fail text",
			wantLen: 0,
			wantErr: true,
		},
		{
			name: "empty text",
			provider: &MockEmbedderProvider{
				embedFunc: func(ctx context.Context, text string) ([]float64, error) {
					if text == "" {
						return []float64{0.0, 0.0}, nil
					}
					return []float64{0.1, 0.2}, nil
				},
			},
			text:    "",
			wantLen: 2,
			wantErr: false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			service := NewService(tc.provider)
			ctx := context.Background()

			embeddings, err := service.Embed(ctx, tc.text)

			if tc.wantErr && err == nil {
				t.Error("Expected error but got none")
			}

			if !tc.wantErr && err != nil {
				t.Errorf("Unexpected error: %v", err)
			}

			if len(embeddings) != tc.wantLen {
				t.Errorf("Expected embedding length %d, got %d", tc.wantLen, len(embeddings))
			}
		})
	}
}

func TestService_Health(t *testing.T) {
	testCases := []struct {
		name     string
		provider *MockEmbedderProvider
		wantErr  bool
	}{
		{
			name: "healthy provider",
			provider: &MockEmbedderProvider{
				healthFunc: func(ctx context.Context) error {
					return nil
				},
			},
			wantErr: false,
		},
		{
			name: "unhealthy provider",
			provider: &MockEmbedderProvider{
				healthFunc: func(ctx context.Context) error {
					return errors.New("provider unhealthy")
				},
			},
			wantErr: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			service := NewService(tc.provider)
			ctx := context.Background()

			err := service.Health(ctx)

			if tc.wantErr && err == nil {
				t.Error("Expected error but got none")
			}

			if !tc.wantErr && err != nil {
				t.Errorf("Unexpected error: %v", err)
			}
		})
	}
}

func TestService_ProviderType(t *testing.T) {
	testCases := []struct {
		name         string
		providerType domain.ProviderType
	}{
		{
			name:         "ollama provider",
			providerType: domain.ProviderOpenAI,
		},
		{
			name:         "openai provider",
			providerType: domain.ProviderOpenAI,
		},
		{
			name:         "lmstudio provider",
			providerType: domain.ProviderOpenAI,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			provider := &MockEmbedderProvider{
				providerType: tc.providerType,
			}

			service := NewService(provider)
			providerType := service.ProviderType()

			if providerType != tc.providerType {
				t.Errorf("Expected provider type %s, got %s", tc.providerType, providerType)
			}
		})
	}
}

func TestService_Embed_ContextCancellation(t *testing.T) {
	provider := &MockEmbedderProvider{
		embedFunc: func(ctx context.Context, text string) ([]float64, error) {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			default:
				return []float64{0.1, 0.2}, nil
			}
		},
	}

	service := NewService(provider)

	// Test with cancelled context
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := service.Embed(ctx, "test")
	if err == nil {
		t.Error("Expected error from cancelled context")
	}

	if err != context.Canceled {
		t.Errorf("Expected context.Canceled error, got %v", err)
	}
}

func TestService_Health_ContextCancellation(t *testing.T) {
	provider := &MockEmbedderProvider{
		healthFunc: func(ctx context.Context) error {
			select {
			case <-ctx.Done():
				return ctx.Err()
			default:
				return nil
			}
		},
	}

	service := NewService(provider)

	// Test with cancelled context
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := service.Health(ctx)
	if err == nil {
		t.Error("Expected error from cancelled context")
	}

	if err != context.Canceled {
		t.Errorf("Expected context.Canceled error, got %v", err)
	}
}

func TestService_EmbedLargeText(t *testing.T) {
	provider := &MockEmbedderProvider{
		embedFunc: func(ctx context.Context, text string) ([]float64, error) {
			// Simulate processing time proportional to text length
			embeddings := make([]float64, min(len(text)/10, 1536)) // Max 1536 dimensions
			for i := range embeddings {
				embeddings[i] = float64(i) * 0.01
			}
			return embeddings, nil
		},
	}

	service := NewService(provider)
	ctx := context.Background()

	// Test with large text
	largeText := make([]byte, 10000)
	for i := range largeText {
		largeText[i] = 'a'
	}

	embeddings, err := service.Embed(ctx, string(largeText))
	if err != nil {
		t.Errorf("Unexpected error with large text: %v", err)
	}

	if len(embeddings) == 0 {
		t.Error("Expected non-empty embeddings for large text")
	}
}

func TestService_EmbedSpecialCharacters(t *testing.T) {
	provider := &MockEmbedderProvider{
		embedFunc: func(ctx context.Context, text string) ([]float64, error) {
			// Return different embeddings based on text content
			if text == "Hello 世界! 🌍" {
				return []float64{0.5, 0.6, 0.7}, nil
			}
			return []float64{0.1, 0.2, 0.3}, nil
		},
	}

	service := NewService(provider)
	ctx := context.Background()

	// Test with special characters
	embeddings, err := service.Embed(ctx, "Hello 世界! 🌍")
	if err != nil {
		t.Errorf("Unexpected error with special characters: %v", err)
	}

	expected := []float64{0.5, 0.6, 0.7}
	if len(embeddings) != len(expected) {
		t.Errorf("Expected %d dimensions, got %d", len(expected), len(embeddings))
	}

	for i, val := range expected {
		if i < len(embeddings) && embeddings[i] != val {
			t.Errorf("Expected embedding[%d] = %f, got %f", i, val, embeddings[i])
		}
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
