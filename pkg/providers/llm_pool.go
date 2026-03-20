package providers

import (
	"context"
	"fmt"
	"math/rand"
	"sync"
	"sync/atomic"
	"time"

	"github.com/liliang-cn/agent-go/v2/pkg/domain"
)

// LoadBalancingStrategy defines how to select an LLM from the pool
type LoadBalancingStrategy string

const (
	// RoundRobinStrategy distributes requests evenly across all providers
	RoundRobinStrategy LoadBalancingStrategy = "round_robin"
	// RandomStrategy randomly selects a provider
	RandomStrategy LoadBalancingStrategy = "random"
	// LeastLoadStrategy selects the provider with the least active requests
	LeastLoadStrategy LoadBalancingStrategy = "least_load"
	// FailoverStrategy uses primary provider and fails over to secondary
	FailoverStrategy LoadBalancingStrategy = "failover"
)

// LLMPoolConfig configuration for the LLM pool
type LLMPoolConfig struct {
	Strategy            LoadBalancingStrategy
	HealthCheckInterval time.Duration
	MaxRetries          int
	RetryDelay          time.Duration
}

// ProviderStatus tracks the health and load of a provider
type ProviderStatus struct {
	Provider    domain.LLMProvider
	Name        string
	Healthy     bool
	ActiveLoads int32
	LastCheck   time.Time
	mu          sync.RWMutex
}

// LLMPool manages multiple LLM providers with load balancing
type LLMPool struct {
	providers     []*ProviderStatus
	config        LLMPoolConfig
	roundRobinIdx uint32
	mu            sync.RWMutex
	stopChan      chan struct{}
	wg            sync.WaitGroup
}

// NewLLMPool creates a new LLM pool with the given providers and configuration
func NewLLMPool(providers map[string]domain.LLMProvider, config LLMPoolConfig) (*LLMPool, error) {
	if len(providers) == 0 {
		return nil, fmt.Errorf("at least one provider is required for LLM pool")
	}

	pool := &LLMPool{
		providers: make([]*ProviderStatus, 0, len(providers)),
		config:    config,
		stopChan:  make(chan struct{}),
	}

	// Initialize provider status
	for name, provider := range providers {
		pool.providers = append(pool.providers, &ProviderStatus{
			Provider:  provider,
			Name:      name,
			Healthy:   true,
			LastCheck: time.Now(),
		})
	}

	// Start health checking if interval is set
	if config.HealthCheckInterval > 0 {
		pool.startHealthChecking()
	}

	return pool, nil
}

// startHealthChecking starts a background goroutine for health checking
func (p *LLMPool) startHealthChecking() {
	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		ticker := time.NewTicker(p.config.HealthCheckInterval)
		defer ticker.Stop()

		for {
			select {
			case <-ticker.C:
				p.checkHealth()
			case <-p.stopChan:
				return
			}
		}
	}()
}

// checkHealth checks the health of all providers
func (p *LLMPool) checkHealth() {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	for _, status := range p.providers {
		go func(s *ProviderStatus) {
			err := s.Provider.Health(ctx)
			s.mu.Lock()
			s.Healthy = err == nil
			s.LastCheck = time.Now()
			s.mu.Unlock()
		}(status)
	}
}

// selectProvider selects a provider based on the configured strategy
func (p *LLMPool) selectProvider() (*ProviderStatus, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()

	// Build list of healthy providers in stable order
	healthyProviders := make([]*ProviderStatus, 0)
	// Always iterate in the same order to ensure failover stability
	for _, provider := range p.providers {
		provider.mu.RLock()
		if provider.Healthy {
			healthyProviders = append(healthyProviders, provider)
		}
		provider.mu.RUnlock()
	}

	if len(healthyProviders) == 0 {
		return nil, fmt.Errorf("no healthy providers available")
	}

	switch p.config.Strategy {
	case RoundRobinStrategy:
		idx := atomic.AddUint32(&p.roundRobinIdx, 1) % uint32(len(healthyProviders))
		return healthyProviders[idx], nil

	case RandomStrategy:
		return healthyProviders[rand.Intn(len(healthyProviders))], nil

	case LeastLoadStrategy:
		var selected *ProviderStatus
		minLoad := int32(^uint32(0) >> 1) // Max int32
		for _, provider := range healthyProviders {
			load := atomic.LoadInt32(&provider.ActiveLoads)
			if load < minLoad {
				minLoad = load
				selected = provider
			}
		}
		return selected, nil

	case FailoverStrategy:
		// Always use first healthy provider in original order as primary
		// This ensures consistent failover behavior
		return healthyProviders[0], nil

	default:
		// Default to round robin
		idx := atomic.AddUint32(&p.roundRobinIdx, 1) % uint32(len(healthyProviders))
		return healthyProviders[idx], nil
	}
}

