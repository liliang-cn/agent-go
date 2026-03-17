package services

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/liliang-cn/agent-go/pkg/config"
	"github.com/liliang-cn/agent-go/pkg/domain"
	"github.com/liliang-cn/agent-go/pkg/pool"
	"github.com/liliang-cn/agent-go/pkg/store"
)

var (
	globalPoolService *GlobalPoolService
	globalPoolMu      sync.RWMutex
)

// GlobalPoolService 管理全局LLM和Embedding Pools
type GlobalPoolService struct {
	config        *config.Config
	llmPool       *pool.Pool
	embeddingPool *pool.Pool
	db            *store.AgentGoDB
	initialized   bool
	mu            sync.RWMutex
}

// GetGlobalPoolService 获取全局pool服务
func GetGlobalPoolService() *GlobalPoolService {
	globalPoolMu.RLock()
	if globalPoolService != nil {
		globalPoolMu.RUnlock()
		return globalPoolService
	}
	globalPoolMu.RUnlock()

	globalPoolMu.Lock()
	defer globalPoolMu.Unlock()

	if globalPoolService != nil {
		return globalPoolService
	}

	globalPoolService = &GlobalPoolService{}
	return globalPoolService
}

// Initialize 初始化pool
func (s *GlobalPoolService) Initialize(ctx context.Context, cfg *config.Config) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.initialized {
		return nil
	}

	s.config = cfg

	// 1. Initialize Unified Database
	db, err := store.NewAgentGoDB(cfg.AgentDBPath())
	if err != nil {
		return fmt.Errorf("failed to initialize agentgo db: %w", err)
	}
	s.db = db

	// 2. Seed DB from config — only insert providers that don't exist yet.
	//    DB is the source of truth after first run; config seeds it on fresh installs.
	existing, err := db.ListProviders()
	if err != nil {
		return fmt.Errorf("failed to list providers from db: %w", err)
	}
	existingNames := make(map[string]bool, len(existing))
	for _, p := range existing {
		existingNames[p.Name] = true
	}
	for _, p := range cfg.LLM.Providers {
		if existingNames[p.Name] {
			continue
		}
		_ = db.SaveProvider(&store.LLMProvider{
			Name:           p.Name,
			BaseURL:        p.BaseURL,
			Key:            p.Key,
			ModelName:      p.ModelName,
			MaxConcurrency: p.MaxConcurrency,
			Capability:     p.Capability,
			Enabled:        true,
		})
	}

	// 3. Load all enabled providers from DB into LLM pool.
	allProviders, err := db.ListProviders()
	if err != nil {
		return fmt.Errorf("failed to load providers from db: %w", err)
	}
	llmProviders := make([]pool.Provider, 0, len(allProviders))
	for _, p := range allProviders {
		if p.Enabled {
			llmProviders = append(llmProviders, store.ToPoolProvider(p))
		}
	}

	// 4. Seed llm.strategy and llm.enabled to DB if not present, then load from DB.
	if _, err := db.GetConfig("llm.strategy"); err != nil {
		_ = db.SaveConfig("llm.strategy", string(cfg.LLM.Strategy))
	}
	if _, err := db.GetConfig("llm.enabled"); err != nil {
		enabled := "false"
		if cfg.LLM.Enabled {
			enabled = "true"
		}
		_ = db.SaveConfig("llm.enabled", enabled)
	}
	llmStrategy := pool.SelectionStrategy(mustGetConfig(db, "llm.strategy", string(cfg.LLM.Strategy)))
	llmEnabled := mustGetConfig(db, "llm.enabled", "true") == "true"

	llmPool, err := pool.NewPool(pool.PoolConfig{
		Enabled:   llmEnabled,
		Strategy:  llmStrategy,
		Providers: llmProviders,
	})
	if err != nil {
		return fmt.Errorf("failed to create LLM pool: %w", err)
	}
	s.llmPool = llmPool

	// 5. Seed embedding providers from config into DB (first-run only).
	existingEmb, err := db.ListEmbeddingProviders()
	if err != nil {
		return fmt.Errorf("failed to list embedding providers from db: %w", err)
	}
	existingEmbNames := make(map[string]bool, len(existingEmb))
	for _, p := range existingEmb {
		existingEmbNames[p.Name] = true
	}
	for _, p := range cfg.RAG.Embedding.Providers {
		if existingEmbNames[p.Name] {
			continue
		}
		_ = db.SaveEmbeddingProvider(&store.EmbeddingProvider{
			Name:           p.Name,
			BaseURL:        p.BaseURL,
			Key:            p.Key,
			ModelName:      p.ModelName,
			MaxConcurrency: p.MaxConcurrency,
			Capability:     p.Capability,
			Enabled:        true,
		})
	}

	// 6. Seed embedding pool config (strategy/enabled) from config if not in DB yet.
	embCfgEnabled := cfg.RAG.Embedding.Enabled || cfg.RAG.Enabled
	embCfgStrategy := cfg.RAG.Embedding.Strategy
	if embCfgStrategy == "" {
		embCfgStrategy = llmStrategy
	}
	if _, err := db.GetConfig("embedding.strategy"); err != nil {
		_ = db.SaveConfig("embedding.strategy", string(embCfgStrategy))
	}
	if _, err := db.GetConfig("embedding.enabled"); err != nil {
		enabled := "false"
		if embCfgEnabled {
			enabled = "true"
		}
		_ = db.SaveConfig("embedding.enabled", enabled)
	}
	embStrategy := pool.SelectionStrategy(mustGetConfig(db, "embedding.strategy", string(embCfgStrategy)))
	embEnabled := mustGetConfig(db, "embedding.enabled", "false") == "true"

	// 7. Build embedding pool from DB providers.
	//    If no dedicated embedding providers exist, fall back to LLM providers
	//    with EmbeddingModel override (backwards-compatible behaviour).
	allEmbProviders, err := db.ListEmbeddingProviders()
	if err != nil {
		return fmt.Errorf("failed to load embedding providers from db: %w", err)
	}
	var embeddingProviders []pool.Provider
	if len(allEmbProviders) > 0 {
		for _, p := range allEmbProviders {
			if p.Enabled {
				embeddingProviders = append(embeddingProviders, store.ToPoolEmbeddingProvider(p))
			}
		}
	} else {
		// Fallback: derive from LLM providers, override model name.
		embeddingProviders = make([]pool.Provider, len(llmProviders))
		for i, p := range llmProviders {
			embeddingProviders[i] = p
			if cfg.RAG.EmbeddingModel != "" {
				embeddingProviders[i].ModelName = cfg.RAG.EmbeddingModel
			}
		}
	}

	embeddingPool, err := pool.NewPool(pool.PoolConfig{
		Enabled:   embEnabled,
		Strategy:  embStrategy,
		Providers: embeddingProviders,
	})
	if err != nil {
		return fmt.Errorf("failed to create embedding pool: %w", err)
	}
	s.embeddingPool = embeddingPool

	s.initialized = true
	return nil
}

