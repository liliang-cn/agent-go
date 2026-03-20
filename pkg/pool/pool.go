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

	"github.com/liliang-cn/agent-go/pkg/domain"
	"github.com/liliang-cn/agent-go/pkg/prompt"
)

// SelectionStrategy 选择策略
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

// Provider LLM Provider配置
type Provider struct {
	Name           string `mapstructure:"name" json:"name"`
	BaseURL        string `mapstructure:"base_url" json:"base_url"`
	Key            string `mapstructure:"key" json:"key"`
	ModelName      string `mapstructure:"model_name" json:"model_name"`
	MaxConcurrency int    `mapstructure:"max_concurrency" json:"max_concurrency"`
	Capability     int    `mapstructure:"capability" json:"capability"` // 1-5 能力等级
}

type SelectionHint struct {
	PreferredProvider string
	PreferredModel    string
	MinCapability     int
}

// PoolConfig Pool配置
type PoolConfig struct {
	Enabled   bool              `mapstructure:"enabled"`
	Strategy  SelectionStrategy `mapstructure:"strategy"`
	Providers []Provider        `mapstructure:"providers"`
}

// clientWrapper 包装client及其状态
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

// NewPool 创建pool. 允许空 providers（可后续通过 AddProvider 动态添加）.
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

	// 解析策略
	if pool.strategy == "" {
		pool.strategy = StrategyRoundRobin
	}

	// 初始化clients
	for _, p := range config.Providers {
		client, err := NewClient(p.Name, p.BaseURL, p.Key, p.ModelName)
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
}

// Get 获取一个client（根据策略）
func (p *Pool) Get() (*Client, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()

	if len(p.clients) == 0 {
		return nil, fmt.Errorf("no clients available")
	}

	// 获取健康的clients
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
		selected = p.selectByCapability(healthy, 0) // 0表示不限制
	case StrategyFailover:
		selected = healthy[0] // 第一个健康的
	default:
		selected = p.selectRoundRobin(healthy)
	}

	if selected == nil {
		return nil, fmt.Errorf("no client selected")
	}

	atomic.AddInt32(&selected.activeRequests, 1)
	return selected.client, nil
}

// GetByName 按名称获取，兼容旧调用；名称指 provider 名称。
func (p *Pool) GetByName(name string) (*Client, error) {
	return p.GetByProvider(name)
}

// GetByProvider 按 provider 名称获取。
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
	return wrapper.client, nil
}

// GetByModel 按模型名获取。
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
		if strings.EqualFold(wrapper.provider.ModelName, modelName) {
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
	return selected.client, nil
}

// GetByCapability 按能力等级获取（>=指定能力的最低负载）
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
	return selected.client, nil
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
	return selected.client, nil
}

// Release 释放client
func (p *Pool) Release(client *Client) {
	p.mu.RLock()
	defer p.mu.RUnlock()

	for _, wrapper := range p.clients {
		if wrapper.client == client {
			atomic.AddInt32(&wrapper.activeRequests, -1)
			return
		}
	}
}

// healthyClients 获取健康的clients
func (p *Pool) healthyClients() []*clientWrapper {
	healthy := make([]*clientWrapper, 0, len(p.clients))
	for _, w := range p.clients {
		if w.healthy {
			// 检查并发限制
			if w.provider.MaxConcurrency <= 0 ||
				atomic.LoadInt32(&w.activeRequests) < int32(w.provider.MaxConcurrency) {
				healthy = append(healthy, w)
			}
		}
	}
	return healthy
}

// selectRoundRobin round-robin选择
func (p *Pool) selectRoundRobin(healthy []*clientWrapper) *clientWrapper {
	idx := atomic.AddUint32(&p.roundRobinIdx, 1) % uint32(len(healthy))
	return healthy[idx]
}

// selectRandom 随机选择
func (p *Pool) selectRandom(healthy []*clientWrapper) *clientWrapper {
	return healthy[rand.Intn(len(healthy))]
}

// selectLeastLoad 最低负载选择
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

// selectByCapability 按能力选择（>=minCapability中负载最低的）
func (p *Pool) selectByCapability(healthy []*clientWrapper, minCapability int) *clientWrapper {
	var selected *clientWrapper
	maxCap := -1
	minLoad := int32(^uint32(0) >> 1)

	for _, w := range healthy {
		// 跳过能力不足的
		if minCapability > 0 && w.provider.Capability < minCapability {
			continue
		}

		// 优先选择高能力的
		if w.provider.Capability > maxCap {
			maxCap = w.provider.Capability
			selected = w
			minLoad = atomic.LoadInt32(&w.activeRequests)
		} else if w.provider.Capability == maxCap && selected != nil {
			// 相同能力选低负载
			load := atomic.LoadInt32(&w.activeRequests)
			if load < minLoad {
				minLoad = load
				selected = w
			}
		}
	}

	// 如果没找到高能力的，直接选最低负载
	if selected == nil && minCapability == 0 {
		return p.selectLeastLoad(healthy)
	}

	return selected
}

