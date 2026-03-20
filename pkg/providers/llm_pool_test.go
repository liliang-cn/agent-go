package providers

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/liliang-cn/agent-go/v2/pkg/domain"
)

// MockLLMProvider is a mock implementation of domain.LLMProvider for testing
type MockLLMProvider struct {
	name           string
	healthy        bool
	failCount      int32
	maxFails       int32
	generateCalled int32
	mu             sync.Mutex
}

func NewMockLLMProvider(name string, healthy bool, maxFails int32) *MockLLMProvider {
	return &MockLLMProvider{
		name:     name,
		healthy:  healthy,
		maxFails: maxFails,
	}
}

func (m *MockLLMProvider) Generate(ctx context.Context, prompt string, opts *domain.GenerationOptions) (string, error) {
	atomic.AddInt32(&m.generateCalled, 1)

	if atomic.LoadInt32(&m.failCount) < m.maxFails {
		atomic.AddInt32(&m.failCount, 1)
		return "", errors.New("mock generation error")
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.healthy {
		return "", errors.New("provider unhealthy")
	}

	return "mock response from " + m.name, nil
}

func (m *MockLLMProvider) Stream(ctx context.Context, prompt string, opts *domain.GenerationOptions, callback func(string)) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if !m.healthy {
		return errors.New("provider unhealthy")
	}

	callback("streaming ")
	callback("response ")
	callback("from ")
	callback(m.name)
	return nil
}

func (m *MockLLMProvider) GenerateWithTools(ctx context.Context, messages []domain.Message, tools []domain.ToolDefinition, opts *domain.GenerationOptions) (*domain.GenerationResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if !m.healthy {
		return nil, errors.New("provider unhealthy")
	}

	return &domain.GenerationResult{
		Content: "tool response from " + m.name,
	}, nil
}

func (m *MockLLMProvider) StreamWithTools(ctx context.Context, messages []domain.Message, tools []domain.ToolDefinition, opts *domain.GenerationOptions, callback domain.ToolCallCallback) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if !m.healthy {
		return errors.New("provider unhealthy")
	}

	return nil
}

func (m *MockLLMProvider) GenerateStructured(ctx context.Context, prompt string, schema interface{}, opts *domain.GenerationOptions) (*domain.StructuredResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if !m.healthy {
		return nil, errors.New("provider unhealthy")
	}

	return &domain.StructuredResult{
		Raw: "structured response from " + m.name,
	}, nil
}

func (m *MockLLMProvider) NewSession(ctx context.Context, tools []domain.ToolDefinition, opts *domain.GenerationOptions) (domain.RealtimeSession, error) {
	return nil, nil
}

func (m *MockLLMProvider) ProviderType() domain.ProviderType {
	return domain.ProviderType("mock_" + m.name)
}

func (m *MockLLMProvider) Health(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if !m.healthy {
		return errors.New("provider unhealthy")
	}
	return nil
}

func (m *MockLLMProvider) ExtractMetadata(ctx context.Context, content string, model string) (*domain.ExtractedMetadata, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if !m.healthy {
		return nil, errors.New("provider unhealthy")
	}

	return &domain.ExtractedMetadata{
		Summary:      "Test Summary from " + m.name,
		Keywords:     []string{"test", "keyword"},
		DocumentType: "test",
	}, nil
}

func (m *MockLLMProvider) RecognizeIntent(ctx context.Context, request string) (*domain.IntentResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if !m.healthy {
		return nil, errors.New("provider unhealthy")
	}

	return &domain.IntentResult{
		Intent:     domain.IntentAction,
		Confidence: 0.9,
		Reasoning:  "Mock intent from " + m.name,
	}, nil
}

func (m *MockLLMProvider) SetHealthy(healthy bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.healthy = healthy
}

func (m *MockLLMProvider) GetGenerateCalled() int32 {
	return atomic.LoadInt32(&m.generateCalled)
}