// GetLLM 获取LLM client（自动选择）
func (s *GlobalPoolService) GetLLM() (*pool.Client, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if !s.initialized {
		return nil, fmt.Errorf("pool service not initialized")
	}

	return s.llmPool.Get()
}

// GetLLMByName 按名称获取LLM client，兼容旧调用；名称指 provider 名称。
func (s *GlobalPoolService) GetLLMByName(name string) (*pool.Client, error) {
	return s.GetLLMByProvider(name)
}

// GetLLMByProvider 按 provider 名称获取LLM client。
func (s *GlobalPoolService) GetLLMByProvider(name string) (*pool.Client, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if !s.initialized {
		return nil, fmt.Errorf("pool service not initialized")
	}

	return s.llmPool.GetByProvider(name)
}

// GetLLMByModel 按模型名获取LLM client。
func (s *GlobalPoolService) GetLLMByModel(modelName string) (*pool.Client, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if !s.initialized {
		return nil, fmt.Errorf("pool service not initialized")
	}

	return s.llmPool.GetByModel(modelName)
}

// GetLLMByCapability 按能力等级获取LLM client
func (s *GlobalPoolService) GetLLMByCapability(minCapability int) (*pool.Client, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if !s.initialized {
		return nil, fmt.Errorf("pool service not initialized")
	}

	return s.llmPool.GetByCapability(minCapability)
}