// withRetry executes a function with retry logic
func (p *LLMPool) withRetry(ctx context.Context, fn func(*ProviderStatus) error) error {
	var lastErr error
	attemptedProviders := make(map[string]bool)

	for attempt := 0; attempt <= p.config.MaxRetries; attempt++ {
		if attempt > 0 && p.config.RetryDelay > 0 {
			select {
			case <-time.After(p.config.RetryDelay):
			case <-ctx.Done():
				return ctx.Err()
			}
		}

		provider, err := p.selectProvider()
		if err != nil {
			lastErr = err
			// If no healthy providers, try again after delay in case health check recovers one
			continue
		}

		// Track which providers we've tried
		attemptedProviders[provider.Name] = true

		// Increment active loads
		atomic.AddInt32(&provider.ActiveLoads, 1)

		err = fn(provider)
		atomic.AddInt32(&provider.ActiveLoads, -1)

		if err == nil {
			return nil
		}

		// Only mark as unhealthy after repeated failures from the same provider
		if attemptedProviders[provider.Name] && attempt > 0 {
			provider.mu.Lock()
			provider.Healthy = false
			provider.mu.Unlock()
		}

		lastErr = fmt.Errorf("provider %s failed: %w", provider.Name, err)

		// For failover strategy, try different providers on retry
		if p.config.Strategy == FailoverStrategy {
			// Mark current provider as temporarily unhealthy to force failover
			provider.mu.Lock()
			provider.Healthy = false
			provider.mu.Unlock()

			// Restore health after a short delay (for next requests)
			go func(p *ProviderStatus) {
				time.Sleep(100 * time.Millisecond)
				p.mu.Lock()
				p.Healthy = true
				p.mu.Unlock()
			}(provider)
		}
	}

	return fmt.Errorf("all retry attempts failed: %w", lastErr)
}

// Generate implements the domain.Generator interface using the pool
func (p *LLMPool) Generate(ctx context.Context, prompt string, opts *domain.GenerationOptions) (string, error) {
	var response string
	err := p.withRetry(ctx, func(provider *ProviderStatus) error {
		var err error
		response, err = provider.Provider.Generate(ctx, prompt, opts)
		return err
	})
	return response, err
}

// Stream implements streaming generation using the pool
func (p *LLMPool) Stream(ctx context.Context, prompt string, opts *domain.GenerationOptions, callback func(string)) error {
	return p.withRetry(ctx, func(provider *ProviderStatus) error {
		return provider.Provider.Stream(ctx, prompt, opts, callback)
	})
}

// GenerateWithTools implements tool-based generation using the pool
func (p *LLMPool) GenerateWithTools(ctx context.Context, messages []domain.Message, tools []domain.ToolDefinition, opts *domain.GenerationOptions) (*domain.GenerationResult, error) {
	var response *domain.GenerationResult
	err := p.withRetry(ctx, func(provider *ProviderStatus) error {
		var err error
		response, err = provider.Provider.GenerateWithTools(ctx, messages, tools, opts)
		return err
	})
	return response, err
}

// StreamWithTools implements streaming tool-based generation using the pool
func (p *LLMPool) StreamWithTools(ctx context.Context, messages []domain.Message, tools []domain.ToolDefinition, opts *domain.GenerationOptions, callback domain.ToolCallCallback) error {
	return p.withRetry(ctx, func(provider *ProviderStatus) error {
		return provider.Provider.StreamWithTools(ctx, messages, tools, opts, callback)
	})
}

// GenerateStructured implements structured generation using the pool
func (p *LLMPool) GenerateStructured(ctx context.Context, prompt string, schema interface{}, opts *domain.GenerationOptions) (*domain.StructuredResult, error) {
	var response *domain.StructuredResult
	err := p.withRetry(ctx, func(provider *ProviderStatus) error {
		var err error
		response, err = provider.Provider.GenerateStructured(ctx, prompt, schema, opts)
		return err
	})
	return response, err
}

// ProviderType returns a composite type for the pool
func (p *LLMPool) ProviderType() domain.ProviderType {
	return domain.ProviderType("pool")
}

// Health checks the health of at least one provider in the pool
func (p *LLMPool) Health(ctx context.Context) error {
	p.mu.RLock()
	defer p.mu.RUnlock()

	for _, provider := range p.providers {
		provider.mu.RLock()
		healthy := provider.Healthy
		provider.mu.RUnlock()

		if healthy {
			if err := provider.Provider.Health(ctx); err == nil {
				return nil
			}
		}
	}

	return fmt.Errorf("no healthy providers in pool")
}

// ExtractMetadata uses the first available provider to extract metadata
func (p *LLMPool) ExtractMetadata(ctx context.Context, content string, model string) (*domain.ExtractedMetadata, error) {
	var metadata *domain.ExtractedMetadata
	err := p.withRetry(ctx, func(provider *ProviderStatus) error {
		var err error
		metadata, err = provider.Provider.ExtractMetadata(ctx, content, model)
		return err
	})
	return metadata, err
}

// RecognizeIntent analyzes a user request to determine its intent using the pool
func (p *LLMPool) RecognizeIntent(ctx context.Context, request string) (*domain.IntentResult, error) {
	var result *domain.IntentResult
	err := p.withRetry(ctx, func(provider *ProviderStatus) error {
		var err error
		result, err = provider.Provider.RecognizeIntent(ctx, request)
		return err
	})
	return result, err
}

// GetProviderStatus returns the current status of all providers
func (p *LLMPool) GetProviderStatus() map[string]bool {
	p.mu.RLock()
	defer p.mu.RUnlock()

	status := make(map[string]bool)
	for _, provider := range p.providers {
		provider.mu.RLock()
		status[provider.Name] = provider.Healthy
		provider.mu.RUnlock()
	}
	return status
}

// Close stops the health checking goroutine and cleans up resources
func (p *LLMPool) Close() error {
	close(p.stopChan)
	p.wg.Wait()
	return nil
}
