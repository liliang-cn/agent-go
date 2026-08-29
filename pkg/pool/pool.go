package pool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/liliang-cn/agent-go/v3/pkg/domain"
	"github.com/liliang-cn/agent-go/v3/pkg/prompt"
)

// SelectionStrategy decides which client a Get returns.
type SelectionStrategy string

var (
	ErrProviderAlreadyExists = errors.New("provider already exists")
	ErrProviderNotFound      = errors.New("provider not found")
)

const (
	StrategyRoundRobin SelectionStrategy = "round_robin"
	StrategyRandom     SelectionStrategy = "random"
	StrategyLeastLoad  SelectionStrategy = "least_load"
	StrategyCapability SelectionStrategy = "capability"
	StrategyFailover   SelectionStrategy = "failover"
)

// Provider is the configuration of one LLM provider.
type Provider struct {
	Name           string   `mapstructure:"name" json:"name"`
	BaseURL        string   `mapstructure:"base_url" json:"base_url"`
	Key            string   `mapstructure:"key" json:"key"`
	ModelName      string   `mapstructure:"model_name" json:"model_name"`
	Models         []string `mapstructure:"models" json:"models,omitempty"`
	MaxConcurrency int      `mapstructure:"max_concurrency" json:"max_concurrency"`
	Capability     int      `mapstructure:"capability" json:"capability"` // capability level, 1-5
}

type SelectionHint struct {
	PreferredProvider string
	PreferredModel    string
	MinCapability     int
}

// PoolConfig configures a Pool.
type PoolConfig struct {
	Enabled   bool              `mapstructure:"enabled"`
	Strategy  SelectionStrategy `mapstructure:"strategy"`
	Providers []Provider        `mapstructure:"providers"`
}

// clientWrapper pairs a client with its runtime state.
type clientWrapper struct {
	client          *Client
	provider        Provider
	activeRequests  int32
	healthy         bool
	lastHealthCheck time.Time
}

// Pool LLM/Embedding Client Pool
type Pool struct {
	config        PoolConfig
	clients       map[string]*clientWrapper // name -> wrapper
	strategy      SelectionStrategy
	promptManager *prompt.Manager

	// round_robin
	roundRobinIdx uint32

	mu sync.RWMutex
}

// NewPool creates a pool. An empty provider list is allowed; providers can be added later with AddProvider.
func NewPool(config PoolConfig) (*Pool, error) {
	if !config.Enabled {
		return &Pool{config: config, clients: make(map[string]*clientWrapper)}, nil
	}

	pool := &Pool{
		config:        config,
		clients:       make(map[string]*clientWrapper),
		strategy:      config.Strategy,
		promptManager: prompt.NewManager(),
	}

	// Resolve the strategy.
	if pool.strategy == "" {
		pool.strategy = StrategyRoundRobin
	}

	// Initialize the clients.
	for _, p := range config.Providers {
		p = normalizeProviderConfig(p)
		client, err := newClientForProvider(p, p.ModelName, pool.promptManager)
		if err != nil {
			return nil, fmt.Errorf("failed to create client %s: %w", p.Name, err)
		}

		pool.clients[p.Name] = &clientWrapper{
			client:          client,
			provider:        p,
			activeRequests:  0,
			healthy:         true,
			lastHealthCheck: time.Now(),
		}
	}

	// Don't start health check loop - let clients be always available
	// go pool.healthCheckLoop()

	return pool, nil
}

func (p *Pool) SetPromptManager(m *prompt.Manager) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.promptManager = m
	for _, wrapper := range p.clients {
		wrapper.client.SetPromptManager(m)
	}
}

func normalizeProviderConfig(prov Provider) Provider {
	defaultModel := strings.TrimSpace(prov.ModelName)
	models := normalizeProviderModels(defaultModel, prov.Models)
	if defaultModel == "" && len(models) > 0 {
		defaultModel = models[0]
	}
	prov.ModelName = defaultModel
	prov.Models = models
	if prov.MaxConcurrency <= 0 {
		prov.MaxConcurrency = 5
	}
	return prov
}