// ReleaseLLM 释放LLM client
func (s *GlobalPoolService) ReleaseLLM(client *pool.Client) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if s.initialized {
		s.llmPool.Release(client)
	}
}

// GetEmbedding 获取Embedding client（自动选择）
func (s *GlobalPoolService) GetEmbedding() (*pool.Client, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if !s.initialized {
		return nil, fmt.Errorf("pool service not initialized")
	}

	return s.embeddingPool.Get()
}

// GetEmbeddingByName 按名称获取Embedding client
func (s *GlobalPoolService) GetEmbeddingByName(name string) (*pool.Client, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if !s.initialized {
		return nil, fmt.Errorf("pool service not initialized")
	}

	return s.embeddingPool.GetByName(name)
}

// GetEmbeddingByCapability 按能力等级获取Embedding client
func (s *GlobalPoolService) GetEmbeddingByCapability(minCapability int) (*pool.Client, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if !s.initialized {
		return nil, fmt.Errorf("pool service not initialized")
	}

	return s.embeddingPool.GetByCapability(minCapability)
}

// ReleaseEmbedding 释放Embedding client
func (s *GlobalPoolService) ReleaseEmbedding(client *pool.Client) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if s.initialized {
		s.embeddingPool.Release(client)
	}
}

// GetAgentGoDB returns the underlying unified database
func (s *GlobalPoolService) GetAgentGoDB() *store.AgentGoDB {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.db
}

// ChatOptions 顶级Chat API配置选项
type ChatOptions struct {
	SessionID         string
	Provider          string
	Model             string
	MaxTokens         int
	Temperature       float64
	SystemPrompt      string
	HistoryLimit      int
	SkipPersistence   bool
}

// Chat 顶级Chat API：支持Provider指定与历史自动持久化
func (s *GlobalPoolService) Chat(ctx context.Context, message string, opts ChatOptions) (string, error) {
	s.mu.RLock()
	if !s.initialized {
		s.mu.RUnlock()
		return "", fmt.Errorf("pool service not initialized")
	}
	s.mu.RUnlock()

	// 1. Resolve Provider and Model
	hint := pool.SelectionHint{
		PreferredProvider: opts.Provider,
		PreferredModel:    opts.Model,
	}
	client, err := s.llmPool.GetWithHint(hint)
	if err != nil {
		return "", err
	}
	defer s.llmPool.Release(client)

	// 2. Load History from Unified DB
	var messages []domain.Message
	if opts.SessionID != "" && s.db != nil {
		history, _ := s.db.GetMessages(opts.SessionID, opts.HistoryLimit)
		for _, m := range history {
			messages = append(messages, domain.Message{Role: m.Role, Content: m.Content})
		}
	}

	// 3. Prepare Current Context
	if opts.SystemPrompt != "" {
		messages = append([]domain.Message{{Role: "system", Content: opts.SystemPrompt}}, messages...)
	}
	messages = append(messages, domain.Message{Role: "user", Content: message})

	// 4. Generate Response
	genOpts := &domain.GenerationOptions{
		MaxTokens:   opts.MaxTokens,
		Temperature: opts.Temperature,
	}
	// Direct LLM chat doesn't use tools here, but we use the flexible GenerateWithTools
	res, err := client.GenerateWithTools(ctx, messages, nil, genOpts)
	if err != nil {
		return "", err
	}
	answer := res.Content

	// 5. Automatic Persistence to agentgo.db
	if !opts.SkipPersistence && opts.SessionID != "" && s.db != nil {
		go func() {
			_ = s.db.AddMessage(opts.SessionID, "user", message, nil)
			_ = s.db.AddMessage(opts.SessionID, "assistant", answer, map[string]interface{}{
				"provider": client.GetProviderName(),
				"model":    client.GetModelName(),
			})
		}()
	}

	return answer, nil
}

