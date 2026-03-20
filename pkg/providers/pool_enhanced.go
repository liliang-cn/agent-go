package providers

import (
	"context"
	"fmt"
	"math"
	"math/rand"
	"sync"
	"sync/atomic"
	"time"

	"github.com/liliang-cn/agent-go/v2/pkg/domain"
)

// WeightedLoadBalancingStrategy selects providers based on weights
type WeightedLoadBalancingStrategy string

const (
	// WeightedRoundRobinStrategy distributes requests based on weights
	WeightedRoundRobinStrategy LoadBalancingStrategy = "weighted_round_robin"
	// LatencyBasedStrategy selects providers based on response latency
	LatencyBasedStrategy LoadBalancingStrategy = "latency_based"
	// CostOptimizedStrategy selects providers based on cost efficiency
	CostOptimizedStrategy LoadBalancingStrategy = "cost_optimized"
)

// CircuitBreakerState represents the state of a circuit breaker
type CircuitBreakerState int

const (
	CircuitClosed CircuitBreakerState = iota
	CircuitOpen
	CircuitHalfOpen
)

// ProviderMetrics tracks detailed metrics for a provider
type ProviderMetrics struct {
	TotalRequests   int64
	SuccessRequests int64
	FailedRequests  int64
	TotalLatency    int64 // in milliseconds
	AverageLatency  float64
	P95Latency      float64
	P99Latency      float64
	LastUpdated     time.Time
	latencyHistory  []int64 // circular buffer for recent latencies
	historyIndex    int
	mu              sync.RWMutex
}

// CircuitBreaker implements the circuit breaker pattern
type CircuitBreaker struct {
	State            CircuitBreakerState
	FailureThreshold int
	RecoveryTimeout  time.Duration
	ConsecutiveFails int
	LastFailTime     time.Time
	mu               sync.RWMutex
}

// EnhancedProviderStatus extends ProviderStatus with additional features
type EnhancedProviderStatus struct {
	*ProviderStatus
	Weight         int
	Cost           float64 // cost per 1000 tokens
	MaxConcurrency int32
	Metrics        *ProviderMetrics
	CircuitBreaker *CircuitBreaker
}

// EnhancedLLMPool extends LLMPool with advanced features
type EnhancedLLMPool struct {
	*LLMPool
	enhancedProviders []*EnhancedProviderStatus
	metricsEnabled    bool
	circuitEnabled    bool
}

// NewProviderMetrics creates new provider metrics
func NewProviderMetrics() *ProviderMetrics {
	return &ProviderMetrics{
		latencyHistory: make([]int64, 100), // Keep last 100 latencies
		LastUpdated:    time.Now(),
	}
}

// RecordRequest records a request and its outcome
func (pm *ProviderMetrics) RecordRequest(success bool, latencyMs int64) {
	pm.mu.Lock()
	defer pm.mu.Unlock()

	atomic.AddInt64(&pm.TotalRequests, 1)
	if success {
		atomic.AddInt64(&pm.SuccessRequests, 1)
	} else {
		atomic.AddInt64(&pm.FailedRequests, 1)
	}

	// Update latency metrics
	atomic.AddInt64(&pm.TotalLatency, latencyMs)

	// Store in circular buffer
	pm.latencyHistory[pm.historyIndex] = latencyMs
	pm.historyIndex = (pm.historyIndex + 1) % len(pm.latencyHistory)

	// Recalculate percentiles periodically
	if pm.TotalRequests%10 == 0 {
		pm.calculatePercentiles()
	}

	pm.AverageLatency = float64(pm.TotalLatency) / float64(pm.TotalRequests)
	pm.LastUpdated = time.Now()
}

