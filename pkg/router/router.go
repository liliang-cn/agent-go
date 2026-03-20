// Package router provides semantic routing for intent classification using cortexdb v2.7.0
package router

import (
	"context"
	"fmt"
	"sync"

	"github.com/liliang-cn/agent-go/v2/pkg/domain"
	semanticrouter "github.com/liliang-cn/cortexdb/v2/pkg/semantic-router"
)

// Router wraps cortexdb's semantic router for intent classification
type Router struct {
	router   *semanticrouter.Router
	embedder domain.Embedder
	mu       sync.RWMutex
	intents  []*Intent // Track registered intents
}

// Config holds router configuration
type Config struct {
	// Threshold is the minimum similarity score for route matching (default: 0.82)
	Threshold float64

	// TopK is the number of top route candidates to consider
	TopK int
}

// DefaultConfig returns sensible defaults
func DefaultConfig() *Config {
	return &Config{
		Threshold: 0.82,
		TopK:      1,
	}
}

// New creates a new semantic router
func New(embedder domain.Embedder, cfg *Config) (*Router, error) {
	if embedder == nil {
		return nil, fmt.Errorf("embedder cannot be nil")
	}
	if cfg == nil {
		cfg = DefaultConfig()
	}

	// Create adapter embedder
	adapter := &embedderAdapter{embedder: embedder}

	// Create cortexdb semantic router
	cortexdbRouter, err := semanticrouter.NewRouter(
		adapter,
		semanticrouter.WithThreshold(cfg.Threshold),
		semanticrouter.WithTopK(cfg.TopK),
		semanticrouter.WithCacheEmbeddings(true), // Enable embedding cache
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create semantic router: %w", err)
	}

	return &Router{
		router:   cortexdbRouter,
		embedder: embedder,
		intents:  make([]*Intent, 0),
	}, nil
}

// Route classifies the query and returns the matched intent
func (r *Router) Route(ctx context.Context, query string) (*RouteResult, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	result, err := r.router.Route(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("routing failed: %w", err)
	}

	return &RouteResult{
		IntentName: result.RouteName,
		Score:      result.Score,
		Matched:    result.Matched,
	}, nil
}

// AddIntent adds a new intent route with example utterances
func (r *Router) AddIntent(intent *Intent) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	route := &semanticrouter.Route{
		Name:       intent.Name,
		Utterances: intent.Utterances,
		Metadata:   intent.Metadata,
	}

	if err := r.router.Add(route); err != nil {
		return err
	}

	r.intents = append(r.intents, intent)
	return nil
}

// AddIntentBatch adds multiple intents at once
func (r *Router) AddIntentBatch(intents []*Intent) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	routes := make([]*semanticrouter.Route, len(intents))
	for i, intent := range intents {
		routes[i] = &semanticrouter.Route{
			Name:       intent.Name,
			Utterances: intent.Utterances,
			Metadata:   intent.Metadata,
		}
	}

	if err := r.router.AddBatch(routes); err != nil {
		return err
	}

	r.intents = append(r.intents, intents...)
	return nil
}

// RemoveIntent removes an intent by name
func (r *Router) RemoveIntent(name string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if err := r.router.Remove(name); err != nil {
		return err
	}

	// Remove from tracking
	for i, intent := range r.intents {
		if intent.Name == name {
			r.intents = append(r.intents[:i], r.intents[i+1:]...)
			break
		}
	}

	return nil
}

// ListIntents returns all registered intents
func (r *Router) ListIntents() []*Intent {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.intents
}

// Close releases resources
func (r *Router) Close() error {
	// Router doesn't hold persistent connections
	return nil
}

// Intent represents a semantic intent with example utterances
type Intent struct {
	Name       string            `json:"name"`
	Utterances []string          `json:"utterances"`
	Metadata   map[string]string `json:"metadata,omitempty"`
}

// RouteResult represents the result of semantic routing
type RouteResult struct {
	IntentName string  `json:"intent_name"`
	Score      float64 `json:"score"`
	Matched    bool    `json:"matched"`
}

// embedderAdapter adapts domain.Embedder to semanticrouter.Embedder
type embedderAdapter struct {
	embedder domain.Embedder
}

func (a *embedderAdapter) Embed(ctx context.Context, text string) ([]float32, error) {
	vec, err := a.embedder.Embed(ctx, text)
	if err != nil {
		return nil, err
	}
	if len(vec) == 0 {
		return nil, fmt.Errorf("empty embedding result")
	}
	return toFloat32(vec), nil
}

func (a *embedderAdapter) EmbedBatch(ctx context.Context, texts []string) ([][]float32, error) {
	result := make([][]float32, len(texts))
	for i, text := range texts {
		vec, err := a.embedder.Embed(ctx, text)
		if err != nil {
			return nil, fmt.Errorf("embed batch failed at index %d: %w", i, err)
		}
		result[i] = toFloat32(vec)
	}
	return result, nil
}

func (a *embedderAdapter) Dimensions() int {
	// Return default dimension, will be detected from first embedding
	return 1536 // OpenAI default, will adjust on first embed
}

func toFloat32(v []float64) []float32 {
	result := make([]float32, len(v))
	for i, f := range v {
		result[i] = float32(f)
	}
	return result
}