// StreamChat 顶级流式Chat API：支持Provider指定与历史自动持久化
func (s *GlobalPoolService) StreamChat(ctx context.Context, message string, opts ChatOptions, callback func(string)) error {
	s.mu.RLock()
	if !s.initialized {
		s.mu.RUnlock()
		return fmt.Errorf("pool service not initialized")
	}
	s.mu.RUnlock()

	// 1. Resolve Provider and Model
	hint := pool.SelectionHint{
		PreferredProvider: opts.Provider,
		PreferredModel:    opts.Model,
	}
	client, err := s.llmPool.GetWithHint(hint)
	if err != nil {
		return err
	}
	defer s.llmPool.Release(client)

	// 2. Load History from Unified DB
	var messages []domain.Message
	if opts.SessionID != "" && s.db != nil {
		history, _ := s.db.GetMessages(opts.SessionID, opts.HistoryLimit)
		for _, m := range history {
			messages = append(messages, domain.Message{Role: m.Role, Content: m.Content})
		}
	}

	// 3. Prepare Current Context
	if opts.SystemPrompt != "" {
		messages = append([]domain.Message{{Role: "system", Content: opts.SystemPrompt}}, messages...)
	}
	messages = append(messages, domain.Message{Role: "user", Content: message})

	// 4. Stream and Capture Answer
	var fullAnswer strings.Builder
	wrappedCallback := func(delta *domain.GenerationResult) error {
		if delta.Content != "" {
			fullAnswer.WriteString(delta.Content)
			callback(delta.Content)
		}
		return nil
	}

	genOpts := &domain.GenerationOptions{
		MaxTokens:   opts.MaxTokens,
		Temperature: opts.Temperature,
	}

	err = client.StreamWithTools(ctx, messages, nil, genOpts, wrappedCallback)

	// 5. Automatic Persistence to agentgo.db once stream ends
	if err == nil && !opts.SkipPersistence && opts.SessionID != "" && s.db != nil {
		go func() {
			_ = s.db.AddMessage(opts.SessionID, "user", message, nil)
			_ = s.db.AddMessage(opts.SessionID, "assistant", fullAnswer.String(), map[string]interface{}{
				"provider": client.GetProviderName(),
				"model":    client.GetModelName(),
				"stream":   true,
			})
		}()
	}

	return err
}

// Generate 使用pool生成文本（自动获取和释放）
func (s *GlobalPoolService) Generate(ctx context.Context, prompt string, opts *domain.GenerationOptions) (string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if !s.initialized {
		return "", fmt.Errorf("pool service not initialized")
	}

	return s.llmPool.Generate(ctx, prompt, opts)
}

// GenerateWithTools 使用pool和工具生成
func (s *GlobalPoolService) GenerateWithTools(ctx context.Context, messages []domain.Message, tools []domain.ToolDefinition, opts *domain.GenerationOptions) (*domain.GenerationResult, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if !s.initialized {
		return nil, fmt.Errorf("pool service not initialized")
	}

	return s.llmPool.GenerateWithTools(ctx, messages, tools, opts)
}

// GenerateStructured 使用pool生成结构化输出
func (s *GlobalPoolService) GenerateStructured(ctx context.Context, prompt string, schema interface{}, opts *domain.GenerationOptions) (*domain.StructuredResult, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if !s.initialized {
		return nil, fmt.Errorf("pool service not initialized")
	}

	return s.llmPool.GenerateStructured(ctx, prompt, schema, opts)
}

// RecognizeIntent 使用pool识别意图
func (s *GlobalPoolService) RecognizeIntent(ctx context.Context, request string) (*domain.IntentResult, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if !s.initialized {
		return nil, fmt.Errorf("pool service not initialized")
	}

	return s.llmPool.RecognizeIntent(ctx, request)
}

