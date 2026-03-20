package router

import (
	"context"

	"fmt"

	"sync"

	"github.com/liliang-cn/agent-go/v2/pkg/domain"

	"github.com/liliang-cn/agent-go/v2/pkg/prompt"
)

// Manager manages multiple routers for different routing contexts

type Manager struct {
	primary *Service

	routers map[string]*Service // Named routers for specific contexts

	embedder domain.Embedder

	mu sync.RWMutex

	promptManager *prompt.Manager
}

// NewManager creates a new router manager

func NewManager(embedder domain.Embedder) (*Manager, error) {

	cfg := DefaultConfig()

	primary, err := NewService(embedder, cfg)

	if err != nil {

		return nil, err

	}

	return &Manager{

		primary: primary,

		routers: make(map[string]*Service),

		embedder: embedder,

		promptManager: prompt.NewManager(),
	}, nil

}

// SetPromptManager sets a custom prompt manager

func (m *Manager) SetPromptManager(mgr *prompt.Manager) {

	m.mu.Lock()

	defer m.mu.Unlock()

	m.promptManager = mgr

}

// Primary returns the primary router service

func (m *Manager) Primary() *Service {

	return m.primary

}

// GetOrCreate gets or creates a named router for a specific context

func (m *Manager) GetOrCreate(name string, cfg *Config) (*Service, error) {

	m.mu.Lock()

	defer m.mu.Unlock()

	if svc, ok := m.routers[name]; ok {

		return svc, nil

	}

	if cfg == nil {

		cfg = DefaultConfig()

	}

	svc, err := NewService(m.embedder, cfg)

	if err != nil {

		return nil, fmt.Errorf("failed to create router %q: %w", name, err)

	}

	m.routers[name] = svc

	return svc, nil

}

// Close closes all routers

func (m *Manager) Close() error {

	m.mu.Lock()

	defer m.mu.Unlock()

	var firstErr error

	for _, svc := range m.routers {

		if err := svc.Close(); err != nil && firstErr == nil {

			firstErr = err

		}

	}

	if err := m.primary.Close(); err != nil && firstErr == nil {

		firstErr = err

	}

	return firstErr

}

// IntentRecognitionResult is the result of intent recognition

// This matches the agent planner's expected result structure

type IntentRecognitionResult struct {
	IntentType string `json:"intent_type"` // The matched intent name

	TargetFile string `json:"target_file"` // Extracted file path if applicable

	Topic string `json:"topic"` // Main topic/subject

	Requirements []string `json:"requirements"` // Specific requirements extracted

	Confidence float64 `json:"confidence"` // Confidence score

}

// RecognizeIntent performs intent recognition using the semantic router

func (s *Service) RecognizeIntent(ctx context.Context, query string) (*IntentRecognitionResult, error) {

	result, err := s.Route(ctx, query)

	if err != nil {

		// Return a fallback result on error

		return &IntentRecognitionResult{

			IntentType: "general_qa",

			Confidence: 0.0,
		}, nil

	}

	intentResult := &IntentRecognitionResult{

		IntentType: result.IntentName,

		Confidence: result.Score,
	}

	// Extract parameters

	if path, ok := result.Parameters["path"]; ok {

		intentResult.TargetFile = path

	}

	if topic, ok := result.Parameters["query"]; ok {

		intentResult.Topic = topic

	} else {

		// Use the full query as topic if no specific query parameter

		intentResult.Topic = query

	}

	return intentResult, nil

}

// FallbackLLMRecognizer provides LLM-based intent recognition as fallback

type FallbackLLMRecognizer struct {
	llm domain.Generator

	fallback *Service

	promptManager *prompt.Manager
}

// NewFallbackLLMRecognizer creates a recognizer that uses semantic router first,

// falling back to LLM-based recognition if no match is found

func NewFallbackLLMRecognizer(router *Service, llm domain.Generator) *FallbackLLMRecognizer {

	return &FallbackLLMRecognizer{

		llm: llm,

		fallback: router,

		promptManager: prompt.NewManager(),
	}

}

// SetPromptManager sets a custom prompt manager

func (r *FallbackLLMRecognizer) SetPromptManager(m *prompt.Manager) {

	r.promptManager = m

}

// RecognizeIntent first tries semantic router, then falls back to LLM

func (r *FallbackLLMRecognizer) RecognizeIntent(ctx context.Context, query string) (*IntentRecognitionResult, error) {

	// Try semantic router first

	result, err := r.fallback.Route(ctx, query)

	if err == nil && result.Matched && result.Score >= 0.75 {

		// Good semantic match

		intentResult := &IntentRecognitionResult{

			IntentType: result.IntentName,

			Confidence: result.Score,
		}

		if path, ok := result.Parameters["path"]; ok {

			intentResult.TargetFile = path

		}

		if topic, ok := result.Parameters["query"]; ok {

			intentResult.Topic = topic

		}

		return intentResult, nil

	}

	// Fall back to LLM-based recognition

	return r.llmRecognize(ctx, query)

}

// llmRecognize uses LLM for intent recognition when semantic router fails

func (r *FallbackLLMRecognizer) llmRecognize(ctx context.Context, query string) (*IntentRecognitionResult, error) {

	promptText := r.buildRecognitionPrompt(query)

	content, err := r.llm.Generate(ctx, promptText, nil)

	if err != nil {

		// Final fallback

		return &IntentRecognitionResult{

			IntentType: "general_qa",

			Confidence: 0.5,

			Topic: query,
		}, nil

	}

	// Parse LLM response

	return r.parseRecognitionResult(content, query)

}

func (r *FallbackLLMRecognizer) buildRecognitionPrompt(query string) string {

	data := map[string]interface{}{

		"Query": query,

		"Intents": "rag_query, memory_recall, memory_save, file_create, file_read, web_search, analysis, general_qa",
	}

	rendered, err := r.promptManager.Render(prompt.RouterIntentAnalysis, data)

	if err != nil {

		return fmt.Sprintf("Classify intent for query: %s", query)

	}

	return rendered

}

func (r *FallbackLLMRecognizer) parseRecognitionResult(content, query string) (*IntentRecognitionResult, error) {
	// Simple parsing - in production would use proper JSON parsing
	// For now, return a basic result
	return &IntentRecognitionResult{
		IntentType: "general_qa",
		Confidence: 0.6,
		Topic:      query,
	}, nil
}
