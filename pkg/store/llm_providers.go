package store

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/liliang-cn/agent-go/v3/pkg/domain"
	"github.com/liliang-cn/agent-go/v3/pkg/pool"
)

// LLMProvider persisted provider configuration.
type LLMProvider struct {
	Name           string   `json:"name"`
	BaseURL        string   `json:"base_url"`
	Key            string   `json:"key"`
	ModelName      string   `json:"model_name"`
	Models         []string `json:"models,omitempty"`
	MaxConcurrency int      `json:"max_concurrency"`
	Capability     int      `json:"capability"`
	// NativeWebSearch declares the provider's built-in web search; see
	// pool.Provider.NativeWebSearch. Empty means undeclared.
	NativeWebSearch string `json:"native_web_search,omitempty"`
	// NativeWebSearchOptions are sent inside the declared field; see
	// pool.Provider.NativeWebSearchOptions. Stored as JSON.
	NativeWebSearchOptions map[string]interface{} `json:"native_web_search_options,omitempty"`
	Enabled                bool                   `json:"enabled"`
	CreatedAt              time.Time              `json:"created_at"`
	UpdatedAt              time.Time              `json:"updated_at"`
}

// SaveProvider inserts or replaces an LLM provider record.
func (s *AgentGoDB) SaveProvider(p *LLMProvider) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	normalizeLLMProvider(p)
	if p.ModelName == "" {
		return fmt.Errorf("provider %q must have a default model", p.Name)
	}
	// Refused here rather than when the pool builds the client: by then the
	// row is written, and a typo would sit in the database failing every
	// start.
	format, err := domain.ParseNativeWebSearchFormat(p.NativeWebSearch)
	if err != nil {
		return fmt.Errorf("provider %q: %w", p.Name, err)
	}
	p.NativeWebSearch = string(format)
	if err := domain.ValidateNativeWebSearchOptions(format, p.NativeWebSearchOptions); err != nil {
		return fmt.Errorf("provider %q: %w", p.Name, err)
	}
	searchOptions := ""
	if len(p.NativeWebSearchOptions) > 0 {
		raw, merr := json.Marshal(p.NativeWebSearchOptions)
		if merr != nil {
			return fmt.Errorf("provider %q: native_web_search_options: %w", p.Name, merr)
		}
		searchOptions = string(raw)
	}

	now := time.Now()
	p.UpdatedAt = now
	if p.CreatedAt.IsZero() {
		p.CreatedAt = now
	}
	if p.MaxConcurrency <= 0 {
		p.MaxConcurrency = 5
	}

	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	_, err = tx.Exec(`
		INSERT INTO llm_providers (name, base_url, key, model_name, max_concurrency, capability, native_web_search, native_web_search_options, enabled, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(name) DO UPDATE SET
			base_url          = excluded.base_url,
			key               = excluded.key,
			model_name        = excluded.model_name,
			max_concurrency   = excluded.max_concurrency,
			capability        = excluded.capability,
			native_web_search = excluded.native_web_search,
			native_web_search_options = excluded.native_web_search_options,
			enabled           = excluded.enabled,
			updated_at        = excluded.updated_at
	`, p.Name, p.BaseURL, p.Key, p.ModelName, p.MaxConcurrency, p.Capability, p.NativeWebSearch, searchOptions, p.Enabled, p.CreatedAt, p.UpdatedAt)
	if err != nil {
		return err
	}

	if _, err = tx.Exec(`DELETE FROM llm_provider_models WHERE provider_name = ?`, p.Name); err != nil {
		return err
	}

	for _, model := range p.Models {
		isDefault := strings.EqualFold(model, p.ModelName)
		if _, err = tx.Exec(`
			INSERT INTO llm_provider_models (provider_name, model_name, is_default, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?)
		`, p.Name, model, isDefault, p.CreatedAt, p.UpdatedAt); err != nil {
			return err
		}
	}

	return tx.Commit()
}