// Stream 使用pool流式生成
func (s *GlobalPoolService) Stream(ctx context.Context, prompt string, opts *domain.GenerationOptions, callback func(string)) error {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if !s.initialized {
		return fmt.Errorf("pool service not initialized")
	}

	return s.llmPool.Stream(ctx, prompt, opts, callback)
}

// StreamWithTools 使用pool和工具流式生成
func (s *GlobalPoolService) StreamWithTools(ctx context.Context, messages []domain.Message, tools []domain.ToolDefinition, opts *domain.GenerationOptions, callback domain.ToolCallCallback) error {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if !s.initialized {
		return fmt.Errorf("pool service not initialized")
	}

	return s.llmPool.StreamWithTools(ctx, messages, tools, opts, callback)
}

// Embed 使用pool向量化
func (s *GlobalPoolService) Embed(ctx context.Context, text string) ([]float64, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if !s.initialized {
		return nil, fmt.Errorf("pool service not initialized")
	}

	return s.embeddingPool.Embed(ctx, text)
}

// EmbedMultiple 使用pool向量化多个文本
func (s *GlobalPoolService) EmbedMultiple(ctx context.Context, texts []string) ([][]float64, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if !s.initialized {
		return nil, fmt.Errorf("pool service not initialized")
	}

	return s.embeddingPool.EmbedMultiple(ctx, texts)
}

// EmbedBatch 使用pool批量向量化（实现 domain.Embedder 接口）
func (s *GlobalPoolService) EmbedBatch(ctx context.Context, texts []string) ([][]float64, error) {
	return s.EmbedMultiple(ctx, texts)
}

// GetLLMStatus 获取LLM pool状态
func (s *GlobalPoolService) GetLLMStatus() map[string]pool.ClientStatus {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if !s.initialized {
		return nil
	}

	return s.llmPool.GetStatus()
}

// GetEmbeddingStatus 获取Embedding pool状态
func (s *GlobalPoolService) GetEmbeddingStatus() map[string]pool.ClientStatus {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if !s.initialized {
		return nil
	}

	return s.embeddingPool.GetStatus()
}

// IsInitialized 是否已初始化
func (s *GlobalPoolService) IsInitialized() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.initialized
}

// Close 关闭pool
func (s *GlobalPoolService) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if !s.initialized {
		return nil
	}

	if s.llmPool != nil {
		s.llmPool.Close()
	}
	if s.embeddingPool != nil {
		s.embeddingPool.Close()
	}

	s.initialized = false
	return nil
}

// Shutdown 关闭并清理全局pool
func (s *GlobalPoolService) Shutdown() error {
	globalPoolMu.Lock()
	defer globalPoolMu.Unlock()

	if err := s.Close(); err != nil {
		return err
	}

	globalPoolService = nil
	return nil
}

// mustGetConfig reads a config key from the DB, returning fallback on any error.
func mustGetConfig(db *store.AgentGoDB, key, fallback string) string {
	v, err := db.GetConfig(key)
	if err != nil || v == "" {
		return fallback
	}
	return v
}

// ===== LLM Pool Config =====

// LLMPoolConfig holds the pool-level settings stored in the database.
type LLMPoolConfig struct {
	Strategy pool.SelectionStrategy `json:"strategy"`
	Enabled  bool                   `json:"enabled"`
}

// GetLLMPoolConfig returns the current pool-level settings from the database.
func (s *GlobalPoolService) GetLLMPoolConfig() (*LLMPoolConfig, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if !s.initialized {
		return nil, fmt.Errorf("pool service not initialized")
	}
	strategy := mustGetConfig(s.db, "llm.strategy", "round_robin")
	enabled := mustGetConfig(s.db, "llm.enabled", "true") == "true"
	return &LLMPoolConfig{
		Strategy: pool.SelectionStrategy(strategy),
		Enabled:  enabled,
	}, nil
}