func normalizeProviderModels(defaultModel string, models []string) []string {
	seen := make(map[string]struct{}, len(models)+1)
	normalized := make([]string, 0, len(models)+1)
	add := func(model string) {
		model = strings.TrimSpace(model)
		if model == "" {
			return
		}
		key := strings.ToLower(model)
		if _, exists := seen[key]; exists {
			return
		}
		seen[key] = struct{}{}
		normalized = append(normalized, model)
	}

	add(defaultModel)
	for _, model := range models {
		add(model)
	}
	return normalized
}

func providerModelName(prov Provider, requested string) (string, bool) {
	prov = normalizeProviderConfig(prov)
	requested = strings.TrimSpace(requested)
	if requested == "" {
		return prov.ModelName, prov.ModelName != ""
	}
	for _, model := range prov.Models {
		if strings.EqualFold(model, requested) {
			return model, true
		}
	}
	if strings.EqualFold(prov.ModelName, requested) {
		return prov.ModelName, true
	}
	return "", false
}

func providerSupportsModel(prov Provider, model string) bool {
	_, ok := providerModelName(prov, model)
	return ok
}

func newClientForProvider(prov Provider, model string, promptMgr *prompt.Manager) (*Client, error) {
	resolvedModel, ok := providerModelName(prov, model)
	if !ok {
		return nil, fmt.Errorf("provider %q does not support model %q", prov.Name, model)
	}
	client, err := NewClient(prov.Name, prov.BaseURL, prov.Key, resolvedModel)
	if err != nil {
		return nil, err
	}
	if promptMgr != nil {
		client.SetPromptManager(promptMgr)
	}
	return client, nil
}

func (p *Pool) clientForWrapper(wrapper *clientWrapper, model string) (*Client, error) {
	if wrapper == nil {
		return nil, fmt.Errorf("client wrapper is required")
	}
	if model == "" || strings.EqualFold(model, wrapper.provider.ModelName) {
		return wrapper.client, nil
	}
	derived, err := newClientForProvider(wrapper.provider, model, p.promptManager)
	if err != nil {
		return nil, err
	}
	// One provider, one native-search verdict: a model-override client shares
	// the wrapper client's evidence instead of starting from unknown.
	if wrapper.client != nil && wrapper.client.nativeSearch != nil {
		derived.nativeSearch = wrapper.client.nativeSearch
	}
	return derived, nil
}

// Get returns a client chosen by the configured strategy.
func (p *Pool) Get() (*Client, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()

	if len(p.clients) == 0 {
		return nil, fmt.Errorf("no clients available")
	}

	// Collect the healthy clients.
	healthy := p.healthyClients()
	if len(healthy) == 0 {
		return nil, fmt.Errorf("no healthy clients available")
	}

	var selected *clientWrapper

	switch p.strategy {
	case StrategyRoundRobin:
		selected = p.selectRoundRobin(healthy)
	case StrategyRandom:
		selected = p.selectRandom(healthy)
	case StrategyLeastLoad:
		selected = p.selectLeastLoad(healthy)
	case StrategyCapability:
		selected = p.selectByCapability(healthy, 0) // 0 means no minimum
	case StrategyFailover:
		selected = healthy[0] // the first healthy one
	default:
		selected = p.selectRoundRobin(healthy)
	}

	if selected == nil {
		return nil, fmt.Errorf("no client selected")
	}

	atomic.AddInt32(&selected.activeRequests, 1)
	return p.clientForWrapper(selected, selected.provider.ModelName)
}

// GetByName returns a client by name (legacy alias; the name is the provider name).
func (p *Pool) GetByName(name string) (*Client, error) {
	return p.GetByProvider(name)
}

// GetByProvider returns a client by provider name.
func (p *Pool) GetByProvider(name string) (*Client, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()

	wrapper, ok := p.clients[name]
	if !ok {
		return nil, fmt.Errorf("client %s not found", name)
	}

	if !wrapper.healthy {
		return nil, fmt.Errorf("client %s is not healthy", name)
	}

	atomic.AddInt32(&wrapper.activeRequests, 1)
	return p.clientForWrapper(wrapper, wrapper.provider.ModelName)
}

