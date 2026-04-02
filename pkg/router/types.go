package router

import (
	"context"
	"encoding/json"

	"fmt"
	"regexp"
	"strings"

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

	Confidence     float64 `json:"confidence"` // Confidence score
	RequiresTools  bool    `json:"requires_tools,omitempty"`
	PreferredAgent string  `json:"preferred_agent,omitempty"`
	Transition     string  `json:"transition,omitempty"`
}

func populateIntentExecutionHints(intent *IntentRecognitionResult) *IntentRecognitionResult {
	if intent == nil {
		return nil
	}
	switch intent.IntentType {
	case "file_create", "file_edit", "web_search", "memory_save":
		intent.RequiresTools = true
	case "file_read", "rag_query", "memory_recall":
		intent.RequiresTools = true
		intent.Transition = "prefer_tooling"
	}

	switch intent.IntentType {
	case "file_create", "file_read", "file_edit", "web_search":
		intent.PreferredAgent = "Operator"
	case "memory_save", "memory_recall":
		intent.PreferredAgent = "Archivist"
	case "analysis", "general_qa", "rag_query":
		intent.PreferredAgent = "Assistant"
	}

	if intent.Transition == "" {
		if intent.RequiresTools {
			intent.Transition = "tool_first"
		} else {
			intent.Transition = "text_first"
		}
	}
	return intent
}

// RecognizeIntent performs intent recognition using the semantic router

func (s *Service) RecognizeIntent(ctx context.Context, query string) (*IntentRecognitionResult, error) {

	result, err := s.Route(ctx, query)

	if err != nil || result == nil || !result.Matched {

		// Return a fallback result on error

		return populateIntentExecutionHints(&IntentRecognitionResult{

			IntentType: "general_qa",

			Confidence: 0.0,
		}), nil

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

	return populateIntentExecutionHints(intentResult), nil

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

		return populateIntentExecutionHints(intentResult), nil

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

		return populateIntentExecutionHints(&IntentRecognitionResult{

			IntentType: "general_qa",

			Confidence: 0.5,

			Topic: query,
		}), nil

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
	content = strings.TrimSpace(content)
	if content == "" {
		return r.basicFallback(query), nil
	}

	var parsed IntentRecognitionResult
	if err := json.Unmarshal([]byte(content), &parsed); err == nil && strings.TrimSpace(parsed.IntentType) != "" {
		if parsed.Topic == "" {
			parsed.Topic = query
		}
		if parsed.Confidence <= 0 {
			parsed.Confidence = 0.6
		}
		return populateIntentExecutionHints(&parsed), nil
	}

	if block := extractJSONBlock(content); block != "" {
		if err := json.Unmarshal([]byte(block), &parsed); err == nil && strings.TrimSpace(parsed.IntentType) != "" {
			if parsed.Topic == "" {
				parsed.Topic = query
			}
			if parsed.Confidence <= 0 {
				parsed.Confidence = 0.6
			}
			return populateIntentExecutionHints(&parsed), nil
		}
	}

	intent := strings.TrimSpace(extractField(content, "intent_type"))
	if intent == "" {
		return r.basicFallback(query), nil
	}
	return populateIntentExecutionHints(&IntentRecognitionResult{
		IntentType: intent,
		TargetFile: strings.TrimSpace(extractField(content, "target_file")),
		Topic:      firstNonEmpty(strings.TrimSpace(extractField(content, "topic")), query),
		Confidence: parseConfidence(extractField(content, "confidence")),
	}), nil
}

func (r *FallbackLLMRecognizer) basicFallback(query string) *IntentRecognitionResult {
	lower := strings.ToLower(strings.TrimSpace(query))
	switch {
	case strings.Contains(lower, "remember"), strings.Contains(lower, "记住"):
		return populateIntentExecutionHints(&IntentRecognitionResult{IntentType: "memory_save", Confidence: 0.7, Topic: query})
	case strings.Contains(lower, "from memory"), strings.Contains(lower, "记得"), strings.Contains(lower, "之前"):
		return populateIntentExecutionHints(&IntentRecognitionResult{IntentType: "memory_recall", Confidence: 0.7, Topic: query})
	case strings.Contains(lower, "latest"), strings.Contains(lower, "current"), strings.Contains(lower, "最新"), strings.Contains(lower, "今天"):
		return populateIntentExecutionHints(&IntentRecognitionResult{IntentType: "web_search", Confidence: 0.68, Topic: query})
	case extractPath(query) != "":
		return populateIntentExecutionHints(&IntentRecognitionResult{IntentType: "file_read", TargetFile: extractPath(query), Confidence: 0.65, Topic: query})
	default:
		return populateIntentExecutionHints(&IntentRecognitionResult{IntentType: "general_qa", Confidence: 0.6, Topic: query})
	}
}

func extractJSONBlock(text string) string {
	re := regexp.MustCompile(`\{[\s\S]*\}`)
	return strings.TrimSpace(re.FindString(text))
}

func extractField(text, field string) string {
	re := regexp.MustCompile(`(?im)^` + regexp.QuoteMeta(field) + `\s*:\s*(.+)$`)
	m := re.FindStringSubmatch(text)
	if len(m) > 1 {
		return strings.TrimSpace(m[1])
	}
	return ""
}

func parseConfidence(raw string) float64 {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0.6
	}
	var f float64
	if _, err := fmt.Sscanf(raw, "%f", &f); err == nil && f > 0 {
		return f
	}
	return 0.6
}

func extractPath(text string) string {
	re := regexp.MustCompile(`[./]?[a-zA-Z0-9_\-./]+\.[a-z]{2,6}`)
	m := re.FindString(text)
	return strings.TrimSpace(m)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