// SaveLLMPoolConfig persists pool-level settings to the database.
// Changes take effect on next restart (pool strategy/enabled cannot be
// hot-swapped without rebuilding the pool).
func (s *GlobalPoolService) SaveLLMPoolConfig(cfg LLMPoolConfig) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if !s.initialized {
		return fmt.Errorf("pool service not initialized")
	}
	enabled := "false"
	if cfg.Enabled {
		enabled = "true"
	}
	if err := s.db.SaveConfig("llm.strategy", string(cfg.Strategy)); err != nil {
		return fmt.Errorf("save llm.strategy: %w", err)
	}
	if err := s.db.SaveConfig("llm.enabled", enabled); err != nil {
		return fmt.Errorf("save llm.enabled: %w", err)
	}
	return nil
}

// ===== Provider CRUD =====

// ListProviders returns all persisted providers from the database.
func (s *GlobalPoolService) ListProviders() ([]*store.LLMProvider, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if !s.initialized {
		return nil, fmt.Errorf("pool service not initialized")
	}
	return s.db.ListProviders()
}

// SaveProvider persists a provider to the database and syncs the live pool.
// Creates a new provider if it doesn't exist; updates it otherwise.
func (s *GlobalPoolService) SaveProvider(p *store.LLMProvider) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if !s.initialized {
		return fmt.Errorf("pool service not initialized")
	}
	if err := s.db.SaveProvider(p); err != nil {
		return err
	}
	return s.syncProviderToPool(p)
}

// DeleteProvider removes a provider from the database and the live pool.
func (s *GlobalPoolService) DeleteProvider(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if !s.initialized {
		return fmt.Errorf("pool service not initialized")
	}
	if err := s.db.DeleteProvider(name); err != nil {
		return err
	}
	_ = s.llmPool.RemoveProvider(name) // ignore "not found" — already gone
	return nil
}

// GetProvider returns a single provider by name from the database.
func (s *GlobalPoolService) GetProvider(name string) (*store.LLMProvider, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if !s.initialized {
		return nil, fmt.Errorf("pool service not initialized")
	}
	return s.db.GetProvider(name)
}

// syncProviderToPool updates (or adds/removes) a provider in the live pool
// based on its Enabled flag. Must be called with s.mu held for writing.
func (s *GlobalPoolService) syncProviderToPool(p *store.LLMProvider) error {
	prov := store.ToPoolProvider(p)
	if !p.Enabled {
		_ = s.llmPool.RemoveProvider(p.Name)
		return nil
	}
	if err := s.llmPool.UpdateProvider(prov); err != nil {
		// Provider not in pool yet — add it.
		return s.llmPool.AddProvider(prov)
	}
	return nil
}

// ===== Embedding Provider CRUD =====

// ListEmbeddingProviders returns all persisted embedding providers from the database.
func (s *GlobalPoolService) ListEmbeddingProviders() ([]*store.EmbeddingProvider, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if !s.initialized {
		return nil, fmt.Errorf("pool service not initialized")
	}
	return s.db.ListEmbeddingProviders()
}

// SaveEmbeddingProvider persists an embedding provider and syncs the live pool.
func (s *GlobalPoolService) SaveEmbeddingProvider(p *store.EmbeddingProvider) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.initialized {
		return fmt.Errorf("pool service not initialized")
	}
	if err := s.db.SaveEmbeddingProvider(p); err != nil {
		return err
	}
	prov := store.ToPoolEmbeddingProvider(p)
	if !p.Enabled {
		_ = s.embeddingPool.RemoveProvider(p.Name)
		return nil
	}
	if err := s.embeddingPool.UpdateProvider(prov); err != nil {
		return s.embeddingPool.AddProvider(prov)
	}
	return nil
}

// DeleteEmbeddingProvider removes an embedding provider from DB and live pool.
func (s *GlobalPoolService) DeleteEmbeddingProvider(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.initialized {
		return fmt.Errorf("pool service not initialized")
	}
	if err := s.db.DeleteEmbeddingProvider(name); err != nil {
		return err
	}
	_ = s.embeddingPool.RemoveProvider(name)
	return nil
}

