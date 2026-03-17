package store

import (
	"fmt"
	"time"

	"github.com/liliang-cn/agent-go/pkg/pool"
)

// EmbeddingProvider represents a persisted embedding provider.
type EmbeddingProvider struct {
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

// ToPoolProvider converts a persisted EmbeddingProvider to pool.Provider.
func ToPoolEmbeddingProvider(p *EmbeddingProvider) pool.Provider {
	return pool.Provider{
		Name:           p.Name,
		BaseURL:        p.BaseURL,
		Key:            p.Key,
		ModelName:      p.ModelName,
		MaxConcurrency: p.MaxConcurrency,
		Capability:     p.Capability,
	}
}

// SaveEmbeddingProvider upserts an embedding provider into the database.
func (s *AgentGoDB) SaveEmbeddingProvider(p *EmbeddingProvider) error {
	now := time.Now()
	_, err := s.db.Exec(`
		INSERT INTO embedding_providers (name, base_url, key, model_name, max_concurrency, capability, enabled, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(name) DO UPDATE SET
			base_url       = excluded.base_url,
			key            = excluded.key,
			model_name     = excluded.model_name,
			max_concurrency = excluded.max_concurrency,
			capability     = excluded.capability,
			enabled        = excluded.enabled,
			updated_at     = excluded.updated_at
	`, p.Name, p.BaseURL, p.Key, p.ModelName, p.MaxConcurrency, p.Capability, p.Enabled, now, now)
	return err
}

// GetEmbeddingProvider returns a single embedding provider by name.
func (s *AgentGoDB) GetEmbeddingProvider(name string) (*EmbeddingProvider, error) {
	row := s.db.QueryRow(`
		SELECT name, base_url, key, model_name, max_concurrency, capability, enabled, created_at, updated_at
		FROM embedding_providers WHERE name = ?`, name)
	p, err := scanEmbeddingProvider(row)
	if err != nil {
		return nil, fmt.Errorf("embedding provider %q not found: %w", name, err)
	}
	return p, nil
}

// ListEmbeddingProviders returns all embedding providers ordered by name.
func (s *AgentGoDB) ListEmbeddingProviders() ([]*EmbeddingProvider, error) {
	rows, err := s.db.Query(`
		SELECT name, base_url, key, model_name, max_concurrency, capability, enabled, created_at, updated_at
		FROM embedding_providers ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var providers []*EmbeddingProvider
	for rows.Next() {
		p, err := scanEmbeddingProvider(rows)
		if err != nil {
			return nil, err
		}
		providers = append(providers, p)
	}
	return providers, rows.Err()
}

// DeleteEmbeddingProvider removes an embedding provider by name.
func (s *AgentGoDB) DeleteEmbeddingProvider(name string) error {
	res, err := s.db.Exec(`DELETE FROM embedding_providers WHERE name = ?`, name)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("embedding provider %q not found", name)
	}
	return nil
}

type embeddingProviderScanner interface {
	Scan(dest ...any) error
}

func scanEmbeddingProvider(s embeddingProviderScanner) (*EmbeddingProvider, error) {
	var p EmbeddingProvider
	err := s.Scan(
		&p.Name, &p.BaseURL, &p.Key, &p.ModelName,
		&p.MaxConcurrency, &p.Capability, &p.Enabled,
		&p.CreatedAt, &p.UpdatedAt,
	)
	return &p, err
}