// calculatePercentiles calculates P95 and P99 latencies
func (pm *ProviderMetrics) calculatePercentiles() {
	// Copy and sort latency history
	sorted := make([]int64, 0, len(pm.latencyHistory))
	for _, lat := range pm.latencyHistory {
		if lat > 0 {
			sorted = append(sorted, lat)
		}
	}

	if len(sorted) == 0 {
		return
	}

	// Simple bubble sort for small dataset
	for i := 0; i < len(sorted); i++ {
		for j := i + 1; j < len(sorted); j++ {
			if sorted[i] > sorted[j] {
				sorted[i], sorted[j] = sorted[j], sorted[i]
			}
		}
	}

	// Calculate percentiles
	p95Index := int(math.Ceil(float64(len(sorted))*0.95)) - 1
	p99Index := int(math.Ceil(float64(len(sorted))*0.99)) - 1

	if p95Index >= 0 && p95Index < len(sorted) {
		pm.P95Latency = float64(sorted[p95Index])
	}
	if p99Index >= 0 && p99Index < len(sorted) {
		pm.P99Latency = float64(sorted[p99Index])
	}
}

// GetSuccessRate returns the success rate
func (pm *ProviderMetrics) GetSuccessRate() float64 {
	pm.mu.RLock()
	defer pm.mu.RUnlock()

	if pm.TotalRequests == 0 {
		return 1.0 // Assume 100% for new providers
	}
	return float64(pm.SuccessRequests) / float64(pm.TotalRequests)
}

// NewCircuitBreaker creates a new circuit breaker
func NewCircuitBreaker(failureThreshold int, recoveryTimeout time.Duration) *CircuitBreaker {
	return &CircuitBreaker{
		State:            CircuitClosed,
		FailureThreshold: failureThreshold,
		RecoveryTimeout:  recoveryTimeout,
	}
}

// RecordSuccess records a successful request
func (cb *CircuitBreaker) RecordSuccess() {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	cb.ConsecutiveFails = 0
	if cb.State == CircuitHalfOpen {
		cb.State = CircuitClosed
	}
}

// RecordFailure records a failed request
func (cb *CircuitBreaker) RecordFailure() bool {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	cb.ConsecutiveFails++
	cb.LastFailTime = time.Now()

	if cb.ConsecutiveFails >= cb.FailureThreshold {
		cb.State = CircuitOpen
		return true // Circuit opened
	}
	return false
}

// CanRequest checks if requests are allowed
func (cb *CircuitBreaker) CanRequest() bool {
	cb.mu.RLock()
	defer cb.mu.RUnlock()

	switch cb.State {
	case CircuitClosed:
		return true
	case CircuitOpen:
		// Check if recovery timeout has passed
		if time.Since(cb.LastFailTime) > cb.RecoveryTimeout {
			cb.mu.RUnlock()
			cb.mu.Lock()
			cb.State = CircuitHalfOpen
			cb.mu.Unlock()
			cb.mu.RLock()
			return true
		}
		return false
	case CircuitHalfOpen:
		return true // Allow one request to test
	default:
		return true
	}
}

// EnhancedLLMPoolConfig extends pool configuration
type EnhancedLLMPoolConfig struct {
	LLMPoolConfig
	EnableMetrics        bool
	EnableCircuitBreaker bool
	CircuitFailThreshold int
	CircuitRecoveryTime  time.Duration
	ProviderWeights      map[string]int
	ProviderCosts        map[string]float64
	MaxConcurrencyLimits map[string]int32
}