func TestNewLLMPool(t *testing.T) {
	tests := []struct {
		name      string
		providers map[string]domain.LLMProvider
		config    LLMPoolConfig
		wantErr   bool
	}{
		{
			name: "successful pool creation",
			providers: map[string]domain.LLMProvider{
				"provider1": NewMockLLMProvider("provider1", true, 0),
				"provider2": NewMockLLMProvider("provider2", true, 0),
			},
			config: LLMPoolConfig{
				Strategy:   RoundRobinStrategy,
				MaxRetries: 2,
			},
			wantErr: false,
		},
		{
			name:      "empty providers",
			providers: map[string]domain.LLMProvider{},
			config: LLMPoolConfig{
				Strategy: RoundRobinStrategy,
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pool, err := NewLLMPool(tt.providers, tt.config)
			if (err != nil) != tt.wantErr {
				t.Errorf("NewLLMPool() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !tt.wantErr && pool == nil {
				t.Error("NewLLMPool() returned nil pool without error")
			}
			if pool != nil {
				defer func() { _ = pool.Close() }()
			}
		})
	}
}

func TestLLMPool_Generate(t *testing.T) {
	ctx := context.Background()

	tests := []struct {
		name    string
		setup   func() *LLMPool
		prompt  string
		wantErr bool
	}{
		{
			name: "successful generation",
			setup: func() *LLMPool {
				providers := map[string]domain.LLMProvider{
					"provider1": NewMockLLMProvider("provider1", true, 0),
				}
				pool, _ := NewLLMPool(providers, LLMPoolConfig{
					Strategy:   RoundRobinStrategy,
					MaxRetries: 0,
				})
				return pool
			},
			prompt:  "test prompt",
			wantErr: false,
		},
		{
			name: "retry on failure",
			setup: func() *LLMPool {
				// Create two providers - one fails initially, other succeeds
				providers := map[string]domain.LLMProvider{
					"provider1": NewMockLLMProvider("provider1", true, 1), // Fails once then succeeds
					"provider2": NewMockLLMProvider("provider2", true, 0), // Always succeeds
				}
				pool, _ := NewLLMPool(providers, LLMPoolConfig{
					Strategy:   RoundRobinStrategy,
					MaxRetries: 2,
					RetryDelay: 10 * time.Millisecond,
				})
				return pool
			},
			prompt:  "test prompt",
			wantErr: false,
		},
		{
			name: "all providers unhealthy",
			setup: func() *LLMPool {
				mock1 := NewMockLLMProvider("provider1", false, 0)
				mock2 := NewMockLLMProvider("provider2", false, 0)
				providers := map[string]domain.LLMProvider{
					"provider1": mock1,
					"provider2": mock2,
				}
				pool, _ := NewLLMPool(providers, LLMPoolConfig{
					Strategy:   RoundRobinStrategy,
					MaxRetries: 1,
				})
				return pool
			},
			prompt:  "test prompt",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pool := tt.setup()
			defer func() { _ = pool.Close() }()

			response, err := pool.Generate(ctx, tt.prompt, nil)
			if (err != nil) != tt.wantErr {
				t.Errorf("Generate() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !tt.wantErr && response == "" {
				t.Error("Generate() returned empty response without error")
			}
		})
	}
}

func TestLLMPool_LoadBalancingStrategies(t *testing.T) {
	ctx := context.Background()

	t.Run("RoundRobinStrategy", func(t *testing.T) {
		mock1 := NewMockLLMProvider("provider1", true, 0)
		mock2 := NewMockLLMProvider("provider2", true, 0)

		providers := map[string]domain.LLMProvider{
			"provider1": mock1,
			"provider2": mock2,
		}

		pool, err := NewLLMPool(providers, LLMPoolConfig{
			Strategy: RoundRobinStrategy,
		})
		if err != nil {
			t.Fatalf("Failed to create pool: %v", err)
		}
		defer func() { _ = pool.Close() }()

		// Make multiple requests
		for i := 0; i < 4; i++ {
			_, err := pool.Generate(ctx, "test", nil)
			if err != nil {
				t.Errorf("Generate() failed: %v", err)
			}
		}

		// Check that both providers were called equally (roughly)
		calls1 := mock1.GetGenerateCalled()
		calls2 := mock2.GetGenerateCalled()

		if calls1 == 0 || calls2 == 0 {
			t.Errorf("Round-robin did not distribute calls: provider1=%d, provider2=%d", calls1, calls2)
		}
	})

	t.Run("RandomStrategy", func(t *testing.T) {
		providers := map[string]domain.LLMProvider{
			"provider1": NewMockLLMProvider("provider1", true, 0),
			"provider2": NewMockLLMProvider("provider2", true, 0),
			"provider3": NewMockLLMProvider("provider3", true, 0),
		}

		pool, err := NewLLMPool(providers, LLMPoolConfig{
			Strategy: RandomStrategy,
		})
		if err != nil {
			t.Fatalf("Failed to create pool: %v", err)
		}
		defer func() { _ = pool.Close() }()

		// Make multiple requests
		for i := 0; i < 10; i++ {
			_, err := pool.Generate(ctx, "test", nil)
			if err != nil {
				t.Errorf("Generate() failed: %v", err)
			}
		}
	})

	t.Run("LeastLoadStrategy", func(t *testing.T) {
		providers := map[string]domain.LLMProvider{
			"provider1": NewMockLLMProvider("provider1", true, 0),
			"provider2": NewMockLLMProvider("provider2", true, 0),
		}

		pool, err := NewLLMPool(providers, LLMPoolConfig{
			Strategy: LeastLoadStrategy,
		})
		if err != nil {
			t.Fatalf("Failed to create pool: %v", err)
		}
		defer func() { _ = pool.Close() }()

		// Make concurrent requests to test load distribution
		var wg sync.WaitGroup
		for i := 0; i < 5; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				_, _ = pool.Generate(ctx, "test", nil)
			}()
		}
		wg.Wait()
	})

	t.Run("FailoverStrategy", func(t *testing.T) {
		mock1 := NewMockLLMProvider("provider1", true, 0)
		mock2 := NewMockLLMProvider("provider2", true, 0)

		providers := map[string]domain.LLMProvider{
			"a_provider1": mock1, // Use alphabetic ordering to ensure consistent order
			"b_provider2": mock2,
		}

		pool, err := NewLLMPool(providers, LLMPoolConfig{
			Strategy:   FailoverStrategy,
			MaxRetries: 1, // Allow one retry to failover
		})
		if err != nil {
			t.Fatalf("Failed to create pool: %v", err)
		}
		defer func() { _ = pool.Close() }()

		// Make multiple requests - should all go to primary (a_provider1)
		for i := 0; i < 3; i++ {
			_, err := pool.Generate(ctx, "test", nil)
			if err != nil {
				t.Errorf("Generate() failed: %v", err)
			}
		}

		// First provider (alphabetically) should have all the calls
		calls1 := mock1.GetGenerateCalled()
		calls2 := mock2.GetGenerateCalled()

		// Since map order is not guaranteed, check that one has all calls and other has none
		if (calls1 != 3 || calls2 != 0) && (calls1 != 0 || calls2 != 3) {
			t.Errorf("Failover strategy did not use single provider exclusively: provider1=%d, provider2=%d", calls1, calls2)
		}

		// Determine which was primary
		var primary, secondary *MockLLMProvider
		if calls1 > 0 {
			primary = mock1
			secondary = mock2
		} else {
			primary = mock2
			secondary = mock1
		}

		// Mark primary as unhealthy
		primary.SetHealthy(false)

		// Force health check to update status
		pool.checkHealth()

		// Next request should go to secondary since primary is unhealthy
		_, err = pool.Generate(ctx, "test", nil)
		if err != nil {
			t.Errorf("Generate() failed after primary failure: %v", err)
		}

		secondaryCalls := secondary.GetGenerateCalled()
		if secondaryCalls != 1 {
			t.Errorf("Failover did not switch to secondary: secondary=%d", secondaryCalls)
		}
	})
}