// GetProvider returns one provider by name.
func (s *AgentGoDB) GetProvider(name string) (*LLMProvider, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	row := s.db.QueryRow(`
		SELECT name, base_url, key, model_name, max_concurrency, capability, native_web_search, native_web_search_options, enabled, created_at, updated_at
		FROM llm_providers WHERE name = ?`, name)

	p, err := scanProvider(row)
	if err != nil {
		return nil, err
	}
	if err := s.hydrateProviderModels(p); err != nil {
		return nil, err
	}
	return p, nil
}

// ListProviders returns all providers ordered by name.
func (s *AgentGoDB) ListProviders() ([]*LLMProvider, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	rows, err := s.db.Query(`
		SELECT name, base_url, key, model_name, max_concurrency, capability, native_web_search, native_web_search_options, enabled, created_at, updated_at
		FROM llm_providers ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var providers []*LLMProvider
	for rows.Next() {
		p, err := scanProvider(rows)
		if err != nil {
			return nil, err
		}
		providers = append(providers, p)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, provider := range providers {
		if err := s.hydrateProviderModels(provider); err != nil {
			return nil, err
		}
	}
	return providers, nil
}

// DeleteProvider removes a provider by name.
func (s *AgentGoDB) DeleteProvider(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, err := s.db.Exec(`DELETE FROM llm_provider_models WHERE provider_name = ?`, name); err != nil {
		return err
	}

	res, err := s.db.Exec(`DELETE FROM llm_providers WHERE name = ?`, name)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("provider %q not found", name)
	}
	return nil
}

// ToPoolProvider converts a persisted LLMProvider to the pool.Provider type.
func ToPoolProvider(p *LLMProvider) pool.Provider {
	normalizeLLMProvider(p)
	return pool.Provider{
		Name:                   p.Name,
		BaseURL:                p.BaseURL,
		Key:                    p.Key,
		ModelName:              p.ModelName,
		Models:                 append([]string(nil), p.Models...),
		MaxConcurrency:         p.MaxConcurrency,
		Capability:             p.Capability,
		NativeWebSearch:        p.NativeWebSearch,
		NativeWebSearchOptions: p.NativeWebSearchOptions,
	}
}

type providerScanner interface {
	Scan(dest ...any) error
}

func scanProvider(s providerScanner) (*LLMProvider, error) {
	var p LLMProvider
	var searchOptions string
	err := s.Scan(&p.Name, &p.BaseURL, &p.Key, &p.ModelName,
		&p.MaxConcurrency, &p.Capability, &p.NativeWebSearch, &searchOptions, &p.Enabled, &p.CreatedAt, &p.UpdatedAt)
	if err != nil {
		return nil, err
	}
	if searchOptions != "" {
		if err := json.Unmarshal([]byte(searchOptions), &p.NativeWebSearchOptions); err != nil {
			return nil, fmt.Errorf("provider %q: native_web_search_options: %w", p.Name, err)
		}
	}
	return &p, nil
}

func (s *AgentGoDB) hydrateProviderModels(p *LLMProvider) error {
	if p == nil {
		return nil
	}

	rows, err := s.db.Query(`
		SELECT model_name
		FROM llm_provider_models
		WHERE provider_name = ?
		ORDER BY is_default DESC, model_name ASC
	`, p.Name)
	if err != nil {
		return err
	}
	defer rows.Close()

	var models []string
	for rows.Next() {
		var model string
		if err := rows.Scan(&model); err != nil {
			return err
		}
		models = append(models, model)
	}
	if err := rows.Err(); err != nil {
		return err
	}

	if len(models) == 0 && strings.TrimSpace(p.ModelName) != "" {
		models = []string{strings.TrimSpace(p.ModelName)}
	}
	p.Models = models
	normalizeLLMProvider(p)
	return nil
}

func normalizeLLMProvider(p *LLMProvider) {
	if p == nil {
		return
	}

	defaultModel := strings.TrimSpace(p.ModelName)
	models := normalizeModelList(defaultModel, p.Models)
	if defaultModel == "" && len(models) > 0 {
		defaultModel = models[0]
	}
	p.ModelName = defaultModel
	p.Models = models
}

func normalizeModelList(defaultModel string, models []string) []string {
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