// GetByProviderAndModel returns a client for the exact provider/model combination.
func (p *Pool) GetByProviderAndModel(name, modelName string) (*Client, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()

	wrapper, ok := p.clients[name]
	if !ok {
		return nil, fmt.Errorf("client %s not found", name)
	}
	if !wrapper.healthy {
		return nil, fmt.Errorf("client %s is not healthy", name)
	}
	resolvedModel, ok := providerModelName(wrapper.provider, modelName)
	if !ok {
		return nil, fmt.Errorf("provider %s does not support model %s", name, modelName)
	}

	atomic.AddInt32(&wrapper.activeRequests, 1)
	return p.clientForWrapper(wrapper, resolvedModel)
}

// GetByModel returns a client by model name.
func (p *Pool) GetByModel(modelName string) (*Client, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()

	modelName = strings.TrimSpace(modelName)
	if modelName == "" {
		return nil, fmt.Errorf("model name is required")
	}

	healthy := p.healthyClients()
	if len(healthy) == 0 {
		return nil, fmt.Errorf("no healthy clients available")
	}

	var preferred []*clientWrapper
	for _, wrapper := range healthy {
		if providerSupportsModel(wrapper.provider, modelName) {
			preferred = append(preferred, wrapper)
		}
	}
	if len(preferred) == 0 {
		return nil, fmt.Errorf("no client found for model %s", modelName)
	}

	selected := p.selectLeastLoad(preferred)
	if selected == nil {
		return nil, fmt.Errorf("no client selected for model %s", modelName)
	}

	atomic.AddInt32(&selected.activeRequests, 1)
	return p.clientForWrapper(selected, modelName)
}

// GetByCapability returns the least-loaded client whose capability is >= the requested level.
func (p *Pool) GetByCapability(minCapability int) (*Client, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()

	healthy := p.healthyClients()
	if len(healthy) == 0 {
		return nil, fmt.Errorf("no healthy clients available")
	}

	selected := p.selectByCapability(healthy, minCapability)
	if selected == nil {
		return nil, fmt.Errorf("no client with capability >= %d", minCapability)
	}

	atomic.AddInt32(&selected.activeRequests, 1)
	return p.clientForWrapper(selected, selected.provider.ModelName)
}

func (p *Pool) GetWithHint(hint SelectionHint) (*Client, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()

	healthy := p.healthyClients()
	if len(healthy) == 0 {
		return nil, fmt.Errorf("no healthy clients available")
	}

	selected := p.selectWithHint(healthy, hint)
	if selected == nil {
		return nil, fmt.Errorf("no client matched selection hint")
	}

	atomic.AddInt32(&selected.activeRequests, 1)
	if modelName, ok := providerModelName(selected.provider, hint.PreferredModel); ok {
		return p.clientForWrapper(selected, modelName)
	}
	return p.clientForWrapper(selected, selected.provider.ModelName)
}

// Release returns a client to the pool.
func (p *Pool) Release(client *Client) {
	p.mu.RLock()
	defer p.mu.RUnlock()

	if client == nil {
		return
	}

	wrapper, ok := p.clients[client.GetProviderName()]
	if ok {
		atomic.AddInt32(&wrapper.activeRequests, -1)
	}
}

// healthyClients returns the currently healthy clients.
func (p *Pool) healthyClients() []*clientWrapper {
	healthy := make([]*clientWrapper, 0, len(p.clients))
	for _, w := range p.clients {
		if w.healthy {
			// Check the concurrency limit.
			if w.provider.MaxConcurrency <= 0 ||
				atomic.LoadInt32(&w.activeRequests) < int32(w.provider.MaxConcurrency) {
				healthy = append(healthy, w)
			}
		}
	}
	return healthy
}

// selectRoundRobin picks the next client round-robin.
func (p *Pool) selectRoundRobin(healthy []*clientWrapper) *clientWrapper {
	idx := atomic.AddUint32(&p.roundRobinIdx, 1) % uint32(len(healthy))
	return healthy[idx]
}