// NewEnhancedLLMPool creates an enhanced LLM pool
func NewEnhancedLLMPool(providers map[string]domain.LLMProvider, config EnhancedLLMPoolConfig) (*EnhancedLLMPool, error) {
	// Create base pool
	basePool, err := NewLLMPool(providers, config.LLMPoolConfig)
	if err != nil {
		return nil, err
	}

	pool := &EnhancedLLMPool{
		LLMPool:           basePool,
		enhancedProviders: make([]*EnhancedProviderStatus, 0, len(providers)),
		metricsEnabled:    config.EnableMetrics,
		circuitEnabled:    config.EnableCircuitBreaker,
	}

	// Create enhanced provider status
	for _, baseStatus := range basePool.providers {
		enhanced := &EnhancedProviderStatus{
			ProviderStatus: baseStatus,
			Weight:         1,     // Default weight
			Cost:           0.001, // Default cost per 1000 tokens
			MaxConcurrency: 10,    // Default max concurrent requests
		}

		// Apply custom weights
		if weight, ok := config.ProviderWeights[baseStatus.Name]; ok {
			enhanced.Weight = weight
		}

		// Apply custom costs
		if cost, ok := config.ProviderCosts[baseStatus.Name]; ok {
			enhanced.Cost = cost
		}

		// Apply concurrency limits
		if limit, ok := config.MaxConcurrencyLimits[baseStatus.Name]; ok {
			enhanced.MaxConcurrency = limit
		}

		// Initialize metrics if enabled
		if config.EnableMetrics {
			enhanced.Metrics = NewProviderMetrics()
		}

		// Initialize circuit breaker if enabled
		if config.EnableCircuitBreaker {
			threshold := config.CircuitFailThreshold
			if threshold == 0 {
				threshold = 5 // Default
			}
			recovery := config.CircuitRecoveryTime
			if recovery == 0 {
				recovery = 30 * time.Second // Default
			}
			enhanced.CircuitBreaker = NewCircuitBreaker(threshold, recovery)
		}

		pool.enhancedProviders = append(pool.enhancedProviders, enhanced)
	}

	return pool, nil
}

// selectEnhancedProvider selects a provider with enhanced strategies
func (p *EnhancedLLMPool) selectEnhancedProvider(strategy LoadBalancingStrategy) (*EnhancedProviderStatus, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()

	// Filter healthy providers that can accept requests
	availableProviders := make([]*EnhancedProviderStatus, 0)
	for _, provider := range p.enhancedProviders {
		provider.mu.RLock()
		healthy := provider.Healthy
		provider.mu.RUnlock()

		if !healthy {
			continue
		}

		// Check circuit breaker
		if p.circuitEnabled && provider.CircuitBreaker != nil {
			if !provider.CircuitBreaker.CanRequest() {
				continue
			}
		}

		// Check concurrency limit
		currentLoad := atomic.LoadInt32(&provider.ActiveLoads)
		if currentLoad >= provider.MaxConcurrency {
			continue
		}

		availableProviders = append(availableProviders, provider)
	}

	if len(availableProviders) == 0 {
		return nil, fmt.Errorf("no healthy providers available")
	}

	switch strategy {
	case WeightedRoundRobinStrategy:
		return p.selectWeightedRoundRobin(availableProviders), nil

	case LatencyBasedStrategy:
		return p.selectLatencyBased(availableProviders), nil

	case CostOptimizedStrategy:
		return p.selectCostOptimized(availableProviders), nil

	default:
		// Fall back to base selection
		baseProvider, err := p.LLMPool.selectProvider()
		if err != nil {
			return nil, err
		}
		// Find corresponding enhanced provider
		for _, ep := range p.enhancedProviders {
			if ep.ProviderStatus == baseProvider {
				return ep, nil
			}
		}
		return nil, fmt.Errorf("provider not found")
	}
}

// selectWeightedRoundRobin implements weighted round-robin selection
func (p *EnhancedLLMPool) selectWeightedRoundRobin(providers []*EnhancedProviderStatus) *EnhancedProviderStatus {
	totalWeight := 0
	for _, p := range providers {
		totalWeight += p.Weight
	}

	if totalWeight == 0 {
		// Fall back to simple round-robin
		idx := atomic.AddUint32(&p.roundRobinIdx, 1) % uint32(len(providers))
		return providers[idx]
	}

	// Select based on weights
	idx := int(atomic.AddUint32(&p.roundRobinIdx, 1)) % totalWeight
	cumWeight := 0
	for _, provider := range providers {
		cumWeight += provider.Weight
		if idx < cumWeight {
			return provider
		}
	}

	return providers[0] // Fallback
}