// GetEmbeddingProvider returns a single embedding provider by name.
func (s *GlobalPoolService) GetEmbeddingProvider(name string) (*store.EmbeddingProvider, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if !s.initialized {
		return nil, fmt.Errorf("pool service not initialized")
	}
	return s.db.GetEmbeddingProvider(name)
}

// EmbeddingPoolConfig holds pool-level settings for the embedding pool.
type EmbeddingPoolConfig struct {
	Strategy pool.SelectionStrategy `json:"strategy"`
	Enabled  bool                   `json:"enabled"`
}

// GetEmbeddingPoolConfig returns current embedding pool settings from the database.
func (s *GlobalPoolService) GetEmbeddingPoolConfig() (*EmbeddingPoolConfig, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if !s.initialized {
		return nil, fmt.Errorf("pool service not initialized")
	}
	strategy := mustGetConfig(s.db, "embedding.strategy", "round_robin")
	enabled := mustGetConfig(s.db, "embedding.enabled", "false") == "true"
	return &EmbeddingPoolConfig{
		Strategy: pool.SelectionStrategy(strategy),
		Enabled:  enabled,
	}, nil
}

// SaveEmbeddingPoolConfig persists embedding pool-level settings.
// Changes take effect on next restart.
func (s *GlobalPoolService) SaveEmbeddingPoolConfig(cfg EmbeddingPoolConfig) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.initialized {
		return fmt.Errorf("pool service not initialized")
	}
	enabled := "false"
	if cfg.Enabled {
		enabled = "true"
	}
	if err := s.db.SaveConfig("embedding.strategy", string(cfg.Strategy)); err != nil {
		return fmt.Errorf("save embedding.strategy: %w", err)
	}
	if err := s.db.SaveConfig("embedding.enabled", enabled); err != nil {
		return fmt.Errorf("save embedding.enabled: %w", err)
	}
	return nil
}

// ===== 兼容层 - 让旧代码继续工作 =====

// llmServiceWrapper 包装Pool为domain.Generator
type llmServiceWrapper struct {
	pool *pool.Pool
	hint pool.SelectionHint
}

func (w *llmServiceWrapper) GetModelName() string {
	client, err := w.pool.GetWithHint(w.hint)
	if err != nil {
		return w.hint.PreferredModel
	}
	defer w.pool.Release(client)
	return client.GetModelName()
}

func (w *llmServiceWrapper) GetBaseURL() string {
	client, err := w.pool.GetWithHint(w.hint)
	if err != nil {
		return ""
	}
	defer w.pool.Release(client)
	return client.GetBaseURL()
}

func (w *llmServiceWrapper) Generate(ctx context.Context, prompt string, opts *domain.GenerationOptions) (string, error) {
	client, err := w.pool.GetWithHint(w.hint)
	if err != nil {
		return "", err
	}
	defer w.pool.Release(client)
	return client.Generate(ctx, prompt, opts)
}

func (w *llmServiceWrapper) Stream(ctx context.Context, prompt string, opts *domain.GenerationOptions, callback func(string)) error {
	client, err := w.pool.GetWithHint(w.hint)
	if err != nil {
		return err
	}
	defer w.pool.Release(client)
	return client.Stream(ctx, prompt, opts, callback)
}

func (w *llmServiceWrapper) GenerateWithTools(ctx context.Context, messages []domain.Message, tools []domain.ToolDefinition, opts *domain.GenerationOptions) (*domain.GenerationResult, error) {
	client, err := w.pool.GetWithHint(w.hint)
	if err != nil {
		return nil, err
	}
	defer w.pool.Release(client)
	return client.GenerateWithTools(ctx, messages, tools, opts)
}

func (w *llmServiceWrapper) StreamWithTools(ctx context.Context, messages []domain.Message, tools []domain.ToolDefinition, opts *domain.GenerationOptions, callback domain.ToolCallCallback) error {
	client, err := w.pool.GetWithHint(w.hint)
	if err != nil {
		return err
	}
	defer w.pool.Release(client)
	return client.StreamWithTools(ctx, messages, tools, opts, callback)
}