// selectRandom picks a client at random.
func (p *Pool) selectRandom(healthy []*clientWrapper) *clientWrapper {
	return healthy[rand.Intn(len(healthy))]
}

// selectLeastLoad picks the least-loaded client.
func (p *Pool) selectLeastLoad(healthy []*clientWrapper) *clientWrapper {
	var selected *clientWrapper
	minLoad := int32(^uint32(0) >> 1)

	for _, w := range healthy {
		load := atomic.LoadInt32(&w.activeRequests)
		if load < minLoad {
			minLoad = load
			selected = w
		}
	}

	return selected
}

// selectByCapability picks the least-loaded client with capability >= minCapability.
func (p *Pool) selectByCapability(healthy []*clientWrapper, minCapability int) *clientWrapper {
	var selected *clientWrapper
	maxCap := -1
	minLoad := int32(^uint32(0) >> 1)

	for _, w := range healthy {
		// Skip clients below the required capability.
		if minCapability > 0 && w.provider.Capability < minCapability {
			continue
		}

		// Prefer the higher capability.
		if w.provider.Capability > maxCap {
			maxCap = w.provider.Capability
			selected = w
			minLoad = atomic.LoadInt32(&w.activeRequests)
		} else if w.provider.Capability == maxCap && selected != nil {
			// Same capability: prefer the lower load.
			load := atomic.LoadInt32(&w.activeRequests)
			if load < minLoad {
				minLoad = load
				selected = w
			}
		}
	}

	// Nothing passed the capability filter: fall back to the least-loaded client.
	if selected == nil && minCapability == 0 {
		return p.selectLeastLoad(healthy)
	}

	return selected
}

func (p *Pool) selectWithHint(healthy []*clientWrapper, hint SelectionHint) *clientWrapper {
	var preferred []*clientWrapper
	filter := func(match func(*clientWrapper) bool) []*clientWrapper {
		candidates := make([]*clientWrapper, 0, len(healthy))
		for _, w := range healthy {
			if hint.MinCapability > 0 && w.provider.Capability < hint.MinCapability {
				continue
			}
			if match(w) {
				candidates = append(candidates, w)
			}
		}
		return candidates
	}

	if hint.PreferredProvider != "" && hint.PreferredModel != "" {
		preferred = filter(func(w *clientWrapper) bool {
			return strings.EqualFold(w.provider.Name, hint.PreferredProvider) &&
				providerSupportsModel(w.provider, hint.PreferredModel)
		})
	}
	if len(preferred) == 0 && hint.PreferredProvider != "" {
		preferred = filter(func(w *clientWrapper) bool {
			return strings.EqualFold(w.provider.Name, hint.PreferredProvider)
		})
	}
	if len(preferred) == 0 && hint.PreferredModel != "" {
		preferred = filter(func(w *clientWrapper) bool {
			return providerSupportsModel(w.provider, hint.PreferredModel)
		})
	}

	if len(preferred) > 0 {
		if hint.MinCapability > 0 {
			if selected := p.selectByCapability(preferred, hint.MinCapability); selected != nil {
				return selected
			}
		}
		return p.selectLeastLoad(preferred)
	}

	if hint.MinCapability > 0 {
		if selected := p.selectByCapability(healthy, hint.MinCapability); selected != nil {
			return selected
		}
	}

	switch p.strategy {
	case StrategyCapability:
		return p.selectByCapability(healthy, 0)
	case StrategyLeastLoad:
		return p.selectLeastLoad(healthy)
	case StrategyRandom:
		return p.selectRandom(healthy)
	case StrategyFailover:
		return healthy[0]
	default:
		return p.selectRoundRobin(healthy)
	}
}

