package store

import (
	"fmt"
	"time"

	"github.com/liliang-cn/agent-go/pkg/pool"
)

// LLMProvider persisted provider configuration.
type LLMProvider struct {
	Name           string    `json:"name"`
	BaseURL        string    `json:"base_url"`
	Key            string    `json:"key"`
	ModelName      string    `json:"model_name"`
	MaxConcurrency int       `json:"max_concurrency"`
	Capability     int       `json:"capability"`
	Enabled        bool      `json:"enabled"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

// SaveProvider inserts or replaces an LLM provider record.
func (s *AgentGoDB) SaveProvider(p *LLMProvider) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now()
	p.UpdatedAt = now
	if p.CreatedAt.IsZero() {
		p.CreatedAt = now
	}
	if p.MaxConcurrency <= 0 {
		p.MaxConcurrency = 5
	}

	_, err := s.db.Exec(`
		INSERT INTO llm_providers (name, base_url, key, model_name, max_concurrency, capability, enabled, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(name) DO UPDATE SET
			base_url        = excluded.base_url,
			key             = excluded.key,
			model_name      = excluded.model_name,
			max_concurrency = excluded.max_concurrency,
			capability      = excluded.capability,
			enabled         = excluded.enabled,
			updated_at      = excluded.updated_at
	`, p.Name, p.BaseURL, p.Key, p.ModelName, p.MaxConcurrency, p.Capability, p.Enabled, p.CreatedAt, p.UpdatedAt)
	return err
}

// GetProvider returns one provider by name.
func (s *AgentGoDB) GetProvider(name string) (*LLMProvider, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	row := s.db.QueryRow(`
		SELECT name, base_url, key, model_name, max_concurrency, capability, enabled, created_at, updated_at
		FROM llm_providers WHERE name = ?`, name)

	return scanProvider(row)
}

// ListProviders returns all providers ordered by name.
func (s *AgentGoDB) ListProviders() ([]*LLMProvider, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	rows, err := s.db.Query(`
		SELECT name, base_url, key, model_name, max_concurrency, capability, enabled, created_at, updated_at
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
	return providers, rows.Err()
}

// DeleteProvider removes a provider by name.
func (s *AgentGoDB) DeleteProvider(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

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
	return pool.Provider{
		Name:           p.Name,
		BaseURL:        p.BaseURL,
		Key:            p.Key,
		ModelName:      p.ModelName,
		MaxConcurrency: p.MaxConcurrency,
		Capability:     p.Capability,
	}
}

type providerScanner interface {
	Scan(dest ...any) error
}

func scanProvider(s providerScanner) (*LLMProvider, error) {
	var p LLMProvider
	err := s.Scan(&p.Name, &p.BaseURL, &p.Key, &p.ModelName,
		&p.MaxConcurrency, &p.Capability, &p.Enabled, &p.CreatedAt, &p.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &p, nil
}