func (w *llmServiceWrapper) GenerateStructured(ctx context.Context, prompt string, schema interface{}, opts *domain.GenerationOptions) (*domain.StructuredResult, error) {
	client, err := w.pool.GetWithHint(w.hint)
	if err != nil {
		return nil, err
	}
	defer w.pool.Release(client)
	return client.GenerateStructured(ctx, prompt, schema, opts)
}

func (w *llmServiceWrapper) RecognizeIntent(ctx context.Context, request string) (*domain.IntentResult, error) {
	client, err := w.pool.GetWithHint(w.hint)
	if err != nil {
		return nil, err
	}
	defer w.pool.Release(client)
	return client.RecognizeIntent(ctx, request)
}

func (w *llmServiceWrapper) ExtractMetadata(ctx context.Context, content string, model string) (*domain.ExtractedMetadata, error) {
	return w.pool.ExtractMetadataWithHint(ctx, w.hint, content, model)
}

// embeddingServiceWrapper 包装Pool为domain.Embedder
type embeddingServiceWrapper struct {
	pool *pool.Pool
}

func (w *embeddingServiceWrapper) Embed(ctx context.Context, text string) ([]float64, error) {
	return w.pool.Embed(ctx, text)
}

func (w *embeddingServiceWrapper) EmbedBatch(ctx context.Context, texts []string) ([][]float64, error) {
	return w.pool.EmbedMultiple(ctx, texts)
}

// GetGlobalLLM 获取全局LLM服务（兼容旧代码）
func GetGlobalLLM() (domain.Generator, error) {
	service := GetGlobalPoolService()
	if !service.IsInitialized() {
		return nil, fmt.Errorf("pool service not initialized")
	}
	return &llmServiceWrapper{pool: service.llmPool}, nil
}

// GetGlobalEmbeddingService 获取全局Embedding服务（兼容旧代码）
func GetGlobalEmbeddingService(ctx context.Context) (domain.Embedder, error) {
	service := GetGlobalPoolService()
	if !service.IsInitialized() {
		return nil, fmt.Errorf("pool service not initialized")
	}
	return &embeddingServiceWrapper{pool: service.embeddingPool}, nil
}

// GetGlobalLLMService 获取全局LLM Service（兼容旧代码）
// 这个函数返回GlobalPoolService，兼容旧的GetGlobalLLMService()调用
func GetGlobalLLMService() *GlobalPoolService {
	return GetGlobalPoolService()
}

// GetLLMService 获取LLM服务（兼容旧代码）
func (s *GlobalPoolService) GetLLMService() (domain.Generator, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if !s.initialized {
		return nil, fmt.Errorf("pool service not initialized")
	}
	return &llmServiceWrapper{pool: s.llmPool}, nil
}

func (s *GlobalPoolService) GetLLMServiceByProvider(name string) (domain.Generator, error) {
	return s.GetLLMServiceWithHint(pool.SelectionHint{PreferredProvider: name})
}

func (s *GlobalPoolService) GetLLMServiceByModel(modelName string) (domain.Generator, error) {
	return s.GetLLMServiceWithHint(pool.SelectionHint{PreferredModel: modelName})
}

func (s *GlobalPoolService) GetLLMServiceWithHint(hint pool.SelectionHint) (domain.Generator, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if !s.initialized {
		return nil, fmt.Errorf("pool service not initialized")
	}
	return &llmServiceWrapper{pool: s.llmPool, hint: hint}, nil
}

// GetEmbeddingService 获取Embedding服务（兼容旧代码）
func (s *GlobalPoolService) GetEmbeddingService(ctx context.Context) (domain.Embedder, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if !s.initialized {
		return nil, fmt.Errorf("pool service not initialized")
	}
	return &embeddingServiceWrapper{pool: s.embeddingPool}, nil
}