// GetStatus reports the status of every client.
func (p *Pool) GetStatus() map[string]ClientStatus {
	p.mu.RLock()
	defer p.mu.RUnlock()

	status := make(map[string]ClientStatus)
	for name, w := range p.clients {
		status[name] = ClientStatus{
			Healthy:        w.healthy,
			ActiveRequests: atomic.LoadInt32(&w.activeRequests),
			MaxConcurrency: w.provider.MaxConcurrency,
			Capability:     w.provider.Capability,
			ModelName:      w.provider.ModelName,
			Models:         append([]string(nil), w.provider.Models...),
		}
	}
	return status
}

// ClientStatus is the observable state of one client.
type ClientStatus struct {
	Healthy        bool     `json:"healthy"`
	ActiveRequests int32    `json:"active_requests"`
	MaxConcurrency int      `json:"max_concurrency"`
	Capability     int      `json:"capability"`
	ModelName      string   `json:"model_name"`
	Models         []string `json:"models,omitempty"`
}

// Close shuts the pool down.
func (p *Pool) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()

	for _, w := range p.clients {
		w.client.Close()
	}

	return nil
}

// Generate is the pool-level Generate (acquires and releases a client automatically).
func (p *Pool) Generate(ctx context.Context, prompt string, opts *domain.GenerationOptions) (string, error) {
	client, err := p.Get()
	if err != nil {
		return "", err
	}
	defer p.Release(client)

	return client.Generate(ctx, prompt, opts)
}

// GenerateWithTools is the pool-level GenerateWithTools.
func (p *Pool) GenerateWithTools(ctx context.Context, messages []domain.Message, tools []domain.ToolDefinition, opts *domain.GenerationOptions) (*domain.GenerationResult, error) {
	client, err := p.Get()
	if err != nil {
		return nil, err
	}
	defer p.Release(client)

	return client.GenerateWithTools(ctx, messages, tools, opts)
}

// GenerateStructured is the pool-level GenerateStructured.
func (p *Pool) GenerateStructured(ctx context.Context, prompt string, schema interface{}, opts *domain.GenerationOptions) (*domain.StructuredResult, error) {
	client, err := p.Get()
	if err != nil {
		return nil, err
	}
	defer p.Release(client)

	return client.GenerateStructured(ctx, prompt, schema, opts)
}

// RecognizeIntent is the pool-level RecognizeIntent.
func (p *Pool) RecognizeIntent(ctx context.Context, request string) (*domain.IntentResult, error) {
	client, err := p.Get()
	if err != nil {
		return nil, err
	}
	defer p.Release(client)

	return client.RecognizeIntent(ctx, request)
}

// Stream is the pool-level Stream.
func (p *Pool) Stream(ctx context.Context, prompt string, opts *domain.GenerationOptions, callback func(string)) error {
	client, err := p.Get()
	if err != nil {
		return err
	}
	defer p.Release(client)

	return client.Stream(ctx, prompt, opts, callback)
}

// StreamWithTools is the pool-level StreamWithTools.
func (p *Pool) StreamWithTools(ctx context.Context, messages []domain.Message, tools []domain.ToolDefinition, opts *domain.GenerationOptions, callback domain.ToolCallCallback) error {
	client, err := p.Get()
	if err != nil {
		return err
	}
	defer p.Release(client)

	return client.StreamWithTools(ctx, messages, tools, opts, callback)
}

// Embed is the pool-level Embed (satisfies domain.Embedder; returns the vector of the first text).
func (p *Pool) Embed(ctx context.Context, text string) ([]float64, error) {
	client, err := p.Get()
	if err != nil {
		return nil, err
	}
	defer p.Release(client)

	return client.Embed(ctx, []string{text})
}

// EmbedBatch implements domain.Embedder batch interface, delegating to EmbedMultiple
func (p *Pool) EmbedBatch(ctx context.Context, texts []string) ([][]float64, error) {
	return p.EmbedMultiple(ctx, texts)
}

// EmbedMultiple is the pool-level EmbedMultiple (vectorizes several texts).
func (p *Pool) EmbedMultiple(ctx context.Context, texts []string) ([][]float64, error) {
	client, err := p.Get()
	if err != nil {
		return nil, err
	}
	defer p.Release(client)

	return client.EmbedMultiple(ctx, texts)
}