// selectLatencyBased selects provider with best latency
func (p *EnhancedLLMPool) selectLatencyBased(providers []*EnhancedProviderStatus) *EnhancedProviderStatus {
	if !p.metricsEnabled {
		// Fall back to round-robin if metrics not enabled
		idx := atomic.AddUint32(&p.roundRobinIdx, 1) % uint32(len(providers))
		return providers[idx]
	}

	var selected *EnhancedProviderStatus
	minLatency := math.MaxFloat64

	for _, provider := range providers {
		if provider.Metrics == nil {
			continue
		}

		// Use P95 latency for selection (more stable than average)
		latency := provider.Metrics.P95Latency
		if latency == 0 {
			// No data yet, use average
			latency = provider.Metrics.AverageLatency
		}

		// Add small random factor to prevent thundering herd
		latency = latency * (0.9 + 0.2*rand.Float64())

		if latency < minLatency {
			minLatency = latency
			selected = provider
		}
	}

	if selected == nil {
		return providers[0]
	}
	return selected
}

// selectCostOptimized selects most cost-effective provider
func (p *EnhancedLLMPool) selectCostOptimized(providers []*EnhancedProviderStatus) *EnhancedProviderStatus {
	var selected *EnhancedProviderStatus
	minCost := math.MaxFloat64

	for _, provider := range providers {
		// Calculate effective cost considering success rate
		effectiveCost := provider.Cost
		if p.metricsEnabled && provider.Metrics != nil {
			successRate := provider.Metrics.GetSuccessRate()
			if successRate > 0 {
				// Adjust cost based on success rate (failed requests waste money)
				effectiveCost = provider.Cost / successRate
			}
		}

		if effectiveCost < minCost {
			minCost = effectiveCost
			selected = provider
		}
	}

	if selected == nil {
		return providers[0]
	}
	return selected
}

// GenerateWithMetrics wraps Generate with metrics collection
func (p *EnhancedLLMPool) GenerateWithMetrics(ctx context.Context, prompt string, opts *domain.GenerationOptions) (string, error) {
	start := time.Now()

	var response string
	var selectedProvider *EnhancedProviderStatus

	err := p.withRetry(ctx, func(provider *ProviderStatus) error {
		// Find enhanced provider
		for _, ep := range p.enhancedProviders {
			if ep.ProviderStatus == provider {
				selectedProvider = ep
				break
			}
		}

		if selectedProvider == nil {
			return fmt.Errorf("provider not found")
		}

		// Execute request
		var err error
		response, err = provider.Provider.Generate(ctx, prompt, opts)

		// Record metrics
		if p.metricsEnabled && selectedProvider.Metrics != nil {
			latency := time.Since(start).Milliseconds()
			selectedProvider.Metrics.RecordRequest(err == nil, latency)
		}

		// Update circuit breaker
		if p.circuitEnabled && selectedProvider.CircuitBreaker != nil {
			if err == nil {
				selectedProvider.CircuitBreaker.RecordSuccess()
			} else {
				selectedProvider.CircuitBreaker.RecordFailure()
			}
		}

		return err
	})

	return response, err
}

// GetMetrics returns current metrics for all providers
func (p *EnhancedLLMPool) GetMetrics() map[string]*ProviderMetrics {
	metrics := make(map[string]*ProviderMetrics)

	for _, provider := range p.enhancedProviders {
		if provider.Metrics != nil {
			metrics[provider.Name] = provider.Metrics
		}
	}

	return metrics
}

// GetCircuitStates returns circuit breaker states
func (p *EnhancedLLMPool) GetCircuitStates() map[string]CircuitBreakerState {
	states := make(map[string]CircuitBreakerState)

	for _, provider := range p.enhancedProviders {
		if provider.CircuitBreaker != nil {
			provider.CircuitBreaker.mu.RLock()
			states[provider.Name] = provider.CircuitBreaker.State
			provider.CircuitBreaker.mu.RUnlock()
		}
	}

	return states
}