func (p *Pool) selectWithHint(healthy []*clientWrapper, hint SelectionHint) *clientWrapper {
	var preferred []*clientWrapper
	for _, w := range healthy {
		if hint.MinCapability > 0 && w.provider.Capability < hint.MinCapability {
			continue
		}

		if hint.PreferredProvider != "" && strings.EqualFold(w.provider.Name, hint.PreferredProvider) {
			preferred = append(preferred, w)
			continue
		}

		if hint.PreferredModel != "" && strings.EqualFold(w.provider.ModelName, hint.PreferredModel) {
			preferred = append(preferred, w)
		}
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

// healthCheckLoop 健康检查循环
func (p *Pool) healthCheckLoop() {
	// Less frequent health checks to avoid interference
	ticker := time.NewTicker(60 * time.Second)
	defer ticker.Stop()

	for range ticker.C {
		p.checkHealth()
	}
}

// checkHealth 检查所有clients健康状态
func (p *Pool) checkHealth() {
	p.mu.Lock()
	defer p.mu.Unlock()

	for _, w := range p.clients {
		// Skip health check if there are active requests - don't interrupt ongoing work
		if atomic.LoadInt32(&w.activeRequests) > 0 {
			// If it's working, mark it healthy
			w.healthy = true
			w.lastHealthCheck = time.Now()
			continue
		}

		// Only check health when idle - use longer timeout
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		err := w.client.Health(ctx)
		cancel()

		if err != nil {
			w.healthy = false
		} else {
			w.healthy = true
		}
		w.lastHealthCheck = time.Now()
	}
}

// GetStatus 获取所有clients状态
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
		}
	}
	return status
}

// ClientStatus 客户端状态
type ClientStatus struct {
	Healthy        bool   `json:"healthy"`
	ActiveRequests int32  `json:"active_requests"`
	MaxConcurrency int    `json:"max_concurrency"`
	Capability     int    `json:"capability"`
	ModelName      string `json:"model_name"`
}

// Close 关闭pool
func (p *Pool) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()

	for _, w := range p.clients {
		w.client.Close()
	}

	return nil
}

// Generate Pool级别的Generate方法（自动获取和释放）
func (p *Pool) Generate(ctx context.Context, prompt string, opts *domain.GenerationOptions) (string, error) {
	client, err := p.Get()
	if err != nil {
		return "", err
	}
	defer p.Release(client)

	return client.Generate(ctx, prompt, opts)
}

// GenerateWithTools Pool级别的GenerateWithTools
func (p *Pool) GenerateWithTools(ctx context.Context, messages []domain.Message, tools []domain.ToolDefinition, opts *domain.GenerationOptions) (*domain.GenerationResult, error) {
	client, err := p.Get()
	if err != nil {
		return nil, err
	}
	defer p.Release(client)

	return client.GenerateWithTools(ctx, messages, tools, opts)
}

// GenerateStructured Pool级别的GenerateStructured
func (p *Pool) GenerateStructured(ctx context.Context, prompt string, schema interface{}, opts *domain.GenerationOptions) (*domain.StructuredResult, error) {
	client, err := p.Get()
	if err != nil {
		return nil, err
	}
	defer p.Release(client)

	return client.GenerateStructured(ctx, prompt, schema, opts)
}

// RecognizeIntent Pool级别的RecognizeIntent
func (p *Pool) RecognizeIntent(ctx context.Context, request string) (*domain.IntentResult, error) {
	client, err := p.Get()
	if err != nil {
		return nil, err
	}
	defer p.Release(client)

	return client.RecognizeIntent(ctx, request)
}

// Stream Pool级别的Stream
func (p *Pool) Stream(ctx context.Context, prompt string, opts *domain.GenerationOptions, callback func(string)) error {
	client, err := p.Get()
	if err != nil {
		return err
	}
	defer p.Release(client)

	return client.Stream(ctx, prompt, opts, callback)
}

// StreamWithTools Pool级别的StreamWithTools
func (p *Pool) StreamWithTools(ctx context.Context, messages []domain.Message, tools []domain.ToolDefinition, opts *domain.GenerationOptions, callback domain.ToolCallCallback) error {
	client, err := p.Get()
	if err != nil {
		return err
	}
	defer p.Release(client)

	return client.StreamWithTools(ctx, messages, tools, opts, callback)
}

// Embed Pool级别的Embed (兼容domain.Embedder接口，返回第一个文本的向量)
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

// EmbedMultiple Pool级别的EmbedMultiple (向量化多个文本)
func (p *Pool) EmbedMultiple(ctx context.Context, texts []string) ([][]float64, error) {
	client, err := p.Get()
	if err != nil {
		return nil, err
	}
	defer p.Release(client)

	return client.EmbedMultiple(ctx, texts)
}

// ExtractMetadata Pool级别的ExtractMetadata
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

	client, err := NewClient(prov.Name, prov.BaseURL, prov.Key, prov.ModelName)
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

	client, err := NewClient(prov.Name, prov.BaseURL, prov.Key, prov.ModelName)
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
		providers = append(providers, w.provider)
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