// ExtractMetadata is the pool-level ExtractMetadata.
func (p *Pool) ExtractMetadata(ctx context.Context, content string, model string) (*domain.ExtractedMetadata, error) {
	return p.extractMetadataWithClient(ctx, SelectionHint{}, content, model)
}

func (p *Pool) ExtractMetadataWithHint(ctx context.Context, hint SelectionHint, content string, model string) (*domain.ExtractedMetadata, error) {
	return p.extractMetadataWithClient(ctx, hint, content, model)
}

// AddProvider adds a new provider to the pool at runtime.
// Returns an error if a provider with the same name already exists.
func (p *Pool) AddProvider(prov Provider) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if _, exists := p.clients[prov.Name]; exists {
		return fmt.Errorf("%w: %q; use UpdateProvider to modify it", ErrProviderAlreadyExists, prov.Name)
	}

	if prov.MaxConcurrency <= 0 {
		prov.MaxConcurrency = 5
	}
	prov = normalizeProviderConfig(prov)

	client, err := newClientForProvider(prov, prov.ModelName, p.promptManager)
	if err != nil {
		return fmt.Errorf("failed to create client for %s: %w", prov.Name, err)
	}

	p.clients[prov.Name] = &clientWrapper{
		client:          client,
		provider:        prov,
		healthy:         true,
		lastHealthCheck: time.Now(),
	}
	return nil
}

// RemoveProvider removes a provider from the pool at runtime, closing its client.
func (p *Pool) RemoveProvider(name string) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	w, ok := p.clients[name]
	if !ok {
		return fmt.Errorf("%w: %q", ErrProviderNotFound, name)
	}

	w.client.Close()
	delete(p.clients, name)
	return nil
}

// UpdateProvider replaces an existing provider's configuration at runtime.
// Returns an error if the provider does not exist.
func (p *Pool) UpdateProvider(prov Provider) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	old, ok := p.clients[prov.Name]
	if !ok {
		return fmt.Errorf("%w: %q; use AddProvider to create it", ErrProviderNotFound, prov.Name)
	}

	if prov.MaxConcurrency <= 0 {
		prov.MaxConcurrency = 5
	}
	prov = normalizeProviderConfig(prov)

	client, err := newClientForProvider(prov, prov.ModelName, p.promptManager)
	if err != nil {
		return fmt.Errorf("failed to create client for %s: %w", prov.Name, err)
	}

	old.client.Close()
	p.clients[prov.Name] = &clientWrapper{
		client:          client,
		provider:        prov,
		healthy:         true,
		lastHealthCheck: time.Now(),
	}
	return nil
}

// ListProviders returns a snapshot of all providers currently in the pool.
func (p *Pool) ListProviders() []Provider {
	p.mu.RLock()
	defer p.mu.RUnlock()

	providers := make([]Provider, 0, len(p.clients))
	for _, w := range p.clients {
		provider := w.provider
		provider.Models = append([]string(nil), provider.Models...)
		providers = append(providers, provider)
	}
	return providers
}

func (p *Pool) extractMetadataWithClient(ctx context.Context, hint SelectionHint, content string, model string) (*domain.ExtractedMetadata, error) {
	client, err := p.GetWithHint(hint)
	if err != nil {
		return nil, err
	}
	defer p.Release(client)

	// Use a simple prompt-based extraction
	data := map[string]interface{}{
		"Content": content,
	}
	rendered, err := p.promptManager.Render(prompt.MetadataExtraction, data)
	if err != nil {
		rendered = fmt.Sprintf("Extract metadata from: %s", content)
	}

	result, err := client.Generate(ctx, rendered, &domain.GenerationOptions{Temperature: 0.1})
	if err != nil {
		return nil, err
	}

	// Try to parse as JSON
	var metadata domain.ExtractedMetadata
	if err := json.Unmarshal([]byte(result), &metadata); err != nil {
		// If parsing fails, return basic metadata
		return &domain.ExtractedMetadata{
			Summary:  content[:min(len(content), 200)] + "...",
			Keywords: []string{},
		}, nil
	}

	return &metadata, nil
}