func TestLLMPool_HealthChecking(t *testing.T) {
	mock1 := NewMockLLMProvider("provider1", true, 0)
	mock2 := NewMockLLMProvider("provider2", true, 0)

	providers := map[string]domain.LLMProvider{
		"provider1": mock1,
		"provider2": mock2,
	}

	pool, err := NewLLMPool(providers, LLMPoolConfig{
		Strategy:            RoundRobinStrategy,
		HealthCheckInterval: 50 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("Failed to create pool: %v", err)
	}
	defer func() { _ = pool.Close() }()

	// Initially both should be healthy
	status := pool.GetProviderStatus()
	if !status["provider1"] || !status["provider2"] {
		t.Error("Initial providers should be healthy")
	}

	// Mark one as unhealthy
	mock1.SetHealthy(false)

	// Wait for health check to run
	time.Sleep(100 * time.Millisecond)

	// Check status again
	status = pool.GetProviderStatus()
	if status["provider1"] {
		t.Error("Provider1 should be marked unhealthy after health check")
	}
	if !status["provider2"] {
		t.Error("Provider2 should still be healthy")
	}
}

func TestLLMPool_ConcurrentAccess(t *testing.T) {
	providers := map[string]domain.LLMProvider{
		"provider1": NewMockLLMProvider("provider1", true, 0),
		"provider2": NewMockLLMProvider("provider2", true, 0),
		"provider3": NewMockLLMProvider("provider3", true, 0),
	}

	pool, err := NewLLMPool(providers, LLMPoolConfig{
		Strategy:   RoundRobinStrategy,
		MaxRetries: 1,
	})
	if err != nil {
		t.Fatalf("Failed to create pool: %v", err)
	}
	defer func() { _ = pool.Close() }()

	ctx := context.Background()

	// Run concurrent operations
	var wg sync.WaitGroup
	errors := make(chan error, 100)

	// Concurrent Generate calls
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := pool.Generate(ctx, "test", nil)
			if err != nil {
				errors <- err
			}
		}()
	}

	// Concurrent Stream calls
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := pool.Stream(ctx, "test", nil, func(s string) {})
			if err != nil {
				errors <- err
			}
		}()
	}

	// Concurrent Health checks
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := pool.Health(ctx)
			if err != nil {
				errors <- err
			}
		}()
	}

	// Concurrent status checks
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = pool.GetProviderStatus()
		}()
	}

	wg.Wait()
	close(errors)

	// Check for errors
	errorCount := 0
	for err := range errors {
		t.Errorf("Concurrent operation failed: %v", err)
		errorCount++
	}

	if errorCount > 0 {
		t.Errorf("Total %d errors during concurrent access", errorCount)
	}
}

func TestLLMPool_Stream(t *testing.T) {
	ctx := context.Background()

	providers := map[string]domain.LLMProvider{
		"provider1": NewMockLLMProvider("provider1", true, 0),
	}

	pool, err := NewLLMPool(providers, LLMPoolConfig{
		Strategy: RoundRobinStrategy,
	})
	if err != nil {
		t.Fatalf("Failed to create pool: %v", err)
	}
	defer func() { _ = pool.Close() }()

	var result string
	err = pool.Stream(ctx, "test", nil, func(s string) {
		result += s
	})

	if err != nil {
		t.Errorf("Stream() failed: %v", err)
	}

	if result != "streaming response from provider1" {
		t.Errorf("Stream() returned unexpected result: %s", result)
	}
}

func TestLLMPool_ProviderType(t *testing.T) {
	providers := map[string]domain.LLMProvider{
		"provider1": NewMockLLMProvider("provider1", true, 0),
	}

	pool, err := NewLLMPool(providers, LLMPoolConfig{
		Strategy: RoundRobinStrategy,
	})
	if err != nil {
		t.Fatalf("Failed to create pool: %v", err)
	}
	defer func() { _ = pool.Close() }()

	if pool.ProviderType() != "pool" {
		t.Errorf("ProviderType() = %v, want pool", pool.ProviderType())
	}
}
